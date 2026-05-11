package mailparse

import (
	"bytes"
	"strings"
)

// XHeroldRecipientHeader is the herold-internal synthetic header
// rendered onto inbound message bytes at JMAP Email/get / IMAP FETCH
// BODY[] / blob-download time, carrying the per-recipient envelope
// RCPT TO that produced the fan-out row (REQ-FLOW-33..34). The header
// is never persisted into the stored blob; it is folded in at render
// time and stripped from outbound submissions (REQ-FLOW-35).
const XHeroldRecipientHeader = "X-Herold-Recipient"

// InjectXHeroldRecipient prepends `X-Herold-Recipient: <value>\r\n` to
// raw (the rendered RFC 5322 bytes of a message) and returns the
// resulting buffer. The header is placed at the very top so simple
// header-section consumers see it without re-parsing folded
// continuations from the existing block. The function returns raw
// unchanged when value is empty; per REQ-FLOW-34 we do not emit the
// header for pre-feature memberships whose received_to is "".
//
// The value is emitted verbatim except for CR/LF, which are stripped
// to keep the rendered header on a single line and prevent header
// injection from a malformed received_to.
func InjectXHeroldRecipient(raw []byte, value string) []byte {
	if value == "" {
		return raw
	}
	safe := sanitiseHeaderValue(value)
	if safe == "" {
		return raw
	}
	prefix := []byte(XHeroldRecipientHeader + ": " + safe + "\r\n")
	out := make([]byte, 0, len(prefix)+len(raw))
	out = append(out, prefix...)
	out = append(out, raw...)
	return out
}

// StripXHeroldRecipient removes every `X-Herold-Recipient:` header
// (case-insensitive, with continuation lines) from raw and returns
// the resulting buffer. Used on the outbound submission path
// (REQ-FLOW-35) so the synthetic per-recipient header herold injects
// for inbound rendering is never relayed to upstream MTAs / DKIM
// signers / external SMTP smart hosts.
//
// The strip looks at the header block only — i.e. up to the first
// CRLFCRLF / LFLF body separator. Bytes after the separator are
// returned verbatim. A message without a body separator (RFC-loose
// fragments produced by tests or partial buffers) is treated as
// header-only.
func StripXHeroldRecipient(raw []byte) []byte {
	if len(raw) == 0 {
		return raw
	}
	hdrEnd, sepLen := headerBlockEnd(raw)
	header := raw[:hdrEnd]
	body := raw[hdrEnd+sepLen:]
	stripped := stripHeaderField(header, XHeroldRecipientHeader)
	if len(stripped) == len(header) {
		// No change — return original slice to avoid the copy.
		return raw
	}
	out := make([]byte, 0, len(stripped)+sepLen+len(body))
	out = append(out, stripped...)
	if sepLen > 0 {
		out = append(out, raw[hdrEnd:hdrEnd+sepLen]...)
	}
	out = append(out, body...)
	return out
}

// headerBlockEnd reports the offset of the first byte after the header
// block and the byte length of the body separator (4 for CRLFCRLF, 2
// for LFLF, 0 when no separator is present). For inputs without a
// separator the returned offset is len(raw).
func headerBlockEnd(raw []byte) (offset, sepLen int) {
	if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 {
		return i, 4
	}
	if i := bytes.Index(raw, []byte("\n\n")); i >= 0 {
		return i, 2
	}
	return len(raw), 0
}

// stripHeaderField removes every occurrence of the named header field
// (case-insensitive) from a raw header block, including its
// continuation lines.
func stripHeaderField(header []byte, name string) []byte {
	lower := strings.ToLower(name)
	out := make([]byte, 0, len(header))
	i := 0
	for i < len(header) {
		// Find end of the current logical line + its continuations.
		fieldStart := i
		fieldEnd := fieldStart
		for fieldEnd < len(header) {
			nl := bytes.IndexByte(header[fieldEnd:], '\n')
			if nl < 0 {
				fieldEnd = len(header)
				break
			}
			fieldEnd += nl + 1
			// Look at next byte: continuation if SP/HTAB.
			if fieldEnd >= len(header) {
				break
			}
			c := header[fieldEnd]
			if c != ' ' && c != '\t' {
				break
			}
		}
		field := header[fieldStart:fieldEnd]
		// Determine the field name.
		colon := bytes.IndexByte(field, ':')
		match := false
		if colon > 0 {
			fname := bytes.TrimSpace(field[:colon])
			if strings.EqualFold(string(fname), lower) {
				match = true
			}
		}
		if !match {
			out = append(out, field...)
		}
		i = fieldEnd
	}
	return out
}

func sanitiseHeaderValue(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	// Drop CR/LF; values are rendered on a single line.
	v = strings.NewReplacer("\r", "", "\n", "").Replace(v)
	return strings.TrimSpace(v)
}

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
