package mailparse_test

import (
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/mailparse"
)

// TestInjectXHeroldRecipient_PrependsHeader verifies that the helper
// adds the X-Herold-Recipient header at the top of the raw bytes. The
// header lives before any existing header so simple top-line consumers
// (IMAP FETCH BODY[HEADER] readers, header-name scanners) see it
// without continuation-line walking. REQ-FLOW-34.
func TestInjectXHeroldRecipient_PrependsHeader(t *testing.T) {
	raw := []byte("From: a@example.test\r\nSubject: hi\r\n\r\nbody")
	out := mailparse.InjectXHeroldRecipient(raw, "alice@example.test")
	got := string(out)
	if !strings.HasPrefix(got, "X-Herold-Recipient: alice@example.test\r\n") {
		t.Fatalf("missing X-Herold-Recipient at top; got prefix:\n%s", got[:min(64, len(got))])
	}
	// Body separator and tail must still be present unmodified.
	if !strings.HasSuffix(got, "\r\n\r\nbody") {
		t.Fatalf("body not preserved; got tail:\n%s", got[max(0, len(got)-32):])
	}
}

// TestInjectXHeroldRecipient_EmptyIsNoOp ensures pre-feature rows
// (received_to == "") skip the header (REQ-FLOW-34 final sentence).
func TestInjectXHeroldRecipient_EmptyIsNoOp(t *testing.T) {
	raw := []byte("From: a@example.test\r\n\r\nbody")
	out := mailparse.InjectXHeroldRecipient(raw, "")
	if string(out) != string(raw) {
		t.Fatalf("empty received_to should not modify the body; got:\n%s", string(out))
	}
}

// TestInjectXHeroldRecipient_StripsNewlines guards against header
// injection from a malformed received_to. A value containing CR/LF
// must collapse to a single-line header (no body smuggling). The
// resulting first line still contains exactly one `\r\n`, ending the
// synthetic header — never two — even when the caller smuggles CRLF
// into the value.
func TestInjectXHeroldRecipient_StripsNewlines(t *testing.T) {
	raw := []byte("From: a@example.test\r\n\r\nbody")
	out := mailparse.InjectXHeroldRecipient(raw, "alice@example.test\r\nBcc: evil@example.test")
	got := string(out)
	firstLineEnd := strings.Index(got, "\r\n")
	if firstLineEnd < 0 {
		t.Fatalf("no CRLF in output:\n%s", got)
	}
	firstLine := got[:firstLineEnd]
	if strings.Contains(firstLine, "\n") || strings.Contains(firstLine, "\r") {
		t.Fatalf("CRLF leaked into synthetic header line: %q", firstLine)
	}
	if !strings.HasPrefix(firstLine, "X-Herold-Recipient: alice@example.testBcc: evil@example.test") {
		t.Fatalf("unexpected first line: %q", firstLine)
	}
}

// TestStripXHeroldRecipient_RemovesHeader verifies REQ-FLOW-35: the
// outbound submission path strips X-Herold-Recipient (case-insensitive)
// before persisting the body. Other headers and the body remain intact.
func TestStripXHeroldRecipient_RemovesHeader(t *testing.T) {
	raw := []byte("From: a@example.test\r\nX-Herold-Recipient: leaked@example.test\r\nSubject: hi\r\n\r\nbody")
	out := mailparse.StripXHeroldRecipient(raw)
	if strings.Contains(strings.ToLower(string(out)), "x-herold-recipient") {
		t.Fatalf("header not stripped:\n%s", string(out))
	}
	if !strings.Contains(string(out), "From: a@example.test\r\n") {
		t.Fatalf("From: lost during strip:\n%s", string(out))
	}
	if !strings.Contains(string(out), "Subject: hi\r\n") {
		t.Fatalf("Subject: lost during strip:\n%s", string(out))
	}
	if !strings.HasSuffix(string(out), "\r\n\r\nbody") {
		t.Fatalf("body lost during strip; tail:\n%s", string(out)[max(0, len(string(out))-32):])
	}
}

// TestStripXHeroldRecipient_CaseInsensitive matches headers regardless
// of the case the forging client used.
func TestStripXHeroldRecipient_CaseInsensitive(t *testing.T) {
	raw := []byte("X-HEROLD-RECIPIENT: a@example.test\r\nSubject: x\r\n\r\nbody")
	out := mailparse.StripXHeroldRecipient(raw)
	if strings.Contains(strings.ToLower(string(out)), "x-herold-recipient") {
		t.Fatalf("upper-case variant not stripped:\n%s", string(out))
	}
}

// TestStripXHeroldRecipient_HandlesContinuation peels off a folded
// continuation line as part of the same field.
func TestStripXHeroldRecipient_HandlesContinuation(t *testing.T) {
	raw := []byte("X-Herold-Recipient: a@example.test\r\n (continuation)\r\nSubject: x\r\n\r\nbody")
	out := mailparse.StripXHeroldRecipient(raw)
	if strings.Contains(string(out), "continuation") {
		t.Fatalf("continuation line not stripped with parent:\n%s", string(out))
	}
	if !strings.Contains(string(out), "Subject: x\r\n") {
		t.Fatalf("Subject lost during strip:\n%s", string(out))
	}
}

// TestStripXHeroldRecipient_AbsentIsNoOp returns the original slice
// unchanged so the common case (no synthetic header) skips the alloc.
func TestStripXHeroldRecipient_AbsentIsNoOp(t *testing.T) {
	raw := []byte("From: a@example.test\r\nSubject: hi\r\n\r\nbody")
	out := mailparse.StripXHeroldRecipient(raw)
	if string(out) != string(raw) {
		t.Fatalf("strip should be a no-op when header is absent:\n%s", string(out))
	}
}

// TestRoundTripInjectStrip confirms the strip undoes the inject so the
// two operations together leave the message back at its persisted
// shape. Useful as a documentation example (REQ-FLOW-34 / 35 are
// designed to be exact inverses on the header byte).
func TestRoundTripInjectStrip(t *testing.T) {
	original := []byte("From: a@example.test\r\nSubject: hi\r\n\r\nbody")
	injected := mailparse.InjectXHeroldRecipient(original, "alice@example.test")
	stripped := mailparse.StripXHeroldRecipient(injected)
	if string(stripped) != string(original) {
		t.Fatalf("inject/strip not idempotent.\noriginal: %q\nstripped: %q", string(original), string(stripped))
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
