package extimg

import (
	"strings"
	"testing"
)

func TestExtractCandidates_BasicImg(t *testing.T) {
	html := `<html><body>
		<img src="https://example.com/a.png">
		<img src="cid:already-inline">
		<img src="data:image/gif;base64,abc">
		<img src="../relative.png">
		<img src="https://example.com/b.png">
	</body></html>`
	cands, err := extractCandidates([]byte(html))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(cands) != 2 {
		t.Fatalf("got %d candidates, want 2: %+v", len(cands), cands)
	}
	got := []string{cands[0].URL, cands[1].URL}
	if got[0] != "https://example.com/a.png" || got[1] != "https://example.com/b.png" {
		t.Fatalf("unexpected order: %v", got)
	}
}

func TestExtractCandidates_Srcset(t *testing.T) {
	html := `<picture>
		<source srcset="https://x.test/1x.png 1x, https://x.test/2x.png 2x">
		<img src="https://x.test/fallback.png" srcset="https://x.test/wide.png 1024w">
	</picture>`
	cands, err := extractCandidates([]byte(html))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	want := map[string]bool{
		"https://x.test/1x.png":       true,
		"https://x.test/2x.png":       true,
		"https://x.test/fallback.png": true,
		"https://x.test/wide.png":     true,
	}
	got := map[string]bool{}
	for _, c := range cands {
		got[c.URL] = true
	}
	for k := range want {
		if !got[k] {
			t.Errorf("missing srcset url %q", k)
		}
	}
}

func TestExtractCandidates_CSSStyle(t *testing.T) {
	html := `<div style="background: url('https://cdn.example.test/bg.jpg') no-repeat;
	            border-image: url(https://cdn.example.test/frame.png) 30 round;">
	</div>`
	cands, err := extractCandidates([]byte(html))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(cands) != 2 {
		t.Fatalf("got %d, want 2: %+v", len(cands), cands)
	}
}

func TestExtractCandidates_SVG(t *testing.T) {
	html := `<svg><image href="https://svg.example.test/logo.png" width="20" height="20"/></svg>`
	cands, err := extractCandidates([]byte(html))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(cands) != 1 || cands[0].URL != "https://svg.example.test/logo.png" {
		t.Fatalf("got %+v, want one href", cands)
	}
}

func TestRewriteHTML_ImgSrc(t *testing.T) {
	html := `<html><body><img src="https://example.com/a.png"></body></html>`
	cidMap := map[string]string{"https://example.com/a.png": "abc@herold"}
	out, err := rewriteHTML([]byte(html), cidMap)
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if !strings.Contains(string(out), `src="cid:abc@herold"`) {
		t.Fatalf("rewritten output missing cid: ref: %s", out)
	}
	if strings.Contains(string(out), "https://example.com/a.png") {
		t.Fatalf("original url leaked into output: %s", out)
	}
}

func TestRewriteHTML_FailedFetchUntouched(t *testing.T) {
	html := `<img src="https://succeeded.test/a.png"><img src="https://failed.test/b.png">`
	cidMap := map[string]string{"https://succeeded.test/a.png": "ok@herold"}
	out, err := rewriteHTML([]byte(html), cidMap)
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, `src="cid:ok@herold"`) {
		t.Fatalf("succeeded fetch not rewritten: %s", s)
	}
	if !strings.Contains(s, "https://failed.test/b.png") {
		t.Fatalf("failed fetch should preserve original URL: %s", s)
	}
}

func TestRewriteHTML_Srcset(t *testing.T) {
	html := `<img srcset="https://x.test/1x.png 1x, https://x.test/2x.png 2x" src="https://x.test/fallback.png">`
	cidMap := map[string]string{
		"https://x.test/1x.png":       "one@herold",
		"https://x.test/2x.png":       "two@herold",
		"https://x.test/fallback.png": "fb@herold",
	}
	out, err := rewriteHTML([]byte(html), cidMap)
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	s := string(out)
	if strings.Contains(s, "https://x.test/") {
		t.Fatalf("original urls leaked: %s", s)
	}
	if !strings.Contains(s, "cid:one@herold 1x") || !strings.Contains(s, "cid:two@herold 2x") {
		t.Fatalf("srcset descriptors not preserved: %s", s)
	}
}

func TestRewriteHTML_CSSStyle(t *testing.T) {
	html := `<div style="background: url('https://cdn.test/bg.jpg') center;"></div>`
	cidMap := map[string]string{"https://cdn.test/bg.jpg": "bg@herold"}
	out, err := rewriteHTML([]byte(html), cidMap)
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "url(&#39;cid:bg@herold&#39;)") &&
		!strings.Contains(s, "url('cid:bg@herold')") {
		t.Fatalf("CSS url not rewritten: %s", s)
	}
}

