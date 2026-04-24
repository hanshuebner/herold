package mailparse

import (
	"bytes"
	"fmt"
	"io"
	"net/mail"
	"strings"

	"github.com/jhillyerd/enmime"
)

// DefaultMaxSize caps a parsed message at 50 MiB unless the caller overrides it.
const DefaultMaxSize int64 = 50 * 1024 * 1024

// DefaultMaxDepth caps the nesting of multipart trees the parser will walk.
const DefaultMaxDepth = 32

// DefaultMaxParts caps the total number of MIME parts (branches + leaves) accepted.
const DefaultMaxParts = 10000

// DefaultMaxHeaderLine is the upper bound on the length of any header line in bytes.
// RFC 5322 §2.1.1 requires 998 chars; we allow a small tolerance for accidental trailing CR.
const DefaultMaxHeaderLine = 998

// ParseOptions controls the strictness caps applied by Parse. The zero value is
// not recommended; use NewParseOptions for defaults.
type ParseOptions struct {
	MaxSize       int64
	MaxDepth      int
	MaxParts      int
	MaxHeaderLine int
	StrictCharset bool
	StrictBase64  bool
	StrictQP      bool
	// StrictHeaderLine, when true, rejects any header line longer than MaxHeaderLine.
	StrictHeaderLine bool
	// StrictBoundary, when true, rejects messages that end without a closing boundary marker.
	StrictBoundary bool
}

// NewParseOptions returns the recommended defaults for production use.
func NewParseOptions() ParseOptions {
	return ParseOptions{
		MaxSize:          DefaultMaxSize,
		MaxDepth:         DefaultMaxDepth,
		MaxParts:         DefaultMaxParts,
		MaxHeaderLine:    DefaultMaxHeaderLine,
		StrictCharset:    true,
		StrictBase64:     true,
		StrictQP:         true,
		StrictHeaderLine: false,
		StrictBoundary:   true,
	}
}

// applyDefaults fills zero fields with production defaults so callers may pass a sparse struct.
func (o *ParseOptions) applyDefaults() {
	if o.MaxSize <= 0 {
		o.MaxSize = DefaultMaxSize
	}
	if o.MaxDepth <= 0 {
		o.MaxDepth = DefaultMaxDepth
	}
	if o.MaxParts <= 0 {
		o.MaxParts = DefaultMaxParts
	}
	if o.MaxHeaderLine <= 0 {
		o.MaxHeaderLine = DefaultMaxHeaderLine
	}
}

// Parse consumes an RFC 5322 message from r and returns the decoded Message.
// Parse is pure CPU work; it does not take a context. Helpers that may call
// external decoders in later phases accept context.Context.
func Parse(r io.Reader, opts ParseOptions) (Message, error) {
	opts.applyDefaults()

	raw, err := readCapped(r, opts.MaxSize)
	if err != nil {
		return Message{}, err
	}

	if bad := findOverlongHeaderLine(raw, opts.MaxHeaderLine); bad > 0 {
		if opts.StrictHeaderLine {
			return Message{}, &ParseError{
				Reason:     ReasonMalformed,
				Message:    "header line exceeds maximum length",
				PartIndex:  -1,
				HeaderLine: bad,
			}
		}
	}

	env, enerr := enmime.ReadEnvelope(bytes.NewReader(raw))
	if enerr != nil {
		return Message{}, &ParseError{
			Reason:    ReasonMalformed,
			Message:   "enmime rejected input",
			PartIndex: -1,
			Cause:     enerr,
		}
	}
	if env == nil || env.Root == nil {
		return Message{}, &ParseError{
			Reason:    ReasonMalformed,
			Message:   "no parseable root part",
			PartIndex: -1,
		}
	}

	msg := Message{
		Headers:        convertHeaders(env.Root.Header),
		AuthResultsRaw: joinHeader(env.Root.Header, "Authentication-Results"),
		Size:           int64(len(raw)),
		Raw:            raw,
	}
	msg.Envelope = buildEnvelope(env, msg.Headers)

	counter := &partCounter{max: opts.MaxParts}
	body, convErr := convertPart(env.Root, &opts, 0, counter)
	if convErr != nil {
		return Message{}, convErr
	}
	msg.Body = body

	if opts.StrictBoundary {
		if terr := checkTruncation(env, counter.index); terr != nil {
			return Message{}, terr
		}
		if terr := checkRawBoundariesRecursive(raw, env.Root, counter.index); terr != nil {
			return Message{}, terr
		}
	}

	return msg, nil
}

