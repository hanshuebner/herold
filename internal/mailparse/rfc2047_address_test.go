// Tests for RFC 2047 encoded-word decoding in address-header display names
// (re #16). The Envelope contract is documented at types.go:101 — every
// address field must carry the decoded UTF-8 display name, never the raw
// `=?charset?encoding?text?=` form. The autocomplete dropdown in the suite
// reads `Envelope.From[0].Name` directly into the SeenAddress / Contact
// row, so any leakage of the encoded form lands in the user's contact list.

package mailparse

import (
	"bytes"
	"strings"
	"testing"
)

func TestEnvelopeDecodesRFC2047DisplayNames(t *testing.T) {
	cases := []struct {
		name      string
		header    string // value of the From: header
		wantName  string // expected Envelope.From[0].Name (decoded)
		wantEmail string // expected Envelope.From[0].Address
	}{
		{
			name:      "utf-8 quoted-printable (from issue #16)",
			header:    "=?utf-8?q?Hans_H=C3=BCbner?= <hans.huebner@gmail.com>",
			wantName:  "Hans Hübner",
			wantEmail: "hans.huebner@gmail.com",
		},
		{
			name:      "utf-8 base64",
			header:    "=?utf-8?B?SGFucyBIw7xibmVy?= <hans.huebner@gmail.com>",
			wantName:  "Hans Hübner",
			wantEmail: "hans.huebner@gmail.com",
		},
		{
			name:      "iso-8859-1 quoted-printable (Jörg)",
			header:    "=?iso-8859-1?Q?J=F6rg_M=FCller?= <joerg@example.de>",
			wantName:  "Jörg Müller",
			wantEmail: "joerg@example.de",
		},
		{
			// "Grüße" encoded as iso-8859-1 bytes: 47 72 fc df 65 → base64
			// "R3L832U=". The earlier draft of this case used UTF-8 bytes
			// labelled as iso-8859-1 (R3L8w59l), which mailparse correctly
			// decoded as latin-1 producing mojibake — a fixture bug, not a
			// parser bug.
			name:      "iso-8859-1 base64 (Grüße)",
			header:    "=?iso-8859-1?B?R3L832U=?= <gruesse@example.de>",
			wantName:  "Grüße",
			wantEmail: "gruesse@example.de",
		},
		{
			name:      "plain ASCII (no encoded-word, must round-trip)",
			header:    "Alice Smith <alice@example.com>",
			wantName:  "Alice Smith",
			wantEmail: "alice@example.com",
		},
		{
			name:      "two encoded-words concatenated",
			header:    "=?utf-8?q?Hans?= =?utf-8?q?_H=C3=BCbner?= <hans@example.com>",
			wantName:  "Hans Hübner",
			wantEmail: "hans@example.com",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := buildMessageWithFrom(tc.header)
			msg, err := Parse(bytes.NewReader(raw), NewParseOptions())
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(msg.Envelope.From) == 0 {
				t.Fatalf("envelope From is empty (header %q)", tc.header)
			}
			from := msg.Envelope.From[0]
			if from.Address != tc.wantEmail {
				t.Errorf("address: got %q want %q", from.Address, tc.wantEmail)
			}
			if from.Name != tc.wantName {
				t.Errorf("display name not decoded: got %q want %q (raw header was %q)",
					from.Name, tc.wantName, tc.header)
			}
			// Defence in depth: never leak the encoded-word marker even when
			// charset is partially recognised.
			if strings.Contains(from.Name, "=?") {
				t.Errorf("display name still contains encoded-word marker %q", from.Name)
			}
		})
	}
}

// buildMessageWithFrom returns the minimum valid RFC 5322 message that
// carries the given From: header value, plus enough other headers that
// enmime's parser will accept it.
func buildMessageWithFrom(from string) []byte {
	const tmpl = "From: " + "%s" + "\r\n" +
		"To: recipient@example.local\r\n" +
		"Subject: test\r\n" +
		"Date: Mon, 26 May 2026 12:00:00 +0000\r\n" +
		"Message-ID: <test@example.local>\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"hello\r\n"
	return []byte(strings.Replace(tmpl, "%s", from, 1))
}
