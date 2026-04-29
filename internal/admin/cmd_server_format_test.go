package admin

import (
	"bytes"
	"strings"
	"testing"
)

// TestWriteResult_NonTTYDefaultsToJSON asserts that writeResult emits
// pretty-printed JSON when the writer is not a terminal -- the case for
// pipes, redirects, and test buffers. Scripts can rely on JSON without
// passing --json on every invocation.
func TestWriteResult_NonTTYDefaultsToJSON(t *testing.T) {
	var buf bytes.Buffer
	body := map[string]any{
		"id":              float64(3),
		"canonical_email": "bob@example.test",
		"flags":           []any{},
	}
	if err := writeResult(&buf, &globalOptions{}, body); err != nil {
		t.Fatalf("writeResult: %v", err)
	}
	out := buf.String()
	if !strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("expected JSON output for non-TTY writer, got:\n%s", out)
	}
	if !strings.Contains(out, "\"canonical_email\": \"bob@example.test\"") {
		t.Errorf("expected JSON to carry canonical_email, got:\n%s", out)
	}
}

// TestWriteHumanResult renders a sample principal map and asserts the
// human form: sorted keys, aligned key column, RFC3339 timestamps
// formatted as local time, bools as yes/no, empty list shown as (none).
func TestWriteHumanResult(t *testing.T) {
	var buf bytes.Buffer
	body := map[string]any{
		"id":              float64(3),
		"canonical_email": "bob@example.test",
		"flags":           []any{},
		"totp_enabled":    false,
		"created_at":      "2026-04-29T06:00:57.985206Z",
	}
	if err := writeHumanResult(&buf, body); err != nil {
		t.Fatalf("writeHumanResult: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "canonical_email  bob@example.test") {
		t.Errorf("expected aligned key/value line, got:\n%s", out)
	}
	if !strings.Contains(out, "flags            (none)") {
		t.Errorf("expected empty list rendered as (none), got:\n%s", out)
	}
	if !strings.Contains(out, "totp_enabled     no") {
		t.Errorf("expected bool false as 'no', got:\n%s", out)
	}
	if strings.Contains(out, "2026-04-29T06:00:57") {
		t.Errorf("expected RFC3339 timestamp formatted as local time, got:\n%s", out)
	}
	if strings.Contains(out, "map[") {
		t.Errorf("must not leak Go-fmt map syntax in human output, got:\n%s", out)
	}
}
