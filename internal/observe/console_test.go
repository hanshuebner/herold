package observe

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
)

// fixedClock returns a FakeClock anchored at a predictable instant for golden
// output tests.
func fixedClock() clock.Clock {
	t, _ := time.ParseInLocation("2006-01-02T15:04:05.000", "2025-01-15T10:30:00.123", time.Local)
	return clock.NewFake(t)
}

var boolTrue = true
var boolFalse = false

func TestConsoleHandler_BasicNoColor(t *testing.T) {
	var buf bytes.Buffer
	clk := fixedClock()
	h := NewConsoleHandlerWithClock(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}, clk, &boolFalse)
	logger := slog.New(h)
	logger.Info("herold starting", "version", "1.0.0", "activity", "system")

	out := buf.String()
	if !strings.HasPrefix(out, "10:30:00.123") {
		t.Errorf("timestamp prefix: got %q", out[:min(len(out), 20)])
	}
	if !strings.Contains(out, "INFO") {
		t.Errorf("missing level: %q", out)
	}
	if !strings.Contains(out, "herold starting") {
		t.Errorf("missing message: %q", out)
	}
	if !strings.Contains(out, "activity") || !strings.Contains(out, "system") {
		t.Errorf("missing activity=system: %q", out)
	}
	if !strings.Contains(out, "version") || !strings.Contains(out, "1.0.0") {
		t.Errorf("missing version=1.0.0: %q", out)
	}
}

func TestConsoleHandler_LevelAbbreviations(t *testing.T) {
	cases := []struct {
		level slog.Level
		want  string
	}{
		{LevelTrace, "TRCE"},
		{slog.LevelDebug, "DEBG"},
		{slog.LevelInfo, "INFO"},
		{slog.LevelWarn, "WARN"},
		{slog.LevelError, "ERRO"},
	}
	clk := fixedClock()
	for _, tc := range cases {
		var buf bytes.Buffer
		h := NewConsoleHandlerWithClock(&buf, &slog.HandlerOptions{Level: LevelTrace}, clk, &boolFalse)
		logger := slog.New(h)
		logger.Log(nil, tc.level, "msg")
		if !strings.Contains(buf.String(), tc.want) {
			t.Errorf("level %v: want %q in %q", tc.level, tc.want, buf.String())
		}
	}
}

func TestConsoleHandler_LevelFilter(t *testing.T) {
	var buf bytes.Buffer
	clk := fixedClock()
	h := NewConsoleHandlerWithClock(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}, clk, &boolFalse)
	logger := slog.New(h)
	logger.Info("suppressed")
	logger.Warn("visible")
	out := buf.String()
	if strings.Contains(out, "suppressed") {
		t.Fatalf("info should be suppressed at warn: %q", out)
	}
	if !strings.Contains(out, "visible") {
		t.Fatalf("warn should appear: %q", out)
	}
}

func TestConsoleHandler_SubsystemAndModule(t *testing.T) {
	var buf bytes.Buffer
	clk := fixedClock()
	h := NewConsoleHandlerWithClock(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}, clk, &boolFalse)
	logger := slog.New(h).With("subsystem", "protojmap", "module", "Email")
	logger.Info("method called")
	out := buf.String()
	if !strings.Contains(out, "protojmap|Email") {
		t.Errorf("subsystem|module not rendered: %q", out)
	}
}

func TestConsoleHandler_SubsystemOnly(t *testing.T) {
	var buf bytes.Buffer
	clk := fixedClock()
	h := NewConsoleHandlerWithClock(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}, clk, &boolFalse)
	logger := slog.New(h).With("subsystem", "queue")
	logger.Info("delivery attempt")
	out := buf.String()
	if !strings.Contains(out, "queue") {
		t.Errorf("subsystem not rendered: %q", out)
	}
}

func TestConsoleHandler_FieldOrderActivity(t *testing.T) {
	// activity must appear before arbitrary extra fields.
	var buf bytes.Buffer
	clk := fixedClock()
	h := NewConsoleHandlerWithClock(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}, clk, &boolFalse)
	logger := slog.New(h)
	logger.Info("login", "user", "alice", "activity", "audit", "request_id", "r1")
	out := buf.String()
	// Find positions using key names (with possible padding before =).
	actIdx := strings.Index(out, " activity")
	requestIdx := strings.Index(out, " request_id")
	userIdx := strings.Index(out, " user")
	if actIdx < 0 {
		t.Fatalf("missing activity field: %q", out)
	}
	if requestIdx < 0 {
		t.Fatalf("missing request_id field: %q", out)
	}
	if userIdx < 0 {
		t.Fatalf("missing user field: %q", out)
	}
	// activity before request_id before user (lexicographic extras come last)
	if actIdx > requestIdx {
		t.Errorf("activity should come before request_id: %q", out)
	}
	if requestIdx > userIdx {
		t.Errorf("request_id should come before user: %q", out)
	}
}

