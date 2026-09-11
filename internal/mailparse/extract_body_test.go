package mailparse

import (
	"strings"
	"testing"
)

// TestExtractBodyText_Native: a multipart/alternative with text/plain
// and text/html parts returns the text/plain verbatim with origin
// "native".
func TestExtractBodyText_Native(t *testing.T) {
	raw := "From: a@example.net\r\n" +
		"To: b@example.com\r\n" +
		"Subject: hi\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/alternative; boundary=B\r\n" +
		"\r\n" +
		"--B\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"Hi team,\r\n\r\nThe printer on floor 3 is fixed.\r\n" +
		"--B\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"\r\n" +
		"<p>Hi team,</p><p>The printer on floor 3 is <b>fixed</b>.</p>\r\n" +
		"--B--\r\n"
	msg, err := Parse(strings.NewReader(raw), NewParseOptions())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	text, origin := ExtractBodyText(msg)
	if origin != BodyTextOriginNative {
		t.Fatalf("origin = %q, want native", origin)
	}
	if !strings.Contains(text, "Hi team") || !strings.Contains(text, "printer on floor 3 is fixed") {
		t.Fatalf("text/plain not returned verbatim: %q", text)
	}
	if strings.Contains(text, "<b>") || strings.Contains(text, "</p>") {
		t.Fatalf("text/plain leaked HTML markup: %q", text)
	}
}

// TestExtractBodyText_DerivedFromHTML: an html-only message renders to
// plain text with link preservation in the `text (url)` form.
func TestExtractBodyText_DerivedFromHTML(t *testing.T) {
	raw := "From: a@example.net\r\n" +
		"To: b@example.com\r\n" +
		"Subject: hi\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"\r\n" +
		"<p>Hi team,</p>" +
		"<p>See the <a href=\"https://wiki/printer\">printer wiki</a> for the fix.</p>" +
		"<p>Plain url: <a href=\"https://x/\">https://x/</a></p>"
	msg, err := Parse(strings.NewReader(raw), NewParseOptions())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	text, origin := ExtractBodyText(msg)
	if origin != BodyTextOriginDerivedFromHTML {
		t.Fatalf("origin = %q, want derived_from_html", origin)
	}
	if !strings.Contains(text, "printer wiki (https://wiki/printer)") {
		t.Fatalf("link not rendered as `text (url)`: %q", text)
	}
	if !strings.Contains(text, "https://x/") {
		t.Fatalf("self-titled link not preserved: %q", text)
	}
	if strings.Contains(text, "<a") || strings.Contains(text, "</a>") {
		t.Fatalf("HTML markup leaked: %q", text)
	}
}

// TestExtractBodyText_None: a message whose only body is a non-text
// attachment (e.g. application/pdf) yields origin "none" with empty
// text.
func TestExtractBodyText_None(t *testing.T) {
	raw := "From: a@example.net\r\n" +
		"To: b@example.com\r\n" +
		"Subject: pdf-only\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: application/pdf\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		"JVBERi0xLjQK\r\n"
	msg, err := Parse(strings.NewReader(raw), NewParseOptions())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	text, origin := ExtractBodyText(msg)
	if origin != BodyTextOriginNone {
		t.Fatalf("origin = %q, want none", origin)
	}
	if text != "" {
		t.Fatalf("text = %q, want empty", text)
	}
}

// TestExtractBodyText_AppleMailLikeShape: Apple Mail flips the order
// inside multipart/alternative; the text/plain leaf still wins.
func TestExtractBodyText_AppleMailLikeShape(t *testing.T) {
	raw := "From: a@example.net\r\n" +
		"To: b@example.com\r\n" +
		"Subject: apple\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/alternative; boundary=BNDRY\r\n" +
		"\r\n" +
		"--BNDRY\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"\r\n" +
		"<p>HTML version of the body.</p>\r\n" +
		"--BNDRY\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"Plain version of the body.\r\n" +
		"--BNDRY--\r\n"
	msg, err := Parse(strings.NewReader(raw), NewParseOptions())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	text, origin := ExtractBodyText(msg)
	if origin != BodyTextOriginNative {
		t.Fatalf("origin = %q, want native", origin)
	}
	if !strings.Contains(text, "Plain version of the body") {
		t.Fatalf("text = %q", text)
	}
}

