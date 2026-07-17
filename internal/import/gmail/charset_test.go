package gmail

// Tests for RFC 2047 encoded-word charset handling on the Gmail-import
// header paths (re #259). ParseGmailLabels, envelopeFromStdlib, and
// envelopeFromManualScan each built their own zero-value mime.WordDecoder,
// sharing the #257 defect: an encoded-word in a charset htmlindex knows but
// the stdlib does not special-case inline (ISO-8859-15, windows-1252, ...)
// makes mime.WordDecoder.DecodeHeader return an error for the WHOLE header,
// and each site's fallback used the still-encoded raw value.

import (
	"strings"
	"testing"
)

// grueseISO885915 is "Grüße" in ISO-8859-15 Q-encoding: 0x47 0x72 0xFC 0xDF
// 0x65, i.e. "Gr=FC=DFe".
const grueseISO885915 = "=?ISO-8859-15?Q?Gr=FC=DFe?="

func TestParseGmailLabels_DecodesHtmlindexOnlyCharset(t *testing.T) {
	got := ParseGmailLabels(grueseISO885915)
	want := []string{"Grüße"}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("ParseGmailLabels(%q) = %v, want %v", grueseISO885915, got, want)
	}
}

func TestEnvelopeFromStdlib_DecodesHtmlindexOnlyCharset(t *testing.T) {
	raw := []byte("From: sender@example.local\r\n" +
		"To: recipient@example.local\r\n" +
		"Subject: " + grueseISO885915 + "\r\n" +
		"Date: Mon, 26 May 2026 12:00:00 +0000\r\n" +
		"Message-ID: <test259@example.local>\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=us-ascii\r\n" +
		"\r\n" +
		"hi\r\n")

	env, ok := envelopeFromStdlib(raw)
	if !ok {
		t.Fatalf("envelopeFromStdlib: parse failed")
	}
	const want = "Grüße"
	if env.Subject != want {
		t.Errorf("Subject = %q, want %q", env.Subject, want)
	}
}

func TestEnvelopeFromManualScan_DecodesHtmlindexOnlyCharset(t *testing.T) {
	raw := []byte("From: sender@example.local\r\n" +
		"To: recipient@example.local\r\n" +
		"Subject: " + grueseISO885915 + "\r\n" +
		"Date: Mon, 26 May 2026 12:00:00 +0000\r\n" +
		"\r\n" +
		"hi\r\n")

	env := envelopeFromManualScan(raw)
	const want = "Grüße"
	if env.Subject != want {
		t.Errorf("Subject = %q, want %q", env.Subject, want)
	}
}

// bogusCharsetEncodedWord is an encoded-word in a charset name that is not a
// real IANA charset, so htmlindex.Get also rejects it.
const bogusCharsetEncodedWord = "=?totally-bogus-charset?Q?Hi=FC?="

func TestParseGmailLabels_UnsupportedCharsetDegradesGracefully(t *testing.T) {
	got := ParseGmailLabels(bogusCharsetEncodedWord)
	if len(got) != 1 {
		t.Fatalf("ParseGmailLabels(%q) = %v, want exactly one label", bogusCharsetEncodedWord, got)
	}
	if got[0] == "" {
		t.Fatalf("label blanked entirely for an unsupported charset")
	}
	if strings.Contains(got[0], "=?") {
		t.Errorf("label still contains an encoded-word marker: got %q", got[0])
	}
}

func TestEnvelopeFromStdlib_UnsupportedCharsetDegradesGracefully(t *testing.T) {
	raw := []byte("From: sender@example.local\r\n" +
		"To: recipient@example.local\r\n" +
		"Subject: " + bogusCharsetEncodedWord + "\r\n" +
		"Date: Mon, 26 May 2026 12:00:00 +0000\r\n" +
		"Message-ID: <test259c@example.local>\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=us-ascii\r\n" +
		"\r\n" +
		"hi\r\n")

	env, ok := envelopeFromStdlib(raw)
	if !ok {
		t.Fatalf("envelopeFromStdlib: parse failed")
	}
	if env.Subject == "" {
		t.Fatalf("subject blanked entirely for an unsupported charset")
	}
	if strings.Contains(env.Subject, "=?") {
		t.Errorf("subject still contains an encoded-word marker: got %q", env.Subject)
	}
}
