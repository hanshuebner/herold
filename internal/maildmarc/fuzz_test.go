package maildmarc_test

import (
	"testing"

	"github.com/emersion/go-msgauth/dmarc"
)

// FuzzParseRecord feeds arbitrary strings into the DMARC record parser
// supplied by go-msgauth. The parser MUST NOT panic.
func FuzzParseRecord(f *testing.F) {
	seeds := []string{
		"",
		"v=DMARC1",
		"v=DMARC1; p=none",
		"v=DMARC1; p=reject; rua=mailto:a@b.example",
		"v=DMARC1; p=quarantine; adkim=s; aspf=s; pct=50",
		"v=DMARC1; p=reject; sp=quarantine; fo=1:d:s",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		if len(in) > 4096 {
			t.Skip()
		}
		_, _ = dmarc.Parse(in)
	})
}
