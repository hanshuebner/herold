package mailparse

import (
	"fmt"
	"io"
	"net/mail"
	"strings"
)

// Disposition is the Content-Disposition classification for a part.
type Disposition int

const (
	// DispositionUnknown is the default when no Content-Disposition header is present.
	DispositionUnknown Disposition = iota
	// DispositionInline marks parts with Content-Disposition: inline.
	DispositionInline
	// DispositionAttachment marks parts with Content-Disposition: attachment.
	DispositionAttachment
)

// String returns the wire-form name of the disposition.
func (d Disposition) String() string {
	switch d {
	case DispositionInline:
		return "inline"
	case DispositionAttachment:
		return "attachment"
	default:
		return ""
	}
}

// Headers is a case-insensitive accessor over an ordered list of header lines.
type Headers struct {
	// order preserves first-seen insertion order for stable iteration.
	order []string
	// values is keyed by the canonical MIME header form (textproto.CanonicalMIMEHeaderKey).
	values map[string][]string
}

// NewHeaders returns an empty Headers.
func NewHeaders() Headers {
	return Headers{values: map[string][]string{}}
}

// Get returns the first value for name, case-insensitive. Empty string if absent.
func (h Headers) Get(name string) string {
	if h.values == nil {
		return ""
	}
	vs := h.values[canonicalHeaderKey(name)]
	if len(vs) == 0 {
		return ""
	}
	return vs[0]
}

// GetAll returns every value for name in the order they appeared.
func (h Headers) GetAll(name string) []string {
	if h.values == nil {
		return nil
	}
	return append([]string(nil), h.values[canonicalHeaderKey(name)]...)
}

// Keys returns the unique header keys in first-seen order.
func (h Headers) Keys() []string {
	return append([]string(nil), h.order...)
}

// add appends a value for name, preserving order on the first occurrence of the key.
func (h *Headers) add(name, value string) {
	if h.values == nil {
		h.values = map[string][]string{}
	}
	key := canonicalHeaderKey(name)
	if _, ok := h.values[key]; !ok {
		h.order = append(h.order, key)
	}
	h.values[key] = append(h.values[key], value)
}

func canonicalHeaderKey(name string) string {
	// Mirror textproto.CanonicalMIMEHeaderKey behavior but keep the helper local.
	var b strings.Builder
	b.Grow(len(name))
	upper := true
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case upper && 'a' <= c && c <= 'z':
			c -= 'a' - 'A'
		case !upper && 'A' <= c && c <= 'Z':
			c += 'a' - 'A'
		}
		b.WriteByte(c)
		upper = c == '-'
	}
	return b.String()
}

// Envelope groups the common RFC 5322 address and identity fields after
// RFC 2047 decoding and SMTPUTF8-aware normalization.
type Envelope struct {
	From       []mail.Address
	Sender     *mail.Address
	ReplyTo    []mail.Address
	To         []mail.Address
	Cc         []mail.Address
	Bcc        []mail.Address
	Subject    string
	MessageID  string
	Date       string
	InReplyTo  []string
	References []string
}

// String returns a compact display form of the envelope, not a wire form.
func (e Envelope) String() string {
	var b strings.Builder
	if len(e.From) > 0 {
		b.WriteString("From: ")
		b.WriteString(addrList(e.From))
		b.WriteByte('\n')
	}
	if len(e.To) > 0 {
		b.WriteString("To: ")
		b.WriteString(addrList(e.To))
		b.WriteByte('\n')
	}
	if len(e.Cc) > 0 {
		b.WriteString("Cc: ")
		b.WriteString(addrList(e.Cc))
		b.WriteByte('\n')
	}
	if e.Subject != "" {
		b.WriteString("Subject: ")
		b.WriteString(e.Subject)
		b.WriteByte('\n')
	}
	if e.MessageID != "" {
		b.WriteString("Message-ID: ")
		b.WriteString(e.MessageID)
		b.WriteByte('\n')
	}
	if e.Date != "" {
		b.WriteString("Date: ")
		b.WriteString(e.Date)
		b.WriteByte('\n')
	}
	return b.String()
}

