package mailparse

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// corpusCase is the expected outcome for one spike-corpus file.
type corpusCase struct {
	file       string
	wantErr    error
	topType    string // expected top-level ContentType on success
	textCount  int    // expected count of text/* leaves
	attachCnt  int    // expected count of Attachments(m)
	subjectHas string // substring expected in Subject
}

var corpus = []corpusCase{
	{file: "01-plain-ascii.eml", topType: "text/plain", textCount: 1, subjectHas: "Quarterly report"},
	{file: "02-multipart-alternative.eml", topType: "multipart/alternative", textCount: 2},
	{file: "03-multipart-mixed-pdf.eml", topType: "multipart/mixed", textCount: 1, attachCnt: 1, subjectHas: "Q1 report"},
	{file: "04-nested-multipart.eml", topType: "multipart/mixed", textCount: 2, attachCnt: 1, subjectHas: "Ticket 8812"},
	{file: "05-message-rfc822.eml", topType: "multipart/mixed", textCount: 1, attachCnt: 1},
	{file: "06-quoted-printable.eml", topType: "text/plain", textCount: 1, subjectHas: "Rendez-vous"},
	{file: "07-base64-body.eml", topType: "text/plain", textCount: 1, subjectHas: "Project update"},
	{file: "08-rfc2047-subject.eml", topType: "text/plain", textCount: 1, subjectHas: "M"},
	{file: "09-smtputf8.eml", topType: "text/plain", textCount: 1, subjectHas: "新しいプロジェクト"},
	{file: "10-8bitmime-latin1.eml", topType: "text/plain", textCount: 1},
	{file: "11-boundary-false-match.eml", topType: "multipart/mixed", textCount: 1, attachCnt: 1},
	{file: "12-missing-content-type.eml", topType: "text/plain", textCount: 1},
	{file: "13-malformed-content-type.eml", topType: "text/plain", textCount: 1},
	{file: "14-duplicate-headers.eml", topType: "text/plain", textCount: 1},
	{file: "15-very-long-header.eml", topType: "text/plain", textCount: 1},
	{file: "16-zero-length-body.eml", topType: "text/plain", textCount: 1},
	{file: "17-binary-nul.eml", topType: "multipart/mixed", textCount: 1, attachCnt: 1},
	{file: "18-broken-base64.eml", wantErr: ErrMalformedBase64},
	{file: "19-wrong-charset-label.eml", wantErr: ErrUnknownCharset},
	{file: "20-related-inline-image.eml", topType: "multipart/related", textCount: 1, attachCnt: 0},
	{file: "21-missing-end-boundary.eml", wantErr: ErrTruncated},
	{file: "22-mixed-line-endings.eml", topType: "text/plain", textCount: 1},
}

