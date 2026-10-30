package spam

import (
	"fmt"
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

// TestStripHTMLTags_LinkMarker covers the anchor-host marker's edge
// cases (re #551): each row names substrings the excerpt must and must
// not contain.
func TestStripHTMLTags_LinkMarker(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    []string
		notWant []string
	}{
		{"unclosed anchor", `<a href="https://evil.example/">Go`, []string{"[link: evil.example]", "Go"}, nil},
		{"quoted gt in earlier attribute", `<a title="a>b" href="https://evil.example/">Go</a>`, []string{"[link: evil.example]", "Go"}, []string{"b\"", "title", "a>b"}},
		{"protocol relative", `<a href="//evil.example/x">Go</a>`, []string{"[link: evil.example]"}, nil},
		{"uppercase tag and attr", `<A HREF="HTTPS://Evil.Example/">Go</A>`, []string{"[link: evil.example]"}, nil},
		{"entity encoded scheme", `<a href="&#104;ttps://evil.example/">Go</a>`, []string{"[link: evil.example]"}, nil},
		{"entity encoded ampersand in query", `<a href="https://evil.example/?a=1&amp;b=2">Go</a>`, []string{"[link: evil.example]"}, []string{"a=1"}},
		{"single quoted href", `<a href='https://evil.example/'>Go</a>`, []string{"[link: evil.example]"}, nil},
		{"unquoted href", `<a href=https://evil.example/>Go</a>`, []string{"[link: evil.example]"}, nil},
		{"port stripped", `<a href="https://evil.example:8443/">Go</a>`, []string{"[link: evil.example]"}, []string{"8443"}},
		{"forged marker in text", `<a href="https://evil.example/">Pay [link: paypal.com]</a>`, []string{"[link: evil.example]", "(link: paypal.com]"}, []string{"[link: paypal.com]"}},
		{"forged marker mixed case", `Pay [LiNk: paypal.com] <a href="https://evil.example/">x</a>`, []string{"[link: evil.example]"}, []string{"[LiNk:", "[link: paypal.com]"}},
		{"mailto", `<a href="mailto:a@b.example">m</a>`, []string{"m"}, []string{"link:", "b.example"}},
		{"javascript", `<a href="javascript:alert(1)">j</a>`, []string{"j"}, []string{"link:"}},
		{"data", `<a href="data:text/html;base64,AAAA">d</a>`, []string{"d"}, []string{"link:"}},
		{"relative", `<a href="/path/x">r</a>`, []string{"r"}, []string{"link:"}},
		{"fragment", `<a href="#top">f</a>`, []string{"f"}, []string{"link:"}},
		{"no href", `<a name="x">n</a>`, []string{"n"}, []string{"link:"}},
		{"comment with gt", `<!-- a>b -->text`, []string{"text"}, []string{"a>b", "-->"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := normalizeExcerpt(stripHTMLTags(c.in))
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in %q", w, got)
				}
			}
			for _, w := range c.notWant {
				if strings.Contains(got, w) {
					t.Errorf("unexpected %q in %q", w, got)
				}
			}
		})
	}
}

// TestStripHTMLTags_LinkMarkerCap: thousands of anchors on distinct hosts
// contribute at most maxLinkMarkers markers.
func TestStripHTMLTags_LinkMarkerCap(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&sb, `<a href="https://h%d.example/">x</a>`, i)
	}
	in := sb.String()
	got := normalizeExcerpt(stripHTMLTags(in))
	if n := strings.Count(got, "[link:"); n != maxLinkMarkers {
		t.Fatalf("markers = %d, want %d", n, maxLinkMarkers)
	}
}
