package mailparse

import (
	"bytes"
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
			if !bytes.Equal(msg.Raw, data) {
				t.Error("Raw bytes should match input")
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
