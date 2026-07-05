package extimg

import (
	"bytes"
	"fmt"

	"golang.org/x/net/html"
)

// PlaceholderDataURI is the inline 1x1 transparent GIF that
// RewriteForPlaceholder substitutes for every external image src.
// Self-contained data URI so the renderer does not need to fetch
// anything to display it. Identical bytes are emitted by every call,
// so the rewriter is byte-for-byte deterministic.
const PlaceholderDataURI = "data:image/gif;base64,R0lGODlhAQABAIAAAP///wAAACH5BAEAAAAALAAAAAABAAEAAAICRAEAOw=="

// RewriteForPlaceholder walks raw as HTML and replaces every external
// image reference with PlaceholderDataURI. Used by the Email/get path
// when the message row has InternalizePending = true: the user sees
// the message body with image placeholders, no leak of open-rate /
// IP to the original image origin, while the background internalize
// worker (REQ-EXTIMG-BG-01) prepares the rewritten blob.
//
// Inline images (cid:, data:, blob:) and non-HTML content (plain
// text, OOXML) are not touched. Preserves DOM structure including
// existing alt text, srcset attributes are blanked entirely (the
// browser falls back to the placeholder src). CSS background-image
// url(...) references are placeholderified.
//
// Errors are reserved for HTML parse / render failures; an
// unparseable body is returned unchanged with err == nil so the
// caller never refuses to render a message because it could not be
// placeholderified.
func RewriteForPlaceholder(raw []byte) ([]byte, error) {
	doc, err := html.Parse(bytes.NewReader(raw))
	if err != nil {
		return raw, nil
	}
	visit(doc, placeholderifyNode)
	var buf bytes.Buffer
	if err := html.Render(&buf, doc); err != nil {
		return nil, fmt.Errorf("extimg: placeholder render: %w", err)
	}
	return buf.Bytes(), nil
}

// placeholderifyNode replaces every external src / srcset / href on
// img / source / image elements with the placeholder. CSS
// background-image url(...) values are likewise rewritten. The
// deprecated HTML background= attribute on table elements is also
// placeholderified so that newsletter images supplied via that
// mechanism show a consistent placeholder while the background
// internalize worker prepares the rewritten blob.
func placeholderifyNode(n *html.Node) {
	switch n.Data {
	case "img":
		if isExternal(attrValue(n, "src")) {
			setAttr(n, "src", PlaceholderDataURI)
		}
		if attrValue(n, "srcset") != "" {
			// A srcset can carry many candidates; rather than parse
			// them, blank the whole attribute. The browser falls
			// back to the (already-placeholderified) src.
			setAttr(n, "srcset", "")
		}
	case "source":
		if isExternal(attrValue(n, "src")) {
			setAttr(n, "src", PlaceholderDataURI)
		}
		if attrValue(n, "srcset") != "" {
			setAttr(n, "srcset", "")
		}
	case "image":
		if isExternal(attrValue(n, "href")) {
			setAttr(n, "href", PlaceholderDataURI)
		}
		if isExternal(attrValue(n, "xlink:href")) {
			setAttr(n, "xlink:href", PlaceholderDataURI)
		}
	case "body", "table", "tr", "td", "th":
		// Placeholderify deprecated HTML background= attribute on table elements.
		if isExternal(attrValue(n, "background")) {
			setAttr(n, "background", PlaceholderDataURI)
		}
	}
	if styleVal := attrValue(n, "style"); styleVal != "" {
		newStyle := cssPlaceholderify(styleVal)
		if newStyle != styleVal {
			setAttr(n, "style", newStyle)
		}
	}
}

// isExternal reports whether u is a fetchable external URL. Inline-
// image schemes (cid:, data:, blob:) and empty values are ignored;
// everything else (http(s), path-relative, unknown schemes) is
// treated as external so the placeholderifier never leaks an open
// rate.
func isExternal(u string) bool {
	if u == "" {
		return false
	}
	switch {
	case startsWith(u, "cid:"),
		startsWith(u, "data:"),
		startsWith(u, "blob:"):
		return false
	}
	return true
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// cssPlaceholderify replaces external url(...) references in a CSS
// value (typically the inline `style="..."` attribute) with the
// placeholder data URI. Inline url(data:...) and url(cid:...) are
// preserved.
func cssPlaceholderify(style string) string {
	urls := cssExtractURLs(style)
	if len(urls) == 0 {
		return style
	}
	m := make(map[string]string, len(urls))
	for _, u := range urls {
		if isExternal(u) {
			m[u] = PlaceholderDataURI
		}
	}
	if len(m) == 0 {
		return style
	}
	return cssRewriteURLs(style, m)
}
