package mailparse

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestMaxSize(t *testing.T) {
	opts := NewParseOptions()
	opts.MaxSize = 128
	big := make([]byte, 1024)
	for i := range big {
		big[i] = 'x'
	}
	_, err := Parse(bytes.NewReader(big), opts)
	if err == nil {
		t.Fatal("expected ErrTooLarge, got nil")
	}
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("expected ErrTooLarge, got %v", err)
	}
}

// TestMaxSize_HeadersRecovered pins the re #244 fix: a message whose body
// exceeds MaxSize must still surface From/Subject/Message-ID via the
// returned Message's Envelope, even though Parse also reports ErrTooLarge.
// Before the fix, Parse returned a fully zero-valued Message on this path,
// which is what made a too-large sent message render as "(no sender)" and
// persisted an empty env_message_id at create time.
func TestMaxSize_HeadersRecovered(t *testing.T) {
	opts := NewParseOptions()
	opts.MaxSize = 1024

	var body strings.Builder
	body.WriteString("From: Alice Example <alice@example.test>\r\n")
	body.WriteString("To: bob@example.test\r\n")
	body.WriteString("Subject: A message that will exceed the cap\r\n")
	body.WriteString("Message-ID: <big-message-1@example.test>\r\n")
	body.WriteString("\r\n")
	for int64(body.Len()) <= opts.MaxSize {
		body.WriteString("padding padding padding padding padding\r\n")
	}

	msg, err := Parse(strings.NewReader(body.String()), opts)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("expected ErrTooLarge, got %v", err)
	}
	if msg.Envelope.MessageID != "<big-message-1@example.test>" {
		t.Errorf("Envelope.MessageID = %q, want <big-message-1@example.test>", msg.Envelope.MessageID)
	}
	if len(msg.Envelope.From) != 1 || msg.Envelope.From[0].Address != "alice@example.test" {
		t.Errorf("Envelope.From = %+v, want [alice@example.test]", msg.Envelope.From)
	}
	if msg.Envelope.Subject != "A message that will exceed the cap" {
		t.Errorf("Envelope.Subject = %q, want %q", msg.Envelope.Subject, "A message that will exceed the cap")
	}
	// The body itself must not have been walked: Body is the zero Part.
	if msg.Body.ContentType != "" || len(msg.Body.Children) != 0 {
		t.Errorf("Body should be the zero Part on the too-large path, got %+v", msg.Body)
	}
}

// TestMaxSize_UnparseableHeaders_StillReportsTooLarge covers the fallback
// when even the recoverable header block is malformed: Parse must still
// report ErrTooLarge (not silently swallow it) and never panic.
func TestMaxSize_UnparseableHeaders_StillReportsTooLarge(t *testing.T) {
	opts := NewParseOptions()
	opts.MaxSize = 128
	// No header/body separator at all within reach of the cap.
	big := make([]byte, 1024)
	for i := range big {
		big[i] = 'x'
	}
	msg, err := Parse(bytes.NewReader(big), opts)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("expected ErrTooLarge, got %v", err)
	}
	if msg.Envelope.MessageID != "" || len(msg.Envelope.From) != 0 {
		t.Errorf("expected zero-value Envelope when headers are unparseable, got %+v", msg.Envelope)
	}
}

func TestMaxDepth(t *testing.T) {
	// Build a pathologically nested multipart message with 12 levels.
	// Keep below the parser's own tolerance but above our MaxDepth cap.
	opts := NewParseOptions()
	opts.MaxDepth = 3
	msg := buildNested(8)
	_, err := Parse(strings.NewReader(msg), opts)
	if err == nil {
		t.Fatal("expected ErrDepthExceeded, got nil")
	}
	if !errors.Is(err, ErrDepthExceeded) {
		t.Fatalf("expected ErrDepthExceeded, got %v", err)
	}
}

func TestMaxParts(t *testing.T) {
	opts := NewParseOptions()
	opts.MaxParts = 2
	// A simple multipart/alternative with text+html is 3 parts (root + 2 children).
	data := []byte(`From: a@example.com
To: b@example.com
Subject: many
MIME-Version: 1.0
Content-Type: multipart/alternative; boundary="b"

--b
Content-Type: text/plain

one
--b
Content-Type: text/plain

two
--b
Content-Type: text/plain

three
--b--
`)
	_, err := Parse(bytes.NewReader(data), opts)
	if err == nil {
		t.Fatal("expected ErrTooManyParts, got nil")
	}
	if !errors.Is(err, ErrTooManyParts) {
		t.Fatalf("expected ErrTooManyParts, got %v", err)
	}
}