func loadCorpus(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("testdata", "spike", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return data
}

func TestParseCorpus(t *testing.T) {
	opts := NewParseOptions()
	for _, tc := range corpus {
		t.Run(tc.file, func(t *testing.T) {
			data := loadCorpus(t, tc.file)
			msg, err := Parse(bytes.NewReader(data), opts)
			if tc.wantErr != nil {
				if err == nil {
					t.Fatalf("expected error %v, got nil (top=%s)", tc.wantErr, msg.Body.ContentType)
				}
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("expected errors.Is(%v) match, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := normalizedCT(msg.Body.ContentType); got != tc.topType {
				t.Errorf("top content-type: got %q want %q", got, tc.topType)
			}
			if got := len(TextParts(msg)); got != tc.textCount {
				t.Errorf("text parts: got %d want %d", got, tc.textCount)
			}
			if got := len(Attachments(msg)); got != tc.attachCnt {
				t.Errorf("attachments: got %d want %d", got, tc.attachCnt)
			}
			if tc.subjectHas != "" && !strings.Contains(msg.Envelope.Subject, tc.subjectHas) {
				t.Errorf("subject %q does not contain %q", msg.Envelope.Subject, tc.subjectHas)
			}
			if int64(len(data)) != msg.Size {
				t.Errorf("size: got %d want %d", msg.Size, len(data))
			}
		})
	}
}

func normalizedCT(ct string) string {
	// Strip any parameters; enmime already returns bare media type but be safe.
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		return strings.ToLower(strings.TrimSpace(ct[:i]))
	}
	return strings.ToLower(strings.TrimSpace(ct))
}

func TestZeroLengthBodyNotAnError(t *testing.T) {
	data := loadCorpus(t, "16-zero-length-body.eml")
	msg, err := Parse(bytes.NewReader(data), NewParseOptions())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(TextParts(msg)) != 1 {
		t.Fatalf("expected one text part with empty body, got %d", len(TextParts(msg)))
	}
	if got := TextParts(msg)[0].Text; got != "" {
		t.Errorf("expected empty text body, got %q", got)
	}
	if msg.Envelope.Subject != "No body" {
		t.Errorf("subject: got %q want %q", msg.Envelope.Subject, "No body")
	}
}

func TestEnvelopeFieldsPopulated(t *testing.T) {
	data := loadCorpus(t, "01-plain-ascii.eml")
	msg, err := Parse(bytes.NewReader(data), NewParseOptions())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(msg.Envelope.From) != 1 || msg.Envelope.From[0].Address != "alice@example.org" {
		t.Errorf("From: %+v", msg.Envelope.From)
	}
	if len(msg.Envelope.To) != 1 || msg.Envelope.To[0].Address != "bob@example.com" {
		t.Errorf("To: %+v", msg.Envelope.To)
	}
	if msg.Envelope.MessageID != "<20260414091500.01@example.org>" {
		t.Errorf("Message-ID: %q", msg.Envelope.MessageID)
	}
	if msg.Envelope.Date == "" {
		t.Error("Date is empty")
	}
}

func TestHeadersCaseInsensitive(t *testing.T) {
	data := loadCorpus(t, "01-plain-ascii.eml")
	msg, err := Parse(bytes.NewReader(data), NewParseOptions())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if msg.Headers.Get("FROM") == "" {
		t.Error("Get should be case-insensitive")
	}
	if msg.Headers.Get("from") != msg.Headers.Get("From") {
		t.Error("case variants should return the same value")
	}
}

func TestDuplicateHeadersPreserved(t *testing.T) {
	data := loadCorpus(t, "14-duplicate-headers.eml")
	msg, err := Parse(bytes.NewReader(data), NewParseOptions())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	subjects := msg.Headers.GetAll("Subject")
	if len(subjects) != 2 {
		t.Errorf("expected 2 Subject headers, got %d: %v", len(subjects), subjects)
	}
}

func TestParseRoundTripText(t *testing.T) {
	// For text-only messages, re-parsing the assembled Text should yield an identical
	// Text body. We guard against messages that contain non-text parts (not round-trippable).
	textOnly := []string{
		"01-plain-ascii.eml",
		"06-quoted-printable.eml",
		"07-base64-body.eml",
		"08-rfc2047-subject.eml",
		"09-smtputf8.eml",
		"12-missing-content-type.eml",
		"22-mixed-line-endings.eml",
	}
	for _, name := range textOnly {
		t.Run(name, func(t *testing.T) {
			data := loadCorpus(t, name)
			m1, err := Parse(bytes.NewReader(data), NewParseOptions())
			if err != nil {
				t.Fatalf("first parse: %v", err)
			}
			if len(Attachments(m1)) > 0 {
				t.Skip("has attachments, skipping round-trip")
			}
			if len(m1.Body.Children) > 0 {
				t.Skip("multipart, skipping round-trip")
			}
			// Reassemble a minimal text/plain message from the decoded body.
			body := m1.Body.Text
			reassembled := "Content-Type: text/plain; charset=utf-8\r\n\r\n" + body
			m2, err := Parse(strings.NewReader(reassembled), NewParseOptions())
			if err != nil {
				t.Fatalf("second parse: %v", err)
			}
			if m1.Body.Text != m2.Body.Text {
				t.Errorf("round-trip mismatch:\nfirst:  %q\nsecond: %q", m1.Body.Text, m2.Body.Text)
			}
		})
	}
}

// pngBytesForFallbackTest is a minimal 1x1 PNG, used to build a
// base64-encoded body for TestFallbackContentType_Base64PartDefaultsToOctetStream.
func pngBytesForFallbackTest() []byte {
	return []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4,
		0x89, 0x00, 0x00, 0x00, 0x0D, 0x49, 0x44, 0x41,
		0x54, 0x78, 0x9C, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0D, 0x0A, 0x2D, 0xB4, 0x00,
		0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE,
		0x42, 0x60, 0x82,
	}
}

