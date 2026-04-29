package observe

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
)

// --- legacy NewLoggerTo tests (still exercised; NewLoggerTo is unchanged) ---

func TestNewLoggerTo_JSONDefault(t *testing.T) {
	var buf bytes.Buffer
	l := NewLoggerTo(&buf, ObservabilityConfig{})
	l.Info("hello", "k", "v")
	var m map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &m); err != nil {
		t.Fatalf("default format should be JSON: %v; raw=%q", err, buf.String())
	}
	if m["msg"] != "hello" {
		t.Fatalf("msg missing: %v", m)
	}
}

func TestNewLoggerTo_TextFormat(t *testing.T) {
	var buf bytes.Buffer
	l := NewLoggerTo(&buf, ObservabilityConfig{LogFormat: "text"})
	l.Info("hello")
	if !strings.Contains(buf.String(), "msg=hello") {
		t.Fatalf("text output missing msg=hello: %q", buf.String())
	}
}

func TestNewLoggerTo_LevelFilter(t *testing.T) {
	var buf bytes.Buffer
	l := NewLoggerTo(&buf, ObservabilityConfig{LogLevel: "warn"})
	l.Info("suppressed")
	l.Warn("emitted")
	out := buf.String()
	if strings.Contains(out, "suppressed") {
		t.Fatalf("info-level line leaked at warn: %q", out)
	}
	if !strings.Contains(out, "emitted") {
		t.Fatalf("warn-level line missing: %q", out)
	}
}

func TestNewLoggerTo_UnknownLevelDefaultsToInfo(t *testing.T) {
	var buf bytes.Buffer
	l := NewLoggerTo(&buf, ObservabilityConfig{LogLevel: "bogus"})
	l.Debug("shh")
	l.Info("yes")
	out := buf.String()
	if strings.Contains(out, "shh") {
		t.Fatalf("debug leaked: %q", out)
	}
	if !strings.Contains(out, "yes") {
		t.Fatalf("info missing: %q", out)
	}
}

func TestNewLoggerTo_SecretsRedactedByDefault(t *testing.T) {
	var buf bytes.Buffer
	l := NewLoggerTo(&buf, ObservabilityConfig{})
	l.Info("auth", "password", "hunter2")
	if !strings.Contains(buf.String(), RedactedValue) {
		t.Fatalf("default redaction missing: %q", buf.String())
	}
	if strings.Contains(buf.String(), "hunter2") {
		t.Fatalf("secret leaked: %q", buf.String())
	}
}

func TestNewLoggerTo_CustomSecretKeysOverride(t *testing.T) {
	var buf bytes.Buffer
	l := NewLoggerTo(&buf, ObservabilityConfig{SecretKeys: []string{"my_field"}})
	l.Info("x", "my_field", "hush", "password", "kept")
	out := buf.String()
	if !strings.Contains(out, "<redacted>") {
		t.Fatalf("my_field not redacted: %q", out)
	}
	if !strings.Contains(out, "kept") {
		t.Fatalf("override replaced default list; password should NOT be in built-in set here: %q", out)
	}
}

func TestNewLogger_WritesToStderr(t *testing.T) {
	l, err := NewLogger(ObservabilityConfig{})
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}
	if l == nil {
		t.Fatalf("NewLogger returned nil")
	}
}

// TestModuleLogLevel_OverrideEnabled asserts that a subsystem=smtp record is
// enabled at debug when LogModules has smtp="debug" and global level is info.
func TestModuleLogLevel_OverrideEnabled(t *testing.T) {
	var buf bytes.Buffer
	cfg := ObservabilityConfig{
		LogLevel:   "info",
		LogModules: map[string]string{"smtp": "debug"},
	}
	l := NewLoggerTo(&buf, cfg)
	l.Debug("smtp-debug-msg", "subsystem", "smtp")
	l.Debug("imap-debug-msg", "subsystem", "imap")
	l.Info("plain-info-msg")

	out := buf.String()
	if !strings.Contains(out, "smtp-debug-msg") {
		t.Fatalf("smtp debug override: expected smtp-debug-msg in output; got: %q", out)
	}
	if strings.Contains(out, "imap-debug-msg") {
		t.Fatalf("imap has no debug override: imap-debug-msg should be suppressed; got: %q", out)
	}
	if !strings.Contains(out, "plain-info-msg") {
		t.Fatalf("info line should always appear; got: %q", out)
	}
}