func TestStrictHeaderLine(t *testing.T) {
	opts := NewParseOptions()
	opts.StrictHeaderLine = true
	opts.MaxHeaderLine = 100
	data := loadCorpus(t, "15-very-long-header.eml")
	_, err := Parse(bytes.NewReader(data), opts)
	if err == nil {
		t.Fatal("expected ErrMalformed, got nil")
	}
	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("expected ErrMalformed, got %v", err)
	}
	var pe *ParseError
	if errors.As(err, &pe) {
		if pe.HeaderLine == 0 {
			t.Error("expected HeaderLine > 0 in ParseError")
		}
	} else {
		t.Errorf("expected *ParseError, got %T", err)
	}
}

func TestStrictHeaderLineOffByDefault(t *testing.T) {
	data := loadCorpus(t, "15-very-long-header.eml")
	_, err := Parse(bytes.NewReader(data), NewParseOptions())
	if err != nil {
		t.Fatalf("strictness off by default should accept long header, got %v", err)
	}
}

func TestOptOutOfBase64Strictness(t *testing.T) {
	opts := NewParseOptions()
	opts.StrictBase64 = false
	data := loadCorpus(t, "18-broken-base64.eml")
	msg, err := Parse(bytes.NewReader(data), opts)
	if err != nil {
		t.Fatalf("unexpected error with StrictBase64=false: %v", err)
	}
	// One attachment remains (the decoded best-effort bytes).
	if len(Attachments(msg)) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(Attachments(msg)))
	}
}

func TestOptOutOfCharsetStrictness(t *testing.T) {
	opts := NewParseOptions()
	opts.StrictCharset = false
	data := loadCorpus(t, "19-wrong-charset-label.eml")
	msg, err := Parse(bytes.NewReader(data), opts)
	if err != nil {
		t.Fatalf("unexpected error with StrictCharset=false: %v", err)
	}
	if got := len(TextParts(msg)); got != 1 {
		t.Errorf("expected 1 text part, got %d", got)
	}
}

func TestOptOutOfBoundaryStrictness(t *testing.T) {
	opts := NewParseOptions()
	opts.StrictBoundary = false
	data := loadCorpus(t, "21-missing-end-boundary.eml")
	_, err := Parse(bytes.NewReader(data), opts)
	if err != nil {
		t.Fatalf("unexpected error with StrictBoundary=false: %v", err)
	}
}

func TestReasonString(t *testing.T) {
	cases := map[Reason]string{
		ReasonTooLarge:        "too_large",
		ReasonDepthExceeded:   "depth_exceeded",
		ReasonTooManyParts:    "too_many_parts",
		ReasonMalformedBase64: "malformed_base64",
		ReasonUnknownCharset:  "unknown_charset",
		ReasonTruncated:       "truncated",
		ReasonMalformed:       "malformed",
	}
	for r, want := range cases {
		if got := r.String(); got != want {
			t.Errorf("%d: got %q want %q", r, got, want)
		}
	}
}

func TestPartIndexRecorded(t *testing.T) {
	data := loadCorpus(t, "18-broken-base64.eml")
	_, err := Parse(bytes.NewReader(data), NewParseOptions())
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ParseError, got %T", err)
	}
	if pe.PartIndex < 0 {
		t.Errorf("expected PartIndex >= 0, got %d", pe.PartIndex)
	}
}

// buildNested returns a nested multipart message with depth levels of nesting.
func buildNested(depth int) string {
	var head, tail strings.Builder
	head.WriteString("From: a@example.com\r\nTo: b@example.com\r\nSubject: nested\r\nMIME-Version: 1.0\r\n")
	head.WriteString("Content-Type: multipart/mixed; boundary=\"b0\"\r\n\r\n")
	head.WriteString("--b0\r\n")
	for i := 1; i < depth; i++ {
		b := boundaryName(i)
		prev := boundaryName(i - 1)
		head.WriteString("Content-Type: multipart/mixed; boundary=\"" + b + "\"\r\n\r\n")
		head.WriteString("--" + b + "\r\n")
		tail.WriteString("--" + b + "--\r\n")
		tail.WriteString("--" + prev + "--\r\n")
		_ = prev
	}
	head.WriteString("Content-Type: text/plain\r\n\r\n")
	head.WriteString("hello\r\n")
	// close all boundaries in reverse order
	close := ""
	for i := depth - 1; i >= 0; i-- {
		close += "--" + boundaryName(i) + "--\r\n"
	}
	return head.String() + close
}

func boundaryName(i int) string {
	return "b" + string(rune('0'+i%10)) + string(rune('a'+i/10))
}
