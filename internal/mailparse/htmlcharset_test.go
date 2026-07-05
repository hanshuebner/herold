package mailparse

import (
	"bytes"
	"strings"
	"testing"
)

// TestExtractHTMLMetaCharset_HTML4ContentType tests the HTML4 form:
// <meta http-equiv="Content-Type" content="text/html;charset=UTF-8">
func TestExtractHTMLMetaCharset_HTML4ContentType(t *testing.T) {
	cases := []struct {
		name string
		html string
		want string
	}{
		{
			name: "html4 content-type meta",
			html: `<html><head><meta content="text/html;charset=UTF-8" http-equiv="Content-Type"/></head><body></body></html>`,
			want: "UTF-8",
		},
		{
			name: "html4 content-type meta with space",
			html: `<html><head><meta http-equiv="Content-Type" content="text/html; charset=UTF-8"/></head></html>`,
			want: "UTF-8",
		},
		{
			name: "html4 charset iso",
			html: `<html><head><meta http-equiv="Content-Type" content="text/html; charset=ISO-8859-1"/></head></html>`,
			want: "ISO-8859-1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractHTMLMetaCharset([]byte(tc.html))
			if !strings.EqualFold(got, tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestExtractHTMLMetaCharset_HTML5 tests the HTML5 form:
// <meta charset="UTF-8">
func TestExtractHTMLMetaCharset_HTML5(t *testing.T) {
	html := `<html><head><meta charset="UTF-8"/></head><body></body></html>`
	got := extractHTMLMetaCharset([]byte(html))
	if !strings.EqualFold(got, "UTF-8") {
		t.Errorf("got %q, want UTF-8", got)
	}
}

// TestExtractHTMLMetaCharset_NotFound tests that "" is returned when there is no meta charset.
func TestExtractHTMLMetaCharset_NotFound(t *testing.T) {
	html := `<html><head><title>No charset</title></head><body></body></html>`
	got := extractHTMLMetaCharset([]byte(html))
	if got != "" {
		t.Errorf("got %q, want empty string", got)
	}
}

// TestExtractHTMLMetaCharset_StopsAtBody verifies that the scanner does not
// read past the <body> tag to avoid false positives from body content.
func TestExtractHTMLMetaCharset_StopsAtBody(t *testing.T) {
	// Meta tag appears after <body> — should NOT be found.
	html := `<html><head></head><body><meta charset="UTF-8"/></body></html>`
	got := extractHTMLMetaCharset([]byte(html))
	if got != "" {
		t.Errorf("found charset %q past <body>, want empty", got)
	}
}

// TestExtractHTMLMetaCharset_LimitedScan verifies that a document larger than
// 4096 bytes is scanned only within the first 4096 bytes.
func TestExtractHTMLMetaCharset_LimitedScan(t *testing.T) {
	// Build a document where the meta appears at byte 5000 — outside the scan window.
	padding := strings.Repeat("<!-- padding -->", 350) // >4096 bytes
	html := "<html><head>" + padding + `<meta charset="UTF-8"/></head></html>`
	if len([]byte(html)) <= 4096 {
		t.Skip("padding too short for this test")
	}
	got := extractHTMLMetaCharset([]byte(html))
	if got != "" {
		t.Errorf("found charset %q outside scan window, want empty", got)
	}
}

// TestIsLatinSingleByteCharset covers the common charset names that trigger
// charset reconciliation.
func TestIsLatinSingleByteCharset(t *testing.T) {
	trueCases := []string{
		"iso-8859-1", "ISO-8859-1", "Latin-1", "windows-1252", "cp1252",
		"iso-8859-15", "ISO8859-2",
	}
	for _, cs := range trueCases {
		if !isLatinSingleByteCharset(cs) {
			t.Errorf("isLatinSingleByteCharset(%q) = false, want true", cs)
		}
	}
	falseCases := []string{
		"utf-8", "UTF-8", "utf8", "us-ascii", "", "koi8-r", "shift_jis",
	}
	for _, cs := range falseCases {
		if isLatinSingleByteCharset(cs) {
			t.Errorf("isLatinSingleByteCharset(%q) = true, want false", cs)
		}
	}
}

// buildMimeMsg constructs a minimal MIME message byte slice suitable for
// mailparse.Parse. contentType is the full Content-Type value of the single
// text part, cte is the Content-Transfer-Encoding, and body is the raw
// (already CTE-encoded) body bytes.
func buildMimeMsg(contentType, cte string, body []byte) []byte {
	var b bytes.Buffer
	b.WriteString("From: sender@example.test\r\n")
	b.WriteString("To: rcpt@example.test\r\n")
	b.WriteString("Subject: Test\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: " + contentType + "\r\n")
	if cte != "" {
		b.WriteString("Content-Transfer-Encoding: " + cte + "\r\n")
	}
	b.WriteString("\r\n")
	b.Write(body)
	return b.Bytes()
}

// TestDecodeTextPart_MismatchedMIMECharsetUTF8 is the primary regression test
// for the charset mojibake bug. The MIME header declares ISO-8859-1 but the
// QP-decoded bytes are valid UTF-8 and the HTML meta declares UTF-8.
//
// Pre-fix: decodeTextPart would run convertCharset("ISO-8859-1") on the UTF-8
// bytes and produce mojibake ("Bücher" -> "BÃ¼cher").
// Post-fix: the charset reconciliation detects the conflict and returns the
// bytes as-is (already correct UTF-8).
func TestDecodeTextPart_MismatchedMIMECharsetUTF8(t *testing.T) {
	// Build a minimal text/html part where:
	// - MIME header says charset=ISO-8859-1
	// - The QP-decoded bytes are valid UTF-8
	// - The HTML meta says charset=UTF-8
	//
	// "Bücher" in UTF-8 QP: B=C3=BCcher
	// "für" in UTF-8 QP:    f=C3=BCr
	// "Außerdem" in UTF-8 QP: Au=C3=9Ferdem
	// "•" (U+2022) in UTF-8 QP: =E2=80=A2
	qpBody := []byte(
		"<!DOCTYPE html><html><head>" +
			`<meta content="text/html;charset=UTF-8" http-equiv="Content-Type"/>` +
			"</head><body>" +
			"B=C3=BCcher, f=C3=BCr, Au=C3=9Ferdem, =E2=80=A2" +
			"</body></html>",
	)
	raw := buildMimeMsg(`text/html; charset="ISO-8859-1"`, "quoted-printable", qpBody)
	opts := NewParseOptions()
	opts.StrictCharset = false // tolerate the misdeclaration
	msg, err := Parse(bytes.NewReader(raw), opts)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	text := msg.Body.Text
	wantContains := []string{"Bücher", "für", "Außerdem", "•"}
	// Ã¼ is the Windows-1252 mis-decoding of the UTF-8 bytes 0xC3 0xBC (ü).
	// Its presence in the output indicates the wrong charset path ran.
	wantAbsent := []string{"BÃ¼cher", "fÃ¼r", "Ã¼", "Â"}
	for _, w := range wantContains {
		if !strings.Contains(text, w) {
			t.Errorf("decoded text missing %q\nfull text: %q", w, text)
		}
	}
	for _, w := range wantAbsent {
		if strings.Contains(text, w) {
			t.Errorf("decoded text contains mojibake %q\nfull text: %q", w, text)
		}
	}
}

// TestDecodeTextPart_GenuineLatin1NotMistreated is the negative case: a
// text/html part that genuinely uses ISO-8859-1 (the body bytes are real
// Latin-1, not UTF-8) must NOT be mis-treated as UTF-8.
//
// In ISO-8859-1, ü = 0xFC, ß = 0xDF — these are not valid UTF-8 lead bytes,
// so isUTF8OrASCII(cteDecoded) returns false and the reconciliation is skipped.
func TestDecodeTextPart_GenuineLatin1NotMistreated(t *testing.T) {
	// Build a text/html part with genuine ISO-8859-1 bytes.
	// ü in ISO-8859-1: 0xFC  => QP: =FC
	// ß in ISO-8859-1: 0xDF  => QP: =DF
	// Correct ISO-8859-1 meta (matches MIME header — no conflict).
	qpBody := []byte(
		"<!DOCTYPE html><html><head>" +
			`<meta content="text/html;charset=ISO-8859-1" http-equiv="Content-Type"/>` +
			"</head><body>" +
			"B=FCcher, f=FCr, Au=DFerdem" +
			"</body></html>",
	)
	raw := buildMimeMsg(`text/html; charset="ISO-8859-1"`, "quoted-printable", qpBody)
	opts := NewParseOptions()
	opts.StrictCharset = false
	msg, err := Parse(bytes.NewReader(raw), opts)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	text := msg.Body.Text
	// Correct output: ISO-8859-1 decoded to UTF-8 — German umlauts are correct.
	wantContains := []string{"Bücher", "für", "Außerdem"}
	for _, w := range wantContains {
		if !strings.Contains(text, w) {
			t.Errorf("decoded text missing %q\nfull text: %q", w, text)
		}
	}
}

// TestDecodeTextPart_MismatchedMIMECharsetNonHTMLUnchanged verifies that the
// charset reconciliation only applies to text/html parts, not text/plain.
func TestDecodeTextPart_MismatchedMIMECharsetNonHTMLUnchanged(t *testing.T) {
	// text/plain with UTF-8 bytes misdeclared as ISO-8859-1 has no HTML meta
	// to reconcile against, so the original conversion path runs.
	// The QP bytes 0xC3 0xBC decoded as ISO-8859-1 produce Ã¼ (mojibake).
	qpBody := []byte("B=C3=BCcher")
	raw := buildMimeMsg(`text/plain; charset="ISO-8859-1"`, "quoted-printable", qpBody)
	opts := NewParseOptions()
	opts.StrictCharset = false
	msg, err := Parse(bytes.NewReader(raw), opts)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	text := msg.Body.Text
	// text/plain has no meta, so the reconciliation does not trigger; the
	// ISO-8859-1 conversion runs and produces mojibake. This is intentional:
	// the fix is text/html-only, as plain text has no in-body charset signal.
	if strings.Contains(text, "Bücher") {
		t.Errorf("text/plain should not benefit from HTML meta reconciliation, got %q", text)
	}
}
