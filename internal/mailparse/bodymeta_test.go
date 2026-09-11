package mailparse_test

import (
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/mailparse"
)

// parseTestMsg parses a raw RFC 5322 message with lax options so test fixtures
// need not be perfectly formed.
func parseTestMsg(t *testing.T, raw string) mailparse.Message {
	t.Helper()
	opts := mailparse.NewParseOptions()
	opts.StrictBoundary = false
	opts.StrictBase64 = false
	opts.StrictQP = false
	msg, err := mailparse.Parse(strings.NewReader(raw), opts)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return msg
}

func joinLines(lines ...string) string {
	return strings.Join(lines, "\r\n")
}

// plainMsg builds a minimal plain-text-only message.
func plainMsg(body string) string {
	return joinLines(
		"From: sender@example.test",
		"To: rcpt@example.test",
		"Subject: Test",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"",
		body,
	)
}

// TestBodyPreview_PlainText verifies that BodyPreview returns the trimmed body of
// a plain-text message and respects the limit.
func TestBodyPreview_PlainText(t *testing.T) {
	body := "Hello, world!"
	msg := parseTestMsg(t, plainMsg(body))

	got := mailparse.BodyPreview(msg, 256)
	if got != body {
		t.Errorf("got %q, want %q", got, body)
	}

	got5 := mailparse.BodyPreview(msg, 5)
	if got5 != "Hello" {
		t.Errorf("limit 5: got %q, want %q", got5, "Hello")
	}
}

