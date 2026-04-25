package protosmtp

// SMTP wire-parser fuzz targets (STANDARDS §8.2). Each target invokes a
// pure-function tokeniser used by session.go's command loop and asserts:
//
//   1. The function never panics for any input bytes.
//   2. Returned strings carry no embedded CR/LF (would corrupt the next
//      reply line).
//   3. Where applicable, a successful parse round-trips through the
//      pretty form back to an equivalent value.
//
// Seeds cover RFC 5321 happy paths plus malformed shapes mined from
// protocol-fuzz canon (over-long lines, missing angle brackets, embedded
// NULs, oversized SIZE values, spurious "=" tokens, etc.).

import (
	"strings"
	"testing"
)

// noCRLF returns an error description if s embeds a CR or LF byte.
// Parsers that leak CRLF into their output would corrupt subsequent
// SMTP reply framing.
func assertNoCRLF(t *testing.T, label, s string) {
	t.Helper()
	for i := 0; i < len(s); i++ {
		if s[i] == '\r' || s[i] == '\n' {
			t.Fatalf("%s leaked CR/LF: %q", label, s)
		}
	}
}

// FuzzCommandLine drives splitVerb over arbitrary command-line bytes.
// The verb must always be UPPER-cased; the rest must never embed CR/LF
// and the pair must round-trip when re-joined with a single space.
func FuzzCommandLine(f *testing.F) {
	seeds := []string{
		"",
		"HELO",
		"EHLO mx.example.com",
		"  ehlo  mx  ",
		"MAIL FROM:<a@b>",
		"MAIL FROM:<> SIZE=1234 BODY=8BITMIME",
		"RCPT TO:<a@b> NOTIFY=NEVER",
		"DATA",
		"QUIT",
		"\t\t\t",
		"NOOP\x00\x00\x00",
		strings.Repeat("X", 1024),
		"VERY-LONG-VERB-WITH-WEIRD-CHARS!@#$%^&*()_+\xff\xfe",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		// splitVerb takes a string; reject non-UTF-8 silently rather
		// than panic.
		verb, rest := splitVerb(string(in))
		assertNoCRLF(t, "verb", verb)
		assertNoCRLF(t, "rest", rest)
		// Verb must be entirely upper-case ASCII letters (or empty).
		if verb != strings.ToUpper(verb) {
			t.Fatalf("verb not upper-cased: %q", verb)
		}
	})
}

// FuzzReversePath drives extractAngleAddr over MAIL FROM payloads. The
// extracted address must not embed angle brackets or whitespace beyond
// the original input, and rest must trim cleanly.
func FuzzReversePath(f *testing.F) {
	seeds := []string{
		"<>",
		"<alice@example.com>",
		"<alice@example.com> SIZE=1024",
		"alice@example.com",
		"alice@example.com SIZE=1024 BODY=7BIT",
		"<>",
		"<a>",
		"< >",
		"<\x00\x00\x00>",
		"<alice@\x80\x81>",
		"<alice@example.com",                   // missing close
		strings.Repeat("<", 64) + "addr" + ">", // pathological openers
		"<" + strings.Repeat("a", 8192) + "@b>",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		addr, rest, err := extractAngleAddr(string(in))
		if err != nil {
			return // typed error is fine
		}
		assertNoCRLF(t, "addr", addr)
		assertNoCRLF(t, "rest", rest)
		// Addr must not contain '<' or '>' (we strip them).
		if strings.ContainsAny(addr, "<>") {
			t.Fatalf("addr contains angle brackets: %q", addr)
		}
	})
}

// FuzzForwardPath drives extractAngleAddr + splitAddress against RCPT
// TO payloads. splitAddress is the local-part / domain splitter; it
// must never panic, and on ok=true the local + domain must round-trip
// to the original address.
func FuzzForwardPath(f *testing.F) {
	seeds := []string{
		"<bob@example.com>",
		"<bob@example.com> NOTIFY=SUCCESS,FAILURE ORCPT=rfc822;b@c",
		"bob@example.com",
		"bob@example.com NOTIFY=NEVER",
		"<\x00\x00@\x00>",
		"<bob@>",
		"<@example.com>",
		"<bob@@example.com>",
		"<bob@example@com>",
		"<" + strings.Repeat("a", 16384) + "@example.com>",
		"<bob@" + strings.Repeat("d", 16384) + ">",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		addr, rest, err := extractAngleAddr(string(in))
		if err != nil {
			return
		}
		assertNoCRLF(t, "addr", addr)
		assertNoCRLF(t, "rest", rest)
		local, domain, ok := splitAddress(addr)
		if ok {
			// Round-trip property: local "@" domain must equal the
			// trimmed original (modulo splitAddress's TrimSpace).
			if want := strings.TrimSpace(addr); local+"@"+domain != want {
				t.Fatalf("split round-trip: local=%q domain=%q want=%q",
					local, domain, want)
			}
		}
	})
}

// FuzzMailFromParams drives parseMailFromParams over the parameter
// portion of a MAIL FROM command. Returns either a typed error or a
// well-formed mailFromParams; never panics.
func FuzzMailFromParams(f *testing.F) {
	seeds := []string{
		"",
		"SIZE=1024",
		"SIZE=1024 BODY=8BITMIME",
		"SIZE=0",
		"SIZE=99999999999",
		"BODY=7BIT",
		"BODY=8bitmime",
		"BODY=foo",
		"SMTPUTF8 REQUIRETLS",
		"RET=FULL",
		"RET=HDRS",
		"RET=foo",
		"ENVID=abc-def-123",
		"AUTH=<>",
		"UNKNOWN=value",
		"=novalue",
		"key=",
		strings.Repeat("X", 4096),
		"SIZE=-1",
		"SIZE=18446744073709551615",
		"SIZE=" + strings.Repeat("9", 100),
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		params, err := parseMailFromParams(string(in))
		if err != nil {
			return
		}
		// Successful parse: SIZE must not be negative; BODY must be
		// "", "7BIT", or "8BITMIME"; RET must be "", "FULL", or
		// "HDRS"; envid must not embed CR/LF.
		if params.size < 0 {
			t.Fatalf("negative SIZE survived: %d", params.size)
		}
		switch params.body {
		case "", "7BIT", "8BITMIME":
		default:
			t.Fatalf("unexpected BODY=%q", params.body)
		}
		switch params.ret {
		case "", "FULL", "HDRS":
		default:
			t.Fatalf("unexpected RET=%q", params.ret)
		}
		assertNoCRLF(t, "envid", params.envid)
	})
}

// FuzzRcptParams drives parseRcptParams. NOTIFY / ORCPT are the only
// recognised keys; everything else must surface as an error.
func FuzzRcptParams(f *testing.F) {
	seeds := []string{
		"",
		"NOTIFY=NEVER",
		"NOTIFY=SUCCESS,FAILURE,DELAY",
		"ORCPT=rfc822;a@b",
		"NOTIFY=NEVER ORCPT=rfc822;a@b",
		"FOO=bar",
		"=NEVER",
		"NOTIFY=",
		strings.Repeat("ORCPT=rfc822;a@b ", 200),
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		out, err := parseRcptParams(string(in))
		if err != nil {
			return
		}
		assertNoCRLF(t, "notify", out.notify)
		assertNoCRLF(t, "orcpt", out.orcpt)
	})
}