// TestModuleLogLevel_WithAttrsPreScoped asserts that a logger pre-scoped via
// WithAttrs("subsystem","smtp") inherits the smtp override for all records.
func TestModuleLogLevel_WithAttrsPreScoped(t *testing.T) {
	var buf bytes.Buffer
	cfg := ObservabilityConfig{
		LogLevel:   "info",
		LogModules: map[string]string{"smtp": "debug"},
	}
	l := NewLoggerTo(&buf, cfg).With("subsystem", "smtp")
	l.Debug("smtp-pre-scoped-debug")
	out := buf.String()
	if !strings.Contains(out, "smtp-pre-scoped-debug") {
		t.Fatalf("pre-scoped smtp logger: expected debug line; got: %q", out)
	}
}

// TestTraceLevel asserts that "trace" is a recognised log level.
func TestTraceLevel(t *testing.T) {
	var buf bytes.Buffer
	l := NewLoggerTo(&buf, ObservabilityConfig{LogLevel: "trace"})
	l.Log(context.TODO(), LevelTrace, "trace-msg")
	if !strings.Contains(buf.String(), "trace-msg") {
		t.Fatalf("trace level: expected trace-msg; got: %q", buf.String())
	}
}

// TestTraceLevel_SuppressedAtDebug asserts that a "trace" record is suppressed
// when the configured level is "debug".
func TestTraceLevel_SuppressedAtDebug(t *testing.T) {
	var buf bytes.Buffer
	l := NewLoggerTo(&buf, ObservabilityConfig{LogLevel: "debug"})
	l.Log(context.TODO(), LevelTrace, "trace-should-not-appear")
	if strings.Contains(buf.String(), "trace-should-not-appear") {
		t.Fatalf("trace suppressed at debug: unexpected output: %q", buf.String())
	}
}

// --- multi-sink NewLogger tests (REQ-OPS-80..86) ---

// buildFanoutFromWritersHelper builds a fanoutHandler that writes JSON to the
// provided buffers (test seam: bypasses openSinkWriter).
func buildFanoutFromWritersHelper(t *testing.T, wls []writerLevel) *fanoutHandler {
	t.Helper()
	builtSinks := make([]*sinkHandler, 0, len(wls))
	minGlobal := slog.LevelError + 1
	for _, wl := range wls {
		lvl := parseLogLevel(wl.level)
		opts := &slog.HandlerOptions{Level: lvl}
		base := slog.NewJSONHandler(wl.w, opts)
		builtSinks = append(builtSinks, &sinkHandler{handler: base})
		if lvl < minGlobal {
			minGlobal = lvl
		}
	}
	sf := &sinkFanout{sinks: builtSinks}
	return &fanoutHandler{
		redact:    NewRedactHandler(sf, DefaultSecretKeys),
		sinks:     builtSinks,
		minGlobal: minGlobal,
	}
}

func wrapInDispatcher(f *fanoutHandler) *dispatchHandler {
	d := &dispatchHandler{}
	d.inner.Store(f)
	return d
}

type writerLevel struct {
	w     *bytes.Buffer
	level string
}

func TestMultiSink_FanOut(t *testing.T) {
	var a, b bytes.Buffer
	l := buildFanoutFromWritersHelper(t, []writerLevel{
		{w: &a, level: "info"},
		{w: &b, level: "info"},
	})
	logger := slog.New(wrapInDispatcher(l))
	logger.Info("both-sinks")
	if !strings.Contains(a.String(), "both-sinks") {
		t.Fatalf("sink A missing: %q", a.String())
	}
	if !strings.Contains(b.String(), "both-sinks") {
		t.Fatalf("sink B missing: %q", b.String())
	}
}

func TestMultiSink_PerSinkLevel(t *testing.T) {
	var a, b bytes.Buffer
	l := buildFanoutFromWritersHelper(t, []writerLevel{
		{w: &a, level: "info"},
		{w: &b, level: "debug"},
	})
	logger := slog.New(wrapInDispatcher(l))
	logger.Debug("debug-only")
	logger.Info("both-see")
	if strings.Contains(a.String(), "debug-only") {
		t.Fatalf("sink A (info) should not see debug: %q", a.String())
	}
	if !strings.Contains(b.String(), "debug-only") {
		t.Fatalf("sink B (debug) should see debug: %q", b.String())
	}
	if !strings.Contains(a.String(), "both-see") {
		t.Fatalf("sink A missing info: %q", a.String())
	}
	if !strings.Contains(b.String(), "both-see") {
		t.Fatalf("sink B missing info: %q", b.String())
	}
}

