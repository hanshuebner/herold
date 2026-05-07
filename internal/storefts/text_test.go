package storefts

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/store"
)

// TestMailparseExtractor_AttachmentText verifies that text/plain body +
// HTML attachment + DOCX attachment all land in the indexed string.
func TestMailparseExtractor_AttachmentText(t *testing.T) {
	docx := buildSyntheticDOCX(t,
		`<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p><w:r><w:t>docx-tracer-token-quartz</w:t></w:r></w:p>
  </w:body>
</w:document>`)
	msg := buildMultipart(t, multipartParts{
		Plain:          "plain-tracer-token-eta",
		HTMLAttachment: `<html><body><p>html-tracer-token-rho</p></body></html>`,
		DOCX:           docx,
	})

	e := NewMailparseExtractor()
	got, err := e.Extract(context.Background(), store.Message{}, bytes.NewReader(msg))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for _, want := range []string{
		"plain-tracer-token-eta",
		"html-tracer-token-rho",
		"docx-tracer-token-quartz",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("extracted text missing %q\n--full output--\n%s", want, got)
		}
	}
}

func TestMailparseExtractor_PerMessageCap(t *testing.T) {
	bigPlain := strings.Repeat("a", 200)
	bigHTML := "<p>" + strings.Repeat("b", 200) + "</p>"
	msg := buildMultipart(t, multipartParts{
		Plain:          bigPlain,
		HTMLAttachment: bigHTML,
	})
	e := NewMailparseExtractor()
	e.PerAttachmentMaxBytes = 1000
	e.PerMessageMaxBytes = 100 // covers attachment budget

	got, err := e.Extract(context.Background(), store.Message{}, bytes.NewReader(msg))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	// Body is unaffected by the cap (caps apply to attachments only),
	// so the plain part lands fully and the HTML attachment is then
	// truncated at 100 bytes of message budget.
	if !strings.Contains(got, bigPlain) {
		t.Errorf("body should not be capped; not found")
	}
	// HTML attachment text starts after a "\n\n" separator. The
	// truncation happens within that section.
	idx := strings.Index(got, bigPlain)
	if idx < 0 {
		t.Fatalf("body anchor missing")
	}
	tail := got[idx+len(bigPlain):]
	bCount := strings.Count(tail, "b")
	if bCount > 100 {
		t.Errorf("attachment chunk not capped at 100; got %d 'b' bytes", bCount)
	}
}

type multipartParts struct {
	Plain          string
	HTMLAttachment string
	DOCX           []byte
}

// buildMultipart synthesises a multipart/mixed RFC 5322 message with
// the supplied text body and attachments. Used so the extractor tests
// stay self-contained (no on-disk fixtures, no network).
func buildMultipart(t *testing.T, p multipartParts) []byte {
	t.Helper()
	const boundary = "BOUNDARY-storefts-test"
	var buf bytes.Buffer
	buf.WriteString("From: sender@example.com\r\n")
	buf.WriteString("To: recipient@example.com\r\n")
	buf.WriteString("Subject: extractor test\r\n")
	buf.WriteString("MIME-Version: 1.0\r\n")
	buf.WriteString("Content-Type: multipart/mixed; boundary=\"" + boundary + "\"\r\n")
	buf.WriteString("\r\n")

	if p.Plain != "" {
		buf.WriteString("--" + boundary + "\r\n")
		buf.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
		buf.WriteString("\r\n")
		buf.WriteString(p.Plain)
		buf.WriteString("\r\n")
	}
	if p.HTMLAttachment != "" {
		buf.WriteString("--" + boundary + "\r\n")
		buf.WriteString("Content-Type: text/html; charset=utf-8\r\n")
		buf.WriteString("Content-Disposition: attachment; filename=\"report.html\"\r\n")
		buf.WriteString("\r\n")
		buf.WriteString(p.HTMLAttachment)
		buf.WriteString("\r\n")
	}
	if len(p.DOCX) > 0 {
		buf.WriteString("--" + boundary + "\r\n")
		buf.WriteString("Content-Type: application/vnd.openxmlformats-officedocument.wordprocessingml.document\r\n")
		buf.WriteString("Content-Disposition: attachment; filename=\"report.docx\"\r\n")
		buf.WriteString("Content-Transfer-Encoding: base64\r\n")
		buf.WriteString("\r\n")
		buf.WriteString(b64Wrapped(p.DOCX))
		buf.WriteString("\r\n")
	}
	buf.WriteString("--" + boundary + "--\r\n")
	return buf.Bytes()
}

// b64Wrapped emits standard base64 with a 76-char line length (matches
// what enmime expects for canonical MIME).
func b64Wrapped(b []byte) string {
	const enc = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var out bytes.Buffer
	col := 0
	for i := 0; i < len(b); i += 3 {
		var v uint32
		var pad int
		switch {
		case i+3 <= len(b):
			v = uint32(b[i])<<16 | uint32(b[i+1])<<8 | uint32(b[i+2])
		case i+2 == len(b):
			v = uint32(b[i])<<16 | uint32(b[i+1])<<8
			pad = 1
		default:
			v = uint32(b[i]) << 16
			pad = 2
		}
		c := []byte{
			enc[(v>>18)&0x3F],
			enc[(v>>12)&0x3F],
			enc[(v>>6)&0x3F],
			enc[v&0x3F],
		}
		for k := 0; k < 4-pad; k++ {
			out.WriteByte(c[k])
			col++
			if col == 76 {
				out.WriteString("\r\n")
				col = 0
			}
		}
		for k := 0; k < pad; k++ {
			out.WriteByte('=')
			col++
			if col == 76 {
				out.WriteString("\r\n")
				col = 0
			}
		}
	}
	return out.String()
}