func TestConsoleHandler_MultiLineAttr(t *testing.T) {
	var buf bytes.Buffer
	clk := fixedClock()
	h := NewConsoleHandlerWithClock(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}, clk, &boolFalse)
	logger := slog.New(h)
	logger.Error("crash", "stack", "line1\nline2\nline3")
	out := buf.String()
	if !strings.Contains(out, "  | line2") {
		t.Errorf("continuation marker missing: %q", out)
	}
	if !strings.Contains(out, "  | line3") {
		t.Errorf("third line continuation missing: %q", out)
	}
}

func TestConsoleHandler_QuotedValues(t *testing.T) {
	var buf bytes.Buffer
	clk := fixedClock()
	h := NewConsoleHandlerWithClock(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}, clk, &boolFalse)
	logger := slog.New(h)
	logger.Info("test", "addr", "192.0.2.1:8080", "msg_subject", "hello world")
	out := buf.String()
	// Values with spaces must be quoted.
	if !strings.Contains(out, `"hello world"`) {
		t.Errorf("space in value should be quoted: %q", out)
	}
	// Values without spaces should not be quoted.
	if strings.Contains(out, `"192.0.2.1:8080"`) {
		t.Errorf("addr without space should not be quoted: %q", out)
	}
}

func TestConsoleHandler_NOCOLORHonoured(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf bytes.Buffer
	clk := fixedClock()
	// Pass nil forceColor so the NO_COLOR env var is consulted.
	h := NewConsoleHandlerWithClock(&buf, &slog.HandlerOptions{}, clk, nil)
	logger := slog.New(h)
	logger.Info("no color please")
	out := buf.String()
	if strings.Contains(out, "\x1b[") {
		t.Errorf("ANSI escape found despite NO_COLOR: %q", out)
	}
}

func TestConsoleHandler_ForceColor(t *testing.T) {
	var buf bytes.Buffer
	clk := fixedClock()
	h := NewConsoleHandlerWithClock(&buf, &slog.HandlerOptions{}, clk, &boolTrue)
	logger := slog.New(h)
	logger.Info("colorful")
	out := buf.String()
	if !strings.Contains(out, "\x1b[") {
		t.Errorf("forceColor=true should produce ANSI escapes: %q", out)
	}
}

func TestConsoleHandler_KeyWidthAlignment(t *testing.T) {
	// All key=value pairs in a single record should be padded to the same
	// key width to aid scanning.
	var buf bytes.Buffer
	clk := fixedClock()
	h := NewConsoleHandlerWithClock(&buf, &slog.HandlerOptions{}, clk, &boolFalse)
	logger := slog.New(h)
	// short key "a" (len=1) and long key "long_key" (len=8) in same record.
	logger.Info("align", "a", "1", "long_key", "2")
	out := buf.String()
	// The padded key "a" should be followed by spaces to match "long_key".
	// Check that "a       =" appears (7 trailing spaces).
	if !strings.Contains(out, "a       =") {
		t.Errorf("key alignment missing: %q", out)
	}
}

func TestConsoleHandler_WithAttrs(t *testing.T) {
	var buf bytes.Buffer
	clk := fixedClock()
	h := NewConsoleHandlerWithClock(&buf, &slog.HandlerOptions{}, clk, &boolFalse)
	logger := slog.New(h).With("request_id", "r-123")
	logger.Info("request processed")
	out := buf.String()
	if !strings.Contains(out, "request_id=r-123") {
		t.Errorf("pre-scoped attr missing: %q", out)
	}
}

func TestConsoleHandler_WithGroup(t *testing.T) {
	var buf bytes.Buffer
	clk := fixedClock()
	h := NewConsoleHandlerWithClock(&buf, &slog.HandlerOptions{}, clk, &boolFalse)
	logger := slog.New(h)
	logger.WithGroup("req").Info("ok", "status", "200")
	// Groups are rendered as nested attrs in console output.
	out := buf.String()
	if !strings.Contains(out, "status") {
		t.Errorf("group attr missing: %q", out)
	}
}

func TestConsoleHandler_EmptyNoAttrs(t *testing.T) {
	var buf bytes.Buffer
	clk := fixedClock()
	h := NewConsoleHandlerWithClock(&buf, &slog.HandlerOptions{}, clk, &boolFalse)
	logger := slog.New(h)
	logger.Info("plain message")
	out := buf.String()
	if !strings.Contains(out, "plain message") {
		t.Fatalf("missing message: %q", out)
	}
	// Should end with a newline.
	if !strings.HasSuffix(out, "\n") {
		t.Fatalf("missing trailing newline: %q", out)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
