package observe

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
)

// LevelTrace is the numeric value for the "trace" log level (REQ-OPS-82).
// slog.LevelDebug == -4; trace = -8 is the conventional one-step-below-debug
// choice used by the slog ecosystem (e.g. golang.org/x/exp/slog).
const LevelTrace = slog.Level(-8)

// ObservabilityConfig is the slice of sysconfig.ObservabilityConfig observe
// cares about. Kept narrow to avoid importing sysconfig from observe (layering).
type ObservabilityConfig struct {
	LogFormat string
	LogLevel  string
	// LogModules maps subsystem / module names to per-module level overrides
	// (REQ-OPS-82). Keys match the "subsystem" or "module" slog attribute;
	// values are level strings from the same closed enum as LogLevel.
	LogModules   map[string]string
	MetricsBind  string
	OTLPEndpoint string

	// SecretKeys, if non-nil, overrides the default list of log attribute keys
	// whose values are redacted. Matching is case-insensitive, substring.
	SecretKeys []string
}

// NewLogger returns a *slog.Logger configured from cfg, writing to os.Stderr.
// The logger is wrapped in a secret-stripping handler (REQ-OPS-84).
func NewLogger(cfg ObservabilityConfig) *slog.Logger {
	return NewLoggerTo(os.Stderr, cfg)
}

// NewLoggerTo is like NewLogger but writes to w. Test seam.
func NewLoggerTo(w io.Writer, cfg ObservabilityConfig) *slog.Logger {
	lvl := parseLogLevel(cfg.LogLevel)
	// Per-module map: build a copy keyed by lowercase module name.
	modLevels := make(map[string]slog.Level, len(cfg.LogModules))
	for mod, ls := range cfg.LogModules {
		modLevels[strings.ToLower(mod)] = parseLogLevel(ls)
	}

	// The base handler must be willing to pass through the lowest level that
	// any module override might request. If any module is set to trace/debug
	// while the global level is higher, we need the base handler to at least
	// let those records through the Enabled check at the module-filter layer.
	// We use minLevel to set the floor on the base handler.
	minLvl := lvl
	for _, ml := range modLevels {
		if ml < minLvl {
			minLvl = ml
		}
	}

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
		// Fast path: no per-module overrides needed.
		return slog.New(redact)
	}
	return slog.New(&moduleLevelHandler{
		base:      redact,
		globalLvl: lvl,
		minLvl:    minLvl,
		modules:   modLevels,
	})
}

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
	// If there is a pre-scoped module, use its override exactly.
	if h.preModule != "" {
		if ml, ok := h.modules[h.preModule]; ok {
			return lvl >= ml
		}
		return lvl >= h.globalLvl
	}
	// Without a pre-scoped module we cannot know at Enabled-call time which
	// module a record belongs to. Return true if the level passes any
	// configured threshold (the minimum of global and all overrides). Handle
	// then applies the precise per-record decision.
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

// effectiveLevelForRecord scans attrs on r for "subsystem" or "module" and
// returns the matching override, or globalLvl if none is found.
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