// checkRawBoundariesRecursive walks each multipart container and verifies a closing marker
// is present somewhere in the raw bytes.
func checkRawBoundariesRecursive(raw []byte, p *enmime.Part, idx int) *ParseError {
	if p == nil {
		return nil
	}
	if strings.HasPrefix(strings.ToLower(p.ContentType), "multipart/") {
		boundary := p.Boundary
		if boundary == "" {
			boundary = p.ContentTypeParams["boundary"]
		}
		if boundary != "" {
			close := "--" + boundary + "--"
			if !bytes.Contains(raw, []byte(close)) {
				return &ParseError{
					Reason:    ReasonTruncated,
					Message:   fmt.Sprintf("multipart boundary %q has no closing marker", boundary),
					PartIndex: idx,
				}
			}
		}
	}
	for c := p.FirstChild; c != nil; c = c.NextSibling {
		if e := checkRawBoundariesRecursive(raw, c, idx); e != nil {
			return e
		}
	}
	return nil
}

// readCapped reads at most limit+1 bytes so we can distinguish exactly-at-limit from over-limit.
func readCapped(r io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		limit = DefaultMaxSize
	}
	lr := io.LimitReader(r, limit+1)
	buf, err := io.ReadAll(lr)
	if err != nil {
		return nil, &ParseError{Reason: ReasonReaderError, Message: "read input", PartIndex: -1, Cause: err}
	}
	if int64(len(buf)) > limit {
		return nil, &ParseError{
			Reason:    ReasonTooLarge,
			Message:   fmt.Sprintf("input exceeds MaxSize=%d bytes", limit),
			PartIndex: -1,
		}
	}
	return buf, nil
}

// findOverlongHeaderLine scans the header section and returns the 1-based line
// number of the first line that exceeds max bytes (CRLF excluded), or 0.
func findOverlongHeaderLine(raw []byte, max int) int {
	// Header ends at the first empty line (CRLF CRLF or LF LF).
	end := headerEnd(raw)
	if end <= 0 {
		end = len(raw)
	}
	line := 1
	lineStart := 0
	for i := 0; i < end; i++ {
		if raw[i] == '\n' {
			ll := i - lineStart
			if ll > 0 && raw[i-1] == '\r' {
				ll--
			}
			if ll > max {
				return line
			}
			line++
			lineStart = i + 1
		}
	}
	return 0
}

// headerEnd returns the byte offset of the blank line that terminates the header section.
func headerEnd(raw []byte) int {
	if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 {
		return i
	}
	if i := bytes.Index(raw, []byte("\n\n")); i >= 0 {
		return i
	}
	return -1
}

// joinHeader returns all values of a header joined by ", " preserving raw form.
func joinHeader(h map[string][]string, name string) string {
	key := canonicalHeaderKey(name)
	for k, vs := range h {
		if canonicalHeaderKey(k) == key {
			return strings.Join(vs, ", ")
		}
	}
	return ""
}

// convertHeaders maps a textproto.MIMEHeader-like map into our Headers type.
func convertHeaders(src map[string][]string) Headers {
	h := NewHeaders()
	for k, vs := range src {
		for _, v := range vs {
			h.add(k, v)
		}
	}
	return h
}