func addrList(addrs []mail.Address) string {
	parts := make([]string, 0, len(addrs))
	for _, a := range addrs {
		parts = append(parts, a.String())
	}
	return strings.Join(parts, ", ")
}

// Part is a node in the MIME tree. Text leaves carry decoded Text; non-text
// leaves record only metadata and a raw byte offset so callers can stream the
// body on demand via OpenBody. Branches carry Children.
type Part struct {
	ContentType             string
	Charset                 string
	ContentTransferEncoding string
	Headers                 Headers
	Disposition             Disposition
	Filename                string
	Children                []Part
	// Text is the decoded, charset-converted body of a text/* leaf, capped at
	// ParseOptions.MaxTextPartBytes. Empty for non-text leaves and containers.
	Text string
	// TextTruncated is true when the decoded text body exceeded MaxTextPartBytes
	// and Part.Text holds only the first MaxTextPartBytes bytes of it.
	TextTruncated bool
	// Size is the decoded (CTE-decoded) byte length of this leaf's body.
	// Zero for containers (multipart/*). For text parts it reflects the
	// full decoded size even when Text is truncated.
	Size int64
	// DecodeErrors is a list of non-fatal decode warnings (e.g. lenient
	// base64 recovery). Populated even when Parse does not return an error.
	DecodeErrors []string

	// rawOffset is the byte offset of this part's raw (CTE-encoded) body
	// within the bytes passed to Parse. Zero for containers.
	rawOffset int64
	// rawLen is the byte length of this part's raw (CTE-encoded) body.
	// Zero for containers.
	rawLen int64
}

// IsText reports whether the part's media type is textual.
func (p Part) IsText() bool {
	return strings.HasPrefix(strings.ToLower(p.ContentType), "text/")
}

// IsMultipart reports whether the part's media type is a multipart container.
func (p Part) IsMultipart() bool {
	return strings.HasPrefix(strings.ToLower(p.ContentType), "multipart/")
}

// OpenBody returns a streaming reader that CTE-decodes and (for text parts)
// charset-converts the raw body of this leaf part. src must be an io.ReaderAt
// over the same byte stream that was passed to Parse (e.g. the store blob
// reader, a spill *os.File, or bytes.NewReader for tests). Returns an error
// when the part is a container (Children present) or src is nil.
//
// The returned ReadCloser streams — it does not buffer the whole body. Callers
// should call Close() when done to release any underlying resources (the
// implementation may return a no-op closer).
func (p Part) OpenBody(src io.ReaderAt) (io.ReadCloser, error) {
	if src == nil {
		return nil, fmt.Errorf("mailparse: OpenBody: src is nil")
	}
	if len(p.Children) > 0 {
		return nil, fmt.Errorf("mailparse: OpenBody: part is a multipart container")
	}
	rawSection := io.NewSectionReader(src, p.rawOffset, p.rawLen)
	return openBodyDecoder(rawSection, p.ContentTransferEncoding, p.Charset, p.IsText())
}

// RawBody returns a streaming reader over the raw (CTE-encoded, as-received)
// bytes of this leaf part's body. src must be the same io.ReaderAt that was
// used to produce the bytes passed to Parse. The returned reader never
// materializes the full body: it is an io.SectionReader over src positioned
// at the part's raw byte range.
//
// Unlike OpenBody, which CTE-decodes the body, RawBody returns the original
// encoded bytes verbatim. This is useful for streaming pass-through of
// unchanged parts (attachments, non-HTML body parts) where fidelity to the
// wire encoding is preferred over re-encoding (REQ-STORE-17/19 Phase 2c).
//
// Returns an error when the part is a container (Children present) or src is nil.
func (p Part) RawBody(src io.ReaderAt) (io.Reader, error) {
	if src == nil {
		return nil, fmt.Errorf("mailparse: RawBody: src is nil")
	}
	if len(p.Children) > 0 {
		return nil, fmt.Errorf("mailparse: RawBody: part is a multipart container")
	}
	return io.NewSectionReader(src, p.rawOffset, p.rawLen), nil
}

// Message is the parsed, decoded form of an RFC 5322 message.
type Message struct {
	Headers        Headers
	Envelope       Envelope
	Body           Part
	AuthResultsRaw string
	Size           int64
}
