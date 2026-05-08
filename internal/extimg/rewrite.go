package extimg

import (
	"bytes"
	"fmt"
	"strings"

	"golang.org/x/net/html"
)

// REQ-EXTIMG-10..14: HTML walker. Scans an HTML document for external
// image references in img/src, srcset, picture/source, and SVG image
// hrefs. CSS url(...) inside style="" attributes is also rewritten;
// <style> block contents are left as-is for now (parsing CSS rule
// trees is heavier and is rare in real-world mail HTML).
//
// The walker is a two-pass design:
//
//   pass 1: collect every external URL the rewriter would attempt
//   pass 2: caller fetches → cidMap, walker rewrites references using
//           the map. URLs whose fetch failed (no entry in cidMap)
//           remain untouched in the output, preserving the suite's
//           "load failed images" affordance (REQ-EXTIMG-71).
//
// We use golang.org/x/net/html, not regexes. The parser tolerates
// Outlook-grade HTML; on parse failure the orchestrator falls back to
// passthrough for that one message (REQ-EXTIMG-13 / REQ-EXTIMG-61).

// candidate is one external URL the rewriter wants to fetch.
type candidate struct {
	URL string
}

// extractCandidates parses the HTML and returns the deduplicated set
// of external URLs eligible for fetching, in document order. The HTML
// is not modified by this pass.
func extractCandidates(htmlBytes []byte) ([]candidate, error) {
	doc, err := html.Parse(bytes.NewReader(htmlBytes))
	if err != nil {
		return nil, fmt.Errorf("extimg: html parse: %w", err)
	}
	seen := map[string]struct{}{}
	var out []candidate
	visit(doc, func(n *html.Node) {
		for _, u := range candidateURLsFromNode(n) {
			if _, dup := seen[u]; dup {
				continue
			}
			seen[u] = struct{}{}
			out = append(out, candidate{URL: u})
		}
	})
	return out, nil
}

// rewriteHTML returns the rewritten HTML bytes. cidMap maps URL → cid
// (without angle brackets); URLs absent from the map keep their
// original src. The output is HTML-encoded by the html package, so a
// round-trip through the parser is necessary to ensure attribute
// quoting / entity escaping is correct.
func rewriteHTML(htmlBytes []byte, cidMap map[string]string) ([]byte, error) {
	doc, err := html.Parse(bytes.NewReader(htmlBytes))
	if err != nil {
		return nil, fmt.Errorf("extimg: html parse: %w", err)
	}
	visit(doc, func(n *html.Node) { rewriteNode(n, cidMap) })
	var buf bytes.Buffer
	if err := html.Render(&buf, doc); err != nil {
		return nil, fmt.Errorf("extimg: html render: %w", err)
	}
	return buf.Bytes(), nil
}

// visit is a depth-first traversal calling fn on every Element node.
func visit(n *html.Node, fn func(*html.Node)) {
	if n.Type == html.ElementNode {
		fn(n)
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		visit(c, fn)
	}
}

// candidateURLsFromNode extracts every URL string from n that we'd
// want to fetch. URLs are returned without modification — filtering
// (scheme, eligibility) is done by the SSRF guard / fetcher.
func candidateURLsFromNode(n *html.Node) []string {
	var out []string
	tag := n.Data

	switch tag {
	case "img":
		out = appendIfExternal(out, attrValue(n, "src"))
		out = appendSrcsetURLs(out, attrValue(n, "srcset"))
	case "source":
		out = appendIfExternal(out, attrValue(n, "src"))
		out = appendSrcsetURLs(out, attrValue(n, "srcset"))
	case "image":
		// SVG <image href="..."> and legacy xlink:href.
		out = appendIfExternal(out, attrValue(n, "href"))
		out = appendIfExternal(out, attrValue(n, "xlink:href"))
	}
	if styleVal := attrValue(n, "style"); styleVal != "" {
		for _, u := range cssExtractURLs(styleVal) {
			out = appendIfExternal(out, u)
		}
	}
	return out
}

// rewriteNode replaces eligible URLs in n's attributes with the
// corresponding cid: reference from cidMap. URLs absent from cidMap
// are left untouched.
func rewriteNode(n *html.Node, cidMap map[string]string) {
	tag := n.Data
	switch tag {
	case "img":
		setAttrIfMapped(n, "src", cidMap)
		setSrcsetAttr(n, "srcset", cidMap)
	case "source":
		setAttrIfMapped(n, "src", cidMap)
		setSrcsetAttr(n, "srcset", cidMap)
	case "image":
		setAttrIfMapped(n, "href", cidMap)
		setAttrIfMapped(n, "xlink:href", cidMap)
	}
	if styleVal := attrValue(n, "style"); styleVal != "" {
		newStyle := cssRewriteURLs(styleVal, cidMap)
		if newStyle != styleVal {
			setAttr(n, "style", newStyle)
		}
	}
}

// attrValue returns the named attribute's value, or "" when absent.
func attrValue(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}

