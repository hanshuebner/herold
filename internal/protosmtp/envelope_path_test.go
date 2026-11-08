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
	if addr, rest, err := extractAngleAddr("<bob@example.com> NOTIFY=NEVER"); err != nil || addr != "bob@example.com" || rest != "NOTIFY=NEVER" {
		t.Errorf("legit path: %q %q %v", addr, rest, err)
	}
	if addr, _, err := extractAngleAddr("<>"); err != nil || addr != "" {
		t.Errorf("null path: %q %v", addr, err)
	}
}
