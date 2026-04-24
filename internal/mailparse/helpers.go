package mailparse

import "strings"

// TextParts returns every text/* leaf part in the message in walk order. Useful for FTS indexing.
func TextParts(m Message) []Part {
	var out []Part
	collectText(m.Body, &out)
	return out
}

// Attachments returns every non-text, non-multipart leaf part whose disposition is not Inline.
// Inline text parts are excluded (they belong to TextParts); inline non-text parts are excluded
// because they are body-referenced media, not attachments proper.
func Attachments(m Message) []Part {
	var out []Part
	collectAttachments(m.Body, &out)
	return out
}

func collectText(p Part, out *[]Part) {
	if len(p.Children) == 0 {
		if p.IsText() && p.Disposition != DispositionAttachment {
			*out = append(*out, p)
		}
		return
	}
	for _, c := range p.Children {
		collectText(c, out)
	}
}

func collectAttachments(p Part, out *[]Part) {
	if len(p.Children) == 0 {
		if p.IsMultipart() {
			return
		}
		if p.Disposition == DispositionAttachment {
			*out = append(*out, p)
			return
		}
		if !p.IsText() && p.Disposition != DispositionInline {
			*out = append(*out, p)
		}
		return
	}
	for _, c := range p.Children {
		collectAttachments(c, out)
	}
}

// PrimaryTextBody returns the best-effort plain-text body from a Message by walking
// the text parts and picking the first text/plain leaf; falls back to any text/* part.
func PrimaryTextBody(m Message) string {
	var fallback string
	for _, p := range TextParts(m) {
		ct := strings.ToLower(p.ContentType)
		if ct == "text/plain" {
			return p.Text
		}
		if fallback == "" {
			fallback = p.Text
		}
	}
	return fallback
}
