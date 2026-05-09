package storefts

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/store"
)

// TestMailparseExtractor_MalformedBase64_StillIndexesCleanLeaves pins the
// lenient FTS-extractor contract: a malformed-base64 attachment in a
// multipart message MUST NOT abort the whole extraction. Without the
// lenient mode the worker logs "extract text for message N: storefts:
// parse: mailparse: malformed_base64 part=K: unexpected ':' in base64
// stream" and the message stays un-indexed for the life of its row;
// search never finds it. With the lenient mode the cleanly-parseable
// text/plain leaf still contributes its body to the index.
func TestMailparseExtractor_MalformedBase64_StillIndexesCleanLeaves(t *testing.T) {
	const boundary = "BOUNDARY-malformed-base64-test"
	var buf bytes.Buffer
	buf.WriteString("From: sender@example.com\r\n")
	buf.WriteString("To: recipient@example.com\r\n")
	buf.WriteString("Subject: lenient base64\r\n")
	buf.WriteString("MIME-Version: 1.0\r\n")
	buf.WriteString("Content-Type: multipart/mixed; boundary=\"" + boundary + "\"\r\n\r\n")
	buf.WriteString("--" + boundary + "\r\n")
	buf.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	buf.WriteString("clean-tracer-token-helios\r\n")
	buf.WriteString("--" + boundary + "\r\n")
	buf.WriteString("Content-Type: application/octet-stream; name=\"bad.bin\"\r\n")
	buf.WriteString("Content-Transfer-Encoding: base64\r\n")
	buf.WriteString("Content-Disposition: attachment; filename=\"bad.bin\"\r\n\r\n")
	// A ':' is outside the base64 alphabet and reproduces the wild-input
	// failure mode from the production warning logs.
	buf.WriteString("invalid:base64:stream:here\r\n")
	buf.WriteString("--" + boundary + "--\r\n")

	e := NewMailparseExtractor()
	got, err := e.Extract(context.Background(), store.Message{}, bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("Extract returned error on malformed-base64 attachment; want lenient pass: %v", err)
	}
	if !strings.Contains(got, "clean-tracer-token-helios") {
		t.Errorf("text/plain leaf missing from extracted output; got %q", got)
	}
}
