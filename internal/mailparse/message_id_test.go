package mailparse_test

import (
	"testing"

	"github.com/hanshuebner/herold/internal/mailparse"
)

// TestExtractMessageID_FullMessage verifies the common case: a full
// RFC 5322 message with a Message-ID header among several others.
func TestExtractMessageID_FullMessage(t *testing.T) {
	raw := []byte("From: a@example.test\r\n" +
		"To: b@example.test\r\n" +
		"Message-ID: <abc123@example.test>\r\n" +
		"Subject: hi\r\n\r\n" +
		"body\r\n")
	got := mailparse.ExtractMessageID(raw)
	want := "<abc123@example.test>"
	if got != want {
		t.Fatalf("ExtractMessageID = %q, want %q", got, want)
	}
}

// TestExtractMessageID_HeaderOnly verifies the header block alone (no
// blank-line-terminated body) still parses -- Queue.Submit passes the
// bounded header-block buffer it already read for X-Herold-Recipient
// stripping, which may or may not include the full body.
func TestExtractMessageID_HeaderOnly(t *testing.T) {
	raw := []byte("From: a@example.test\r\nMessage-ID: <hdr-only@example.test>\r\n\r\n")
	got := mailparse.ExtractMessageID(raw)
	want := "<hdr-only@example.test>"
	if got != want {
		t.Fatalf("ExtractMessageID = %q, want %q", got, want)
	}
}

// TestExtractMessageID_Absent returns "" when the header is missing --
// e.g. a synthetic DSN or a hand-built test fixture.
func TestExtractMessageID_Absent(t *testing.T) {
	raw := []byte("From: a@example.test\r\nSubject: hi\r\n\r\nbody\r\n")
	if got := mailparse.ExtractMessageID(raw); got != "" {
		t.Fatalf("ExtractMessageID = %q, want empty", got)
	}
}

// TestExtractMessageID_Unparseable returns "" rather than erroring on
// bytes with no header/body structure at all.
func TestExtractMessageID_Unparseable(t *testing.T) {
	if got := mailparse.ExtractMessageID([]byte("not a message")); got != "" {
		t.Fatalf("ExtractMessageID = %q, want empty", got)
	}
	if got := mailparse.ExtractMessageID(nil); got != "" {
		t.Fatalf("ExtractMessageID(nil) = %q, want empty", got)
	}
}