// buildEnvelope fills the Envelope struct from enmime's convenience accessors, which handle
// RFC 2047 decoding. The SMTPUTF8 case works because enmime uses mail.ParseAddressList under
// the hood, which accepts UTF-8 local parts when they are already UTF-8 bytes.
func buildEnvelope(env *enmime.Envelope, h Headers) Envelope {
	out := Envelope{
		Subject:    env.GetHeader("Subject"),
		MessageID:  strings.TrimSpace(env.GetHeader("Message-ID")),
		Date:       env.GetHeader("Date"),
		InReplyTo:  splitMessageIDs(env.GetHeader("In-Reply-To")),
		References: splitMessageIDs(env.GetHeader("References")),
	}
	if addrs, err := env.AddressList("From"); err == nil {
		out.From = deref(addrs)
	} else {
		out.From = parseAddrsLenient(h.Get("From"))
	}
	if addrs, err := env.AddressList("Sender"); err == nil && len(addrs) > 0 {
		a := *addrs[0]
		out.Sender = &a
	}
	if addrs, err := env.AddressList("Reply-To"); err == nil {
		out.ReplyTo = deref(addrs)
	}
	if addrs, err := env.AddressList("To"); err == nil {
		out.To = deref(addrs)
	} else {
		out.To = parseAddrsLenient(h.Get("To"))
	}
	if addrs, err := env.AddressList("Cc"); err == nil {
		out.Cc = deref(addrs)
	}
	if addrs, err := env.AddressList("Bcc"); err == nil {
		out.Bcc = deref(addrs)
	}
	return out
}

func deref(in []*mail.Address) []mail.Address {
	if len(in) == 0 {
		return nil
	}
	out := make([]mail.Address, 0, len(in))
	for _, a := range in {
		if a != nil {
			out = append(out, *a)
		}
	}
	return out
}

// parseAddrsLenient tolerates malformed address headers; unparseable input yields nil, not an error.
func parseAddrsLenient(s string) []mail.Address {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	addrs, err := mail.ParseAddressList(s)
	if err != nil {
		return nil
	}
	return deref(addrs)
}

// splitMessageIDs extracts angle-bracketed Message-IDs from an In-Reply-To or References header.
func splitMessageIDs(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for i := 0; i < len(s); i++ {
		if s[i] != '<' {
			continue
		}
		j := strings.IndexByte(s[i:], '>')
		if j <= 0 {
			break
		}
		out = append(out, s[i:i+j+1])
		i += j
	}
	return out
}

// partCounter tracks the sequential walk index and enforces MaxParts.
type partCounter struct {
	index int
	max   int
}

func (pc *partCounter) next() (int, error) {
	idx := pc.index
	pc.index++
	if pc.max > 0 && pc.index > pc.max {
		return idx, &ParseError{
			Reason:    ReasonTooManyParts,
			Message:   fmt.Sprintf("part count exceeded MaxParts=%d", pc.max),
			PartIndex: idx,
		}
	}
	return idx, nil
}

// convertPart walks an enmime Part tree, applies the strictness layer, and produces our Part.
func convertPart(src *enmime.Part, opts *ParseOptions, depth int, pc *partCounter) (Part, error) {
	if depth > opts.MaxDepth {
		return Part{}, &ParseError{
			Reason:    ReasonDepthExceeded,
			Message:   fmt.Sprintf("nesting depth exceeded MaxDepth=%d", opts.MaxDepth),
			PartIndex: pc.index,
		}
	}
	idx, err := pc.next()
	if err != nil {
		return Part{}, err
	}

	p := Part{
		ContentType:             src.ContentType,
		Charset:                 src.Charset,
		ContentTransferEncoding: src.Header.Get("Content-Transfer-Encoding"),
		Headers:                 convertHeaders(src.Header),
		Disposition:             parseDisposition(src.Disposition),
		Filename:                src.FileName,
	}

	for _, e := range src.Errors {
		if e == nil {
			continue
		}
		p.DecodeErrors = append(p.DecodeErrors, e.Error())
		if serr := mapEnmimeError(e, idx, opts); serr != nil {
			return Part{}, serr
		}
	}

	// Walk children if present.
	for c := src.FirstChild; c != nil; c = c.NextSibling {
		child, cerr := convertPart(c, opts, depth+1, pc)
		if cerr != nil {
			return Part{}, cerr
		}
		p.Children = append(p.Children, child)
	}

	// Assign content for leaves. enmime populates Content with the decoded,
	// charset-converted bytes for text parts and raw decoded bytes for non-text.
	if src.FirstChild == nil {
		if p.IsText() {
			text := string(src.Content)
			if opts.StrictCharset && p.Charset != "" && !isUTF8OrASCII(text) {
				return Part{}, &ParseError{
					Reason:    ReasonUnknownCharset,
					Message:   fmt.Sprintf("declared charset %q did not decode cleanly", p.Charset),
					PartIndex: idx,
				}
			}
			p.Text = text
		} else {
			p.Bytes = append([]byte(nil), src.Content...)
		}
	}

	return p, nil
}

