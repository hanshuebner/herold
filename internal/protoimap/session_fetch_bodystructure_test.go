package protoimap

import (
	"strings"
	"testing"

	imap "github.com/emersion/go-imap/v2"
)

// reproMultipartAlternative is a synthetic reproduction of the MIME shape
// from herold issue #321 (production message id 3366): a multipart/
// alternative message with a text/plain part and a multipart/related HTML
// part carrying one inline image. Before the fix, formatBodyStructure
// answered every message -- including this one -- with the single-part
// form, leaving IMAP clients that navigate by BODYSTRUCTURE (e.g. the
// Gmail Android app) unable to find a text section to fetch.
const reproMultipartAlternative = "From: sender@example.test\r\n" +
	"To: alice@example.test\r\n" +
	"Subject: repro\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/alternative; boundary=\"outer\"\r\n" +
	"\r\n" +
	"--outer\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"Content-Transfer-Encoding: 7bit\r\n" +
	"\r\n" +
	"hello plain\r\n" +
	"--outer\r\n" +
	"Content-Type: multipart/related; boundary=\"inner\"\r\n" +
	"\r\n" +
	"--inner\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n" +
	"Content-Transfer-Encoding: 7bit\r\n" +
	"\r\n" +
	"<p>hello html</p>\r\n" +
	"--inner\r\n" +
	"Content-Type: image/jpeg\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"Content-ID: <img1>\r\n" +
	"Content-Disposition: inline; filename=\"a.jpg\"\r\n" +
	"\r\n" +
	"aGVsbG8=\r\n" +
	"--inner--\r\n" +
	"--outer--\r\n"

// reproMessageRFC822 is a synthetic multipart/mixed message carrying a
// text/plain part and a forwarded message/rfc822 attachment, exercising
// the nested envelope/body-structure/line-count form (RFC 9051 sec
// 7.5.2) and section numbering that continues inside the encapsulated
// message (BODY[2.HEADER], BODY[2.TEXT], BODY[2.1]).
const reproMessageRFC822 = "From: sender@example.test\r\n" +
	"To: alice@example.test\r\n" +
	"Subject: fwd\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=\"mix\"\r\n" +
	"\r\n" +
	"--mix\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"see attached\r\n" +
	"--mix\r\n" +
	"Content-Type: message/rfc822\r\n" +
	"Content-Disposition: attachment; filename=\"orig.eml\"\r\n" +
	"\r\n" +
	"From: orig@example.test\r\n" +
	"To: someone@example.test\r\n" +
	"Subject: original\r\n" +
	"Date: Thu, 30 Apr 2026 10:00:00 +0000\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"nested body\r\n" +
	"--mix--\r\n"

// TestFormatBodyStructure_MultipartAlternative pins the exact
// BODYSTRUCTURE text for the reproduction shape (re #321): a nested
// multipart/alternative -> multipart/related tree, not the single-part
// form the message previously got flattened into.
func TestFormatBodyStructure_MultipartAlternative(t *testing.T) {
	raw := []byte(reproMultipartAlternative)

	// Sizes/line counts reflect the raw wire bytes: the scanner strips the
	// single CRLF immediately preceding each MIME boundary line (transport
	// framing, not body content), so e.g. "hello plain\r\n" before the
	// boundary is an 11-octet, zero-line body.
	got := formatBodyStructure(raw, true)
	want := `BODYSTRUCTURE (("TEXT" "PLAIN" ("CHARSET" "utf-8") NIL NIL "7BIT" 11 0 NIL NIL NIL NIL)(("TEXT" "HTML" ("CHARSET" "utf-8") NIL NIL "7BIT" 17 0 NIL NIL NIL NIL)("IMAGE" "JPEG" NIL "<img1>" NIL "BASE64" 8 NIL ("INLINE" ("FILENAME" "a.jpg")) NIL NIL) "RELATED" ("BOUNDARY" "inner") NIL NIL NIL) "ALTERNATIVE" ("BOUNDARY" "outer") NIL NIL NIL)`
	if got != want {
		t.Fatalf("BODYSTRUCTURE mismatch:\n got:  %s\n want: %s", got, want)
	}

	// The non-extended BODY form omits the parameter/disposition/language/
	// location extension data for multipart containers, and the MD5/
	// disposition/language/location tail for single parts.
	gotBody := formatBodyStructure(raw, false)
	wantBody := `BODY (("TEXT" "PLAIN" ("CHARSET" "utf-8") NIL NIL "7BIT" 11 0)(("TEXT" "HTML" ("CHARSET" "utf-8") NIL NIL "7BIT" 17 0)("IMAGE" "JPEG" NIL "<img1>" NIL "BASE64" 8) "RELATED") "ALTERNATIVE")`
	if gotBody != wantBody {
		t.Fatalf("BODY mismatch:\n got:  %s\n want: %s", gotBody, wantBody)
	}
}

