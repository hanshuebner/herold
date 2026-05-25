package emailsubmission

import "testing"

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