func TestExtractCandidates_MalformedHTMLDoesNotCrash(t *testing.T) {
	// Outlook-style malformed html — unbalanced tags, weird attrs.
	html := []byte(`<!--[if mso]><img src="https://outlook.test/a.png"><![endif]--><img <img src="https://x.test/b.png">`)
	cands, err := extractCandidates(html)
	if err != nil {
		t.Fatalf("malformed html should still parse: %v", err)
	}
	// Don't assert exact count; assert no crash + at least the
	// well-formed reference comes through.
	hit := false
	for _, c := range cands {
		if c.URL == "https://x.test/b.png" {
			hit = true
		}
	}
	if !hit {
		t.Fatalf("missed well-formed url among malformed input: %+v", cands)
	}
}

// TestExtractCandidates_BackgroundAttr verifies that the deprecated HTML
// background= attribute on table elements is extracted as a candidate URL.
// This covers the newsletter pattern where large content images are supplied
// via <td background="https://..."> rather than <img src="...">.
func TestExtractCandidates_BackgroundAttr(t *testing.T) {
	htmlStr := `<table>
		<tr>
			<td background="https://img.srv2.de/assets/bm/a.png" width="650">
				<img src="data:image/gif;base64,R0lGODlhAQABAIAAAP///wAAACH5BAEAAAAALAAAAAABAAEAAAICRAEAOw==" width="650" height="366">
			</td>
		</tr>
		<tr>
			<td background="https://img.srv2.de/assets/bm/b.png">text</td>
		</tr>
		<tbody>
			<tr>
				<th background="https://img.srv2.de/assets/bm/header.png">header</th>
			</tr>
		</tbody>
	</table>
	<body background="https://img.srv2.de/assets/bm/body-bg.png"></body>`

	cands, err := extractCandidates([]byte(htmlStr))
	if err != nil {
		t.Fatalf("extractCandidates: %v", err)
	}
	wantURLs := map[string]bool{
		"https://img.srv2.de/assets/bm/a.png":       true,
		"https://img.srv2.de/assets/bm/b.png":       true,
		"https://img.srv2.de/assets/bm/header.png":  true,
		"https://img.srv2.de/assets/bm/body-bg.png": true,
	}
	got := map[string]bool{}
	for _, c := range cands {
		got[c.URL] = true
	}
	for u := range wantURLs {
		if !got[u] {
			t.Errorf("missing background= URL %q; got: %v", u, cands)
		}
	}
	// The 1x1 transparent GIF data URI must NOT appear as a candidate.
	for _, c := range cands {
		if strings.HasPrefix(c.URL, "data:") {
			t.Errorf("data: URI leaked into candidates: %q", c.URL)
		}
	}
}

// TestRewriteHTML_BackgroundAttr verifies that background= URLs are rewritten
// to cid: references when a cidMap entry is present.
func TestRewriteHTML_BackgroundAttr(t *testing.T) {
	htmlStr := `<table><tr><td background="https://img.test/hero.png" width="650">content</td></tr></table>`
	cidMap := map[string]string{
		"https://img.test/hero.png": "hero@herold",
	}
	out, err := rewriteHTML([]byte(htmlStr), cidMap)
	if err != nil {
		t.Fatalf("rewriteHTML: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, `background="cid:hero@herold"`) {
		t.Fatalf("background= attr not rewritten; got: %s", s)
	}
	if strings.Contains(s, "https://img.test/hero.png") {
		t.Fatalf("original background= URL still present: %s", s)
	}
}

// TestRewriteHTML_BackgroundAttr_UnmappedUntouched verifies that a background=
// URL absent from cidMap is left unchanged (failed-fetch preservation).
// The <td> must be inside a <table> so the HTML parser preserves it.
func TestRewriteHTML_BackgroundAttr_UnmappedUntouched(t *testing.T) {
	htmlStr := `<table><tr><td background="https://img.test/hero.png">content</td></tr></table>`
	cidMap := map[string]string{} // empty — fetch never completed
	out, err := rewriteHTML([]byte(htmlStr), cidMap)
	if err != nil {
		t.Fatalf("rewriteHTML: %v", err)
	}
	if !strings.Contains(string(out), "https://img.test/hero.png") {
		t.Fatalf("unmapped background= URL was removed: %s", out)
	}
}
