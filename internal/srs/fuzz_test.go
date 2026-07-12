package srs_test

import (
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/srs"
)

// FuzzDecode feeds arbitrary local-part strings into the SRS0/SRS1
// parser. Decode MUST NOT panic on any input; malformed or forged
// addresses are reported as an error.
func FuzzDecode(f *testing.F) {
	secrets := [][]byte{[]byte("fuzz-secret-material-32-bytes!!!")}
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)

	seeds := []string{
		"",
		"alice",
		"SRS0=",
		"SRS0=hash=ts=domain=local",
		srs.EncodeSRS0(secrets[0], now, "alice", "sender.example"),
		srs.EncodeSRS1(secrets[0], "SRS0=abcd=AB=example.com=bob", "relay.example"),
		"SRS1=",
		"SRS1=hash=domain==local",
		"SRS0====",
		"SRS1====",
		"srs0=hash=ts=domain=local",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		if len(in) > 4096 {
			t.Skip()
		}
		_, _, _ = srs.Decode(secrets, now, srs.MaxAgeDefault, in)
	})
}
