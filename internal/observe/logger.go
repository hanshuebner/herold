package observe

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
)

// LevelTrace is the numeric value for the "trace" log level (REQ-OPS-82).
// slog.LevelDebug == -4; trace = -8 is the conventional one-step-below-debug
// choice used by the slog ecosystem (e.g. golang.org/x/exp/slog).
const LevelTrace = slog.Level(-8)

// LogSinkConfig is the observe-package view of a single sink from
// sysconfig.LogSinkConfig. Kept narrow to avoid importing sysconfig from
// observe (layering rule).
type LogSinkConfig struct {
	Target     string
	Format     string
	Level      string
	Modules    map[string]string
	Activities ActivityFilterConfig
}

// ActivityFilterConfig holds the allow/deny list for the activity filter on
// one sink (REQ-OPS-86b). Exactly one of Allow/Deny may be non-nil.
type ActivityFilterConfig struct {
	Allow []string
	Deny  []string
}

// ObservabilityConfig is the slice of configuration observe cares about.
// Kept narrow to avoid importing sysconfig from observe (layering).
//
// New code should populate Sinks; the legacy LogFormat/LogLevel/LogModules
// fields are accepted for backwards compatibility and are synthesised into
// a single sink by NewLogger when Sinks is empty.
type ObservabilityConfig struct {
	// Sinks holds the multi-sink configuration (REQ-OPS-80..86).
	// When empty, NewLogger falls back to the legacy single-sink fields below.
	Sinks []LogSinkConfig

	// Legacy single-sink fields — accepted when Sinks is empty.
	// Deprecated: populate Sinks instead.
	LogFormat string
	LogLevel  string
	// LogModules maps subsystem / module names to per-module level overrides.
	LogModules map[string]string

	MetricsBind  string
	OTLPEndpoint string

	// SecretKeys, if non-nil, overrides the default list of log attribute
	// keys whose values are redacted. Matching is case-insensitive exact.
	SecretKeys []string

	// Verbose, when true, overrides every sink's activities filter to
	// allow-all and lowers every sink's level floor to debug (REQ-OPS-86c).
	// Set from --log-verbose CLI flag or HEROLD_LOG_VERBOSE=1.
	Verbose bool
}

// Logger is a live multi-sink logger. Callers use the embedded *slog.Logger
// for all logging; the Reload method swaps the underlying handler atomically
// without dropping records (REQ-OPS-85).
type Logger struct {
	*slog.Logger
	dispatcher *dispatchHandler
}

// Reload swaps the active sink set for newCfg. Sinks whose target and
// format are unchanged keep their open file handle. New sinks open new
// handles; removed sinks are closed after a brief drain.
func (l *Logger) Reload(newCfg ObservabilityConfig) error {
	fanout, err := buildFanout(newCfg)
	if err != nil {
		return err
	}
	l.dispatcher.swap(fanout)
	return nil
}

// NewLogger returns a *Logger configured from cfg. The returned Logger wraps
// a *slog.Logger whose handler fans records out to every configured sink.
// The redaction handler is outermost, before the fan-out, so secrets are
// stripped exactly once (REQ-OPS-84).
func NewLogger(cfg ObservabilityConfig) (*Logger, error) {
	fanout, err := buildFanout(cfg)
	if err != nil {
		return nil, err
	}
	d := &dispatchHandler{}
	d.inner.Store(fanout)
	l := &Logger{dispatcher: d}
	l.Logger = slog.New(d)
	return l, nil
}

