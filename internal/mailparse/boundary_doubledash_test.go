package mailparse

import (
	"strings"
	"testing"
)

// TestParse_BoundaryEndingWithDoubleDash: pathological but legal MIME
// boundary per RFC 2046 (bcharsnospace permits '-'). The boundary value
// ends with "--", so the start delimiter "--<boundary>" has a tail that
// overlaps with the close delimiter "--<boundary>--", and the parent's
// "--<parent>" is a substring of the nested child's "--<child>" — enmime's
// substring-based boundary detection then mistakes the child's start-line
// for the parent's, kills the parent's content stream early, and the child
// sees zero parts → "(no body)" in the suite. User-reported 2026-05-26.
//
// The fix normalizes any trailing-dash boundary value (in headers and in
// every matching delimiter line) before handing the message to enmime.
func TestParse_BoundaryEndingWithDoubleDash_TwoLevels(t *testing.T) {
	raw := "From: a@example.net\r\n" +
		"To: b@example.com\r\n" +
		"Subject: hi\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/related;\r\n" +
		" boundary=\"------=_NextPart_82278ce9003e216ca51844533caa058d--\"\r\n" +
		"\r\n" +
		"--------=_NextPart_82278ce9003e216ca51844533caa058d--\r\n" +
		"Content-Type: multipart/alternative;\r\n" +
		" boundary=\"----------=_NextPart_82278ce9003e216ca51844533caa058d--\"\r\n" +
		"\r\n" +
		"------------=_NextPart_82278ce9003e216ca51844533caa058d--\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"PLAIN BODY HERE.\r\n" +
		"------------=_NextPart_82278ce9003e216ca51844533caa058d--\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"\r\n" +
		"<html><body>HTML BODY HERE.</body></html>\r\n" +
		"------------=_NextPart_82278ce9003e216ca51844533caa058d----\r\n" +
		"--------=_NextPart_82278ce9003e216ca51844533caa058d----\r\n"
	msg, err := Parse(strings.NewReader(raw), NewParseOptions())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	text, origin := ExtractBodyText(msg)
	if origin != BodyTextOriginNative {
		t.Fatalf("origin = %q, want native (text=%q)", origin, text)
	}
	if !strings.Contains(text, "PLAIN BODY HERE") {
		t.Fatalf("text/plain body not extracted: %q", text)
	}
}

// TestParse_BoundaryEndingWithSingleDash: any boundary ending in '-' can
// trigger enmime's substring confusion when nested or when the close marker
// is sought, so the normalizer trigger covers it.
func TestParse_BoundaryEndingWithSingleDash(t *testing.T) {
	raw := "From: a@example.net\r\n" +
		"To: b@example.com\r\n" +
		"Subject: hi\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/alternative; boundary=\"B-\"\r\n" +
		"\r\n" +
		"--B-\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"plain body\r\n" +
		"--B---\r\n"
	msg, err := Parse(strings.NewReader(raw), NewParseOptions())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	text, _ := ExtractBodyText(msg)
	if !strings.Contains(text, "plain body") {
		t.Fatalf("body not extracted: %q", text)
	}
}

// TestParse_NoTrailingDashBoundaryIsUntouched: when no boundary ends with
// '-', normalization is a no-op and the parser sees the original bytes.
func TestParse_NoTrailingDashBoundaryIsUntouched(t *testing.T) {
	raw := "From: a@example.net\r\n" +
		"To: b@example.com\r\n" +
		"Subject: hi\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/alternative; boundary=B\r\n" +
		"\r\n" +
		"--B\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"plain body\r\n" +
		"--B--\r\n"
	msg, err := Parse(strings.NewReader(raw), NewParseOptions())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	text, _ := ExtractBodyText(msg)
	if !strings.Contains(text, "plain body") {
		t.Fatalf("body not extracted: %q", text)
	}
}

// TestNormalize_Idempotency: running the normalizer twice produces the same
// bytes (the replacement values do not themselves end in '-' so the second
// pass is a no-op).
func TestNormalize_Idempotency(t *testing.T) {
	raw := []byte("Content-Type: multipart/related; boundary=\"X--\"\r\n\r\n--X--\r\nHi\r\n--X----\r\n")
	once := normalizeTrailingDashBoundaries(raw)
	twice := normalizeTrailingDashBoundaries(once)
	if string(once) != string(twice) {
		t.Fatalf("not idempotent:\n once=%q\n twice=%q", once, twice)
	}
}
