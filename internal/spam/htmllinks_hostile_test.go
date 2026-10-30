package spam

import (
	"strings"
	"testing"
	"time"
)

// htmlOnlyMessage wraps an HTML body in a minimal single-part message so
// a test drives the same collectTextBody -> normalizeExcerpt path the
// delivery pipeline uses.
func htmlOnlyMessage(body string) string {
	return "From: Sender <s@sender.example>\r\n" +
		"To: me@example.com\r\n" +
		"Subject: Hostile\r\n" +
		"Date: Mon, 02 Jan 2006 15:04:05 +0000\r\n" +
		"Message-ID: <552@sender.example>\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/html; charset=UTF-8\r\n\r\n" +
		body + "\r\n"
}

// markerHosts returns the hosts named by every "[link: host]" marker in
// the excerpt, in order.
func markerHosts(excerpt string) []string {
	var hosts []string
	rest := excerpt
	for {
		i := strings.Index(rest, "[link: ")
		if i < 0 {
			return hosts
		}
		rest = rest[i+len("[link: "):]
		j := -1
		for k := 0; k < len(rest); k++ {
			if rest[k] == ']' && (k+1 == len(rest) || rest[k+1] == ' ' || rest[k+1] == '\n') {
				j = k
				break
			}
		}
		if j < 0 {
			return hosts
		}
		hosts = append(hosts, rest[:j])
		rest = rest[j+1:]
	}
}

// TestExcerptMarkerForgery (re #552): message content must not be able to
// produce a "[link:" marker in the final excerpt, however the bracket is
// spelled or split. Only the one real anchor's marker may appear.
func TestExcerptMarkerForgery(t *testing.T) {
	cases := []struct{ name, text string }{
		{"numeric entity bracket", `&#91;link: paypal.com]`},
		{"hex entity bracket", `&#x5B;link: paypal.com]`},
		{"named entity bracket", `&lbrack;link: paypal.com]`},
		{"entity in word", `[li&#110;k: paypal.com]`},
		{"double encoded", `&amp;#91;link: paypal.com]`},
		{"split by empty tag", `[li<b></b>nk: paypal.com]`},
		{"split by comment", `[<!-- x -->link: paypal.com]`},
		{"split by comment in word", `[li<!-- x -->nk: paypal.com]`},
		{"split by tag before bracket", `<span>[</span>link: paypal.com]`},
		{"space inside bracket", `[ link: paypal.com]`},
		{"mixed case", `[LiNk: paypal.com]`},
		{"split across paragraphs", "[<p>link: paypal.com]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := "<p>" + c.text + `</p><a href="https://real.example/">Go</a>`
			req := BuildRequest(buildMessage(t, htmlOnlyMessage(body)), nil)
			hosts := markerHosts(req.BodyExcerpt)
			if len(hosts) != 1 || hosts[0] != "real.example" {
				t.Fatalf("markers = %v, want only real.example; excerpt %q", hosts, req.BodyExcerpt)
			}
			if strings.Count(strings.ToLower(req.BodyExcerpt), "[link:") != 1 {
				t.Fatalf("forged bracket-link text survives: %q", req.BodyExcerpt)
			}
		})
	}
}

// TestExcerptMarkerForgeryViaControlEntity: a numeric entity must not
// smuggle a control character that the renderer could read as the
// marker delimiter.
func TestExcerptMarkerForgeryViaControlEntity(t *testing.T) {
	body := `<p>&#1;paypal.com&#1;</p><a href="https://real.example/">Go</a>`
	req := BuildRequest(buildMessage(t, htmlOnlyMessage(body)), nil)
	if hosts := markerHosts(req.BodyExcerpt); len(hosts) != 1 || hosts[0] != "real.example" {
		t.Fatalf("markers = %v; excerpt %q", hosts, req.BodyExcerpt)
	}
	if strings.ContainsAny(req.BodyExcerpt, "\x01\x02") {
		t.Fatalf("control character reached the excerpt: %q", req.BodyExcerpt)
	}
}

// TestExcerptPlainTextUnchanged: a text/plain body is left as written,
// including text that looks like a marker (no HTML was processed).
func TestExcerptPlainTextUnchanged(t *testing.T) {
	raw := "From: Sender <s@sender.example>\r\nTo: me@example.com\r\nSubject: P\r\n" +
		"Date: Mon, 02 Jan 2006 15:04:05 +0000\r\nMessage-ID: <p@sender.example>\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n" +
		"See [link: docs.example] and https://x.example/a?b=1\r\n"
	req := BuildRequest(buildMessage(t, raw), nil)
	if want := "See [link: docs.example] and https://x.example/a?b=1"; req.BodyExcerpt != want {
		t.Fatalf("plain excerpt = %q, want %q", req.BodyExcerpt, want)
	}
}

// TestExcerptLinearTime: a large hostile body finishes well inside the
// delivery budget. The work runs in a goroutine so a quadratic
// implementation fails the test instead of hanging it.
func TestExcerptLinearTime(t *testing.T) {
	bodies := map[string]string{
		"repeated marker text": "<p>" + strings.Repeat("[link:", 100_000) + "</p>",
		"repeated marker mixed": strings.Repeat("[LiNk: a ", 70_000) +
			strings.Repeat("<!-- -->[li", 30_000),
		"many anchors":        strings.Repeat(`<a href="https://h.example/">x</a>`, 20_000),
		"unterminated quotes": strings.Repeat(`<a href="`, 50_000),
		"entities":            strings.Repeat("&#91;link:", 60_000),
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			msg := buildMessage(t, htmlOnlyMessage(body))
			done := make(chan struct{})
			go func() {
				defer close(done)
				_ = BuildRequest(msg, nil)
			}()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatalf("request for a %d-byte body not built within 3s", len(body))
			}
		})
	}
}

