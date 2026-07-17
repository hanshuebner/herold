// Tests for RFC 2047 encoded-word header charset decoding (re #257). Before
// this fix, internal/mailparse/parse.go decoded headers with a zero-value
// mime.WordDecoder, which has no CharsetReader and therefore only recognizes
// utf-8/iso-8859-1/us-ascii inline; any other charset name (e.g. ISO-8859-15)
// made mime.WordDecoder.DecodeHeader return ("", err) for the WHOLE header,
// not just the failing word, blanking the Subject entirely.

package mailparse

import (
	"bytes"
	"strings"
	"testing"
)

func TestSubjectDecodesISO885915(t *testing.T) {
	// Exact reproduction from issue #257: "Angebot IT-Museumsstücke (fwd)"
	// encoded as ISO-8859-15 Q-encoding. 0xFC is "ü" in both ISO-8859-1 and
	// ISO-8859-15 (they differ only in a handful of currency/typography
	// code points, none used here).
	raw := buildMessageWithSubject("=?ISO-8859-15?Q?Angebot_IT-Museumsst=FCcke_=28fwd=29?=")

	msg, err := Parse(bytes.NewReader(raw), NewParseOptions())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	const want = "Angebot IT-Museumsstücke (fwd)"
	if msg.Envelope.Subject != want {
		t.Fatalf("subject: got %q want %q", msg.Envelope.Subject, want)
	}
}

func TestHeaderDecodesOtherHtmlindexCharsets(t *testing.T) {
	cases := []struct {
		name   string
		header string // raw Subject header value
		want   string
	}{
		{
			name:   "windows-1252",
			header: "=?windows-1252?Q?Caf=E9_M=FCnchen?=",
			want:   "Café München",
		},
		{
			name:   "iso-8859-2",
			header: "=?iso-8859-2?Q?Zaj=EAcz?=",
			want:   "Zajęcz",
		},
		{
			name: "iso-8859-15 base64",
			// "Grüße" in ISO-8859-15 bytes: 47 72 fc df 65 -> base64 R3L832U=
			header: "=?ISO-8859-15?B?R3L832U=?=",
			want:   "Grüße",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := buildMessageWithSubject(tc.header)
			msg, err := Parse(bytes.NewReader(raw), NewParseOptions())
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if msg.Envelope.Subject != tc.want {
				t.Errorf("subject: got %q want %q (raw header %q)", msg.Envelope.Subject, tc.want, tc.header)
			}
		})
	}
}

// TestHeaderUnsupportedCharsetDegradesGracefully covers the second checklist
// item: a charset unknown even to htmlindex must not blank the whole header.
// headerCharsetReader falls back to a best-effort Latin-1 decode of the
// offending word's bytes rather than propagating an error, so the Subject
// stays non-empty and still carries the surrounding literal text.
func TestHeaderUnsupportedCharsetDegradesGracefully(t *testing.T) {
	// "totally-bogus-charset" is not a real IANA charset name and htmlindex.Get
	// returns an error for it. 0x48 0x69 0xFC = "Hi" + a high byte.
	header := "prefix =?totally-bogus-charset?Q?Hi=FC?= suffix"
	raw := buildMessageWithSubject(header)

	msg, err := Parse(bytes.NewReader(raw), NewParseOptions())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if msg.Envelope.Subject == "" {
		t.Fatalf("subject blanked entirely for an unsupported charset (raw header %q)", header)
	}
	if !strings.Contains(msg.Envelope.Subject, "prefix") || !strings.Contains(msg.Envelope.Subject, "suffix") {
		t.Errorf("subject lost surrounding literal text: got %q", msg.Envelope.Subject)
	}
	// Latin-1 best-effort decode of 0x48 0x69 0xFC is "Hiü".
	if !strings.Contains(msg.Envelope.Subject, "Hi") {
		t.Errorf("subject missing best-effort decoded word content: got %q", msg.Envelope.Subject)
	}
	if strings.Contains(msg.Envelope.Subject, "=?") {
		t.Errorf("subject still contains an encoded-word marker: got %q", msg.Envelope.Subject)
	}
}

// buildMessageWithSubject returns the minimum valid RFC 5322 message carrying
// the given raw Subject header value.
func buildMessageWithSubject(subject string) []byte {
	const tmpl = "From: sender@example.local\r\n" +
		"To: recipient@example.local\r\n" +
		"Subject: %s\r\n" +
		"Date: Mon, 26 May 2026 12:00:00 +0000\r\n" +
		"Message-ID: <test257@example.local>\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=us-ascii\r\n" +
		"\r\n" +
		"hi\r\n"
	return []byte(strings.Replace(tmpl, "%s", subject, 1))
}
