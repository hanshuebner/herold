package storefts

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/mailparse"
)

// buildOOXMLMessage wraps raw OOXML bytes into a minimal RFC 5322
// message with a single base64-encoded attachment and returns both the
// raw message bytes and the first attachment Part after parsing.
func buildOOXMLMessage(t *testing.T, ct string, raw []byte) ([]byte, mailparse.Part, *bytes.Reader) {
	t.Helper()
	enc := base64.StdEncoding.EncodeToString(raw)
	msg := fmt.Sprintf(
		"From: sender@example.com\r\nTo: rcpt@example.com\r\nContent-Type: multipart/mixed; boundary=b0\r\n\r\n--b0\r\nContent-Type: %s\r\nContent-Transfer-Encoding: base64\r\n\r\n%s\r\n--b0--\r\n",
		ct, enc,
	)
	msgBytes := []byte(msg)
	src := bytes.NewReader(msgBytes)
	parsed, err := mailparse.Parse(src, mailparse.NewParseOptions())
	if err != nil {
		t.Fatalf("mailparse.Parse: %v", err)
	}
	atts := mailparse.Attachments(parsed)
	if len(atts) == 0 {
		t.Fatalf("no attachments found in parsed message")
	}
	return msgBytes, atts[0], bytes.NewReader(msgBytes)
}

func TestExtractAttachmentText_HTML(t *testing.T) {
	html := `<html><body><h1>Quarterly Report</h1><p>Revenue grew <b>15%</b>.</p></body></html>`
	// text/html parts land in Part.Text after mailparse decodes them.
	p := mailparse.Part{
		ContentType: "text/html; charset=utf-8",
		Text:        html,
	}
	got, format, trunc, err := extractAttachmentText(p, nil, 0)
	if err != nil {
		t.Fatalf("extractAttachmentText: %v", err)
	}
	if format != "html" {
		t.Errorf("format = %q; want html", format)
	}
	if trunc {
		t.Errorf("unexpected truncation")
	}
	if !strings.Contains(got, "Quarterly Report") || !strings.Contains(got, "Revenue grew") {
		t.Errorf("html2text output missing expected tokens: %q", got)
	}
}

func TestExtractAttachmentText_PlainText(t *testing.T) {
	body := "first line\nsecond line"
	// text/* parts land in Part.Text after mailparse decodes them.
	p := mailparse.Part{
		ContentType: "text/csv",
		Text:        body,
	}
	got, format, _, err := extractAttachmentText(p, nil, 0)
	if err != nil {
		t.Fatalf("extractAttachmentText: %v", err)
	}
	if format != "text" {
		t.Errorf("format = %q; want text", format)
	}
	if got != body {
		t.Errorf("got %q; want %q", got, body)
	}
}

func TestExtractAttachmentText_DOCX(t *testing.T) {
	docx := buildSyntheticDOCX(t,
		`<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p><w:r><w:t>Hello DOCX universe</w:t></w:r></w:p>
    <w:p><w:r><w:t>Second paragraph</w:t></w:r></w:p>
  </w:body>
</w:document>`)
	_, p, src := buildOOXMLMessage(t,
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document", docx)
	got, format, _, err := extractAttachmentText(p, src, 0)
	if err != nil {
		t.Fatalf("extractAttachmentText: %v", err)
	}
	if format != "docx" {
		t.Errorf("format = %q; want docx", format)
	}
	if !strings.Contains(got, "Hello DOCX universe") {
		t.Errorf("missing first paragraph: %q", got)
	}
	if !strings.Contains(got, "Second paragraph") {
		t.Errorf("missing second paragraph: %q", got)
	}
}

func TestExtractAttachmentText_PPTX(t *testing.T) {
	pptx := buildSyntheticPPTX(t,
		`<?xml version="1.0" encoding="UTF-8"?>
<p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"
       xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">
  <p:cSld><p:spTree>
    <p:sp><p:txBody>
      <a:p><a:r><a:t>Slide title token</a:t></a:r></a:p>
    </p:txBody></p:sp>
  </p:spTree></p:cSld>
</p:sld>`)
	_, p, src := buildOOXMLMessage(t,
		"application/vnd.openxmlformats-officedocument.presentationml.presentation", pptx)
	got, format, _, err := extractAttachmentText(p, src, 0)
	if err != nil {
		t.Fatalf("extractAttachmentText: %v", err)
	}
	if format != "pptx" {
		t.Errorf("format = %q; want pptx", format)
	}
	if !strings.Contains(got, "Slide title token") {
		t.Errorf("missing slide text: %q", got)
	}
}

func TestExtractAttachmentText_XLSX(t *testing.T) {
	xlsx := buildSyntheticXLSX(t,
		`<?xml version="1.0" encoding="UTF-8"?>
<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="2" uniqueCount="2">
  <si><t>cell-token-alpha</t></si>
  <si><t>cell-token-beta</t></si>
</sst>`)
	_, p, src := buildOOXMLMessage(t,
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", xlsx)
	got, format, _, err := extractAttachmentText(p, src, 0)
	if err != nil {
		t.Fatalf("extractAttachmentText: %v", err)
	}
	if format != "xlsx" {
		t.Errorf("format = %q; want xlsx", format)
	}
	if !strings.Contains(got, "cell-token-alpha") || !strings.Contains(got, "cell-token-beta") {
		t.Errorf("missing shared-string tokens: %q", got)
	}
}

// TestExtractAttachmentText_PDF_DisabledStopgap pins the REQ-PDFEX-110
// stopgap contract: every application/pdf part returns the
// formatPDFDisabled sentinel with no error and no extracted text,
// regardless of input shape (valid, empty, or malformed). The
// subprocess wrapper specified in 20-pdf-extraction-isolation.md
// replaces this behaviour. The earlier per-shape PDF tests
// (valid synthetic, empty bytes, malformed bytes) are subsumed here
// because the disabled path makes them indistinguishable.
func TestExtractAttachmentText_PDF_DisabledStopgap(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
	}{
		{"empty", ""},
		{"malformed", "this is not a pdf"},
		{"non-empty-arbitrary", "%PDF-1.4 ... fake"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := mailparse.Part{
				ContentType: "application/pdf",
				Text:        tc.text,
			}
			got, format, trunc, err := extractAttachmentText(p, nil, 0)
			if err != nil {
				t.Fatalf("extractAttachmentText: %v", err)
			}
			if format != formatPDFDisabled {
				t.Errorf("format = %q; want %q", format, formatPDFDisabled)
			}
			if got != "" {
				t.Errorf("expected empty extraction; got %q", got)
			}
			if trunc {
				t.Errorf("trunc = true; want false")
			}
		})
	}
}