// setAttr writes the named attribute, replacing or appending.
func setAttr(n *html.Node, name, value string) {
	for i := range n.Attr {
		if n.Attr[i].Key == name {
			n.Attr[i].Val = value
			return
		}
	}
	n.Attr = append(n.Attr, html.Attribute{Key: name, Val: value})
}

// setAttrIfMapped rewrites n's named attribute when its current value
// is in cidMap. The cid: prefix is added here.
func setAttrIfMapped(n *html.Node, name string, cidMap map[string]string) {
	v := attrValue(n, name)
	if v == "" {
		return
	}
	cid, ok := cidMap[v]
	if !ok {
		return
	}
	setAttr(n, name, "cid:"+cid)
}

// setSrcsetAttr rewrites every URL inside a comma-separated srcset
// attribute. URL ordering and descriptors (e.g. "1x", "300w") are
// preserved verbatim.
func setSrcsetAttr(n *html.Node, name string, cidMap map[string]string) {
	v := attrValue(n, name)
	if v == "" {
		return
	}
	rewritten := rewriteSrcset(v, cidMap)
	if rewritten != v {
		setAttr(n, name, rewritten)
	}
}

// rewriteSrcset rewrites every URL in a srcset value. Spec: comma-
// separated entries, each entry is "URL [descriptor]". We tokenise
// loosely; commas inside parentheses are preserved by the simple
// pass-through (URL fields don't contain commas in practice).
func rewriteSrcset(v string, cidMap map[string]string) string {
	parts := splitSrcset(v)
	for i, part := range parts {
		entry := strings.TrimSpace(part)
		if entry == "" {
			continue
		}
		// Split off the descriptor; the URL is the leading non-space token.
		fields := strings.Fields(entry)
		if len(fields) == 0 {
			continue
		}
		u := fields[0]
		if cid, ok := cidMap[u]; ok {
			fields[0] = "cid:" + cid
		}
		parts[i] = strings.Join(fields, " ")
	}
	return strings.Join(parts, ", ")
}

// splitSrcset splits on commas. In practice srcset URLs do not
// contain commas (the spec disallows unescaped commas in the URL).
func splitSrcset(v string) []string {
	return strings.Split(v, ",")
}

// appendSrcsetURLs extracts every URL from a srcset value into out.
func appendSrcsetURLs(out []string, srcset string) []string {
	if srcset == "" {
		return out
	}
	for _, part := range splitSrcset(srcset) {
		entry := strings.TrimSpace(part)
		if entry == "" {
			continue
		}
		fields := strings.Fields(entry)
		if len(fields) == 0 {
			continue
		}
		out = appendIfExternal(out, fields[0])
	}
	return out
}

// appendIfExternal appends u to out when u looks like an http(s) URL.
// Empty / cid: / data: / mid: / relative URLs are skipped per
// REQ-EXTIMG-12.
func appendIfExternal(out []string, u string) []string {
	u = strings.TrimSpace(u)
	if u == "" {
		return out
	}
	low := strings.ToLower(u)
	if strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://") {
		return append(out, u)
	}
	return out
}

// cssExtractURLs walks a style="" attribute string and returns every
// url(...) reference.
func cssExtractURLs(style string) []string {
	var out []string
	rest := style
	for {
		i := strings.Index(rest, "url(")
		if i < 0 {
			return out
		}
		rest = rest[i+4:]
		end := strings.IndexByte(rest, ')')
		if end < 0 {
			return out
		}
		raw := strings.TrimSpace(rest[:end])
		raw = strings.Trim(raw, "\"'")
		if raw != "" {
			out = append(out, raw)
		}
		rest = rest[end+1:]
	}
}

// cssRewriteURLs replaces url(...) references in a style="" string
// using cidMap. Quoting and surrounding whitespace are preserved.
func cssRewriteURLs(style string, cidMap map[string]string) string {
	var b strings.Builder
	rest := style
	for {
		i := strings.Index(rest, "url(")
		if i < 0 {
			b.WriteString(rest)
			return b.String()
		}
		b.WriteString(rest[:i+4])
		rest = rest[i+4:]
		end := strings.IndexByte(rest, ')')
		if end < 0 {
			b.WriteString(rest)
			return b.String()
		}
		raw := rest[:end]
		trimmed := strings.TrimSpace(raw)
		stripped := strings.Trim(trimmed, "\"'")
		quoted := ""
		if len(trimmed) > 0 && (trimmed[0] == '"' || trimmed[0] == '\'') {
			quoted = string(trimmed[0])
		}
		if cid, ok := cidMap[stripped]; ok {
			if quoted != "" {
				b.WriteString(quoted)
				b.WriteString("cid:")
				b.WriteString(cid)
				b.WriteString(quoted)
			} else {
				b.WriteString("cid:")
				b.WriteString(cid)
			}
		} else {
			b.WriteString(raw)
		}
		b.WriteByte(')')
		rest = rest[end+1:]
	}
}
