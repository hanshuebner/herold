package observe

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

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
	// We can't easily capture os.Stderr without a pipe dance here; the goal
	// is simply that NewLogger returns a non-nil logger wired with defaults.
	if l := NewLogger(ObservabilityConfig{}); l == nil {
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
	// Log at debug with subsystem=smtp; should appear.
	l.Debug("smtp-debug-msg", "subsystem", "smtp")
	// Log at debug with subsystem=imap; should NOT appear (imap has no override).
	l.Debug("imap-debug-msg", "subsystem", "imap")
	// Log at info with no subsystem; should appear (passes global level).
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

// TestTraceLevel asserts that "trace" is a recognised log level and that
// a record logged at LevelTrace appears when the level is set to "trace".
func TestTraceLevel(t *testing.T) {
	var buf bytes.Buffer
	l := NewLoggerTo(&buf, ObservabilityConfig{LogLevel: "trace"})
	l.Log(nil, LevelTrace, "trace-msg") //nolint:staticcheck // nil ctx is fine for tests
	if !strings.Contains(buf.String(), "trace-msg") {
		t.Fatalf("trace level: expected trace-msg; got: %q", buf.String())
	}
}

// TestTraceLevel_SuppressedAtDebug asserts that a "trace" record is suppressed
// when the configured level is "debug".
func TestTraceLevel_SuppressedAtDebug(t *testing.T) {
	var buf bytes.Buffer
	l := NewLoggerTo(&buf, ObservabilityConfig{LogLevel: "debug"})
	l.Log(nil, LevelTrace, "trace-should-not-appear") //nolint:staticcheck
	if strings.Contains(buf.String(), "trace-should-not-appear") {
		t.Fatalf("trace suppressed at debug: unexpected output: %q", buf.String())
	}
}
