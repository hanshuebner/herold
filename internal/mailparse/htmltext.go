package mailparse

import (
	"strings"

	"golang.org/x/net/html"
)

// ExtractTextFromHTML parses htmlSrc and returns its visible text: tags are
// stripped, entities are decoded (x/net/html's parser does this for every
// TextNode), <head> (title/meta/link/etc.), <script>/<style> content, and
// comments are dropped, and runs of whitespace collapse to single spaces
// with the result trimmed. This is the shared computation behind the JMAP
// Email.preview field for HTML-only messages (re #263) so that every
// computation site -- the opportunistic Email/get persist, Email/parse, and
// the background body-meta backfill worker -- derives the same
// human-readable summary instead of leaking raw markup.
//
// html.Parse is a lenient, best-effort HTML5 parser: it does not error on
// malformed markup (unclosed tags, stray text, missing doctype), so this
// function is safe to call on arbitrary sender-supplied HTML mail bodies.
// It only returns "" when htmlSrc itself carries no extractable text.
func ExtractTextFromHTML(htmlSrc string) string {
	doc, err := html.Parse(strings.NewReader(htmlSrc))
	if err != nil {
		return ""
	}
	var b strings.Builder
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch strings.ToLower(n.Data) {
			case "script", "style", "head":
				// Skip element content entirely -- html.Parse always
				// synthesizes a <head> (title/meta/link/...) even for
				// a bare HTML fragment, and none of script/style/head
				// is visible page text.
				return
			}
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			// A space separates adjacent text nodes so that
			// "<p>a</p><p>b</p>" does not collapse into "ab";
			// the whitespace-collapse pass below normalises any
			// resulting doubled spaces.
			b.WriteByte(' ')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return strings.Join(strings.Fields(b.String()), " ")
}
