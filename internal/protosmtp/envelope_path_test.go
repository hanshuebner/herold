package protosmtp

import "testing"

// TestExtractAngleAddr_RefusesWhitespaceAndControl: a path whose angle
// brackets enclose whitespace or control characters is a syntax error, so
// an authenticated submitter cannot queue an address that carries extra
// parameters (re #562).
func TestExtractAngleAddr_RefusesWhitespaceAndControl(t *testing.T) {
	for _, in := range []string{
		"<bob@example.com NOTIFY=NEVER>",
		"<bob@example.com\tfoo>",
		"<bob@exam\x01ple.com>",
	} {
		if addr, _, err := extractAngleAddr(in); err == nil {
			t.Errorf("%q accepted as %q", in, addr)
		}
	}
	// A quoted-string local part may contain whitespace (RFC 5321 4.1.2).
	for in, want := range map[string]string{
		`<"john doe"@example.com> SIZE=100`: `"john doe"@example.com`,
		`<"a\ b"@example.com>`:              `"a\ b"@example.com`,
		`<"a b"@example.com>`:               `"a b"@example.com`,
		`<užívateľ@example.test>`:           `užívateľ@example.test`,
	} {
		if addr, _, err := extractAngleAddr(in); err != nil || addr != want {
			t.Errorf("%q: addr=%q err=%v want %q", in, addr, err, want)
		}
	}
	// Unterminated quote, or whitespace after the quoted part, is refused.
	for _, in := range []string{`<"a b@example.com>`, `<"a b"@example.com NOTIFY=NEVER>`} {
		if addr, _, err := extractAngleAddr(in); err == nil {
			t.Errorf("%q accepted as %q", in, addr)
		}
	}
	if addr, rest, err := extractAngleAddr("<bob@example.com> NOTIFY=NEVER"); err != nil || addr != "bob@example.com" || rest != "NOTIFY=NEVER" {
		t.Errorf("legit path: %q %q %v", addr, rest, err)
	}
	if addr, _, err := extractAngleAddr("<>"); err != nil || addr != "" {
		t.Errorf("null path: %q %v", addr, err)
	}
}