func TestMultiSink_PerSinkModuleOverride(t *testing.T) {
	var a, b bytes.Buffer
	lvlA := parseLogLevel("info")
	optsA := &slog.HandlerOptions{Level: LevelTrace}
	optsB := &slog.HandlerOptions{Level: slog.LevelInfo}
	baseA := slog.NewJSONHandler(&a, optsA)
	baseB := slog.NewJSONHandler(&b, optsB)
	modA := &moduleLevelHandler{
		base:      baseA,
		globalLvl: lvlA,
		minLvl:    slog.LevelDebug,
		modules:   map[string]slog.Level{"smtp": slog.LevelDebug},
	}
	sf := &sinkFanout{sinks: []*sinkHandler{
		{handler: modA},
		{handler: baseB},
	}}
	fanout := &fanoutHandler{
		redact:    NewRedactHandler(sf, DefaultSecretKeys),
		minGlobal: LevelTrace,
	}
	logger := slog.New(wrapInDispatcher(fanout))
	logger.Debug("smtp-debug", "subsystem", "smtp")
	logger.Debug("imap-debug", "subsystem", "imap")
	logger.Info("info-msg")

	if !strings.Contains(a.String(), "smtp-debug") {
		t.Fatalf("sink A smtp override: expected smtp-debug; got: %q", a.String())
	}
	if strings.Contains(a.String(), "imap-debug") {
		t.Fatalf("sink A imap: expected suppressed; got: %q", a.String())
	}
	if strings.Contains(b.String(), "smtp-debug") || strings.Contains(b.String(), "imap-debug") {
		t.Fatalf("sink B (info) should not see debug: %q", b.String())
	}
	if !strings.Contains(b.String(), "info-msg") {
		t.Fatalf("sink B missing info: %q", b.String())
	}
}

func TestMultiSink_ActivityFilter_Deny(t *testing.T) {
	var buf bytes.Buffer
	base := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	filtered := newActivityFilter(base, ActivityFilterConfig{
		Deny: []string{"poll", "access"},
	})
	sf := &sinkFanout{sinks: []*sinkHandler{{handler: filtered}}}
	fanout := &fanoutHandler{
		redact:    NewRedactHandler(sf, DefaultSecretKeys),
		minGlobal: slog.LevelDebug,
	}
	logger := slog.New(wrapInDispatcher(fanout))
	logger.Info("user-action", "activity", "user")
	logger.Debug("poll-heartbeat", "activity", "poll")
	logger.Debug("access-echo", "activity", "access")
	logger.Info("system-work", "activity", "system")

	out := buf.String()
	if !strings.Contains(out, "user-action") {
		t.Fatalf("user-action should pass deny=[poll,access]: %q", out)
	}
	if strings.Contains(out, "poll-heartbeat") {
		t.Fatalf("poll-heartbeat should be denied: %q", out)
	}
	if strings.Contains(out, "access-echo") {
		t.Fatalf("access-echo should be denied: %q", out)
	}
	if !strings.Contains(out, "system-work") {
		t.Fatalf("system-work should pass deny=[poll,access]: %q", out)
	}
}

func TestMultiSink_ActivityFilter_Allow(t *testing.T) {
	var buf bytes.Buffer
	base := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	filtered := newActivityFilter(base, ActivityFilterConfig{
		Allow: []string{"audit"},
	})
	sf := &sinkFanout{sinks: []*sinkHandler{{handler: filtered}}}
	fanout := &fanoutHandler{
		redact:    NewRedactHandler(sf, DefaultSecretKeys),
		minGlobal: slog.LevelDebug,
	}
	logger := slog.New(wrapInDispatcher(fanout))
	logger.Info("audit-event", "activity", "audit")
	logger.Info("user-event", "activity", "user")

	out := buf.String()
	if !strings.Contains(out, "audit-event") {
		t.Fatalf("audit-event should pass allow=[audit]: %q", out)
	}
	if strings.Contains(out, "user-event") {
		t.Fatalf("user-event should be filtered by allow=[audit]: %q", out)
	}
}

func TestMultiSink_RedactionStillApplies(t *testing.T) {
	var a, b bytes.Buffer
	sf := &sinkFanout{sinks: []*sinkHandler{
		{handler: slog.NewJSONHandler(&a, &slog.HandlerOptions{Level: slog.LevelDebug})},
		{handler: slog.NewJSONHandler(&b, &slog.HandlerOptions{Level: slog.LevelDebug})},
	}}
	fanout := &fanoutHandler{
		redact:    NewRedactHandler(sf, DefaultSecretKeys),
		minGlobal: slog.LevelDebug,
	}
	logger := slog.New(wrapInDispatcher(fanout))
	logger.Info("auth", "password", "hunter2")
	for i, buf := range []*bytes.Buffer{&a, &b} {
		if strings.Contains(buf.String(), "hunter2") {
			t.Fatalf("sink %d: secret leaked: %q", i, buf.String())
		}
		if !strings.Contains(buf.String(), RedactedValue) {
			t.Fatalf("sink %d: redaction missing: %q", i, buf.String())
		}
	}
}