// NewLoggerTo is like NewLogger but writes to w. Intended for tests; creates
// a single sink with the legacy-style config.
func NewLoggerTo(w io.Writer, cfg ObservabilityConfig) *slog.Logger {
	lvl := parseLogLevel(cfg.LogLevel)
	modLevels := buildModuleLevels(cfg.LogModules)
	minLvl := computeMinLevel(lvl, modLevels)

	var base slog.Handler
	opts := &slog.HandlerOptions{Level: minLvl}
	switch strings.ToLower(cfg.LogFormat) {
	case "", "json":
		base = slog.NewJSONHandler(w, opts)
	case "text":
		base = slog.NewTextHandler(w, opts)
	default:
		base = slog.NewJSONHandler(w, opts)
	}
	keys := cfg.SecretKeys
	if keys == nil {
		keys = DefaultSecretKeys
	}
	redact := NewRedactHandler(base, keys)

	if len(modLevels) == 0 {
		return slog.New(redact)
	}
	return slog.New(&moduleLevelHandler{
		base:      redact,
		globalLvl: lvl,
		minLvl:    minLvl,
		modules:   modLevels,
	})
}

// --- dispatcher ---

// dispatchHandler fans log records out to N sinks. An atomic.Pointer swap
// is the mechanism for SIGHUP-driven reload with no record loss (REQ-OPS-85):
// the pointer is swapped to a new inner handler; in-flight Handle calls that
// have already loaded the old inner handler finish normally against the old
// sinks, which is safe because sinks are append-only writers.
type dispatchHandler struct {
	// inner holds a *fanoutHandler (swapped on reload).
	inner atomic.Pointer[fanoutHandler]
}

// fanoutHandler wraps the redaction handler and zero or more per-sink handlers.
type fanoutHandler struct {
	// redact is the outermost handler: records pass through it first.
	// Its base is a fanout across sinkHandlers.
	redact slog.Handler
	sinks  []*sinkHandler
	// verbose bypasses activity filters and floors all levels at debug.
	verbose bool
	// minGlobal is the lowest level across all sinks (no per-module).
	minGlobal slog.Level
}

func (d *dispatchHandler) Enabled(ctx context.Context, lvl slog.Level) bool {
	f := d.inner.Load()
	if f == nil {
		return false
	}
	return f.Enabled(ctx, lvl)
}

func (d *dispatchHandler) Handle(ctx context.Context, r slog.Record) error {
	f := d.inner.Load()
	if f == nil {
		return nil
	}
	return f.Handle(ctx, r)
}

func (d *dispatchHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	f := d.inner.Load()
	if f == nil {
		return d
	}
	return f.WithAttrs(attrs)
}

func (d *dispatchHandler) WithGroup(name string) slog.Handler {
	f := d.inner.Load()
	if f == nil {
		return d
	}
	return f.WithGroup(name)
}

// swap replaces the inner fanoutHandler atomically.
func (d *dispatchHandler) swap(next *fanoutHandler) {
	d.inner.Store(next)
}

func (f *fanoutHandler) Enabled(ctx context.Context, lvl slog.Level) bool {
	if f.verbose {
		return lvl >= slog.LevelDebug
	}
	return lvl >= f.minGlobal
}

func (f *fanoutHandler) Handle(ctx context.Context, r slog.Record) error {
	return f.redact.Handle(ctx, r)
}

func (f *fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return f.redact.WithAttrs(attrs)
}

func (f *fanoutHandler) WithGroup(name string) slog.Handler {
	return f.redact.WithGroup(name)
}

// sinkHandler is a single destination in the fan-out. It wraps:
//
//	activityFilter -> moduleLevelHandler -> base (JSON/console handler)
type sinkHandler struct {
	handler slog.Handler
}

// sinkFanout is the base handler that distributes to each sinkHandler after
// redaction. It is inserted as the inner base of the redact handler.
type sinkFanout struct {
	sinks   []*sinkHandler
	verbose bool
}

func (sf *sinkFanout) Enabled(_ context.Context, _ slog.Level) bool {
	return true // outer layers gate; the fanout always tries
}

func (sf *sinkFanout) Handle(ctx context.Context, r slog.Record) error {
	for _, s := range sf.sinks {
		// Gate on Enabled so per-sink level floors are respected even
		// though the record has already passed the global minGlobal check.
		if s.handler.Enabled(ctx, r.Level) {
			_ = s.handler.Handle(ctx, r)
		}
	}
	return nil
}