// TestFallbackContentType_Base64PartDefaultsToOctetStream covers issue
// #324's read-time tolerance: a multipart child whose Content-Type
// header is unparseable (empty subtype, e.g. the malformed
// "Content-Type: image/" a remote image origin sent, which extimg wrote
// onto the rebuilt part verbatim pre-fix) and whose
// Content-Transfer-Encoding is base64 must default to
// application/octet-stream, not the RFC 2045 text/plain default -- so
// the part is treated as opaque binary (IsText()==false, no Text
// decoded) rather than having its raw binary bytes charset-converted
// and exposed as body text (or, under StrictCharset, rejecting the
// whole message outright because binary bytes don't decode cleanly as
// us-ascii).
func TestFallbackContentType_Base64PartDefaultsToOctetStream(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString(pngBytesForFallbackTest())
	var body strings.Builder
	body.WriteString("--BOUND\r\n")
	body.WriteString("Content-Type: image/\r\n") // empty subtype: unparseable
	body.WriteString("Content-Transfer-Encoding: base64\r\n")
	body.WriteString("Content-Disposition: inline\r\n")
	body.WriteString("Content-ID: <img@herold>\r\n")
	body.WriteString("\r\n")
	body.WriteString(b64)
	body.WriteString("\r\n--BOUND--\r\n")

	raw := "From: alice@example.com\r\n" +
		"To: bob@example.com\r\n" +
		"Subject: malformed image content-type\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/related; boundary=\"BOUND\"\r\n" +
		"\r\n" +
		body.String()

	msg, err := Parse(strings.NewReader(raw), NewParseOptions())
	if err != nil {
		t.Fatalf("Parse: %v (pre-fix this fails: RFC2045 text/plain default + StrictCharset rejects binary bytes as invalid us-ascii)", err)
	}
	if len(msg.Body.Children) != 1 {
		t.Fatalf("expected 1 child, got %d", len(msg.Body.Children))
	}
	part := msg.Body.Children[0]
	if part.ContentType != "application/octet-stream" {
		t.Errorf("ContentType=%q, want application/octet-stream", part.ContentType)
	}
	if part.IsText() {
		t.Errorf("IsText()=true, want false -- a binary part must not be read as body text")
	}
	if part.Text != "" {
		t.Errorf("Text=%q, want empty -- non-text parts are not decoded into Text", part.Text)
	}
}

// TestFallbackContentType_NonBase64PartKeepsRFC2045Default pins the
// boundary of the issue #324 fallback change: a part with an
// unparseable Content-Type but a non-base64 (or absent)
// Content-Transfer-Encoding keeps the existing RFC 2045 §5.2
// text/plain;charset=us-ascii default -- only a declared base64 body
// is treated as opaque binary.
func TestFallbackContentType_NonBase64PartKeepsRFC2045Default(t *testing.T) {
	raw := "From: alice@example.com\r\n" +
		"To: bob@example.com\r\n" +
		"Subject: malformed content-type, plain text body\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"BOUND\"\r\n" +
		"\r\n" +
		"--BOUND\r\n" +
		"Content-Type: bogus/\r\n" +
		"\r\n" +
		"hello there\r\n" +
		"--BOUND--\r\n"

	msg, err := Parse(strings.NewReader(raw), NewParseOptions())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(msg.Body.Children) != 1 {
		t.Fatalf("expected 1 child, got %d", len(msg.Body.Children))
	}
	part := msg.Body.Children[0]
	if part.ContentType != "text/plain" {
		t.Errorf("ContentType=%q, want text/plain (RFC2045 default preserved for non-base64 bodies)", part.ContentType)
	}
	if part.Text != "hello there" {
		t.Errorf("Text=%q, want %q", part.Text, "hello there")
	}
}

func TestParseOptionsDefaults(t *testing.T) {
	var zero ParseOptions
	zero.applyDefaults()
	if zero.MaxSize != DefaultMaxSize {
		t.Errorf("MaxSize default: got %d want %d", zero.MaxSize, DefaultMaxSize)
	}
	if zero.MaxDepth != DefaultMaxDepth {
		t.Errorf("MaxDepth default: got %d want %d", zero.MaxDepth, DefaultMaxDepth)
	}
	if zero.MaxParts != DefaultMaxParts {
		t.Errorf("MaxParts default: got %d want %d", zero.MaxParts, DefaultMaxParts)
	}
}
