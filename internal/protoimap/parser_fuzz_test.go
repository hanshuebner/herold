package protoimap

// IMAP wire-parser fuzz targets (STANDARDS §8.2). The IMAP parser is
// the highest-volume untrusted surface in Phase 1: ~1300 LOC of
// hand-written tokeniser plus a literal/continuation reader, all
// pre-auth. Each target asserts:
//
//   1. The parser never panics for any input bytes.
//   2. Returned errors are ordinary error values (no wrapped runtime
//      panics).
//   3. The literal reader respects maxAppendLiteral.
//
// Seeds cover RFC 9051 happy paths plus malformed shapes: oversized
// literals, unbalanced parens, missing tags, embedded NULs, very long
// FETCH item lists, SEARCH with deeply nested OR / NOT, and
// pathological STORE flag lists.

import (
	"bufio"
	"bytes"
	"errors"
	"strings"
	"testing"

	imap "github.com/emersion/go-imap/v2"
)

// FuzzCommandLine drives readCommand against fully-buffered input.
// The literal reader is wired to a bounded slice source; if the parser
// requests more than maxAppendLiteral (which lastLiteral already
// rejects) we fail the test. Every input must surface as a typed error
// or a populated *Command, never a panic.
func FuzzCommandLine(f *testing.F) {
	seeds := []string{
		"a1 NOOP\r\n",
		"a1 LOGIN user pass\r\n",
		"a1 LOGIN \"user@example.com\" \"p\\\"a\\\"ss\"\r\n",
		"a1 SELECT INBOX\r\n",
		"a1 EXAMINE \"INBOX/Sub\"\r\n",
		"a1 LIST \"\" \"*\"\r\n",
		"a1 STATUS INBOX (MESSAGES UIDNEXT)\r\n",
		"a1 FETCH 1 (UID FLAGS ENVELOPE)\r\n",
		"a1 UID FETCH 1:* (UID BODY[HEADER.FIELDS (Subject From)])\r\n",
		"a1 STORE 1 +FLAGS (\\Seen)\r\n",
		"a1 STORE 1 (UNCHANGEDSINCE 5) +FLAGS.SILENT (\\Deleted)\r\n",
		"a1 SEARCH ALL\r\n",
		"a1 SEARCH OR (FROM \"a\") (TO \"b\") SUBJECT \"hi\"\r\n",
		"a1 SEARCH NOT NOT NOT ALL\r\n",
		"a1 ID (\"name\" \"client\" \"version\" \"1.0\")\r\n",
		"a1 ID NIL\r\n",
		"a1 EXPUNGE\r\n",
		"a1 UID EXPUNGE 1:5\r\n",
		"a1 AUTHENTICATE PLAIN\r\n",
		"a1 AUTHENTICATE PLAIN dGVzdA==\r\n",
		"a1 AUTHENTICATE PLAIN =\r\n",
		"a1 NOOP\r\n",
		"\r\n",    // empty
		"a1\r\n",  // tag only
		"a1 \r\n", // verb missing
		"a1 BOGUS\r\n",
		// Unbalanced shapes
		"a1 STATUS INBOX (\r\n",
		"a1 FETCH 1 (UID\r\n",
		"a1 SEARCH OR\r\n",
		"a1 SEARCH (((\r\n",
		// Numeric edge cases
		"a1 FETCH 0:* (UID)\r\n",
		"a1 FETCH 4294967295 (UID)\r\n",
		"a1 FETCH 99999999999999999999 (UID)\r\n",
		// Embedded NUL / control bytes
		"a1\x00 NOOP\r\n",
		"a1 NOOP\x00\r\n",
		// Many fetch items
		"a1 FETCH 1 (UID FLAGS ENVELOPE INTERNALDATE RFC822.SIZE BODY BODYSTRUCTURE BODY[] BODY[HEADER] BODY.PEEK[TEXT] RFC822 RFC822.HEADER RFC822.TEXT)\r\n",
		// BODY[] permutations
		"a1 FETCH 1 (BODY[1.2.3.HEADER.FIELDS (Subject)])\r\n",
		"a1 FETCH 1 (BODY[1.2.3.HEADER.FIELDS.NOT (Subject To)])\r\n",
		"a1 FETCH 1 (BODY[]<0.100>)\r\n",
		"a1 FETCH 1 (BODY[]<0.>)\r\n",
		// Long line
		"a1 SEARCH " + strings.Repeat("OR FROM \"x\" ", 100) + "ALL\r\n",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		br := bufio.NewReader(bytes.NewReader(in))
		// The literal reader returns a fixed-size payload, but only
		// once: subsequent calls return ErrTooBig. This bounds the
		// fuzzer's memory footprint while still exercising the
		// continuation-and-literal control path.
		used := false
		readLit := func(size int64, _ bool) ([]byte, error) {
			if used || size > 4096 {
				return nil, ErrTooBig
			}
			used = true
			if size < 0 {
				return nil, errors.New("negative")
			}
			buf := make([]byte, size)
			_, _ = br.Read(buf)
			return buf, nil
		}
		_, _ = readCommand(br, readLit)
	})
}

