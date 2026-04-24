package spam

import (
	"bytes"
	"testing"

	"github.com/hanshuebner/herold/internal/mailparse"
)

// FuzzBuildRequest drives BuildRequest over random-ish message bytes. It
// must never panic and must always produce a Request whose BodyExcerpt
// length is within the cap.
func FuzzBuildRequest(f *testing.F) {
	seeds := []string{
		"",
		"From: a@b\r\nSubject: x\r\n\r\nhi",
		"Content-Type: text/html\r\n\r\n<html><body><a href=\"x\">y</a></body></html>",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		msg, err := mailparse.Parse(bytes.NewReader(raw), mailparse.ParseOptions{StrictBoundary: false})
		if err != nil {
			return
		}
		req := BuildRequest(msg, nil)
		if len(req.BodyExcerpt) > DefaultBodyExcerptBytes {
			t.Fatalf("excerpt exceeds cap: %d", len(req.BodyExcerpt))
		}
	})
}
