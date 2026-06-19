package protosmtp_test

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/protosmtp"
)

// dotStuffRef is the reference implementation that mirrors the
// pre-streaming writeDotStuffed behaviour: operates on a []byte slice,
// scans for '\n', and prepends '.' for lines that begin with '.'.
// Used to cross-check the streaming implementation against known-correct
// output on all tricky inputs.
func dotStuffRef(body []byte) []byte {
	var out bytes.Buffer
	for len(body) > 0 {
		idx := bytes.IndexByte(body, '\n')
		var line []byte
		if idx < 0 {
			line = body
			body = nil
		} else {
			line = body[:idx+1]
			body = body[idx+1:]
		}
		if len(line) > 0 && line[0] == '.' {
			out.WriteByte('.')
		}
		out.Write(line)
	}
	return out.Bytes()
}

// runDotStuff runs WriteDotStuffedForTest with a 4-KiB bufio.Writer
// (small enough to exercise cross-buffer-boundary cases) and returns
// the complete output.
func runDotStuff(t *testing.T, input []byte) []byte {
	t.Helper()
	var dst bytes.Buffer
	bw := bufio.NewWriterSize(&dst, 4096)
	if err := protosmtp.WriteDotStuffedForTest(bw, bytes.NewReader(input)); err != nil {
		t.Fatalf("writeDotStuffed: %v", err)
	}
	if err := bw.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	return dst.Bytes()
}

// TestWriteDotStuffed_AgainstReference feeds tricky inputs through both
// the streaming implementation and the reference slice implementation
// and asserts byte-identical output.
func TestWriteDotStuffed_AgainstReference(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"no_dots", "Subject: hi\r\n\r\nHello world\r\n"},
		{"leading_dot", ".leading dot\r\n"},
		{"double_dot_line", "..two dots\r\n"},
		{"dot_after_crlf", "line one\r\n.line two starts with dot\r\n"},
		{"dot_mid_line", "line.with.dots.inside\r\n"},
		{"bare_lf", "line1\n.line2\nline3\n"},
		{"crlf_then_dot", "normal\r\n.dotted\r\n"},
		{"ending_without_crlf", "Subject: x\r\n\r\nbody without trailing newline"},
		{"ending_with_crlf", "Subject: x\r\n\r\nbody\r\n"},
		{"only_dot", "."},
		{"only_dot_crlf", ".\r\n"},
		{"multiple_leading_dots", ".one\r\n.two\r\n.three\r\n"},
		{"embedded_cr_no_lf", "line\rwith\rcr\r"},
		{"consecutive_dots_on_own_line", ".\r\n.\r\n.\r\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := []byte(tc.input)
			got := runDotStuff(t, input)
			want := dotStuffRef(input)
			if !bytes.Equal(got, want) {
				t.Errorf("mismatch\ninput: %q\ngot:   %q\nwant:  %q", input, got, want)
			}
		})
	}
}

// TestWriteDotStuffed_LargeBody feeds a multi-MiB body through the
// streaming implementation and compares byte-for-byte against the
// reference (REQ-STORE-17: delivery must stream without holding the
// full body in RAM — the test confirms the output is correct even when
// the body exceeds the internal buffer many times over).
func TestWriteDotStuffed_LargeBody(t *testing.T) {
	// Build a 4 MiB body with dot-lines sprinkled throughout to exercise
	// cross-buffer-boundary dot-stuffing.
	var sb strings.Builder
	for i := range 4 * 1024 {
		if i%17 == 0 {
			fmt.Fprintf(&sb, ".line %d starts with a dot\r\n", i)
		} else {
			fmt.Fprintf(&sb, "line %d ordinary content\r\n", i)
		}
	}
	input := []byte(sb.String())

	got := runDotStuff(t, input)
	want := dotStuffRef(input)
	if !bytes.Equal(got, want) {
		t.Errorf("large body: output mismatch (%d bytes got vs %d bytes want)", len(got), len(want))
	}
}

// TestWriteDotStuffed_MultiMiBLineNoPanic verifies that a single line
// longer than dotStuffBufSize does not panic or corrupt output
// (REQ-STORE-17: no per-line buffer regardless of line length).
func TestWriteDotStuffed_MultiMiBLineNoPanic(t *testing.T) {
	// 64 KiB of 'x' on a single line (no newline — mimics a binary
	// attachment fragment split across multiple transport lines by the
	// base64 encoder; here we just want a line longer than 32 KiB).
	longLine := bytes.Repeat([]byte("x"), 65*1024)
	input := append(longLine, []byte("\r\nend\r\n")...)
	got := runDotStuff(t, input)
	want := dotStuffRef(input)
	if !bytes.Equal(got, want) {
		t.Errorf("long line: mismatch; got %d bytes want %d bytes", len(got), len(want))
	}
}

// TestWriteDotStuffed_RandomBinary exercises the streaming
// implementation with random binary data (simulates 8BITMIME payloads).
func TestWriteDotStuffed_RandomBinary(t *testing.T) {
	input := make([]byte, 128*1024)
	if _, err := rand.Read(input); err != nil {
		t.Fatalf("rand: %v", err)
	}
	got := runDotStuff(t, input)
	want := dotStuffRef(input)
	if !bytes.Equal(got, want) {
		t.Errorf("random binary: mismatch (%d vs %d bytes)", len(got), len(want))
	}
}

// TestWriteDotStuffed_ReaderError verifies that a read error from the
// body reader is propagated to the caller.
func TestWriteDotStuffed_ReaderError(t *testing.T) {
	errReader := &errAfterN{n: 10, err: io.ErrUnexpectedEOF}
	var dst bytes.Buffer
	bw := bufio.NewWriter(&dst)
	err := protosmtp.WriteDotStuffedForTest(bw, errReader)
	if err == nil {
		t.Fatal("expected error from reader, got nil")
	}
}

// errAfterN returns n bytes of 'a' then the configured error.
type errAfterN struct {
	n   int
	err error
}

func (e *errAfterN) Read(p []byte) (int, error) {
	if e.n <= 0 {
		return 0, e.err
	}
	fill := len(p)
	if fill > e.n {
		fill = e.n
	}
	for i := range fill {
		p[i] = 'a'
	}
	e.n -= fill
	return fill, nil
}