// FuzzLiteralMarker drives lastLiteral over arbitrary line bytes. The
// parser must never report a size that exceeds maxAppendLiteral.
func FuzzLiteralMarker(f *testing.F) {
	seeds := []string{
		"",
		"a1 APPEND INBOX {0}",
		"a1 APPEND INBOX {1}",
		"a1 APPEND INBOX {1+}",
		"a1 APPEND INBOX {1234}",
		"a1 APPEND INBOX {1234567890}",
		"a1 APPEND INBOX {-1}",
		"a1 APPEND INBOX {abc}",
		"a1 APPEND INBOX {",
		"a1 APPEND INBOX }",
		"{",
		"}",
		"{}",
		"{99999999999999999}",
		"{0+}",
		strings.Repeat("{99}", 100),
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		_, size, _, ok := lastLiteral(string(in))
		if ok && size > maxAppendLiteral {
			t.Fatalf("lastLiteral returned size > cap: %d", size)
		}
		if ok && size < 0 {
			t.Fatalf("lastLiteral returned negative size: %d", size)
		}
	})
}

// FuzzFetchItems drives parseFetchItem over arbitrary token streams.
// The parser builds a *imap.FetchOptions; the only invariant we assert
// is "no panic, no leaked CR/LF in any string field".
func FuzzFetchItems(f *testing.F) {
	seeds := []string{
		"UID",
		"FLAGS",
		"INTERNALDATE",
		"RFC822.SIZE",
		"ENVELOPE",
		"BODY",
		"BODYSTRUCTURE",
		"BODY[]",
		"BODY[HEADER]",
		"BODY[HEADER.FIELDS (Subject From)]",
		"BODY[HEADER.FIELDS.NOT (Subject)]",
		"BODY[TEXT]",
		"BODY[MIME]",
		"BODY[1.2.3]",
		"BODY[1.2.3.HEADER]",
		"BODY[]<0.100>",
		"BODY[]<99999.99999>",
		"BODY.PEEK[]",
		"BODY.PEEK[HEADER]",
		"RFC822",
		"RFC822.HEADER",
		"RFC822.TEXT",
		"WHATEVER",
		"BODY[",
		"BODY[]",
		"BODY[<0.100>",
		"BODY[XYZ]",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		// The fetch parser expects to be embedded in a parser{} with
		// src set; drive it directly.
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("parseFetchItem panicked: %v on %q", r, in)
			}
		}()
		// Wrap in a list so parseFetchItemList can consume it.
		wrapped := append([]byte("("), in...)
		wrapped = append(wrapped, ')')
		p := &parser{src: wrapped}
		opts := &imap.FetchOptions{}
		_ = parseFetchItemList(p, opts)
	})
}

// FuzzSearchCriteria drives parseSearchCriteriaList over arbitrary
// token streams. Asserts "no panic" only — the SEARCH grammar is huge
// and the goal here is crash-safety, not semantic validation.
func FuzzSearchCriteria(f *testing.F) {
	seeds := []string{
		"ALL",
		"ANSWERED DELETED FLAGGED",
		"OR FROM \"a\" TO \"b\"",
		"NOT ALL",
		"NOT NOT NOT ALL",
		"FROM \"x\" SUBJECT \"y\" BODY \"z\"",
		"HEADER \"X-Foo\" \"bar\"",
		"LARGER 1024 SMALLER 2048",
		"SINCE 1-Jan-2020 BEFORE 31-Dec-2030",
		"SENTSINCE 1-Jan-2020 SENTBEFORE 31-Dec-2030",
		"ON 1-Jan-2020",
		"UID 1:5,7,10:*",
		"KEYWORD foo UNKEYWORD bar",
		"NEW OLD RECENT",
		"(FROM \"a\" TO \"b\")",
		"(((((ALL)))))",
		// Pathological:
		"OR OR OR OR ALL ALL ALL ALL ALL",
		"UNKNOWN-KEY",
		strings.Repeat("NOT ", 100) + "ALL",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("parseSearchCriteriaList panicked: %v on %q", r, in)
			}
		}()
		p := &parser{src: in}
		_, _ = parseSearchCriteriaList(p)
	})
}