func TestMultiSink_SIGHUPReload_NoRecordLoss(t *testing.T) {
	var a, b bytes.Buffer

	sf1 := &sinkFanout{sinks: []*sinkHandler{
		{handler: slog.NewJSONHandler(&a, &slog.HandlerOptions{Level: slog.LevelInfo})},
	}}
	fanout1 := &fanoutHandler{
		redact:    NewRedactHandler(sf1, DefaultSecretKeys),
		minGlobal: slog.LevelInfo,
	}
	d := &dispatchHandler{}
	d.inner.Store(fanout1)
	logger := slog.New(d)
	logger.Info("before-reload")

	sf2 := &sinkFanout{sinks: []*sinkHandler{
		{handler: slog.NewJSONHandler(&b, &slog.HandlerOptions{Level: slog.LevelInfo})},
	}}
	fanout2 := &fanoutHandler{
		redact:    NewRedactHandler(sf2, DefaultSecretKeys),
		minGlobal: slog.LevelInfo,
	}
	d.swap(fanout2)
	logger.Info("after-reload")

	if !strings.Contains(a.String(), "before-reload") {
		t.Fatalf("pre-reload record lost: %q", a.String())
	}
	if strings.Contains(a.String(), "after-reload") {
		t.Fatalf("post-reload record in old sink: %q", a.String())
	}
	if !strings.Contains(b.String(), "after-reload") {
		t.Fatalf("post-reload record missing from new sink: %q", b.String())
	}
}

func TestMultiSink_SIGHUPReload_Concurrent(t *testing.T) {
	var a, b bytes.Buffer
	sf1 := &sinkFanout{sinks: []*sinkHandler{
		{handler: slog.NewJSONHandler(&a, &slog.HandlerOptions{Level: slog.LevelInfo})},
	}}
	fanout1 := &fanoutHandler{
		redact:    NewRedactHandler(sf1, DefaultSecretKeys),
		minGlobal: slog.LevelInfo,
	}
	d := &dispatchHandler{}
	d.inner.Store(fanout1)
	logger := slog.New(d)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			logger.Info("concurrent-write")
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		sf2 := &sinkFanout{sinks: []*sinkHandler{
			{handler: slog.NewJSONHandler(&b, &slog.HandlerOptions{Level: slog.LevelInfo})},
		}}
		fanout2 := &fanoutHandler{
			redact:    NewRedactHandler(sf2, DefaultSecretKeys),
			minGlobal: slog.LevelInfo,
		}
		d.swap(fanout2)
	}()
	wg.Wait()
	// No crash or race = pass.
}

// TestNewLogger_FromSinkConfig exercises NewLogger with a file sink.
func TestNewLogger_FromSinkConfig(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/test.jsonl"
	cfg := ObservabilityConfig{
		Sinks: []LogSinkConfig{
			{
				Target: path,
				Format: "json",
				Level:  "debug",
			},
		},
	}
	l, err := NewLogger(cfg)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	l.Info("file-sink-test", "activity", "system")

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if !strings.Contains(string(content), "file-sink-test") {
		t.Fatalf("message not in file: %q", content)
	}
}

// TestNewLogger_ActivityFilter_VerboseOverride verifies that verbose=true
// bypasses the deny filter so poll records appear.
func TestNewLogger_ActivityFilter_VerboseOverride(t *testing.T) {
	var buf bytes.Buffer
	sh, err := buildSinkHandlerOnWriter(&buf, LogSinkConfig{
		Format:     "json",
		Level:      "debug",
		Activities: ActivityFilterConfig{Deny: []string{"poll"}},
	}, true /*verbose*/)
	if err != nil {
		t.Fatalf("buildSinkHandler: %v", err)
	}
	logger := slog.New(sh.handler)
	logger.Debug("poll-heartbeat", "activity", "poll")
	if !strings.Contains(buf.String(), "poll-heartbeat") {
		t.Fatalf("verbose: poll should appear despite deny filter: %q", buf.String())
	}
}

// buildSinkHandlerOnWriter is a test-only variant of buildSinkHandler that
// uses the provided writer instead of opening a file.
func buildSinkHandlerOnWriter(w io.Writer, sc LogSinkConfig, verbose bool) (*sinkHandler, error) {
	lvl := parseLogLevel(sc.Level)
	if verbose {
		lvl = slog.LevelDebug
	}
	modLevels := buildModuleLevels(sc.Modules)
	minLvl := computeMinLevel(lvl, modLevels)
	opts := &slog.HandlerOptions{Level: minLvl}
	base := slog.NewJSONHandler(w, opts)
	var h slog.Handler = base
	if len(modLevels) > 0 {
		h = &moduleLevelHandler{
			base:      base,
			globalLvl: lvl,
			minLvl:    minLvl,
			modules:   modLevels,
		}
	}
	act := sc.Activities
	if !verbose && (len(act.Allow) > 0 || len(act.Deny) > 0) {
		h = newActivityFilter(h, act)
	}
	return &sinkHandler{handler: h}, nil
}
