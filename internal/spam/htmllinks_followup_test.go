package spam

import (
	"strings"
	"testing"
)

// TestLinkHostMismatch_NotDefeatedByAnchorFlood (re #552): a mismatching
// link after hundreds of same-domain anchors still sets the fact.
func TestLinkHostMismatch_NotDefeatedByAnchorFlood(t *testing.T) {
	body := strings.Repeat(`<a href="https://sender.example/x">ok</a>`, 2000) +
		`<a href="https://evil.example/d">Download</a>`
	req := BuildRequest(buildMessage(t, lureMessage("sender.example", body)), nil)
	if !req.LinkHostMismatch {
		t.Fatalf("link_host_mismatch not set behind an anchor flood")
	}
}

// TestExcerptMarkers_OnePerDistinctHost: a flood of anchors on one host
// yields one marker, so a different host after it is still shown.
func TestExcerptMarkers_OnePerDistinctHost(t *testing.T) {
	body := strings.Repeat(`<a href="https://sender.example/x">ok</a>`, 200) +
		`<a href="https://evil.example/d">Download</a>`
	req := BuildRequest(buildMessage(t, lureMessage("sender.example", body)), nil)
	hosts := markerHosts(req.BodyExcerpt)
	if len(hosts) != 2 || hosts[0] != "sender.example" || hosts[1] != "evil.example" {
		t.Fatalf("markers = %v, want [sender.example evil.example]", hosts)
	}
}

// TestStripHTMLTags_OtherLinkElements (re #552): links a browser follows
// from <area href>, SVG <a href> and SVG <a xlink:href> are annotated and
// count for the mismatch fact; xlink:href on an HTML <a> is not a link.
func TestStripHTMLTags_OtherLinkElements(t *testing.T) {
	cases := []struct {
		name, in string
		hosts    []string
	}{
		{"area", `<map name="m"><area shape="rect" coords="0,0,9,9" href="https://evil.example/d"></map>`, []string{"evil.example"}},
		{"area self closing", `<map><area href="https://evil.example/d"/></map>`, []string{"evil.example"}},
		{"svg a href", `<svg><a href="https://evil.example/d"><text>Go</text></a></svg>`, []string{"evil.example"}},
		{"svg a xlink href", `<svg xmlns:xlink="http://www.w3.org/1999/xlink"><a xlink:href="https://evil.example/d"><text>Go</text></a></svg>`, []string{"evil.example"}},
		{"svg a href wins over xlink", `<svg><a xlink:href="https://x.example/" href="https://evil.example/d">Go</a></svg>`, []string{"evil.example"}},
		{"html a xlink href only", `<a xlink:href="https://evil.example/d">Go</a>`, nil},
		{"xlink href after svg closed", `<svg></svg><a xlink:href="https://evil.example/d">Go</a>`, nil},
		{"area inside svg", `<svg><area href="https://evil.example/d"></svg>`, nil},
		{"mixed in order", `<a href="https://a.example/">A</a><map><area href="https://b.example/"></map><svg><a href="https://c.example/">C</a></svg>`, []string{"a.example", "b.example", "c.example"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := markerHosts(normalizeExcerpt(stripHTMLTags(c.in)))
			if strings.Join(got, ",") != strings.Join(c.hosts, ",") {
				t.Fatalf("hosts = %v, want %v", got, c.hosts)
			}
			var vis []string
			anchorLinkHosts(c.in, 100, func(h string) bool { vis = append(vis, h); return true })
			if strings.Join(vis, ",") != strings.Join(c.hosts, ",") {
				t.Fatalf("mismatch scan hosts = %v, want %v", vis, c.hosts)
			}
		})
	}
	for _, c := range []struct{ name, body string }{
		{"area", `<map><area href="https://evil.example/d"></map>`},
		{"svg a", `<svg><a href="https://evil.example/d">Go</a></svg>`},
		{"svg xlink", `<svg><a xlink:href="https://evil.example/d">Go</a></svg>`},
	} {
		t.Run("mismatch "+c.name, func(t *testing.T) {
			req := BuildRequest(buildMessage(t, lureMessage("sender.example", c.body)), nil)
			if !req.LinkHostMismatch {
				t.Fatalf("link_host_mismatch not set; excerpt %q", req.BodyExcerpt)
			}
		})
	}
}

// TestStripHTMLTags_Noscript (re #552): <noscript> content is parsed as
// markup, as a mail client without scripting renders it: its links are
// annotated and no raw tag text reaches the excerpt.
func TestStripHTMLTags_Noscript(t *testing.T) {
	in := `<p>Hi</p><noscript><p>Enable it</p><a href="https://evil.example/d">Download</a></noscript><p>Bye</p>`
	got := normalizeExcerpt(stripHTMLTags(in))
	if hosts := markerHosts(got); len(hosts) != 1 || hosts[0] != "evil.example" {
		t.Fatalf("markers = %v; excerpt %q", hosts, got)
	}
	if strings.ContainsAny(got, "<>") || !strings.Contains(got, "Enable it") || !strings.Contains(got, "Bye") {
		t.Fatalf("excerpt = %q", got)
	}
	req := BuildRequest(buildMessage(t, lureMessage("sender.example", in)), nil)
	if !req.LinkHostMismatch {
		t.Fatalf("link in noscript not counted for the mismatch fact")
	}
}
