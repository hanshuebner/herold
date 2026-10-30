package spam

import (
	"strings"
	"testing"
)

// TestBuildRequest_HTMLAnchorKeepsLinkHost is the re #551 regression test:
// a call-to-action button in an HTML body must reach the classifier with
// its destination host, because the tag stripper used to drop the href
// and left the model judging a "download the file" lure from its label
// alone.
func TestBuildRequest_HTMLAnchorKeepsLinkHost(t *testing.T) {
	const htmlMsg = "From: Sender <s@sender.example>\r\n" +
		"To: me@example.com\r\n" +
		"Subject: Receipts\r\n" +
		"Date: Mon, 02 Jan 2006 15:04:05 +0000\r\n" +
		"Message-ID: <551@sender.example>\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/html; charset=UTF-8\r\n\r\n" +
		"<html><body><p>Receipts attached.</p>" +
		"<a style=\"x\" href=\"https://Redirect.Example.net/abc?x=1&amp;y=2\"><b>Download the file</b></a>" +
		"<a href='mailto:a@b.example'>mail</a>" +
		"<a href=\"#top\">top</a>" +
		"<p>Regards</p></body></html>\r\n"
	req := BuildRequest(buildMessage(t, htmlMsg), nil)
	if !strings.Contains(req.BodyExcerpt, "Download the file") {
		t.Fatalf("anchor text lost: %q", req.BodyExcerpt)
	}
	if !strings.Contains(req.BodyExcerpt, "redirect.example.net") {
		t.Fatalf("link host missing from excerpt: %q", req.BodyExcerpt)
	}
	if strings.Contains(req.BodyExcerpt, "abc") || strings.Contains(req.BodyExcerpt, "x=1") {
		t.Fatalf("excerpt carries the full URL, want host only: %q", req.BodyExcerpt)
	}
	if strings.Contains(req.BodyExcerpt, "<") || strings.Contains(req.BodyExcerpt, "b.example") {
		t.Fatalf("non-web anchors or markup leaked: %q", req.BodyExcerpt)
	}
}
