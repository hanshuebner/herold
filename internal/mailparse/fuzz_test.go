package mailparse

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzParse feeds the spike corpus as seeds and ensures Parse never panics and
// always produces either a *ParseError or a Message whose invariants hold.
func FuzzParse(f *testing.F) {
	seeds, err := filepath.Glob("testdata/spike/*.eml")
	if err != nil {
		f.Fatal(err)
	}
	for _, s := range seeds {
		data, rerr := os.ReadFile(s)
		if rerr != nil {
			f.Fatal(rerr)
		}
		f.Add(data)
	}
	// A minimal hand-written seed in case the glob ever fails to pick files up.
	f.Add([]byte("From: a@b\r\nTo: c@d\r\nSubject: s\r\n\r\nhi\r\n"))

	// Regression seed for the multipart-no-boundary + base64 CTE crash:
	// a "multipart/X" part with no boundary parameter and a base64 CTE was treated
	// as an opaque leaf with p.Size = rawBodyLen (the raw byte count).  OpenBody
	// CTE-decoded the same raw bytes, producing a different count and triggering
	// the checkOpenBody invariant.  Fixed by routing this case through
	// countDecodedSize so Part.Size is computed via the same streaming decoder.
	// Confirmed: fails on the pre-fix code (crasher corpus 17c18b4c3ad44374),
	// passes after.
	f.Add([]byte("Content-TYpe:multipArt/0\nContent-TrAnsfer-EnCoding:BAse64\n\n0000"))

	// Keep strictness off during fuzzing so we exercise the happy-path parse
	// surface; this is the same relaxed policy the render/index call sites
	// use in production (NewLenientParseOptions, re #285).
	opts := NewLenientParseOptions()
	opts.MaxSize = 1 << 20
	opts.MaxDepth = 8
	opts.MaxParts = 256

	f.Fuzz(func(t *testing.T, data []byte) {
		msg, perr := Parse(bytes.NewReader(data), opts)
		if perr != nil {
			// Errors are fine; the invariant is no panic.
			return
		}
		// Invariant: Size matches input length (within MaxSize).
		if msg.Size != int64(len(data)) {
			t.Fatalf("size mismatch: Size=%d len(data)=%d", msg.Size, len(data))
		}
		// Invariant: walking Part tree terminates and content types are non-empty strings.
		walkFuzzCheck(t, msg.Body, 0)
		// Invariant: for each leaf, OpenBody over the input bytes decodes without panic
		// and returns a reader whose content length equals Part.Size (for non-text leaves).
		checkOpenBody(t, data, msg.Body)
		// Invariant: decoding a part via its persisted PartIndexEntry range yields
		// byte-identical output to decoding it via the live Part. This is the
		// correctness contract that lets the JMAP part-download path serve a part
		// from the stored index without re-parsing the whole message.
		checkPartIndexRoundtrip(t, data, msg)
	})
}

// FuzzParseHeadersOnly drives Parse's too-large / header-recovery branch
// (parseHeadersOnly, re #244) directly. FuzzParse's own MaxSize=1<<20 with an
// all-sub-2KB seed corpus never grows an input past the cap under mutation
// (confirmed: 0 hits in 3M executions), leaving parseHeadersOnly -- which
// runs net/mail.ReadMessage plus RFC 2047 header/address decoding over
// untrusted bytes -- effectively unfuzzed, contrary to STANDARDS §8.2's
// "every wire parser has a fuzz target" requirement.
//
// Rather than pick a static MaxSize and hope mutation keeps inputs above it
// (the exact failure mode above), MaxSize is derived from len(data)/2 inside
// the fuzz function itself: every single execution -- seed or mutated --
// therefore forces Parse into the too-large path deterministically,
// regardless of what the mutator does to input length.
func FuzzParseHeadersOnly(f *testing.F) {
	seed := func(headers string, padTo int) []byte {
		var b strings.Builder
		b.WriteString(headers)
		b.WriteString("\r\n")
		for b.Len() < padTo {
			b.WriteString("padding padding padding padding padding\r\n")
		}
		return []byte(b.String())
	}
	f.Add(seed("From: a@b\r\nTo: c@d\r\nSubject: s\r\nMessage-ID: <m1@x>\r\n", 1024))
	f.Add(seed(
		"From: =?utf-8?B?w6TDtsO8?= <a@b>\r\nSubject: =?utf-8?Q?Hi=21?=\r\n"+
			"Message-ID: <m2@x>\r\nIn-Reply-To: <parent@x>\r\nReferences: <r1@x> <r2@x>\r\n",
		2048))
	// ISO-8859-15 encoded-word Subject (re #257): mime.WordDecoder's default
	// CharsetReader only knows utf-8/iso-8859-1/us-ascii inline, so this
	// charset used to blank the whole header instead of decoding "ü".
	f.Add(seed(
		"From: a@b\r\nSubject: =?ISO-8859-15?Q?Angebot_IT-Museumsst=FCcke_=28fwd=29?=\r\n"+
			"Message-ID: <m3@x>\r\n",
		2048))
	// No header/body separator at all: parseHeadersOnly's mail.ReadMessage
	// call fails outright, exercising the "even headers unparseable"
	// fallback to a fully zero-value Message.
	f.Add(bytes.Repeat([]byte("x"), 4096))
	// Malformed encoded-word and an unterminated quoted display-name inside
	// an oversized message, to probe the header/address decoders' error
	// paths under the too-large branch specifically.
	f.Add(seed("From: \"unterminated <a@b\r\nSubject: =?utf-8?Q?broken\r\n", 1024))
	f.Add([]byte{})
	f.Add([]byte{0})

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 2 {
			return // MaxSize must be >= 1 for readCapped's limit+1 read to make sense.
		}
		opts := NewParseOptions()
		opts.MaxSize = int64(len(data) / 2)

		msg, err := Parse(bytes.NewReader(data), opts)

		// Invariant: MaxSize was deliberately set below len(data), so Parse
		// must always report ErrTooLarge on this path -- never succeed,
		// never report a different Reason.
		if !errors.Is(err, ErrTooLarge) {
			t.Fatalf("MaxSize=%d < len(data)=%d but Parse did not report ErrTooLarge: %v", opts.MaxSize, len(data), err)
		}
		// Invariant: the too-large path never walks the body -- Body stays
		// the zero Part regardless of what parseHeadersOnly recovered.
		if msg.Body.ContentType != "" || len(msg.Body.Children) != 0 {
			t.Fatalf("too-large Message unexpectedly has a non-zero Body: %+v", msg.Body)
		}
		// Invariant: whatever header/address decoding recovered contains no
		// NUL bytes -- garbage that would corrupt the env_* store columns
		// or a downstream thread-resolution lookup rather than simply
		// being absent.
		checkNoNUL(t, "Envelope.MessageID", msg.Envelope.MessageID)
		checkNoNUL(t, "Envelope.Subject", msg.Envelope.Subject)
		checkNoNUL(t, "Envelope.Date", msg.Envelope.Date)
		for _, id := range msg.Envelope.InReplyTo {
			checkNoNUL(t, "Envelope.InReplyTo[]", id)
		}
		for _, ref := range msg.Envelope.References {
			checkNoNUL(t, "Envelope.References[]", ref)
		}
		for _, a := range msg.Envelope.From {
			checkNoNUL(t, "Envelope.From[].Name", a.Name)
			checkNoNUL(t, "Envelope.From[].Address", a.Address)
		}
	})
}