// parseDisposition maps enmime's raw disposition string to our enum.
func parseDisposition(raw string) Disposition {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "inline":
		return DispositionInline
	case "attachment":
		return DispositionAttachment
	default:
		return DispositionUnknown
	}
}

// mapEnmimeError turns one of enmime's *Error entries into a structural error if the
// caller has strictness enabled for the corresponding class. enmime marks several classes
// of silent corruption as non-severe warnings; we treat them as hard errors when strictness
// is on, because the spike identified these as the silent-corruption cases to catch.
func mapEnmimeError(e *enmime.Error, partIdx int, opts *ParseOptions) *ParseError {
	switch e.Name {
	case enmime.ErrorMalformedBase64:
		if opts.StrictBase64 {
			return &ParseError{
				Reason:    ReasonMalformedBase64,
				Message:   e.Detail,
				PartIndex: partIdx,
			}
		}
	case enmime.ErrorCharsetConversion, enmime.ErrorCharsetDeclaration:
		// enmime reports this when it cannot resolve the declared charset name, or when
		// auto-detected bytes contradict the declaration. The authoritative check for
		// "bytes do not match declared charset" is the UTF-8 validation performed on the
		// decoded leaf text in convertPart; surfacing enmime's warning here would also
		// fire on malformed-but-fallback-parseable Content-Type headers, which the spike
		// deliberately wants us to tolerate. Leave this as a DecodeErrors note.
	case enmime.ErrorContentEncoding:
		// Covers malformed quoted-printable and similar CTE failures.
		if opts.StrictQP && looksLikeQPError(e.Detail) {
			return &ParseError{
				Reason:    ReasonMalformedQP,
				Message:   e.Detail,
				PartIndex: partIdx,
			}
		}
	}
	return nil
}

func looksLikeQPError(detail string) bool {
	d := strings.ToLower(detail)
	return strings.Contains(d, "quoted") || strings.Contains(d, "qp") || strings.Contains(d, "quotedprintable")
}

// isUTF8OrASCII reports whether s decodes as a valid UTF-8 string. A naive byte-range check.
func isUTF8OrASCII(s string) bool {
	// Fast path: use the stdlib's invariant that range over string iterates runes and returns
	// RuneError for invalid sequences.
	for _, r := range s {
		if r == '�' {
			// Only treat as invalid if the original bytes actually had no valid U+FFFD; we
			// re-check by scanning raw bytes.
			return !hasRawInvalidUTF8(s)
		}
	}
	return true
}

// hasRawInvalidUTF8 does a strict byte-level UTF-8 validation.
func hasRawInvalidUTF8(s string) bool {
	for i := 0; i < len(s); {
		b := s[i]
		switch {
		case b < 0x80:
			i++
		case b < 0xC2:
			return true
		case b < 0xE0:
			if i+1 >= len(s) || s[i+1]&0xC0 != 0x80 {
				return true
			}
			i += 2
		case b < 0xF0:
			if i+2 >= len(s) || s[i+1]&0xC0 != 0x80 || s[i+2]&0xC0 != 0x80 {
				return true
			}
			i += 3
		case b < 0xF5:
			if i+3 >= len(s) || s[i+1]&0xC0 != 0x80 || s[i+2]&0xC0 != 0x80 || s[i+3]&0xC0 != 0x80 {
				return true
			}
			i += 4
		default:
			return true
		}
	}
	return false
}
