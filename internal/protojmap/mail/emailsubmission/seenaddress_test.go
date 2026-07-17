package emailsubmission

import (
	"strings"
	"testing"
)

// TestParseDisplayNames_DecodesEncodedWord guards the issue #16 fix:
// when the outbound To/Cc/Bcc header carries an RFC 2047 Q-encoded
// display name, parseDisplayNames must decode it before the value is
// stored as a SeenAddress (and surfaced in the compose autocomplete).
// Without the decode, users see "=?utf-8?q?Hans_H=C3=BCbner?=" in the
// dropdown instead of "Hans Hübner".
func TestParseDisplayNames_DecodesEncodedWord(t *testing.T) {
	cases := []struct {
		name     string
		header   string
		wantAddr string
		wantName string
	}{
		{
			name:     "Q-encoded utf-8 with quoted phrase",
			header:   `"=?utf-8?q?Hans_H=C3=BCbner?=" <hans.huebner@gmail.com>`,
			wantAddr: "hans.huebner@gmail.com",
			wantName: "Hans Hübner",
		},
		{
			name:     "Q-encoded utf-8 without surrounding quotes",
			header:   `=?utf-8?q?Hans_H=C3=BCbner?= <hans.huebner@gmail.com>`,
			wantAddr: "hans.huebner@gmail.com",
			wantName: "Hans Hübner",
		},
		{
			name:     "B-encoded utf-8",
			header:   `=?utf-8?B?SGFucyBIw7xibmVy?= <hans@example.com>`,
			wantAddr: "hans@example.com",
			wantName: "Hans Hübner",
		},
		{
			name:     "Plain ASCII name passes through",
			header:   `Alice <alice@example.com>`,
			wantAddr: "alice@example.com",
			wantName: "Alice",
		},
		{
			name:     "Bare address yields empty name",
			header:   `bob@example.com`,
			wantAddr: "bob@example.com",
			wantName: "",
		},
		{
			name:     "Garbage encoded-word falls back to raw",
			header:   `"=?utf-8?q?not%really%encoded?=" <c@example.com>`,
			wantAddr: "c@example.com",
			wantName: "not%really%encoded", // decoder accepts; Q decodes % as literal
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := parseDisplayNames(tc.header)
			got, ok := out[tc.wantAddr]
			if !ok {
				t.Fatalf("no entry for %q in %v", tc.wantAddr, out)
			}
			if got != tc.wantName {
				t.Errorf("name = %q, want %q", got, tc.wantName)
			}
		})
	}
}

// TestParseDisplayNames_DecodesHtmlindexOnlyCharset guards re #259: the
// package's own zero-value mime.WordDecoder (dnDecoder) had no
// CharsetReader, so an encoded-word display name in a charset htmlindex
// knows but the stdlib does not special-case inline (ISO-8859-15,
// windows-1252, ...) made mime.WordDecoder.DecodeHeader return an error
// for the whole header, and decodeDisplayName's error path returned the
// still-encoded raw name instead of the decoded text.
func TestParseDisplayNames_DecodesHtmlindexOnlyCharset(t *testing.T) {
	// "Grüße" in ISO-8859-15 bytes: 47 72 fc df 65 -> base64 R3L832U=
	header := `=?ISO-8859-15?B?R3L832U=?= <gretchen@example.com>`
	out := parseDisplayNames(header)
	const wantAddr = "gretchen@example.com"
	const wantName = "Grüße"
	got, ok := out[wantAddr]
	if !ok {
		t.Fatalf("no entry for %q in %v", wantAddr, out)
	}
	if got != wantName {
		t.Errorf("name = %q, want %q", got, wantName)
	}
}

// TestParseDisplayNames_UnsupportedCharsetDegradesGracefully guards that a
// charset unknown even to htmlindex still produces a best-effort non-empty,
// non-encoded-word name rather than the raw "=?charset?...?=" text.
func TestParseDisplayNames_UnsupportedCharsetDegradesGracefully(t *testing.T) {
	// "totally-bogus-charset" is not a real IANA charset name.
	header := `=?totally-bogus-charset?Q?Hi=FC?= <d@example.com>`
	out := parseDisplayNames(header)
	got, ok := out["d@example.com"]
	if !ok {
		t.Fatalf("no entry for d@example.com in %v", out)
	}
	if got == "" {
		t.Fatalf("name blanked entirely for an unsupported charset (raw header %q)", header)
	}
	if strings.Contains(got, "=?") {
		t.Errorf("name still contains an encoded-word marker: got %q", got)
	}
	if !strings.Contains(got, "Hi") {
		t.Errorf("name missing best-effort decoded content: got %q", got)
	}
}

// TestParseDisplayNames_MultipleRecipients_PreservesAllEncodedNames
// guards that the decode is applied per-address rather than per-header.
func TestParseDisplayNames_MultipleRecipients_PreservesAllEncodedNames(t *testing.T) {
	hdr := `"=?utf-8?q?Hans_H=C3=BCbner?=" <a@example.com>, ` +
		`=?utf-8?q?Andr=C3=A9?= <b@example.com>, ` +
		`Plain Charlie <c@example.com>`
	out := parseDisplayNames(hdr)
	want := map[string]string{
		"a@example.com": "Hans Hübner",
		"b@example.com": "André",
		"c@example.com": "Plain Charlie",
	}
	for addr, name := range want {
		if got := out[addr]; got != name {
			t.Errorf("name[%s] = %q, want %q", addr, got, name)
		}
	}
}