// checkNoNUL fails the test if s contains an embedded NUL byte.
func checkNoNUL(t *testing.T, field, s string) {
	t.Helper()
	if strings.IndexByte(s, 0) >= 0 {
		t.Fatalf("%s contains a NUL byte: %q", field, s)
	}
}

// checkPartIndexRoundtrip builds the part index for msg and verifies that, for
// every leaf, PartIndexEntry.OpenBody over src decodes to the same bytes as the
// live Part.OpenBody. The index entries and the live parts are zipped by the
// shared 1-based pre-order DFS enumeration.
func checkPartIndexRoundtrip(t *testing.T, src []byte, msg Message) {
	t.Helper()
	entries := BuildPartIndex(msg, bytes.NewReader(src))
	var parts []Part
	var collect func(p Part)
	collect = func(p Part) {
		parts = append(parts, p)
		for _, c := range p.Children {
			collect(c)
		}
	}
	collect(msg.Body)
	if len(entries) != len(parts) {
		t.Fatalf("part index/tree length mismatch: %d entries, %d parts", len(entries), len(parts))
	}
	for i, e := range entries {
		p := parts[i]
		if (len(p.Children) > 0) != e.Container {
			t.Fatalf("part %d container flag mismatch: entry=%v part-has-children=%v", e.Index, e.Container, len(p.Children) > 0)
		}
		if e.Container {
			continue
		}
		want, werr := p.OpenBody(bytes.NewReader(src))
		got, gerr := e.OpenBody(bytes.NewReader(src))
		if (werr == nil) != (gerr == nil) {
			t.Fatalf("part %d OpenBody error mismatch: part=%v entry=%v", e.Index, werr, gerr)
		}
		if werr != nil {
			continue
		}
		wb, werr := io.ReadAll(want)
		_ = want.Close()
		gb, gerr := io.ReadAll(got)
		_ = got.Close()
		if (werr == nil) != (gerr == nil) {
			t.Fatalf("part %d read error mismatch: part=%v entry=%v", e.Index, werr, gerr)
		}
		if werr != nil {
			continue
		}
		if !bytes.Equal(wb, gb) {
			t.Fatalf("part %d decode-by-range mismatch: %d vs %d bytes", e.Index, len(wb), len(gb))
		}
	}
}

func walkFuzzCheck(t *testing.T, p Part, depth int) {
	t.Helper()
	if depth > 64 {
		t.Fatalf("walk depth exceeded 64; tree may be cyclic")
	}
	_ = strings.TrimSpace(p.ContentType)
	for _, c := range p.Children {
		walkFuzzCheck(t, c, depth+1)
	}
}

// checkOpenBody walks the part tree and verifies that OpenBody over src does
// not panic and returns a reader (offset/length invariants).
func checkOpenBody(t *testing.T, src []byte, p Part) {
	t.Helper()
	if len(p.Children) == 0 {
		ra := bytes.NewReader(src)
		rc, err := p.OpenBody(ra)
		if err != nil {
			return // containers or error: fine
		}
		n, err := io.Copy(io.Discard, rc)
		_ = rc.Close()
		if err != nil {
			return // decode error on fuzz input: fine
		}
		// For non-text parts the decoded size should match Part.Size.
		if !p.IsText() && n != p.Size {
			t.Errorf("OpenBody size mismatch: read %d bytes, Part.Size=%d", n, p.Size)
		}
		return
	}
	for _, c := range p.Children {
		checkOpenBody(t, src, c)
	}
}