// TestStripHTMLTags_BrowserHrefParsing (re #552): hosts are resolved the
// way a browser resolves the href, for the spellings an attacker can use
// to hide the destination from a strict URL parser.
func TestStripHTMLTags_BrowserHrefParsing(t *testing.T) {
	cases := []struct {
		name, href, host string
	}{
		{"backslashes after scheme", `https:\\evil.example\x`, "evil.example"},
		{"backslash pair no scheme", `\\evil.example/x`, "evil.example"},
		{"slash backslash no scheme", `/\evil.example/x`, "evil.example"},
		{"backslash slash no scheme", `\/evil.example/x`, "evil.example"},
		{"one slash scheme", `https:/evil.example/x`, "evil.example"},
		{"no slash scheme", `https:evil.example/x`, "evil.example"},
		{"http one slash", `http:/evil.example`, "evil.example"},
		{"many slashes", `https:////evil.example/x`, "evil.example"},
		{"protocol relative", `//evil.example/x`, "evil.example"},
		{"userinfo lookalike", `https://paypal.com@evil.example/x`, "evil.example"},
		{"userinfo backslash", `https://evil.example\@paypal.com/x`, "evil.example"},
		{"embedded tab and newline", "ht\ttps://evil.\nexample/x", "evil.example"},
		{"leading control and space", "\x01 https://evil.example/", "evil.example"},
		{"mixed case scheme", `HtTpS://Evil.Example`, "evil.example"},
		{"trailing dot", `https://evil.example./x`, "evil.example"},
		{"percent encoded host", `https://evil%2Eexample/x`, "evil.example"},
		{"fullwidth dot host", "https://evil。example/x", "evil.example"},
		{"ipv6 literal", `https://[2001:db8::1]:8443/x`, "[2001:db8::1]"},
		{"query only authority", `https://evil.example?x=1`, "evil.example"},
		{"fragment only authority", `https://evil.example#x`, "evil.example"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := markerHosts(normalizeExcerpt(stripHTMLTags(`<a href="` + c.href + `">Go</a>`)))
			if len(got) != 1 || got[0] != c.host {
				t.Fatalf("href %q -> %v, want [%s]", c.href, got, c.host)
			}
		})
	}
	for _, href := range []string{
		`javascript:alert(1)`, `mailto:a@b.example`, `ftp://evil.example/`, `https://`, `https:///`,
		`relative/path`, `#frag`, ``, `https://bad host.example/`, `https://a<b.example/`,
	} {
		t.Run("no marker "+href, func(t *testing.T) {
			got := normalizeExcerpt(stripHTMLTags(`<a href="` + href + `">Go</a>`))
			if strings.Contains(got, "[link:") {
				t.Fatalf("href %q produced a marker: %q", href, got)
			}
		})
	}
}

// TestStripHTMLTags_BaseHref (re #552): relative hrefs resolve against
// the document's first <base href>, wherever it sits.
func TestStripHTMLTags_BaseHref(t *testing.T) {
	cases := []struct {
		name, in string
		hosts    []string
	}{
		{"relative path", `<base href="https://base.example/dir/"><a href="x">Go</a>`, []string{"base.example"}},
		{"root relative", `<base href="https://base.example/"><a href="/x">Go</a>`, []string{"base.example"}},
		{"fragment only", `<base href="https://base.example/"><a href="#x">Go</a>`, []string{"base.example"}},
		{"empty href", `<base href="https://base.example/"><a href="">Go</a>`, []string{"base.example"}},
		{"base after anchor", `<a href="x">Go</a><base href="https://base.example/">`, []string{"base.example"}},
		{"absolute href wins", `<base href="https://base.example/"><a href="https://own.example/">Go</a>`, []string{"own.example"}},
		{"first base wins", `<base href="https://one.example/"><base href="https://two.example/"><a href="x">Go</a>`, []string{"one.example"}},
		{"base without href skipped", `<base target="_blank"><base href="https://base.example/"><a href="x">Go</a>`, []string{"base.example"}},
		{"upper case base", `<BASE HREF="https://base.example/"><a href="x">Go</a>`, []string{"base.example"}},
		{"same scheme single slash", `<base href="https://base.example/"><a href="https:/x">Go</a>`, []string{"base.example"}},
		{"same scheme no slash", `<base href="https://base.example/"><a href="https:x">Go</a>`, []string{"base.example"}},
		{"same scheme with authority", `<base href="https://base.example/"><a href="https://own.example/">Go</a>`, []string{"own.example"}},
		{"base entity encoded", `<base href="&#104;ttps://base.example/"><a href="x">Go</a>`, []string{"base.example"}},
		{"non-web base", `<base href="mailto:a@b.example"><a href="x">Go</a>`, nil},
		{"no base relative", `<a href="x">Go</a>`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := markerHosts(normalizeExcerpt(stripHTMLTags(c.in)))
			if strings.Join(got, ",") != strings.Join(c.hosts, ",") {
				t.Fatalf("hosts = %v, want %v", got, c.hosts)
			}
		})
	}
}