// TestExtractSection_NumberedParts resolves BODY[1], BODY[2.1], BODY[2.2]
// and BODY[2.2.MIME] against the reproduction shape's nested tree.
func TestExtractSection_NumberedParts(t *testing.T) {
	raw := []byte(reproMultipartAlternative)

	cases := []struct {
		name string
		sec  *imap.FetchItemBodySection
		want string
	}{
		{"part1_plain_text", &imap.FetchItemBodySection{Part: []int{1}}, "hello plain"},
		{"part2.1_html_text", &imap.FetchItemBodySection{Part: []int{2, 1}}, "<p>hello html</p>"},
		{"part2.2_image_raw", &imap.FetchItemBodySection{Part: []int{2, 2}}, "aGVsbG8="},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractSection(raw, tc.sec)
			if string(got) != tc.want {
				t.Fatalf("extractSection(%v) = %q, want %q", tc.sec.Part, got, tc.want)
			}
		})
	}

	mime := extractSection(raw, &imap.FetchItemBodySection{Part: []int{2, 2}, Specifier: imap.PartSpecifierMIME})
	for _, want := range []string{"Content-Type: image/jpeg", "Content-ID: <img1>", "Content-Disposition: inline"} {
		if !strings.Contains(string(mime), want) {
			t.Fatalf("BODY[2.2.MIME] = %q, missing %q", mime, want)
		}
	}

	// A container part number with no further specifier has no defined
	// wire form; must not panic and must return nothing rather than a
	// guess.
	if got := extractSection(raw, &imap.FetchItemBodySection{Part: []int{2}}); got != nil {
		t.Fatalf("BODY[2] (a multipart container) = %q, want nil", got)
	}

	// Out-of-range and zero part numbers are rejected, not panics.
	if got := extractSection(raw, &imap.FetchItemBodySection{Part: []int{9}}); got != nil {
		t.Fatalf("BODY[9] out of range = %q, want nil", got)
	}
}

// TestFormatBodyStructure_MessageRFC822 pins the nested envelope + body
// structure + line-count form for a message/rfc822 part (RFC 9051 sec
// 7.5.2), and the section numbering that continues inside it.
func TestFormatBodyStructure_MessageRFC822(t *testing.T) {
	raw := []byte(reproMessageRFC822)

	// The nested message's own envelope has no Sender/Reply-To header, so
	// those envelope members are NIL rather than defaulting to From --
	// matching convertEnvelope's handling of the top-level ENVELOPE fetch
	// item (session_mailbox.go) for consistency.
	got := formatBodyStructure(raw, true)
	want := `BODYSTRUCTURE (("TEXT" "PLAIN" ("CHARSET" "utf-8") NIL NIL "7BIT" 12 0 NIL NIL NIL NIL)("MESSAGE" "RFC822" NIL NIL NIL "7BIT" 163 ("Thu, 30 Apr 2026 10:00:00 +0000" "original" ((NIL NIL "orig" "example.test")) NIL NIL ((NIL NIL "someone" "example.test")) NIL NIL NIL NIL) ("TEXT" "PLAIN" ("CHARSET" "utf-8") NIL NIL "7BIT" 11 0 NIL NIL NIL NIL) 6 NIL ("ATTACHMENT" ("FILENAME" "orig.eml")) NIL NIL) "MIXED" ("BOUNDARY" "mix") NIL NIL NIL)`
	if got != want {
		t.Fatalf("BODYSTRUCTURE mismatch:\n got:  %s\n want: %s", got, want)
	}
}

// TestExtractSection_NestedMessageRFC822 fetches the encapsulated
// message's header, text, and first subpart via BODY[2.HEADER],
// BODY[2.TEXT], and BODY[2.1], and confirms BODY[2.1.HEADER] -- HEADER
// applied to a leaf that is not itself message/rfc822 -- is rejected.
func TestExtractSection_NestedMessageRFC822(t *testing.T) {
	raw := []byte(reproMessageRFC822)

	hdr := extractSection(raw, &imap.FetchItemBodySection{Part: []int{2}, Specifier: imap.PartSpecifierHeader})
	if !strings.Contains(string(hdr), "Subject: original") || !strings.Contains(string(hdr), "From: orig@example.test") {
		t.Fatalf("BODY[2.HEADER] = %q, missing expected header fields", hdr)
	}

	text := extractSection(raw, &imap.FetchItemBodySection{Part: []int{2}, Specifier: imap.PartSpecifierText})
	if string(text) != "nested body" {
		t.Fatalf("BODY[2.TEXT] = %q, want %q", text, "nested body")
	}

	sub := extractSection(raw, &imap.FetchItemBodySection{Part: []int{2, 1}})
	if string(sub) != "nested body" {
		t.Fatalf("BODY[2.1] = %q, want %q", sub, "nested body")
	}

	if got := extractSection(raw, &imap.FetchItemBodySection{Part: []int{2, 1}, Specifier: imap.PartSpecifierHeader}); got != nil {
		t.Fatalf("BODY[2.1.HEADER] on a non-message leaf = %q, want nil", got)
	}
}
