package storefts

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/mailparse"
)

func TestExtractAttachmentText_HTML(t *testing.T) {
	html := `<html><body><h1>Quarterly Report</h1><p>Revenue grew <b>15%</b>.</p></body></html>`
	p := mailparse.Part{
		ContentType: "text/html; charset=utf-8",
		Bytes:       []byte(html),
	}
	got, format, trunc, err := extractAttachmentText(p, 0)
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
	p := mailparse.Part{
		ContentType: "text/csv",
		Bytes:       []byte(body),
	}
	got, format, _, err := extractAttachmentText(p, 0)
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
	p := mailparse.Part{
		ContentType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		Bytes:       docx,
	}
	got, format, _, err := extractAttachmentText(p, 0)
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
	p := mailparse.Part{
		ContentType: "application/vnd.openxmlformats-officedocument.presentationml.presentation",
		Bytes:       pptx,
	}
	got, format, _, err := extractAttachmentText(p, 0)
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
	p := mailparse.Part{
		ContentType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		Bytes:       xlsx,
	}
	got, format, _, err := extractAttachmentText(p, 0)
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

func TestExtractAttachmentText_PDF(t *testing.T) {
	pdfBytes, err := base64.StdEncoding.DecodeString(syntheticPDFBase64)
	if err != nil {
		t.Fatalf("decode synthetic pdf: %v", err)
	}
	p := mailparse.Part{
		ContentType: "application/pdf",
		Bytes:       pdfBytes,
	}
	got, format, _, err := extractAttachmentText(p, 0)
	if err != nil {
		t.Fatalf("extractAttachmentText: %v", err)
	}
	if format != "pdf" {
		t.Errorf("format = %q; want pdf", format)
	}
	if !strings.Contains(got, "pdf-tracer-token-omega") {
		t.Errorf("PDF text extraction missing token; got %q", got)
	}
}

func TestExtractAttachmentText_PDF_Empty(t *testing.T) {
	p := mailparse.Part{
		ContentType: "application/pdf",
		Bytes:       nil,
	}
	got, format, _, err := extractAttachmentText(p, 0)
	if err != nil {
		t.Fatalf("extractAttachmentText: %v", err)
	}
	if format != "pdf" {
		t.Errorf("format = %q; want pdf", format)
	}
	if got != "" {
		t.Errorf("expected empty extraction, got %q", got)
	}
}

func TestExtractAttachmentText_PDF_Malformed(t *testing.T) {
	p := mailparse.Part{
		ContentType: "application/pdf",
		Bytes:       []byte("this is not a pdf"),
	}
	_, format, _, err := extractAttachmentText(p, 0)
	if err == nil {
		t.Fatalf("expected error for malformed PDF")
	}
	if format != "pdf" {
		t.Errorf("format = %q; want pdf", format)
	}
}

func TestExtractAttachmentText_PerAttachmentCap(t *testing.T) {
	body := strings.Repeat("a", 1024)
	p := mailparse.Part{
		ContentType: "text/plain",
		Bytes:       []byte(body),
	}
	got, _, trunc, err := extractAttachmentText(p, 100)
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
		Bytes:       []byte{0x00, 0x01, 0x02, 0x03},
	}
	got, format, _, err := extractAttachmentText(p, 0)
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
	p := mailparse.Part{
		ContentType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		Bytes:       []byte("this is not a zip file"),
	}
	_, format, _, err := extractAttachmentText(p, 0)
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

// syntheticPDFBase64 is a minimal valid PDF (1.4) carrying the literal
// string "pdf-tracer-token-omega" as its single page's text content.
// 630 bytes uncompressed; the structure is hand-rolled (catalog, pages
// tree, single page, Helvetica font, content stream) so the test does
// not depend on a binary fixture file or a runtime PDF generator dep.
const syntheticPDFBase64 = "JVBERi0xLjQKJcTl8uUKMSAwIG9iago8PCAvVHlwZSAvQ2F0YWxvZyAvUGFnZXMgMiAwIFIgPj4KZW5kb2JqCjIgMCBvYmoKPDwgL1R5cGUgL1BhZ2VzIC9LaWRzIFszIDAgUl0gL0NvdW50IDEgPj4KZW5kb2JqCjMgMCBvYmoKPDwgL1R5cGUgL1BhZ2UgL1BhcmVudCAyIDAgUiAvTWVkaWFCb3ggWzAgMCA2MTIgNzkyXSAvUmVzb3VyY2VzIDw8IC9Gb250IDw8IC9GMSA0IDAgUiA+PiA+PiAvQ29udGVudHMgNSAwIFIgPj4KZW5kb2JqCjQgMCBvYmoKPDwgL1R5cGUgL0ZvbnQgL1N1YnR5cGUgL1R5cGUxIC9CYXNlRm9udCAvSGVsdmV0aWNhIC9FbmNvZGluZyAvV2luQW5zaUVuY29kaW5nID4+CmVuZG9iago1IDAgb2JqCjw8IC9MZW5ndGggNTQgPj4Kc3RyZWFtCkJUCi9GMSAxMiBUZgo3MiA3MjAgVGQKKHBkZi10cmFjZXItdG9rZW4tb21lZ2EpIFRqCkVUCmVuZHN0cmVhbQplbmRvYmoKeHJlZgowIDYKMDAwMDAwMDAwMCA2NTUzNSBmIAowMDAwMDAwMDE1IDAwMDAwIG4gCjAwMDAwMDAwNjQgMDAwMDAgbiAKMDAwMDAwMDEyMSAwMDAwMCBuIAowMDAwMDAwMjQ3IDAwMDAwIG4gCjAwMDAwMDAzNDQgMDAwMDAgbiAKdHJhaWxlcgo8PCAvU2l6ZSA2IC9Sb290IDEgMCBSID4+CnN0YXJ0eHJlZgo0NDcKJSVFT0YK"

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
