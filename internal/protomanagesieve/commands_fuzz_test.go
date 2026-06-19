package protomanagesieve

import "testing"

// FuzzTokeniseLine exercises the ManageSieve (RFC 5804) line tokeniser.
// STANDARDS §8.2 requires every wire parser to ship a fuzz target;
// tokeniseLine handles quoted strings, escapes, and {N}/{N+} literal
// placeholders, which is exactly the kind of hand-rolled parser most
// likely to surface state-machine bugs on adversarial input.
//
// Input-size guard: tokeniseLine is called by readCommand on lines
// already bounded by readLineRaw at maxCmdLineBytes (session.go).
// Inputs exceeding that limit are skipped so the fuzzer stays in the
// realistic input domain and per-execution memory stays bounded.
func FuzzTokeniseLine(f *testing.F) {
	seeds := []string{
		"",
		"CAPABILITY",
		"PUTSCRIPT \"name\" {25}",
		"PUTSCRIPT \"name\" {25+}",
		"GETSCRIPT \"name\"",
		"AUTHENTICATE \"PLAIN\" \"AGFAdGVzdAA=\"",
		`"escape\"inside"`,
		"NOOP",
		"{0}",
		"{99999999}",
		"  ",
		"\t\t\"x\"\t\t",
		`"unterminated`,
		"x{",
		"x {",
		"\"quoted\\\\backslash\"",
		// At the production limit — boundary behavior is worth fuzzing.
		string(make([]byte, maxCmdLineBytes)),
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, line string) {
		// The production session rejects lines longer than maxCmdLineBytes
		// before tokeniseLine is called.
		if len(line) > maxCmdLineBytes {
			return
		}
		// The tokeniser must never panic, regardless of input shape.
		// Parse errors on malformed literals are expected. The structural
		// invariant we enforce is finished/lit consistency: when the
		// tokeniser reports a literal placeholder, it must not also flag
		// the line as terminated, since the caller is expected to read
		// the literal bytes before resuming.
		_, finished, lit, err := tokeniseLine(line)
		if err != nil {
			return
		}
		if lit != nil && finished {
			t.Fatalf("tokeniseLine reported finished=true with a non-nil literal spec for %q", line)
		}
	})
}

// FuzzDecodeSASLPayload covers the base64 decoder used on AUTHENTICATE
// continuation lines. The empty-payload sentinel ("=" / "") is the
// trickiest spot per RFC 4422 §5; a regression here would let an
// attacker ship a hand-rolled empty payload past the auth state machine.
//
// Input-size guard: SASL continuation lines arrive via readLineRaw,
// which caps at maxCmdLineBytes. Inputs exceeding that bound are skipped.
func FuzzDecodeSASLPayload(f *testing.F) {
	seeds := []string{
		"",
		"=",
		"AGFAdGVzdAA=",
		"   ",
		"not-base64!",
		"AAAA",
		"A",
		"AB",
		"ABC",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		// SASL payloads arrive on lines bounded by readLineRaw at
		// maxCmdLineBytes. Skip inputs outside that domain.
		if len(in) > maxCmdLineBytes {
			return
		}
		_, _ = decodeSASLPayload(in) // smoke: must not panic
		_, _ = decodeSASLLine(in)
	})
}
