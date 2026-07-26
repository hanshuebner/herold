package email

// render_preview_test.go -- tests for previewFromValues's HTML-extraction
// fallback (re #263) and its agreement with mailparse.BodyPreview, the
// computation used by the background body-meta backfill worker.
//
// These tests live in the `email` package (not `email_test`) so they can
// call the unexported previewFromValues function directly, same as
// render_test.go / render_bodylists_test.go.

import (
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/mailparse"
)

// previewViaRender reproduces the render path's preview computation:
// walkParts followed by previewFromValues, exactly as renderFull does.
func previewViaRender(t *testing.T, raw string) string {
	t.Helper()
	msg := parseMsg(t, raw)
	_, values, textParts, _, _ := walkParts(msg.Body, 0, "hashpreview", nil)
	return previewFromValues(values, textParts, 256)
}

// previewViaBodyMeta reproduces the background worker's preview
// computation.
func previewViaBodyMeta(t *testing.T, raw string) string {
	t.Helper()
	msg := parseMsg(t, raw)
	return mailparse.BodyPreview(msg, 256)
}

// TestPreview_PlainOnly_NoRegression verifies a genuine text/plain message
// previews exactly as before: the raw value, trimmed, with no HTML
// extraction applied.
func TestPreview_PlainOnly_NoRegression(t *testing.T) {
	raw := rawMsg(
		"From: sender@example.test",
		"To: rcpt@example.test",
		"Subject: plain",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"  Hello, this is plain text.  ",
	)
	want := "Hello, this is plain text."
	if got := previewViaRender(t, raw); got != want {
		t.Errorf("previewFromValues: got %q, want %q", got, want)
	}
	if got := previewViaBodyMeta(t, raw); got != want {
		t.Errorf("BodyPreview: got %q, want %q", got, want)
	}
}

// TestPreview_HTMLOnly_ExtractsText verifies that an HTML-only message
// (text/html leaf, no text/plain alternative) previews as extracted text --
// tags stripped, entities decoded, whitespace collapsed, comment removed --
// rather than raw HTML source (re #263). This reproduces the ticket's DIE
// ZEIT/Hermes symptom shape: a doctype-leading HTML body.
func TestPreview_HTMLOnly_ExtractsText(t *testing.T) {
	raw := rawMsg(
		"From: sender@example.test",
		"To: rcpt@example.test",
		"Subject: html only",
		"MIME-Version: 1.0",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<!-- FILE: undefined -->"+
			"<!DOCTYPE html PUBLIC \"-//W3C//DTD XHTML 1.0 Transitional//EN\">"+
			"<html><head><title>x</title></head><body>"+
			"<p>WunschAblageort erfolgreich gebucht.</p>"+
			"</body></html>",
	)
	want := "WunschAblageort erfolgreich gebucht."

	got := previewViaRender(t, raw)
	if got != want {
		t.Errorf("previewFromValues: got %q, want %q", got, want)
	}
	if got == "" || got[0] == '<' {
		t.Errorf("previewFromValues leaked raw markup: %q", got)
	}

	gotBM := previewViaBodyMeta(t, raw)
	if gotBM != want {
		t.Errorf("BodyPreview: got %q, want %q", gotBM, want)
	}

	if got != gotBM {
		t.Errorf("previewFromValues and BodyPreview disagree: %q vs %q", got, gotBM)
	}
}

// TestPreview_PlainCRLFFlowed_WhitespaceCollapsed verifies that a text/plain
// message previews identically at both computation sites even when the two
// sites disagree on how the raw text is decoded before the preview is
// derived (re #265). A format=flowed body's soft-broken line is reflowed
// (CRLF/CR normalized to LF, the soft break joined) by walkParts before
// previewFromValues sees it, but mailparse.BodyPreview reads the same
// part's raw, unreflowed text (CRLF preserved). Collapsing whitespace at
// both sites -- the same policy ExtractTextFromHTML already applies for the
// HTML case -- removes the CRLF/LF divergence and yields a clean,
// single-line preview from either input shape.
func TestPreview_PlainCRLFFlowed_WhitespaceCollapsed(t *testing.T) {
	raw := rawMsg(
		"From: sender@example.test",
		"To: rcpt@example.test",
		"Subject: plain crlf flowed",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8; format=flowed",
		"",
		"Line  one\twith a soft break ",
		"continues here.",
	)
	want := "Line one with a soft break continues here."

	got := previewViaRender(t, raw)
	if got != want {
		t.Errorf("previewFromValues: got %q, want %q", got, want)
	}
	if strings.ContainsAny(got, "\r\n") {
		t.Errorf("previewFromValues: preview contains an embedded line break: %q", got)
	}

	gotBM := previewViaBodyMeta(t, raw)
	if gotBM != want {
		t.Errorf("BodyPreview: got %q, want %q", gotBM, want)
	}
	if strings.ContainsAny(gotBM, "\r\n") {
		t.Errorf("BodyPreview: preview contains an embedded line break: %q", gotBM)
	}

	if got != gotBM {
		t.Errorf("previewFromValues and BodyPreview disagree: %q vs %q", got, gotBM)
	}
}

// TestPreview_MultipartAlternative_PrefersPlain verifies that a
// multipart/alternative (text+html) message previews from the genuine
// text/plain part, not from the html part, and that both computation sites
// agree.
func TestPreview_MultipartAlternative_PrefersPlain(t *testing.T) {
	raw := rawMsg(
		"From: sender@example.test",
		"To: rcpt@example.test",
		"Subject: alternative",
		"MIME-Version: 1.0",
		"Content-Type: multipart/alternative; boundary=\"b\"",
		"",
		"--b",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"Plain text body here.",
		"--b",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<p>HTML body here.</p>",
		"--b--",
	)
	want := "Plain text body here."

	got := previewViaRender(t, raw)
	if got != want {
		t.Errorf("previewFromValues: got %q, want %q", got, want)
	}
	gotBM := previewViaBodyMeta(t, raw)
	if gotBM != want {
		t.Errorf("BodyPreview: got %q, want %q", gotBM, want)
	}
	if got != gotBM {
		t.Errorf("previewFromValues and BodyPreview disagree: %q vs %q", got, gotBM)
	}
}

// TestPreview_LeadingCommentStripped verifies that an html body starting
// with an HTML comment (the ticket's stray `<!-- FILE: undefined -->`
// Mailjet template artifact) has the comment stripped from the preview.
func TestPreview_LeadingCommentStripped(t *testing.T) {
	raw := rawMsg(
		"From: sender@example.test",
		"To: rcpt@example.test",
		"Subject: leading comment",
		"MIME-Version: 1.0",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<!-- FILE: undefined --><p>Gysi in der Strandmuschel</p>",
	)
	want := "Gysi in der Strandmuschel"
	if got := previewViaRender(t, raw); got != want {
		t.Errorf("previewFromValues: got %q, want %q", got, want)
	}
	if got := previewViaBodyMeta(t, raw); got != want {
		t.Errorf("BodyPreview: got %q, want %q", got, want)
	}
}