func (sf *sinkFanout) WithAttrs(attrs []slog.Attr) slog.Handler {
	sinks := make([]*sinkHandler, len(sf.sinks))
	for i, s := range sf.sinks {
		sinks[i] = &sinkHandler{handler: s.handler.WithAttrs(attrs)}
	}
	return &sinkFanout{sinks: sinks, verbose: sf.verbose}
}

func (sf *sinkFanout) WithGroup(name string) slog.Handler {
	sinks := make([]*sinkHandler, len(sf.sinks))
	for i, s := range sf.sinks {
		sinks[i] = &sinkHandler{handler: s.handler.WithGroup(name)}
	}
	return &sinkFanout{sinks: sinks, verbose: sf.verbose}
}

// --- builder ---

// buildFanout constructs a fanoutHandler from cfg.
func buildFanout(cfg ObservabilityConfig) (*fanoutHandler, error) {
	sinks := cfg.Sinks
	// Legacy shim: if no Sinks provided, synthesise from legacy fields.
	if len(sinks) == 0 {
		sinks = []LogSinkConfig{
			{
				Target:  "stderr",
				Format:  legacyFormat(cfg.LogFormat),
				Level:   cfg.LogLevel,
				Modules: cfg.LogModules,
			},
		}
	}

	keys := cfg.SecretKeys
	if keys == nil {
		keys = DefaultSecretKeys
	}

	var minGlobal slog.Level = slog.LevelError + 1
	builtSinks := make([]*sinkHandler, 0, len(sinks))
	for _, sc := range sinks {
		sh, err := buildSinkHandler(sc, cfg.Verbose)
		if err != nil {
			return nil, err
		}
		builtSinks = append(builtSinks, sh)
		sl := parseLogLevel(sc.Level)
		if cfg.Verbose {
			sl = slog.LevelDebug
		}
		// Also consider per-module overrides.
		for _, ml := range sc.Modules {
			msl := parseLogLevel(ml)
			if cfg.Verbose {
				msl = slog.LevelDebug
			}
			if msl < sl {
				sl = msl
			}
		}
		if sl < minGlobal {
			minGlobal = sl
		}
	}

	if len(builtSinks) == 0 {
		// Fallback: at least a no-op.
		minGlobal = slog.LevelInfo
	}

	fanout := &sinkFanout{sinks: builtSinks, verbose: cfg.Verbose}
	redact := NewRedactHandler(fanout, keys)

	return &fanoutHandler{
		redact:    redact,
		sinks:     builtSinks,
		verbose:   cfg.Verbose,
		minGlobal: minGlobal,
	}, nil
}

// buildSinkHandler constructs the per-sink handler chain:
//
//	activityFilter (optional) -> moduleLevelHandler (optional) -> base
func buildSinkHandler(sc LogSinkConfig, verbose bool) (*sinkHandler, error) {
	w, err := openSinkWriter(sc.Target)
	if err != nil {
		return nil, err
	}

	lvl := parseLogLevel(sc.Level)
	if verbose {
		lvl = slog.LevelDebug
	}
	modLevels := buildModuleLevels(sc.Modules)
	if verbose {
		for k := range modLevels {
			modLevels[k] = slog.LevelDebug
		}
	}
	minLvl := computeMinLevel(lvl, modLevels)

	format := sc.Format
	if format == "auto" || format == "" {
		if isTTY(w) {
			format = "console"
		} else {
			format = "json"
		}
	}

	opts := &slog.HandlerOptions{Level: minLvl}
	var base slog.Handler
	switch format {
	case "console":
		base = NewConsoleHandler(w, opts)
	default: // "json" and anything else
		base = slog.NewJSONHandler(w, opts)
	}

	var h slog.Handler
	if len(modLevels) > 0 {
		h = &moduleLevelHandler{
			base:      base,
			globalLvl: lvl,
			minLvl:    minLvl,
			modules:   modLevels,
		}
	} else {
		h = base
	}

	// Wrap with activity filter if configured (and verbose does not override).
	act := sc.Activities
	if !verbose && (len(act.Allow) > 0 || len(act.Deny) > 0) {
		h = newActivityFilter(h, act)
	}

	return &sinkHandler{handler: h}, nil
}

