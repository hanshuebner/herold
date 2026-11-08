package queue

import (
	"strings"
	"testing"
)

// assertWireSafe fails when msg carries a bare CR or LF or a line longer
// than 998 octets.
func assertWireSafe(t *testing.T, msg []byte) {
	t.Helper()
	for i, c := range msg {
		if c == '\n' && (i == 0 || msg[i-1] != '\r') {
			t.Fatalf("bare LF at offset %d: %.60q", i, msg[max(0, i-30):min(len(msg), i+30)])
		}
		if c == '\r' && (i+1 >= len(msg) || msg[i+1] != '\n') {
			t.Fatalf("bare CR at offset %d", i)
		}
	}
	for _, l := range strings.Split(string(msg), "\r\n") {
		if len(l) > 998 {
			t.Fatalf("line of %d octets: %.40q", len(l), l)
		}
	}
}

// TestBuildDSN_MultiLineDiagnostic: a multi-line remote reply (joined with
// LF by the outbound session) yields no bare LF in the human-readable
// Diagnostic line or the Diagnostic-Code field, and the text survives
// with the line break replaced by a space.
func TestBuildDSN_MultiLineDiagnostic(t *testing.T) {
	out, err := buildDSN(dsnInput{
		Kind:           DSNKindFailure,
		ReportingMTA:   "dns; mail.example.test",
		From:           "postmaster@mail.example.test",
		To:             "alice@sender.test",
		FinalRcpt:      "bob@dest.test",
		DiagnosticCode: "smtp; data: 550 5.7.1 line one\nline two\r\nline three\rline four",
		Now:            testNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertWireSafe(t, out)
	if !strings.Contains(string(out), "Diagnostic: smtp; data: 550 5.7.1 line one line two") {
		t.Errorf("diagnostic text lost:\n%s", out)
	}
}

// TestBuildDSN_OverLongDiagnostic: an over-long diagnostic is bounded and
// Diagnostic-Code is folded so no line exceeds 998 octets.
func TestBuildDSN_OverLongDiagnostic(t *testing.T) {
	for _, diag := range []string{
		"smtp; " + strings.Repeat("blocked by policy ", 400),
		"smtp; " + strings.Repeat("x", 5000),
	} {
		out, err := buildDSN(dsnInput{
			Kind:           DSNKindFailure,
			From:           "postmaster@mail.example.test",
			To:             "alice@sender.test",
			FinalRcpt:      "bob@dest.test",
			DiagnosticCode: diag,
			Now:            testNow,
		})
		if err != nil {
			t.Fatal(err)
		}
		assertWireSafe(t, out)
		if len(out) > 4000 {
			t.Errorf("DSN not bounded: %d octets", len(out))
		}
	}
}
