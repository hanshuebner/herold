package mailparse

import (
	"strings"
	"unicode"
)

// BodyTextOrigin classifies how mailparse derived a webhook payload's
// body.text.  Wire values match REQ-HOOK-EXTRACTED-02:
//
//   - "native"             — message had a text/plain part; we returned it
//     verbatim.
//   - "derived_from_html"  — no text/plain; we rendered text/html via the
//     same pipeline the FTS indexer uses, with
//     links preserved as `text (url)`.
//   - "none"               — neither a text/plain nor a renderable
//     text/html part was found.
type BodyTextOrigin string

// Origin token constants.  Use these instead of literals so callers
// match the wire vocabulary.
const (
	BodyTextOriginNative          BodyTextOrigin = "native"
	BodyTextOriginDerivedFromHTML BodyTextOrigin = "derived_from_html"
	BodyTextOriginNone            BodyTextOrigin = "none"
)

// ExtractBodyText walks the parsed message tree and returns the
// best-effort plain-text body alongside an Origin tag.  The function is
// deliberately small and dependency-free so it can be reused both by
// the FTS pipeline and by the protowebhook extracted-mode payload
// builder (REQ-HOOK-EXTRACTED-01..02).
//
// Selection rule:
//
//  1. The first text/plain leaf with a non-empty body wins.  Origin
//     reports "native" and the part's decoded Text is returned
//     verbatim.
//  2. Else the first text/html leaf with a non-empty body wins.  The
//     HTML is rendered to plain text via htmlToText (links preserved
//     as `text (url)`) and Origin reports "derived_from_html".
//  3. Otherwise Origin reports "none" and text is empty.
//
// Attachments are skipped: a text/* leaf with Disposition ==
// DispositionAttachment is treated as an attachment, not a body.
func ExtractBodyText(m Message) (text string, origin BodyTextOrigin) {
	// Pass 1: prefer text/plain.
	if body := firstNonEmptyLeaf(m.Body, "text/plain"); body != "" {
		return body, BodyTextOriginNative
	}
	// Pass 2: derive from text/html.
	if html := firstNonEmptyLeaf(m.Body, "text/html"); html != "" {
		return htmlToText(html), BodyTextOriginDerivedFromHTML
	}
	return "", BodyTextOriginNone
}

// firstNonEmptyLeaf walks the tree depth-first and returns the decoded
// Text of the first non-attachment leaf whose Content-Type starts with
// the supplied prefix and which carries a non-empty body.  Returns ""
// when nothing matches.
func firstNonEmptyLeaf(p Part, ctPrefix string) string {
	if len(p.Children) == 0 {
		if p.Disposition == DispositionAttachment {
			return ""
		}
		ct := strings.ToLower(p.ContentType)
		if strings.HasPrefix(ct, ctPrefix) && p.Text != "" {
			return p.Text
		}
		return ""
	}
	for _, c := range p.Children {
		if t := firstNonEmptyLeaf(c, ctPrefix); t != "" {
			return t
		}
	}
	return ""
}

