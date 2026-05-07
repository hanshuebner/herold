package mailparse

import "testing"

// FuzzParseReferences exercises the RFC 5322 References / In-Reply-To
// header parser. STANDARDS §8.2 requires every wire parser to ship a
// fuzz target; ParseReferences is the parser used by JMAP and IMAP
// threading to extract Message-ID tokens from inbound mail.
func FuzzParseReferences(f *testing.F) {
	seeds := []string{
		"",
		"<a@b>",
		"<a@b> <c@d>",
		"<a@b>\r\n  <c@d>",
		"<>",
		"<a@b",
		"a@b>",
		"<<<<>>>>",
		"<a@b> garbage <c@d> more",
		" \t<x@y> ",
		"<A@B> <a@b>",
		"<<a@b>>",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		out := ParseReferences(in)
		// Every returned id must be the lowercased, angle-bracket-free
		// content NormalizeMessageID promised. Drift between the two
		// would produce mismatched threading lookups.
		for _, id := range out {
			if id == "" {
				t.Fatalf("ParseReferences returned empty id from %q", in)
			}
			if id != NormalizeMessageID(id) {
				t.Fatalf("id %q is not idempotent under NormalizeMessageID (input %q)", id, in)
			}
		}
	})
}

// FuzzNormalizeMessageID covers the smaller normaliser. Idempotence
// (NormalizeMessageID(NormalizeMessageID(x)) == NormalizeMessageID(x))
// is the invariant; it is what the threading lookup relies on.
func FuzzNormalizeMessageID(f *testing.F) {
	seeds := []string{
		"",
		"a@b",
		"<a@b>",
		"  <A@B>  ",
		"<<a@b>>",
		"\nfoo\n",
		"<\x00>",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		once := NormalizeMessageID(in)
		twice := NormalizeMessageID(once)
		if once != twice {
			t.Fatalf("NormalizeMessageID not idempotent: %q -> %q -> %q", in, once, twice)
		}
	})
}
