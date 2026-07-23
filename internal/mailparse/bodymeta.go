package mailparse

import (
	"strings"
)

// BodyPreview returns the first n runes of the leftmost text/plain body
// part's decoded text, or -- when the message has no text/plain part at all
// -- the first n runes of text extracted from the leftmost text/html body
// part (tags stripped, entities decoded, comments/script/style removed; see
// ExtractTextFromHTML). This is the canonical computation for the RFC 8621
// Email.preview field so that ingest, the background body-meta backfill
// worker, and the metadata-only render path (previewFromValues in
// internal/protojmap/mail/email/render.go) all produce identical values,
// for both the genuine-text/plain case and the HTML-only case (re #263).
//
// The result is TrimSpace-d (or, for the HTML fallback, whitespace-collapsed
// by ExtractTextFromHTML) before truncation. If n <= 0 the entire text is
// returned. If the message has neither a text/plain nor a text/html part,
// the function returns "".
func BodyPreview(m Message, n int) string {
	s := strings.TrimSpace(firstNonAttachmentTextPlain(m.Body))
	if s == "" {
		if h := firstNonAttachmentTextHTML(m.Body); h != "" {
			s = ExtractTextFromHTML(h)
		}
	}
	if s == "" {
		return ""
	}
	if n <= 0 || len(s) <= n {
		return s
	}
	// Truncate at a rune boundary to avoid splitting multi-byte codepoints.
	s = s[:n]
	for len(s) > 0 && (s[len(s)-1]&0xC0) == 0x80 {
		s = s[:len(s)-1]
	}
	return s
}

// firstNonAttachmentTextPlain returns the Text of the leftmost text/plain leaf
// that is not a Content-Disposition: attachment. Returns "" when no such part
// exists.
func firstNonAttachmentTextPlain(p Part) string {
	if len(p.Children) == 0 {
		if p.Disposition == DispositionAttachment {
			return ""
		}
		if strings.EqualFold(p.ContentType, "text/plain") {
			return p.Text
		}
		return ""
	}
	for _, c := range p.Children {
		if t := firstNonAttachmentTextPlain(c); t != "" {
			return t
		}
	}
	return ""
}

// firstNonAttachmentTextHTML returns the Text of the leftmost text/html leaf
// that is not a Content-Disposition: attachment. Returns "" when no such
// part exists. Mirrors firstNonAttachmentTextPlain for the text/html
// fallback BodyPreview uses when a message has no text/plain part.
func firstNonAttachmentTextHTML(p Part) string {
	if len(p.Children) == 0 {
		if p.Disposition == DispositionAttachment {
			return ""
		}
		if strings.EqualFold(p.ContentType, "text/html") {
			return p.Text
		}
		return ""
	}
	for _, c := range p.Children {
		if t := firstNonAttachmentTextHTML(c); t != "" {
			return t
		}
	}
	return ""
}

// HasAttachment reports whether the message contains at least one non-inline
// attachment part. The classification mirrors walkParts in
// internal/protojmap/mail/email/render.go: a leaf part is an attachment when
// its Content-Disposition is "attachment", OR it is a non-text leaf that is
// not explicitly "inline" with a Content-ID (those become inline-referenced
// media rather than attachments).
//
// Specifically a leaf counts as an attachment when any of these hold:
//   - Content-Disposition: attachment
//   - Not text/plain or text/html AND (not Content-Disposition: inline OR no Content-ID)
func HasAttachment(m Message) bool {
	return hasAttachmentInPart(m.Body)
}

// hasAttachmentInPart recursively walks the MIME tree and returns true when
// any leaf qualifies as an attachment by the same criteria that walkParts uses.
func hasAttachmentInPart(p Part) bool {
	if len(p.Children) == 0 {
		return isAttachmentLeaf(p)
	}
	for _, c := range p.Children {
		if hasAttachmentInPart(c) {
			return true
		}
	}
	return false
}

// isAttachmentLeaf mirrors the walkParts leaf classification in render.go.
// A leaf is an attachment when:
//  1. Content-Disposition is explicitly "attachment", OR
//  2. It is neither text/plain nor text/html AND is not inline-with-CID.
//
// "Inline with CID" means Content-Disposition: inline AND a non-empty
// Content-ID header -- those are referenced via cid: URIs in the HTML body
// and are not considered standalone attachments.
func isAttachmentLeaf(p Part) bool {
	switch p.Disposition {
	case DispositionAttachment:
		return true
	case DispositionInline:
		// Inline is only truly inline when a Content-ID header is present
		// (mirrors the render.go CID check). Without a CID the part is
		// treated as a regular attachment.
		cid := strings.TrimSpace(p.Headers.Get("Content-ID"))
		cid = strings.Trim(cid, "<> \t")
		if cid != "" {
			// Truly inline (cid-referenced); not an attachment.
			return false
		}
		// No CID: falls through to attachment handling.
	}
	ct := strings.ToLower(p.ContentType)
	if strings.EqualFold(ct, "text/plain") || strings.EqualFold(ct, "text/html") {
		return false
	}
	// Non-text, non-explicitly-inline-with-CID leaf: attachment.
	return true
}
