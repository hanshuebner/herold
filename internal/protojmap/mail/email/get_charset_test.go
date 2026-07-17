package email

// Test for the header:*:asText dynamic header accessor's RFC 2047 charset
// handling (re #259). resolveHeaderProperty built its own zero-value
// mime.WordDecoder, which shares the #257 defect: an encoded-word in a
// charset htmlindex knows but the stdlib does not special-case inline
// (ISO-8859-15, windows-1252, ...) makes mime.WordDecoder.DecodeHeader
// return an error for the WHOLE header, and the fallback here discarded the
// decode instead of routing through the shared htmlindex-backed decoder.

import (
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/mailparse"
)

func TestResolveHeaderProperty_AsText_DecodesHtmlindexOnlyCharset(t *testing.T) {
	// "Grüße" in ISO-8859-15 bytes: 47 72 fc df 65 -> base64 R3L832U=
	raw := "From: sender@example.local\r\n" +
		"To: recipient@example.local\r\n" +
		"X-Test: =?ISO-8859-15?B?R3L832U=?=\r\n" +
		"Date: Mon, 26 May 2026 12:00:00 +0000\r\n" +
		"Message-ID: <test259@example.local>\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=us-ascii\r\n" +
		"\r\n" +
		"hi\r\n"

	parsed, err := mailparse.Parse(strings.NewReader(raw), mailparse.NewParseOptions())
	if err != nil {
		t.Fatalf("mailparse.Parse: %v", err)
	}

	got := resolveHeaderProperty(parsed, "header:X-Test:asText")
	const want = `"Grüße"`
	if string(got) != want {
		t.Errorf("resolveHeaderProperty asText = %s, want %s", got, want)
	}
}

// TestResolveHeaderProperty_AsText_UnsupportedCharsetDegradesGracefully
// guards that a charset unknown even to htmlindex still yields a
// best-effort non-empty, non-encoded-word value.
func TestResolveHeaderProperty_AsText_UnsupportedCharsetDegradesGracefully(t *testing.T) {
	raw := "From: sender@example.local\r\n" +
		"To: recipient@example.local\r\n" +
		"X-Test: =?totally-bogus-charset?Q?Hi=FC?=\r\n" +
		"Date: Mon, 26 May 2026 12:00:00 +0000\r\n" +
		"Message-ID: <test259b@example.local>\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=us-ascii\r\n" +
		"\r\n" +
		"hi\r\n"

	parsed, err := mailparse.Parse(strings.NewReader(raw), mailparse.NewParseOptions())
	if err != nil {
		t.Fatalf("mailparse.Parse: %v", err)
	}

	got := string(resolveHeaderProperty(parsed, "header:X-Test:asText"))
	if got == "null" {
		t.Fatalf("value blanked entirely for an unsupported charset")
	}
	if strings.Contains(got, "=?") {
		t.Errorf("value still contains an encoded-word marker: got %s", got)
	}
	if !strings.Contains(got, "Hi") {
		t.Errorf("value missing best-effort decoded content: got %s", got)
	}
}