func TestExtractAttachmentText_PerAttachmentCap(t *testing.T) {
	body := strings.Repeat("a", 1024)
	p := mailparse.Part{
		ContentType: "text/plain",
		Text:        body,
	}
	got, _, trunc, err := extractAttachmentText(p, nil, 100)
	if err != nil {
		t.Fatalf("extractAttachmentText: %v", err)
	}
	if !trunc {
		t.Errorf("expected truncation at 100 bytes")
	}
	if len(got) != 100 {
		t.Errorf("got %d bytes; want 100", len(got))
	}
}

func TestExtractAttachmentText_UnknownFormat(t *testing.T) {
	p := mailparse.Part{
		ContentType: "application/octet-stream",
	}
	got, format, _, err := extractAttachmentText(p, nil, 0)
	if err != nil {
		t.Fatalf("extractAttachmentText: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty extraction, got %q", got)
	}
	if format != "skipped" {
		t.Errorf("format = %q; want skipped", format)
	}
}

func TestExtractAttachmentText_MalformedDOCX(t *testing.T) {
	// Construct a message with a non-zip payload in a docx part.
	_, p, src := buildOOXMLMessage(t,
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		[]byte("this is not a zip file"))
	_, format, _, err := extractAttachmentText(p, src, 0)
	if err == nil {
		t.Fatalf("expected zip error, got nil")
	}
	if format != "docx" {
		t.Errorf("format = %q; want docx", format)
	}
}

func TestCapString(t *testing.T) {
	cases := []struct {
		in     string
		max    int
		want   string
		wantTr bool
	}{
		{"hello", 0, "hello", false},
		{"hello", -1, "hello", false},
		{"hello", 100, "hello", false},
		{"hello", 3, "hel", true},
		{"héllo", 2, "h", true},   // truncate at start of multi-byte rune
		{"héllo", 4, "hél", true}, // 'l' (0x6C) is not a continuation byte
		{"abcdef", 6, "abcdef", false},
	}
	for _, tc := range cases {
		got, gotTr := capString(tc.in, tc.max)
		if got != tc.want || gotTr != tc.wantTr {
			t.Errorf("capString(%q, %d) = (%q, %v); want (%q, %v)",
				tc.in, tc.max, got, gotTr, tc.want, tc.wantTr)
		}
	}
}

// buildSyntheticDOCX writes a minimal DOCX zip whose word/document.xml
// is the supplied content. The synthetic doc is not a valid Word file
// (no _rels, no [Content_Types].xml) but exercises the OOXML walker.
func buildSyntheticDOCX(t *testing.T, documentXML string) []byte {
	t.Helper()
	return zipBytes(t, map[string]string{
		"word/document.xml": documentXML,
	})
}

// buildSyntheticPPTX writes a one-slide PPTX zip.
func buildSyntheticPPTX(t *testing.T, slideXML string) []byte {
	t.Helper()
	return zipBytes(t, map[string]string{
		"ppt/slides/slide1.xml": slideXML,
	})
}

// buildSyntheticXLSX writes an XLSX zip whose sharedStrings.xml is the
// supplied content. The walker pulls text from sharedStrings even if no
// worksheet references those indices.
func buildSyntheticXLSX(t *testing.T, sharedStringsXML string) []byte {
	t.Helper()
	return zipBytes(t, map[string]string{
		"xl/sharedStrings.xml": sharedStringsXML,
	})
}

func zipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}
