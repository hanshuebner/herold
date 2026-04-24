package mailspf_test

import (
	"testing"

	"github.com/hanshuebner/herold/internal/mailspf"
)

// FuzzParseRecord feeds arbitrary bytes into the SPF record parser. The
// parser MUST NOT panic; malformed input is reported as an error.
func FuzzParseRecord(f *testing.F) {
	seeds := []string{
		"",
		"v=spf1",
		"v=spf1 -all",
		"v=spf1 ip4:192.0.2.0/24 -all",
		"v=spf1 include:_spf.example.com ~all",
		"v=spf1 redirect=_spf.example.net",
		"v=spf1 a/24//64 mx -all",
		"v=spf1 exists:%{d} ~all",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		// Bound: the parser operates over a single record string; reject
		// inputs longer than 10 KiB to keep fuzz iterations fast. Real
		// DNS TXT records are at most 255-byte segments with the record
		// itself bounded far below 1 KiB.
		if len(in) > 10*1024 {
			t.Skip()
		}
		_, _ = mailspf.ParseRecord(in)
	})
}
