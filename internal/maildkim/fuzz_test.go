package maildkim_test

import (
	"testing"

	"github.com/hanshuebner/herold/internal/maildkim"
)

// FuzzExtractSignatureTags exercises the DKIM-Signature tag scanner with
// arbitrary input. The scanner MUST NOT panic on any byte sequence; it
// returns an empty slice on malformed input.
func FuzzExtractSignatureTags(f *testing.F) {
	seeds := []string{
		"",
		"DKIM-Signature: v=1\r\n\r\n",
		"DKIM-Signature: v=1; a=rsa-sha256; s=brisbane; d=example.com;\r\n b=abcd\r\n\r\n",
		"dkim-signature:\r\n\r\nbody\r\n",
		"DKIM-Signature: v=1; s=\r\n;\r\n\r\n",
		"From: x@y\r\nDKIM-Signature: v=1; a=ed25519-sha256; s=s1; d=d1\r\n\r\n",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		tags := maildkim.ExtractSignatureTags(raw)
		for _, tg := range tags {
			// Tags are free-form strings on malformed input; assert
			// only that they contain no embedded CR/LF, which would
			// indicate a failed un-fold and could confuse downstream
			// Authentication-Results rendering.
			for _, s := range []string{tg.Selector, tg.Algorithm, tg.Domain} {
				for _, b := range []byte(s) {
					if b == '\r' || b == '\n' {
						t.Fatalf("tag contains CR/LF: %q", s)
					}
				}
			}
		}
	})
}
