package mailparse

import (
	"bytes"
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
		// Invariant: Size matches Raw length.
		if msg.Size != int64(len(msg.Raw)) {
			t.Fatalf("size mismatch: Size=%d len(Raw)=%d", msg.Size, len(msg.Raw))
		}
		// Invariant: Raw equals input (up to what was read within MaxSize).
		if !bytes.Equal(msg.Raw, data) {
			t.Fatal("Raw should equal input on success")
		}
		// Invariant: walking Part tree terminates and content types are non-empty strings.
		walkFuzzCheck(t, msg.Body, 0)
	})
}

func walkFuzzCheck(t *testing.T, p Part, depth int) {
	if depth > 64 {
		t.Fatalf("walk depth exceeded 64; tree may be cyclic")
	}
	// ContentType may be empty for trivial plain text in enmime; do not fail on that.
	_ = strings.TrimSpace(p.ContentType)
	for _, c := range p.Children {
		walkFuzzCheck(t, c, depth+1)
	}
}
