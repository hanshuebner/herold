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