// htmlToText renders HTML to plain text, preserving anchor URLs in the
// `text (url)` shape required by REQ-HOOK-EXTRACTED-02.  The renderer
// is intentionally small: it walks the byte stream once, drops tag
// content, decodes a small set of named entities, and tracks the most
// recent <a href="..."> attribute so a closing </a> can append the URL
// when the visible text differs from it.
//
// This is the same shape the FTS indexer wants for indexing (text +
// link URLs become searchable) so it lives in mailparse rather than in
// protowebhook.  The implementation is dependency-free; richer HTML
// constructs (tables, scripts) collapse to whitespace, which matches
// the spec's "readable plain text" commitment.
func htmlToText(in string) string {
	var (
		out      strings.Builder
		i        int
		inTag    bool
		tagBuf   strings.Builder
		linkText strings.Builder
		linkURL  string
		inLink   bool
		inScript bool
		inStyle  bool
	)
	out.Grow(len(in))
	flushChar := func(r rune) {
		if inLink {
			linkText.WriteRune(r)
		} else {
			out.WriteRune(r)
		}
	}
	for i < len(in) {
		c := in[i]
		if !inTag {
			switch c {
			case '<':
				inTag = true
				tagBuf.Reset()
				i++
				continue
			case '&':
				// Decode a small entity set.
				end := strings.IndexByte(in[i:], ';')
				if end < 0 || end > 8 {
					flushChar(rune(c))
					i++
					continue
				}
				ent := in[i+1 : i+end]
				switch strings.ToLower(ent) {
				case "amp":
					flushChar('&')
				case "lt":
					flushChar('<')
				case "gt":
					flushChar('>')
				case "quot":
					flushChar('"')
				case "apos":
					flushChar('\'')
				case "nbsp":
					flushChar(' ')
				default:
					flushChar('&')
					flushChar(rune(ent[0]))
					// Best-effort: leave the rest for the next pass to
					// handle as plain text.  (We've already consumed
					// the leading '&' and one rune; advance.)
					i += 2
					continue
				}
				i += end + 1
				continue
			}
			if inScript || inStyle {
				i++
				continue
			}
			flushChar(rune(c))
			i++
			continue
		}
		// Inside a tag.
		if c == '>' {
			inTag = false
			tag := strings.TrimSpace(tagBuf.String())
			lower := strings.ToLower(tag)
			switch {
			case strings.HasPrefix(lower, "/script"):
				inScript = false
			case strings.HasPrefix(lower, "script"):
				inScript = true
			case strings.HasPrefix(lower, "/style"):
				inStyle = false
			case strings.HasPrefix(lower, "style"):
				inStyle = true
			case strings.HasPrefix(lower, "a "), lower == "a":
				inLink = true
				linkText.Reset()
				linkURL = extractHrefAttr(tag)
			case lower == "/a":
				visible := strings.TrimSpace(linkText.String())
				inLink = false
				linkText.Reset()
				switch {
				case visible == "" && linkURL != "":
					out.WriteString(linkURL)
				case visible != "" && linkURL != "" && !strings.EqualFold(visible, linkURL):
					out.WriteString(visible)
					out.WriteString(" (")
					out.WriteString(linkURL)
					out.WriteByte(')')
				case visible != "":
					out.WriteString(visible)
				}
				linkURL = ""
			case lower == "br", strings.HasPrefix(lower, "br "), strings.HasPrefix(lower, "br/"),
				lower == "br/", lower == "/p", strings.HasPrefix(lower, "/p"),
				lower == "/div", strings.HasPrefix(lower, "/div"),
				lower == "/li", strings.HasPrefix(lower, "/li"),
				lower == "/tr", strings.HasPrefix(lower, "/tr"),
				lower == "/h1", lower == "/h2", lower == "/h3", lower == "/h4", lower == "/h5", lower == "/h6":
				if inLink {
					linkText.WriteByte('\n')
				} else {
					out.WriteByte('\n')
				}
			}
			i++
			continue
		}
		tagBuf.WriteByte(c)
		i++
	}
	return collapseWhitespace(out.String())
}

// extractHrefAttr pulls the (case-insensitive) href="..." or href='...'
// or unquoted href=... value out of a tag's attribute soup.  Returns
// "" when no href is present.
func extractHrefAttr(tag string) string {
	lower := strings.ToLower(tag)
	idx := strings.Index(lower, "href")
	if idx < 0 {
		return ""
	}
	// Find the '=' that follows.
	eq := strings.IndexByte(tag[idx:], '=')
	if eq < 0 {
		return ""
	}
	rest := strings.TrimLeft(tag[idx+eq+1:], " \t")
	if rest == "" {
		return ""
	}
	switch rest[0] {
	case '"':
		end := strings.IndexByte(rest[1:], '"')
		if end < 0 {
			return ""
		}
		return rest[1 : 1+end]
	case '\'':
		end := strings.IndexByte(rest[1:], '\'')
		if end < 0 {
			return ""
		}
		return rest[1 : 1+end]
	}
	end := strings.IndexAny(rest, " \t>")
	if end < 0 {
		return rest
	}
	return rest[:end]
}

// collapseWhitespace turns runs of whitespace (other than newlines)
// into a single space and collapses ≥3 newlines to two so the rendered
// output is readable.  Leading and trailing whitespace are trimmed.
func collapseWhitespace(in string) string {
	var b strings.Builder
	b.Grow(len(in))
	var lastSpace, lastNL bool
	nlRun := 0
	for _, r := range in {
		switch {
		case r == '\n':
			lastSpace = false
			nlRun++
			if nlRun > 2 {
				continue
			}
			b.WriteRune('\n')
			lastNL = true
		case unicode.IsSpace(r):
			if lastSpace || lastNL {
				continue
			}
			b.WriteRune(' ')
			lastSpace = true
		default:
			b.WriteRune(r)
			lastSpace = false
			lastNL = false
			nlRun = 0
		}
	}
	return strings.TrimSpace(b.String())
}