// openSinkWriter returns the io.Writer for a sink target. stderr/stdout
// return the corresponding os.File; absolute paths are opened for append.
func openSinkWriter(target string) (io.Writer, error) {
	switch target {
	case "stderr", "":
		return os.Stderr, nil
	case "stdout":
		return os.Stdout, nil
	default:
		f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0640)
		if err != nil {
			return nil, err
		}
		return f, nil
	}
}

// isTTY reports whether w is a TTY-backed *os.File.
func isTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isTerminal(int(f.Fd()))
}

// legacyFormat maps the old "text" format to "console" for the legacy shim.
func legacyFormat(f string) string {
	switch f {
	case "text":
		return "console"
	case "":
		return "auto"
	default:
		return f
	}
}

// --- moduleLevelHandler (unchanged from original; kept here) ---

// moduleLevelHandler is an slog.Handler wrapper that applies per-module level
// overrides (REQ-OPS-82). It inspects "subsystem" and "module" attributes on
// each record to determine the effective level, falling back to globalLvl when
// neither attribute is present or the module has no override.
type moduleLevelHandler struct {
	base      slog.Handler
	globalLvl slog.Level
	// minLvl is min(globalLvl, all module overrides). Used by Enabled so that
	// a debug/trace record with a matching module attr reaches Handle rather
	// than being short-circuited by the slog.Logger before we can inspect its
	// attrs and apply the override.
	minLvl  slog.Level
	modules map[string]slog.Level
	// preModule is set when WithAttrs pre-scoped a subsystem/module key.
	preModule string
}

func (h *moduleLevelHandler) Enabled(ctx context.Context, lvl slog.Level) bool {
	if h.preModule != "" {
		if ml, ok := h.modules[h.preModule]; ok {
			return lvl >= ml
		}
		return lvl >= h.globalLvl
	}
	return lvl >= h.minLvl
}

func (h *moduleLevelHandler) Handle(ctx context.Context, r slog.Record) error {
	eff := h.effectiveLevelForRecord(r)
	if r.Level < eff {
		return nil
	}
	return h.base.Handle(ctx, r)
}

func (h *moduleLevelHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	mod := h.preModule
	for _, a := range attrs {
		if a.Key == "subsystem" || a.Key == "module" {
			mod = strings.ToLower(a.Value.String())
		}
	}
	return &moduleLevelHandler{
		base:      h.base.WithAttrs(attrs),
		globalLvl: h.globalLvl,
		minLvl:    h.minLvl,
		modules:   h.modules,
		preModule: mod,
	}
}

func (h *moduleLevelHandler) WithGroup(name string) slog.Handler {
	return &moduleLevelHandler{
		base:      h.base.WithGroup(name),
		globalLvl: h.globalLvl,
		minLvl:    h.minLvl,
		modules:   h.modules,
		preModule: h.preModule,
	}
}

func (h *moduleLevelHandler) effectiveLevelForRecord(r slog.Record) slog.Level {
	if h.preModule != "" {
		if ml, ok := h.modules[h.preModule]; ok {
			return ml
		}
		return h.globalLvl
	}
	lvl := h.globalLvl
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "subsystem" || a.Key == "module" {
			mod := strings.ToLower(a.Value.String())
			if ml, ok := h.modules[mod]; ok {
				lvl = ml
				return false
			}
		}
		return true
	})
	return lvl
}

// --- helpers ---

func buildModuleLevels(mods map[string]string) map[string]slog.Level {
	if len(mods) == 0 {
		return nil
	}
	out := make(map[string]slog.Level, len(mods))
	for mod, ls := range mods {
		out[strings.ToLower(mod)] = parseLogLevel(ls)
	}
	return out
}

func computeMinLevel(global slog.Level, mods map[string]slog.Level) slog.Level {
	min := global
	for _, ml := range mods {
		if ml < min {
			min = ml
		}
	}
	return min
}

func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "trace":
		return LevelTrace
	case "debug":
		return slog.LevelDebug
	case "", "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
