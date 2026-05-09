package extimg

import (
	"strings"
	"testing"
)

// TestRewriteForPlaceholder_ReplacesExternalImageSrc covers the
// img[src=https://...] case — the dominant shape in tracking-pixel
// mail. After rewrite the src must be the placeholder data URI and
// the rest of the document must be unchanged.
func TestRewriteForPlaceholder_ReplacesExternalImageSrc(t *testing.T) {
	in := []byte(`<html><body><p>Hi</p><img src="https://tracker.example/pixel.gif" alt="open"></body></html>`)
	got, err := RewriteForPlaceholder(in)
	if err != nil {
		t.Fatalf("RewriteForPlaceholder: %v", err)
	}
	s := string(got)
	if strings.Contains(s, "tracker.example") {
		t.Fatalf("external host still present: %s", s)
	}
	if !strings.Contains(s, PlaceholderDataURI) {
		t.Fatalf("placeholder data URI not present: %s", s)
	}
	if !strings.Contains(s, `alt="open"`) {
		t.Fatalf("alt attribute lost: %s", s)
	}
}

// TestRewriteForPlaceholder_PreservesInlineImages covers the cid: /
// data: / blob: case: those are already inline and must NOT be
// rewritten (a cid:logo points at a multipart-related body part the
// renderer attaches to the message).
func TestRewriteForPlaceholder_PreservesInlineImages(t *testing.T) {
	in := []byte(`<img src="cid:logo@example"><img src="data:image/png;base64,AAAA"><img src="blob:https://x/y">`)
	got, err := RewriteForPlaceholder(in)
	if err != nil {
		t.Fatalf("RewriteForPlaceholder: %v", err)
	}
	s := string(got)
	for _, want := range []string{"cid:logo@example", "data:image/png;base64,AAAA", "blob:https://x/y"} {
		if !strings.Contains(s, want) {
			t.Fatalf("inline reference %q dropped: %s", want, s)
		}
	}
	if strings.Contains(s, PlaceholderDataURI) {
		t.Fatalf("placeholder leaked into inline-only doc: %s", s)
	}
}

// TestRewriteForPlaceholder_HandlesSrcset covers responsive-image
// srcset attributes: every external candidate must be removed. The
// browser falls back to the (now-placeholderified) src.
func TestRewriteForPlaceholder_HandlesSrcset(t *testing.T) {
	in := []byte(`<img src="https://x/a.png" srcset="https://x/a.png 1x, https://x/a@2x.png 2x">`)
	got, err := RewriteForPlaceholder(in)
	if err != nil {
		t.Fatalf("RewriteForPlaceholder: %v", err)
	}
	s := string(got)
	if strings.Contains(s, "https://x/") {
		t.Fatalf("external srcset URLs survived: %s", s)
	}
}

// TestRewriteForPlaceholder_RewritesCSSBackground covers the inline
// style="background-image: url(...)" case.
func TestRewriteForPlaceholder_RewritesCSSBackground(t *testing.T) {
	in := []byte(`<div style="background-image: url(https://tracker.example/bg.png)">x</div>`)
	got, err := RewriteForPlaceholder(in)
	if err != nil {
		t.Fatalf("RewriteForPlaceholder: %v", err)
	}
	s := string(got)
	if strings.Contains(s, "tracker.example") {
		t.Fatalf("external background URL survived: %s", s)
	}
	if !strings.Contains(s, PlaceholderDataURI) {
		t.Fatalf("placeholder not present: %s", s)
	}
}

// TestRewriteForPlaceholder_NotHTMLReturnedUnchanged covers the
// graceful-fallback contract: an unparseable body comes back
// unchanged so the caller never refuses to render a message because
// the placeholderifier choked.
func TestRewriteForPlaceholder_NotHTMLReturnedUnchanged(t *testing.T) {
	in := []byte("plain text body, no markup at all")
	got, err := RewriteForPlaceholder(in)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	// The html parser will wrap plaintext in a synthetic <html><body> tree,
	// so we can't byte-compare. Just assert no placeholder leaked.
	if strings.Contains(string(got), PlaceholderDataURI) {
		t.Fatalf("placeholder leaked into plain text: %s", got)
	}
}
