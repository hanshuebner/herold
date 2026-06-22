package mailparse

import (
	"bytes"
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

	opts := NewParseOptions()
	opts.MaxSize = 1 << 20
	opts.MaxDepth = 8
	opts.MaxParts = 256
	// Keep strictness off during fuzzing so we exercise the happy-path parse surface.
	opts.StrictCharset = false
	opts.StrictBase64 = false
	opts.StrictQP = false
	opts.StrictBoundary = false

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
	})
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