// TestExtractBodyText_MultipartMixedNestedAlternative: a Gmail-style
// multipart/mixed with an inner multipart/alternative still finds the
// text/plain part.
func TestExtractBodyText_MultipartMixedNestedAlternative(t *testing.T) {
	raw := "From: a@example.net\r\n" +
		"To: b@example.com\r\n" +
		"Subject: gmail\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=OUT\r\n" +
		"\r\n" +
		"--OUT\r\n" +
		"Content-Type: multipart/alternative; boundary=IN\r\n" +
		"\r\n" +
		"--IN\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"Native plain body.\r\n" +
		"--IN\r\n" +
		"Content-Type: text/html\r\n" +
		"\r\n" +
		"<p>html body</p>\r\n" +
		"--IN--\r\n" +
		"--OUT\r\n" +
		"Content-Type: application/octet-stream\r\n" +
		"Content-Disposition: attachment; filename=x.bin\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		"AAEC\r\n" +
		"--OUT--\r\n"
	msg, err := Parse(strings.NewReader(raw), NewParseOptions())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	text, origin := ExtractBodyText(msg)
	if origin != BodyTextOriginNative {
		t.Fatalf("origin = %q, want native", origin)
	}
	if !strings.Contains(text, "Native plain body") {
		t.Fatalf("text = %q", text)
	}
}

// pngLeafRawMessage builds a multipart/related message shaped like herold
// issue #325's store message 3400: a multipart/alternative whose genuine
// text/plain part decodes to plainAlt (empty, or whitespace-only) alongside
// a text/html part, plus a sibling leaf carrying a base64-encoded PNG
// signature that is mislabelled text/plain (mirroring the #324 extimg
// defect, which invalidates the Content-Type of an inline image so
// mailparse defaults it to "text/plain; charset=us-ascii" per RFC 2045).
// The PNG-labelled leaf precedes the alternative so that, pre-fix, it is
// the first text/plain candidate firstNonEmptyLeaf would select.
func pngLeafRawMessage(plainAlt string) string {
	return strings.Join([]string{
		"From: sender@example.test",
		"To: rcpt@example.test",
		"Subject: inline image mislabelled text/plain",
		"MIME-Version: 1.0",
		"Content-Type: multipart/related; boundary=\"rel\"",
		"",
		"--rel",
		"Content-Type: text/plain; charset=us-ascii",
		"Content-Transfer-Encoding: base64",
		"Content-ID: <img1>",
		"Content-Disposition: inline",
		"",
		"iVBORw0KGgoAAAANSUhEUg==", // base64 of the PNG signature + start of an IHDR chunk
		"--rel",
		"Content-Type: multipart/alternative; boundary=\"alt\"",
		"",
		"--alt",
		"Content-Type: text/plain; charset=utf-8",
		"",
		plainAlt,
		"--alt",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<p>Reduce tus costos de embalaje desde hoy</p>",
		"--alt--",
		"--rel--",
	}, "\r\n")
}

// TestExtractBodyText_SkipsMislabelledBinaryTextPlain verifies that
// ExtractBodyText never surfaces the raw bytes of a text/plain-labelled
// leaf whose decoded content is actually binary (re #325): firstNonEmptyLeaf
// skips the PNG-signature leaf, falls through the genuine-but-empty
// text/plain alternative, and lands on the HTML-derived text with origin
// "derived_from_html". This is the same fixture that pins the defect in
// mailparse.BodyPreview and protojmap/mail/email.previewFromValues.
func TestExtractBodyText_SkipsMislabelledBinaryTextPlain(t *testing.T) {
	raw := pngLeafRawMessage("")
	msg, err := Parse(strings.NewReader(raw), NewLenientParseOptions())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	text, origin := ExtractBodyText(msg)
	want := "Reduce tus costos de embalaje desde hoy"
	if text != want {
		t.Fatalf("text = %q, want %q", text, want)
	}
	if origin != BodyTextOriginDerivedFromHTML {
		t.Fatalf("origin = %q, want %q", origin, BodyTextOriginDerivedFromHTML)
	}
	if strings.Contains(text, "\x89PNG") {
		t.Fatalf("ExtractBodyText leaked the PNG signature: %q (bytes % x)", text, []byte(text))
	}
}

// TestHTMLToText_LinksAndEntities exercises the small renderer
// directly.  Link text rendered as `text (url)`; entities decoded.
func TestHTMLToText_LinksAndEntities(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			"plain entities",
			"a &amp; b &lt; c &gt; d &quot;e&quot; f &nbsp;g",
			`a & b < c > d "e" f g`,
		},
		{
			"link with different visible text",
			`<p>see <a href="https://example.com/help">the help page</a> for more</p>`,
			"see the help page (https://example.com/help) for more",
		},
		{
			"link whose text equals the url",
			`<p>visit <a href="https://example.com/">https://example.com/</a></p>`,
			"visit https://example.com/",
		},
		{
			"empty link text",
			`<a href="https://example.com"></a> tail`,
			"https://example.com tail",
		},
		{
			"script and style stripped",
			`<style>p{color:red}</style>hello<script>alert(1)</script>world`,
			"helloworld",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := htmlToText(tc.in)
			if got != tc.want {
				t.Fatalf("htmlToText(%q)\n  got:  %q\n  want: %q", tc.in, got, tc.want)
			}
		})
	}
}