// TestBodyPreview_TrimSpace verifies that leading/trailing whitespace is
// stripped before the limit is applied.
func TestBodyPreview_TrimSpace(t *testing.T) {
	msg := parseTestMsg(t, plainMsg("  \n  hello  \n  "))
	got := mailparse.BodyPreview(msg, 256)
	if got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

// TestBodyPreview_NoTextPart verifies that a message with no text/plain body
// falls back to extracted text from the text/html part rather than raw
// markup or an empty string (re #263).
func TestBodyPreview_NoTextPart(t *testing.T) {
	raw := joinLines(
		"From: sender@example.test",
		"To: rcpt@example.test",
		"Subject: HTML only",
		"MIME-Version: 1.0",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<html><body>hello</body></html>",
	)
	msg := parseTestMsg(t, raw)
	got := mailparse.BodyPreview(msg, 256)
	if got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

// TestBodyPreview_HTMLOnly_StripsTagsAndComments verifies that an HTML-only
// message previews as extracted text -- tags stripped, entities decoded,
// comments/script/style removed, whitespace collapsed -- not raw markup
// (re #263). It also reproduces the ticket's leading `<!-- FILE: undefined
// -->` template-artifact comment and confirms it does not leak into the
// preview.
func TestBodyPreview_HTMLOnly_StripsTagsAndComments(t *testing.T) {
	raw := joinLines(
		"From: sender@example.test",
		"To: rcpt@example.test",
		"Subject: HTML only",
		"MIME-Version: 1.0",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<!-- FILE: undefined -->"+
			"<!DOCTYPE html><html><head><style>body{color:red}</style>"+
			"<script>alert('x')</script></head><body>"+
			"<p>Hello&nbsp;&amp;&nbsp;welcome</p><p>to the newsletter</p>"+
			"</body></html>",
	)
	msg := parseTestMsg(t, raw)
	got := mailparse.BodyPreview(msg, 256)
	want := "Hello & welcome to the newsletter"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if strings.Contains(got, "<") || strings.Contains(got, "DOCTYPE") {
		t.Errorf("preview leaked raw markup: %q", got)
	}
	if strings.Contains(got, "FILE:") || strings.Contains(got, "alert") || strings.Contains(got, "color:red") {
		t.Errorf("preview leaked comment/script/style content: %q", got)
	}
}

// TestBodyPreview_MultipartAlternative verifies that BodyPreview picks the
// leftmost text/plain in a multipart/alternative message.
func TestBodyPreview_MultipartAlternative(t *testing.T) {
	raw := joinLines(
		"From: sender@example.test",
		"To: rcpt@example.test",
		"Subject: multipart/alternative",
		"MIME-Version: 1.0",
		"Content-Type: multipart/alternative; boundary=\"b\"",
		"",
		"--b",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"Plain text body here",
		"--b",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<html><body>HTML body here</body></html>",
		"--b--",
	)
	msg := parseTestMsg(t, raw)
	got := mailparse.BodyPreview(msg, 256)
	if got != "Plain text body here" {
		t.Errorf("got %q, want %q", got, "Plain text body here")
	}
}

// parseLenientTestMsg parses raw with the lenient options the ingest and
// background body-meta backfill paths use in production
// (mailparse.NewLenientParseOptions, per internal/bodymeta/worker.go). A
// declared charset that does not decode cleanly -- as happens when a
// binary payload is defaulted to "text/plain; charset=us-ascii" per RFC
// 2045 (re #324) -- is an encoding-problem flag, not a parse error, under
// these options; parseTestMsg's stricter defaults would reject the fixture
// before BodyPreview ever saw it.
func parseLenientTestMsg(t *testing.T, raw string) mailparse.Message {
	t.Helper()
	msg, err := mailparse.Parse(strings.NewReader(raw), mailparse.NewLenientParseOptions())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return msg
}

// pngLeafMsg builds a multipart/related message shaped like herold issue
// #325's store message 3400: a multipart/alternative whose genuine
// text/plain part decodes to plainAlt (empty, or whitespace-only) alongside
// a text/html part, plus a sibling leaf carrying a base64-encoded PNG
// signature that is mislabelled text/plain (mirroring the #324 extimg
// defect, which invalidates the Content-Type of an inline image so
// mailparse defaults it to "text/plain; charset=us-ascii" per RFC 2045).
// The PNG-labelled leaf precedes the alternative so that, pre-fix, it is
// the first candidate BOTH mailparse.BodyPreview and
// protojmap/mail/email.previewFromValues would select.
func pngLeafMsg(plainAlt string) string {
	return joinLines(
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
	)
}

// TestBodyPreview_SkipsMislabelledBinaryTextPlain verifies that BodyPreview
// never surfaces the raw bytes of a text/plain-labelled leaf whose decoded
// content is actually binary (re #325): it skips the PNG-signature leaf,
// falls through the genuine-but-empty text/plain alternative (already
// empty, so already skipped by the pre-existing empty-candidate check --
// this settles the issue's "unverified" question: the genuine alternative
// is skipped because it decodes to the empty string, not for any other
// reason), and lands on the HTML-extracted text.
func TestBodyPreview_SkipsMislabelledBinaryTextPlain(t *testing.T) {
	msg := parseLenientTestMsg(t, pngLeafMsg(""))
	got := mailparse.BodyPreview(msg, 256)
	want := "Reduce tus costos de embalaje desde hoy"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	pngSig := "\x89PNG\r\n\x1a\n"
	if strings.Contains(got, pngSig) || strings.Contains(got, "\x89PNG") {
		t.Errorf("preview leaked the PNG signature: %q (bytes % x)", got, []byte(got))
	}
}

// TestBodyPreview_SkipsMislabelledBinaryTextPlain_WhitespaceOnlyAlt is the
// same fixture with a whitespace-only (rather than zero-byte) genuine
// text/plain alternative, covering the issue's "empty or whitespace-only"
// wording. CollapseWhitespace reduces the whitespace-only candidate to the
// empty string, so it falls through exactly like the fully-empty case.
func TestBodyPreview_SkipsMislabelledBinaryTextPlain_WhitespaceOnlyAlt(t *testing.T) {
	msg := parseLenientTestMsg(t, pngLeafMsg("   \t  "))
	got := mailparse.BodyPreview(msg, 256)
	want := "Reduce tus costos de embalaje desde hoy"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if strings.Contains(got, "\x89PNG") {
		t.Errorf("preview leaked the PNG signature: %q (bytes % x)", got, []byte(got))
	}
}

// TestBodyPreview_LimitRune verifies that BodyPreview matches the byte-truncation
// semantics of previewFromValues in render.go: n is a byte limit; the walk-back
// removes continuation bytes (mask 0x80) but not leading multi-byte bytes.
// This test documents and guards the exact same truncation shape.
func TestBodyPreview_LimitRune(t *testing.T) {
	// The snowman character (U+2603) is 3 bytes: 0xe2 0x98 0x83.
	// A body of 255 'A's (bytes 0-254) + snowman (bytes 255-257) = 258 bytes.
	//
	// With n=256:
	//   s[:256] => bytes 0-255 = 255 'A's + 0xe2 (leading byte of snowman).
	//   Walk-back: 0xe2 & 0xC0 = 0xC0 != 0x80 => stop.
	//   Result: 256 bytes ending in a bare 0xe2 (matches previewFromValues semantics).
	body := strings.Repeat("A", 255) + "☃"
	msg := parseTestMsg(t, plainMsg(body))

	got := mailparse.BodyPreview(msg, 256)
	want := strings.Repeat("A", 255) + "\xe2" // bare leading byte, matching render.go semantics
	if got != want {
		t.Errorf("BodyPreview(msg, 256): got len=%d, want len=%d\ngot:  %q\nwant: %q",
			len(got), len(want), got, want)
	}

	// With n=257: s[:257] ends with 0x98 (second byte of snowman).
	// Walk-back: 0x98 & 0xC0 = 0x80 => remove. Then 0xe2 & 0xC0 = 0xC0 => stop.
	// Result: 255 bytes (just the 'A's + bare 0xe2, then removed 0x98).
	// Actually: after removing 0x98, we check 0xe2: it does NOT match 0x80, so
	// we stop at 256 bytes (255 'A's + 0xe2). Same as the 256-byte case.
	got257 := mailparse.BodyPreview(msg, 257)
	if got257 != want {
		t.Errorf("BodyPreview(msg, 257): got len=%d, want len=%d", len(got257), len(want))
	}

	// With n=0 (no limit) the full body is returned.
	gotFull := mailparse.BodyPreview(msg, 0)
	if gotFull != body {
		t.Errorf("n=0 should return full body; got len=%d want len=%d", len(gotFull), len(body))
	}

	// With a limit larger than the body size the full body is returned.
	gotAll := mailparse.BodyPreview(msg, 1000)
	if gotAll != body {
		t.Errorf("large limit should return full body; got len=%d want len=%d", len(gotAll), len(body))
	}
}

// TestHasAttachment_PlainOnly verifies that a plain-text-only message returns
// false.
func TestHasAttachment_PlainOnly(t *testing.T) {
	msg := parseTestMsg(t, plainMsg("no attachments here"))
	if mailparse.HasAttachment(msg) {
		t.Error("want false for plain-text-only message")
	}
}

// TestHasAttachment_WithAttachment verifies that a multipart/mixed message with
// an explicit attachment returns true.
func TestHasAttachment_WithAttachment(t *testing.T) {
	raw := joinLines(
		"From: sender@example.test",
		"To: rcpt@example.test",
		"Subject: with attachment",
		"MIME-Version: 1.0",
		"Content-Type: multipart/mixed; boundary=\"b\"",
		"",
		"--b",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"see attached",
		"--b",
		"Content-Type: image/jpeg",
		"Content-Disposition: attachment; filename=\"photo.jpg\"",
		"",
		"FAKEDATA",
		"--b--",
	)
	msg := parseTestMsg(t, raw)
	if !mailparse.HasAttachment(msg) {
		t.Error("want true for message with explicit Content-Disposition: attachment part")
	}
}

// TestHasAttachment_InlineWithCID verifies that a multipart/related message
// with an inline image referenced by CID returns false (it is not a
// standalone attachment).
func TestHasAttachment_InlineWithCID(t *testing.T) {
	raw := joinLines(
		"From: sender@example.test",
		"To: rcpt@example.test",
		"Subject: inline image with cid",
		"MIME-Version: 1.0",
		"Content-Type: multipart/related; boundary=\"b\"",
		"",
		"--b",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<html><body><img src=\"cid:abc@mail\"/></body></html>",
		"--b",
		"Content-Type: image/jpeg",
		"Content-Disposition: inline; filename=\"photo.jpg\"",
		"Content-ID: <abc@mail>",
		"",
		"FAKEDATA",
		"--b--",
	)
	msg := parseTestMsg(t, raw)
	if mailparse.HasAttachment(msg) {
		t.Error("want false: inline image with CID is not a standalone attachment")
	}
}

// TestHasAttachment_InlineNoCID verifies that an image with Content-Disposition:
// inline but NO Content-ID is treated as an attachment.
func TestHasAttachment_InlineNoCID(t *testing.T) {
	raw := joinLines(
		"From: sender@example.test",
		"To: rcpt@example.test",
		"Subject: inline image no cid",
		"MIME-Version: 1.0",
		"Content-Type: multipart/mixed; boundary=\"b\"",
		"",
		"--b",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"see attached",
		"--b",
		"Content-Type: image/jpeg",
		"Content-Disposition: inline",
		"",
		"FAKEDATA",
		"--b--",
	)
	msg := parseTestMsg(t, raw)
	if !mailparse.HasAttachment(msg) {
		t.Error("want true: inline image without CID is treated as a regular attachment")
	}
}

// TestHasAttachment_NestedMultipart verifies correct classification for deeply
// nested structures.
func TestHasAttachment_NestedMultipart(t *testing.T) {
	// multipart/mixed containing multipart/alternative (text+html) + an attachment.
	raw := joinLines(
		"From: sender@example.test",
		"To: rcpt@example.test",
		"Subject: nested",
		"MIME-Version: 1.0",
		"Content-Type: multipart/mixed; boundary=\"outer\"",
		"",
		"--outer",
		"Content-Type: multipart/alternative; boundary=\"inner\"",
		"",
		"--inner",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"text body",
		"--inner",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<p>html body</p>",
		"--inner--",
		"--outer",
		"Content-Type: application/pdf",
		"Content-Disposition: attachment; filename=\"doc.pdf\"",
		"",
		"PDFDATA",
		"--outer--",
	)
	msg := parseTestMsg(t, raw)
	if !mailparse.HasAttachment(msg) {
		t.Error("want true for nested message with PDF attachment")
	}
}

// TestHasAttachment_MultipartRelatedInlineOnly verifies that a multipart/related
// message with inline-CID images only returns false.
func TestHasAttachment_MultipartRelatedInlineOnly(t *testing.T) {
	// multipart/mixed containing multipart/alternative (text+html+related-images)
	// with NO standalone attachments.
	raw := joinLines(
		"From: sender@example.test",
		"To: rcpt@example.test",
		"Subject: newsletter with inline images only",
		"MIME-Version: 1.0",
		"Content-Type: multipart/related; boundary=\"r\"",
		"",
		"--r",
		"Content-Type: multipart/alternative; boundary=\"a\"",
		"",
		"--a",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"newsletter text",
		"--a",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<html><body><img src=\"cid:img1@mail\"/></body></html>",
		"--a--",
		"--r",
		"Content-Type: image/png",
		"Content-Disposition: inline",
		"Content-ID: <img1@mail>",
		"",
		"PNGDATA",
		"--r--",
	)
	msg := parseTestMsg(t, raw)
	if mailparse.HasAttachment(msg) {
		t.Error("want false: all images are inline-with-CID in a multipart/related newsletter")
	}
}
