package storefts

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"testing"
)

// TestWithSilencedStdout_SwallowsWritesInsideFn pins the contract that
// extractPDFText relies on: anything fn writes to os.Stdout disappears
// (ledongthuc/pdf's "DEBUG: pdf.keyword(<<). Skip dict" lines), but
// writes outside the wrapper still reach stdout. Without the swallow
// the FTS backlog would print one debug line per malformed PDF.
func TestWithSilencedStdout_SwallowsWritesInsideFn(t *testing.T) {
	saved := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	defer func() {
		os.Stdout = saved
		_ = r.Close()
	}()

	// Read the captured pipe in the background so the writer never
	// blocks on a full pipe buffer.
	type readResult struct {
		buf []byte
		err error
	}
	done := make(chan readResult, 1)
	go func() {
		var buf bytes.Buffer
		_, err := io.Copy(&buf, r)
		done <- readResult{buf: buf.Bytes(), err: err}
	}()

	withSilencedStdout(func() {
		fmt.Println("must-be-silenced sentinel")
	})
	fmt.Println("must-be-captured sentinel")

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	res := <-done
	if res.err != nil {
		t.Fatalf("read pipe: %v", res.err)
	}
	got := string(res.buf)
	if !bytes.Contains(res.buf, []byte("must-be-captured")) {
		t.Errorf("post-silence write not captured; got %q", got)
	}
	if bytes.Contains(res.buf, []byte("must-be-silenced")) {
		t.Errorf("write inside withSilencedStdout leaked to stdout; got %q", got)
	}
}
