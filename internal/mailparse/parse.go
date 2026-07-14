package mailparse

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"strings"

	"golang.org/x/text/transform"
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

// DefaultMaxTextPartBytes caps how many decoded bytes are stored in Part.Text.
// Parts larger than this are truncated and Part.TextTruncated is set to true.
// The decoded size is always stored accurately in Part.Size.
const DefaultMaxTextPartBytes = 1 << 20 // 1 MiB

// ParseOptions controls the strictness caps applied by Parse. The zero value is
// not recommended; use NewParseOptions for defaults.
type ParseOptions struct {
	MaxSize       int64
	MaxDepth      int
	MaxParts      int
	MaxHeaderLine int
	// MaxTextPartBytes caps the number of decoded bytes stored in Part.Text.
	// Parts larger than this are truncated and Part.TextTruncated is set.
	// The full decoded size is always recorded in Part.Size.
	// Defaults to DefaultMaxTextPartBytes when zero.
	MaxTextPartBytes int64
	StrictCharset    bool
	StrictBase64     bool
	StrictQP         bool
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
		MaxTextPartBytes: DefaultMaxTextPartBytes,
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
	if o.MaxTextPartBytes <= 0 {
		o.MaxTextPartBytes = DefaultMaxTextPartBytes
	}
}

// Parse consumes an RFC 5322 message from r and returns the decoded Message.
// Parse is pure CPU work; it does not take a context.
//
// The raw message is buffered in RAM (bounded by MaxSize) in order to walk the
// MIME tree and record raw part offsets. Non-text part bodies are NOT decoded
// into RAM; only their raw byte offset and decoded size are stored. Text parts
// are decoded into Part.Text up to MaxTextPartBytes. Call Part.OpenBody to
// stream any part's decoded content on demand.
func Parse(r io.Reader, opts ParseOptions) (Message, error) {
	opts.applyDefaults()

	raw, tooLarge, err := readCapped(r, opts.MaxSize)
	if err != nil {
		return Message{}, err
	}
	if tooLarge {
		tooLargeErr := &ParseError{
			Reason:    ReasonTooLarge,
			Message:   fmt.Sprintf("input exceeds MaxSize=%d bytes", opts.MaxSize),
			PartIndex: -1,
		}
		// raw holds MaxSize+1 bytes -- far more than any realistic header
		// block -- so the headers (and the Envelope built from them) can
		// still be recovered even though the body is too large to walk.
		// Callers that only check for a nil error today already discard
		// this Message; callers that inspect it via errors.Is(err,
		// ErrTooLarge) get a non-empty From/Subject/MessageID instead of
		// a fully zero-valued Message (re #244).
		if msg, herr := parseHeadersOnly(raw); herr == nil {
			return msg, tooLargeErr
		}
		return Message{}, tooLargeErr
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

	w := &mimeWalker{
		raw:  raw,
		opts: &opts,
	}
	return w.parseMessage()
}

// mimeWalker drives the recursive MIME parse over the raw bytes.
type mimeWalker struct {
	raw       []byte
	opts      *ParseOptions
	partCount int // total parts allocated so far
}

func (w *mimeWalker) nextPartIdx() (int, error) {
	idx := w.partCount
	w.partCount++
	if w.partCount > w.opts.MaxParts {
		return idx, &ParseError{
			Reason:    ReasonTooManyParts,
			Message:   fmt.Sprintf("part count exceeded MaxParts=%d", w.opts.MaxParts),
			PartIndex: idx,
		}
	}
	return idx, nil
}

func (w *mimeWalker) parseMessage() (Message, error) {
	// net/mail.ReadMessage parses top-level RFC 5322 headers. It is very
	// tolerant of malformed headers and missing MIME-Version.
	nmsg, err := mail.ReadMessage(bytes.NewReader(w.raw))
	if err != nil {
		return Message{}, &ParseError{
			Reason:    ReasonMalformed,
			Message:   "net/mail: " + err.Error(),
			PartIndex: -1,
		}
	}

	hdrs := mimeHeaderToHeaders(textproto.MIMEHeader(nmsg.Header))

	// Locate the body start offset within w.raw.
	bodyStart := findBodyStart(w.raw)

	var wd mime.WordDecoder
	rawSubject := hdrs.Get("Subject")
	subject, _ := wd.DecodeHeader(rawSubject)

	// Collect Authentication-Results values.
	arVals := hdrs.GetAll("Authentication-Results")
	authResultsRaw := strings.Join(arVals, ", ")

	msg := Message{
		Headers:        hdrs,
		Size:           int64(len(w.raw)),
		AuthResultsRaw: authResultsRaw,
	}

	// Parse top-level Content-Type.
	ct, ctParams, ctErr := parseContentType(hdrs.Get("Content-Type"))
	if ctErr != nil || ct == "" {
		// RFC 2045 §5.2: no Content-Type defaults to text/plain; charset=us-ascii.
		ct = "text/plain"
		ctParams = map[string]string{"charset": "us-ascii"}
	}

	bodyLen := int64(len(w.raw)) - bodyStart
	if bodyLen < 0 {
		bodyLen = 0
	}

	body, perr := w.walkPart(hdrs, ct, ctParams, bodyStart, bodyLen, 0)
	if perr != nil {
		return Message{}, perr
	}
	msg.Body = body
	msg.Envelope = buildEnvelopeFromHeaders(hdrs, subject, &wd)

	return msg, nil
}

// walkPart processes a single MIME part (either the message root or a part
// within a multipart container). rawBodyOff and rawBodyLen describe the raw
// (CTE-encoded) body bytes within w.raw. depth is the recursion depth for cap enforcement.
func (w *mimeWalker) walkPart(hdrs Headers, ct string, ctParams map[string]string, rawBodyOff, rawBodyLen int64, depth int) (Part, error) {
	if depth > w.opts.MaxDepth {
		return Part{}, &ParseError{
			Reason:    ReasonDepthExceeded,
			Message:   fmt.Sprintf("nesting depth exceeded MaxDepth=%d", w.opts.MaxDepth),
			PartIndex: w.partCount,
		}
	}
	idx, err := w.nextPartIdx()
	if err != nil {
		return Part{}, err
	}

	ctLower := strings.ToLower(ct)
	charset := strings.TrimSpace(ctParams["charset"])
	cteHeader := strings.TrimSpace(hdrs.Get("Content-Transfer-Encoding"))
	cteLower := strings.ToLower(cteHeader)

	dispHeader := hdrs.Get("Content-Disposition")
	dispType, dispParams, _ := parseContentType(dispHeader)

	p := Part{
		ContentType:             ct,
		Charset:                 charset,
		ContentTransferEncoding: cteHeader,
		Headers:                 hdrs,
		Disposition:             parseDisposition(dispType),
		Filename:                extractFilename(ctParams, dispParams),
		rawOffset:               rawBodyOff,
		rawLen:                  rawBodyLen,
	}

	// Multipart containers: recurse into children.
	if strings.HasPrefix(ctLower, "multipart/") {
		boundary := ctParams["boundary"]
		if boundary == "" {
			// No boundary parameter: treat as an opaque leaf.  Guard against a
			// negative rawBodyLen produced by malformed scanner output.
			if rawBodyLen < 0 || rawBodyOff < 0 || rawBodyOff+rawBodyLen > int64(len(w.raw)) {
				p.rawOffset = 0
				p.rawLen = 0
				p.Size = 0
				return p, nil
			}
			// The part may still carry a Content-Transfer-Encoding, so compute
			// the decoded size via the same streaming decoder as OpenBody.
			// Using rawBodyLen directly (the raw byte count) would diverge from
			// what OpenBody yields when a CTE is present, triggering the
			// checkOpenBody invariant (crasher corpus 17c18b4c3ad44374).
			rawBody := w.raw[rawBodyOff : rawBodyOff+rawBodyLen]
			size, decErrs, perr := w.countDecodedSize(rawBody, cteLower, idx)
			if perr != nil {
				return Part{}, perr
			}
			p.Size = size
			p.DecodeErrors = decErrs
			return p, nil
		}
		// Bounds check.
		if rawBodyOff < 0 || rawBodyLen < 0 || rawBodyOff+rawBodyLen > int64(len(w.raw)) {
			// Safety: treat as opaque leaf with no accessible body.
			p.rawOffset = 0
			p.rawLen = 0
			p.Size = 0
			return p, nil
		}
		bodySlice := w.raw[rawBodyOff : rawBodyOff+rawBodyLen]
		children, truncated, walkErr := w.walkMultipart(bodySlice, rawBodyOff, boundary, depth)
		if walkErr != nil {
			return Part{}, walkErr
		}
		if truncated && w.opts.StrictBoundary {
			return Part{}, &ParseError{
				Reason:    ReasonTruncated,
				Message:   fmt.Sprintf("multipart boundary %q has no closing marker", boundary),
				PartIndex: idx,
			}
		}
		p.Children = children
		// Containers have no direct raw body for OpenBody.
		p.rawOffset = 0
		p.rawLen = 0
		return p, nil
	}

	// Leaf part: decode for text parts, count size for non-text.
	if rawBodyOff < 0 || rawBodyLen < 0 || rawBodyOff+rawBodyLen > int64(len(w.raw)) {
		// Safety bounds failure: clear raw offsets so OpenBody cannot read
		// bytes that were not accounted for in p.Size.
		p.rawOffset = 0
		p.rawLen = 0
		return p, nil
	}
	rawBody := w.raw[rawBodyOff : rawBodyOff+rawBodyLen]

	if strings.HasPrefix(ctLower, "text/") {
		text, size, truncated, decErrs, parseErr := w.decodeTextPart(rawBody, cteLower, charset, ctLower, idx)
		if parseErr != nil {
			return Part{}, parseErr
		}
		p.Text = text
		p.TextTruncated = truncated
		p.Size = size
		p.DecodeErrors = decErrs
	} else {
		size, decErrs, parseErr := w.countDecodedSize(rawBody, cteLower, idx)
		if parseErr != nil {
			return Part{}, parseErr
		}
		p.Size = size
		p.DecodeErrors = decErrs
	}

	return p, nil
}

// walkMultipart walks the raw body of a multipart/* part using a custom boundary
// scanner to track exact byte offsets. bodySlice is the raw bytes of the
// multipart body (after the container's headers). bodyBaseOff is the absolute
// offset of bodySlice[0] within w.raw. Returns child parts, a truncation flag,
// and any fatal error.
func (w *mimeWalker) walkMultipart(bodySlice []byte, bodyBaseOff int64, boundary string, depth int) ([]Part, bool, error) {
	// Use our own boundary scanner instead of mime/multipart so we have exact
	// byte offsets for each part's body. mime/multipart strips the pre-boundary
	// CRLF from part bodies, making offset tracking via substring search
	// unreliable when parts share content.
	parts, hasClose := scanMultipartBoundaries(bodySlice, boundary)

	var children []Part
	for _, sp := range parts {
		// Parse the part headers using net/mail's textproto reader.
		partHdrs, hdrErr := parsePartHeaders(bodySlice[sp.hdrStart:sp.hdrEnd])
		if hdrErr != nil {
			// Malformed part headers: skip this part but continue.
			continue
		}

		partCT, partCTParams, partCTErr := parseContentType(partHdrs.Get("Content-Type"))
		if partCTErr != nil || partCT == "" {
			partCT = "text/plain"
			partCTParams = map[string]string{"charset": "us-ascii"}
		}

		absBodyOff := bodyBaseOff + sp.bodyStart
		bodyLen := sp.bodyEnd - sp.bodyStart

		child, perr := w.walkPart(partHdrs, partCT, partCTParams, absBodyOff, bodyLen, depth+1)
		if perr != nil {
			return nil, false, perr
		}
		children = append(children, child)
	}

	return children, !hasClose, nil
}

// scannedPart holds the byte ranges of one MIME part within a multipart body.
type scannedPart struct {
	hdrStart  int64 // offset of first header byte within bodySlice
	hdrEnd    int64 // offset of blank line terminator end within bodySlice
	bodyStart int64 // offset of first body byte (after header blank line)
	bodyEnd   int64 // offset of last body byte + 1 (before trailing CRLF before boundary)
}

// scanMultipartBoundaries splits bodySlice into MIME parts using the given boundary
// string. It returns the list of parts found and whether the close boundary
// (--boundary--) was present.
//
// Per RFC 2046 §5.1.1, each part is delimited by --boundary lines.
// The boundary line may optionally have a trailing LWSP before the CRLF.
// This scanner tolerates both CRLF and LF line endings.
func scanMultipartBoundaries(bodySlice []byte, boundary string) ([]scannedPart, bool) {
	delimiter := []byte("--" + boundary)
	var parts []scannedPart
	hasClose := false

	// Find all delimiter positions. A delimiter must appear at the start of a
	// line (i.e., preceded by \n or at position 0) and be followed by optional
	// LWSP and then CRLF or LF.
	pos := 0
	for pos < len(bodySlice) {
		// Find next occurrence of delimiter.
		idx := bytes.Index(bodySlice[pos:], delimiter)
		if idx < 0 {
			break
		}
		delimStart := pos + idx

		// Verify it's at the start of a line.
		if delimStart > 0 && bodySlice[delimStart-1] != '\n' {
			pos = delimStart + 1
			continue
		}

		// Find end of delimiter line.
		lineEnd := delimStart + len(delimiter)
		// Skip optional LWSP.
		for lineEnd < len(bodySlice) && (bodySlice[lineEnd] == ' ' || bodySlice[lineEnd] == '\t') {
			lineEnd++
		}
		// Check for close marker (--boundary--).
		isClose := false
		if lineEnd+2 <= len(bodySlice) && bodySlice[lineEnd] == '-' && bodySlice[lineEnd+1] == '-' {
			isClose = true
			lineEnd += 2
			// Skip optional LWSP after --.
			for lineEnd < len(bodySlice) && (bodySlice[lineEnd] == ' ' || bodySlice[lineEnd] == '\t') {
				lineEnd++
			}
		}
		// Skip CRLF or LF at end of delimiter line.
		if lineEnd < len(bodySlice) && bodySlice[lineEnd] == '\r' {
			lineEnd++
		}
		if lineEnd < len(bodySlice) && bodySlice[lineEnd] == '\n' {
			lineEnd++
		}

		if isClose {
			hasClose = true
			break
		}

		// This is a part-start delimiter. Part headers start at lineEnd.
		hdrStart := int64(lineEnd)

		// Find the end of the part headers (blank line: CRLFCRLF or LFLF).
		hdrEnd, bodyStart := findHdrEnd(bodySlice, lineEnd)
		if hdrEnd < 0 {
			// No header separator found; treat the rest as one big body.
			hdrEnd = len(bodySlice)
			bodyStart = hdrEnd
		}

		// Find start of next delimiter line (which ends this part's body).
		nextDelimPos := findNextBoundaryLine(bodySlice, bodyStart, delimiter)
		var bodyEnd int64
		if nextDelimPos < 0 {
			// No next boundary: body runs to end of bodySlice.
			bodyEnd = int64(len(bodySlice))
		} else {
			bodyEnd = int64(nextDelimPos)
			// Strip the trailing CRLF or LF that precedes the boundary.
			if bodyEnd > 0 && bodySlice[bodyEnd-1] == '\n' {
				bodyEnd--
			}
			if bodyEnd > 0 && bodySlice[bodyEnd-1] == '\r' {
				bodyEnd--
			}
		}

		parts = append(parts, scannedPart{
			hdrStart:  hdrStart,
			hdrEnd:    int64(hdrEnd),
			bodyStart: int64(bodyStart),
			bodyEnd:   bodyEnd,
		})

		// Advance scan position to just before the next delimiter.
		if nextDelimPos >= 0 {
			pos = nextDelimPos
		} else {
			break
		}
	}

	return parts, hasClose
}

// findHdrEnd finds the blank line (CRLFCRLF or LFLF) that separates part
// headers from the body, starting from startOff. Returns (hdrEnd, bodyStart)
// where hdrEnd is the index of the first byte of the blank-line separator and
// bodyStart is the first byte after it. Returns (-1, -1) if not found.
func findHdrEnd(b []byte, startOff int) (hdrEnd, bodyStart int) {
	sub := b[startOff:]
	if i := bytes.Index(sub, []byte("\r\n\r\n")); i >= 0 {
		return startOff + i, startOff + i + 4
	}
	if i := bytes.Index(sub, []byte("\n\n")); i >= 0 {
		return startOff + i, startOff + i + 2
	}
	return -1, -1
}

// findNextBoundaryLine finds the byte offset (within b) of the next delimiter
// line (--boundary) that starts at the beginning of a line, at or after fromOff.
// Returns -1 if not found.
func findNextBoundaryLine(b []byte, fromOff int, delimiter []byte) int {
	pos := fromOff
	for pos < len(b) {
		idx := bytes.Index(b[pos:], delimiter)
		if idx < 0 {
			return -1
		}
		delimPos := pos + idx
		// Must be at line start: preceded by \n or at pos 0.
		if delimPos == 0 || b[delimPos-1] == '\n' {
			return delimPos
		}
		pos = delimPos + 1
	}
	return -1
}

// parsePartHeaders parses the raw header bytes of a MIME part.
// hdrBytes should NOT include the trailing blank line; it's the raw header block.
func parsePartHeaders(hdrBytes []byte) (Headers, error) {
	// Append \r\n\r\n to make net/mail.ReadMessage happy.
	// net/mail requires a proper header+body input.
	full := make([]byte, 0, len(hdrBytes)+4)
	full = append(full, hdrBytes...)
	full = append(full, '\r', '\n', '\r', '\n')
	nmsg, err := mail.ReadMessage(bytes.NewReader(full))
	if err != nil {
		return NewHeaders(), err
	}
	return mimeHeaderToHeaders(textproto.MIMEHeader(nmsg.Header)), nil
}

// findBodyStart returns the byte offset of the first byte after the header/body
// separator (CRLFCRLF or LFLF) in raw. Returns int64(len(raw)) if no separator found.
func findBodyStart(raw []byte) int64 {
	if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 {
		return int64(i + 4)
	}
	if i := bytes.Index(raw, []byte("\n\n")); i >= 0 {
		return int64(i + 2)
	}
	return int64(len(raw))
}

// decodeTextPart CTE-decodes and charset-converts the raw encoded body of a text part.
// contentType is the MIME content type (e.g. "text/html") used for charset reconciliation.
// Returns text (capped), full decoded size, truncation flag, non-fatal warnings, and any fatal error.
func (w *mimeWalker) decodeTextPart(raw []byte, cteLower, charset, contentType string, idx int) (text string, size int64, truncated bool, decErrs []string, parseErr error) {
	cteDecoded, warn, parseErr := decodeCTEBytes(raw, cteLower, w.opts.StrictBase64, w.opts.StrictQP, idx)
	if parseErr != nil {
		return "", 0, false, nil, parseErr
	}
	if warn != "" {
		decErrs = append(decErrs, warn)
	}
	size = int64(len(cteDecoded))

	// For text/html parts: reconcile MIME-declared charset with the in-document
	// HTML meta charset. A common real-world pattern is a sender that misdeclares
	// charset=ISO-8859-1 in the MIME header while the document bytes are actually
	// valid UTF-8 and a <meta charset> inside the document correctly says UTF-8.
	// The Windows-1252 decoder (htmlindex's canonical ISO-8859-1) would then
	// produce mojibake that happens to be valid UTF-8 (e.g. 0xC3 0xBC -> "Ã¼"),
	// silently bypassing the isUTF8OrASCII guard below.
	//
	// When the MIME charset is a single-byte Latin encoding, the CTE-decoded bytes
	// are valid UTF-8, AND an HTML meta in the document declares UTF-8, we trust
	// the meta and skip the charset conversion — the bytes are used as-is.
	//
	// The check is conservative: if the bytes are NOT valid UTF-8 (a genuinely
	// ISO-8859-1 document with high-range bytes like 0xFC for ü) the condition
	// is false and the original conversion path runs unchanged.
	effectiveCharset := charset
	if contentType == "text/html" && isLatinSingleByteCharset(charset) && isUTF8OrASCII(string(cteDecoded)) {
		if metaCS := extractHTMLMetaCharset(cteDecoded); isUTF8Charset(metaCS) {
			effectiveCharset = "utf-8" // charsetDecoder returns nil for utf-8; conversion is skipped
		}
	}

	// Charset-convert to UTF-8.
	utf8Bytes, charsetErr := convertCharset(cteDecoded, effectiveCharset)
	if charsetErr != nil {
		if w.opts.StrictCharset {
			return "", 0, false, nil, &ParseError{
				Reason:    ReasonUnknownCharset,
				Message:   fmt.Sprintf("declared charset %q did not decode cleanly: %v", charset, charsetErr),
				PartIndex: idx,
			}
		}
		decErrs = append(decErrs, fmt.Sprintf("charset %q: %v", charset, charsetErr))
		utf8Bytes = cteDecoded // best effort: use raw bytes
	}

	utf8Text := string(utf8Bytes)

	// Validate UTF-8 when strictness is on.
	if w.opts.StrictCharset && charset != "" && !isUTF8OrASCII(utf8Text) {
		return "", 0, false, nil, &ParseError{
			Reason:    ReasonUnknownCharset,
			Message:   fmt.Sprintf("declared charset %q did not decode cleanly", charset),
			PartIndex: idx,
		}
	}

	// Cap to MaxTextPartBytes.
	if int64(len(utf8Text)) > w.opts.MaxTextPartBytes {
		cut := int(w.opts.MaxTextPartBytes)
		// Retreat to a UTF-8 rune boundary.
		for cut > 0 && (utf8Text[cut]&0xC0) == 0x80 {
			cut--
		}
		return utf8Text[:cut], size, true, decErrs, nil
	}
	return utf8Text, size, false, decErrs, nil
}

// countDecodedSize counts the decoded byte length of raw for a non-text leaf by
// streaming raw through the same decoder that OpenBody uses. This guarantees
// Part.Size == the number of bytes a caller reading OpenBody would obtain,
// which is the invariant checked by FuzzParse/checkOpenBody.
//
// The previous implementation called decodeCTEBytes (a batch decoder) whose
// lenient base64 path returned raw bytes on total failure while OpenBody's
// streaming base64.NewDecoder decoded the valid prefix — two divergent paths on
// malformed input that triggered the FuzzParse crasher.
//
// For identity CTEs the raw length is returned directly (no re-encoding).
// In strict mode the batch decoder is consulted first so malformed input still
// produces a fatal ParseError rather than a silent size mismatch.
func (w *mimeWalker) countDecodedSize(raw []byte, cteLower string, idx int) (size int64, decErrs []string, parseErr error) {
	switch cteLower {
	case "", "7bit", "8bit", "binary":
		return int64(len(raw)), nil, nil
	}

	// Strict-mode pre-check: propagate a ParseError for malformed encoding so
	// callers that set StrictBase64/StrictQP still get a fatal error.  For valid
	// input this path also runs, but the subsequent streaming count will agree
	// (both decoders are identical on well-formed data).
	if (cteLower == "base64" && w.opts.StrictBase64) || (cteLower == "quoted-printable" && w.opts.StrictQP) {
		_, warn, perr := decodeCTEBytes(raw, cteLower, w.opts.StrictBase64, w.opts.StrictQP, idx)
		if perr != nil {
			return 0, nil, perr
		}
		if warn != "" {
			decErrs = append(decErrs, warn)
		}
	}

	// Count bytes via the streaming OpenBody decoder so Part.Size is by
	// construction equal to what OpenBody yields.  openBodyDecoder with
	// isText=false never returns a non-nil error for any CTE, so the error
	// return is not checked here.
	r, _ := openBodyDecoder(bytes.NewReader(raw), cteLower, "", false)
	n, copyErr := io.Copy(io.Discard, r)
	r.Close()
	if copyErr != nil {
		// Non-fatal: record the decode warning; Size is the bytes decoded so far,
		// exactly matching what a caller of OpenBody + io.Copy would see before
		// the error causes them to stop reading.
		decErrs = append(decErrs, fmt.Sprintf("decode error (partial read): %v", copyErr))
	}
	return n, decErrs, nil
}

// decodeCTEBytes decodes b according to its Content-Transfer-Encoding.
// For base64 and quoted-printable it returns the decoded bytes; for identity
// encodings it returns b unmodified. strict* controls whether errors are fatal.
func decodeCTEBytes(b []byte, cteLower string, strictBase64, strictQP bool, partIdx int) (decoded []byte, warn string, parseErr error) {
	switch cteLower {
	case "base64":
		stripped := stripBase64Whitespace(b)
		decoded, err := base64.StdEncoding.DecodeString(string(stripped))
		if err != nil {
			if strictBase64 {
				return nil, "", &ParseError{
					Reason:    ReasonMalformedBase64,
					Message:   err.Error(),
					PartIndex: partIdx,
				}
			}
			decoded, warn = lenientBase64Decode(stripped)
		}
		return decoded, warn, nil

	case "quoted-printable":
		decoded, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(b)))
		if err != nil {
			if strictQP {
				return nil, "", &ParseError{
					Reason:    ReasonMalformedQP,
					Message:   err.Error(),
					PartIndex: partIdx,
				}
			}
			warn = fmt.Sprintf("quoted-printable decode error: %v", err)
			decoded = b // fallback to raw
		}
		return decoded, warn, nil

	default:
		return b, "", nil
	}
}

// stripBase64Whitespace removes ASCII whitespace (CR LF SP HT) from base64 input.
func stripBase64Whitespace(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for _, c := range b {
		if c != ' ' && c != '\t' && c != '\r' && c != '\n' {
			out = append(out, c)
		}
	}
	return out
}

// lenientBase64Decode attempts to salvage a malformed base64 payload by stripping
// all non-alphabet characters and re-decoding. Returns best-effort decoded bytes
// and a warning message.
func lenientBase64Decode(b []byte) ([]byte, string) {
	const alpha = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/="
	cleaned := make([]byte, 0, len(b))
	for _, c := range b {
		if strings.IndexByte(alpha, c) >= 0 {
			cleaned = append(cleaned, c)
		}
	}
	for len(cleaned)%4 != 0 {
		cleaned = append(cleaned, '=')
	}
	decoded, err := base64.StdEncoding.DecodeString(string(cleaned))
	if err != nil {
		return b, fmt.Sprintf("base64 lenient decode failed: %v", err)
	}
	return decoded, "base64: invalid characters stripped in lenient mode"
}

// convertCharset converts src from the named charset to UTF-8.
// If charset is empty, "utf-8", "utf8", "us-ascii", "ascii", or "7bit", src
// is returned unchanged (it is already ASCII/UTF-8). Returns an error if the
// charset is unknown or conversion fails.
func convertCharset(src []byte, charset string) ([]byte, error) {
	dec := charsetDecoder(charset)
	if dec == nil {
		return src, nil
	}
	decoded, _, err := transform.Bytes(dec, src)
	if err != nil {
		return nil, fmt.Errorf("charset decode %q: %w", charset, err)
	}
	return decoded, nil
}

// parseContentType parses a Content-Type or Content-Disposition header value.
// Returns empty strings + nil map on error or empty input.
func parseContentType(v string) (mediaType string, params map[string]string, err error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil, fmt.Errorf("empty")
	}
	mt, ps, err := mime.ParseMediaType(v)
	if err != nil {
		return "", nil, err
	}
	return strings.ToLower(mt), ps, nil
}

// parseDisposition maps a raw Content-Disposition media-type string to our enum.
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

// extractFilename returns the filename for a part by checking the
// Content-Disposition filename= parameter first, then Content-Type name=.
func extractFilename(ctParams, dispParams map[string]string) string {
	if fn := dispParams["filename"]; fn != "" {
		return decodeHeaderWord(fn)
	}
	if fn := ctParams["name"]; fn != "" {
		return decodeHeaderWord(fn)
	}
	return ""
}

// decodeHeaderWord applies RFC 2047 encoded-word decoding to a MIME parameter value.
func decodeHeaderWord(v string) string {
	var wd mime.WordDecoder
	decoded, err := wd.DecodeHeader(v)
	if err != nil {
		return v
	}
	return decoded
}

// mimeHeaderToHeaders converts a textproto.MIMEHeader to our Headers type.
func mimeHeaderToHeaders(src textproto.MIMEHeader) Headers {
	h := NewHeaders()
	for k, vs := range src {
		for _, v := range vs {
			h.add(k, v)
		}
	}
	return h
}

// buildEnvelopeFromHeaders constructs an Envelope from parsed message headers.
func buildEnvelopeFromHeaders(h Headers, decodedSubject string, wd *mime.WordDecoder) Envelope {
	out := Envelope{
		Subject:    decodedSubject,
		MessageID:  strings.TrimSpace(h.Get("Message-ID")),
		Date:       h.Get("Date"),
		InReplyTo:  splitMessageIDs(decodeHeaderWord(h.Get("In-Reply-To"))),
		References: splitMessageIDs(decodeHeaderWord(h.Get("References"))),
	}

	parseAddrField := func(raw string) []mail.Address {
		decoded, _ := wd.DecodeHeader(raw)
		if addrs, err := mail.ParseAddressList(decoded); err == nil {
			return derefAddrs(addrs)
		}
		// Lenient fallback: try raw.
		return parseAddrsLenient(raw)
	}

	out.From = parseAddrField(h.Get("From"))
	out.To = parseAddrField(h.Get("To"))
	out.Cc = parseAddrField(h.Get("Cc"))
	out.Bcc = parseAddrField(h.Get("Bcc"))
	out.ReplyTo = parseAddrField(h.Get("Reply-To"))

	if raw := h.Get("Sender"); raw != "" {
		decoded, _ := wd.DecodeHeader(raw)
		if addrs, err := mail.ParseAddressList(decoded); err == nil && len(addrs) > 0 {
			a := *addrs[0]
			out.Sender = &a
		}
	}

	return out
}

func derefAddrs(in []*mail.Address) []mail.Address {
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

// parseAddrsLenient tolerates malformed address headers.
func parseAddrsLenient(s string) []mail.Address {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	addrs, err := mail.ParseAddressList(s)
	if err != nil {
		return nil
	}
	return derefAddrs(addrs)
}

// splitMessageIDs extracts angle-bracketed Message-IDs from a References / In-Reply-To value.
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

// readCapped reads at most limit+1 bytes so we can distinguish exactly-at-limit
// from over. When the input exceeds limit, tooLarge is true and buf still
// holds the limit+1 bytes read (the caller uses them to recover the header
// block rather than discarding the input outright).
func readCapped(r io.Reader, limit int64) (buf []byte, tooLarge bool, err error) {
	if limit <= 0 {
		limit = DefaultMaxSize
	}
	lr := io.LimitReader(r, limit+1)
	buf, err = io.ReadAll(lr)
	if err != nil {
		return nil, false, &ParseError{Reason: ReasonReaderError, Message: "read input", PartIndex: -1, Cause: err}
	}
	if int64(len(buf)) > limit {
		return buf, true, nil
	}
	return buf, false, nil
}

// parseHeadersOnly extracts the RFC 5322 headers and the Envelope built
// from them, without walking the MIME body tree. Used by Parse when the
// full message exceeds ParseOptions.MaxSize: the header block is always
// far smaller than the cap, so From/Subject/Message-ID survive even
// though the body itself cannot be parsed. The returned Message's Body is
// the zero Part and its Size is not set -- callers must key off the
// caller-visible ErrTooLarge, not Body/Size, to detect the degraded case.
func parseHeadersOnly(raw []byte) (Message, error) {
	nmsg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return Message{}, err
	}
	hdrs := mimeHeaderToHeaders(textproto.MIMEHeader(nmsg.Header))

	var wd mime.WordDecoder
	rawSubject := hdrs.Get("Subject")
	subject, _ := wd.DecodeHeader(rawSubject)

	arVals := hdrs.GetAll("Authentication-Results")

	return Message{
		Headers:        hdrs,
		AuthResultsRaw: strings.Join(arVals, ", "),
		Envelope:       buildEnvelopeFromHeaders(hdrs, subject, &wd),
	}, nil
}

// findOverlongHeaderLine scans the header section and returns the 1-based line
// number of the first line exceeding max bytes (CRLF excluded), or 0.
func findOverlongHeaderLine(raw []byte, max int) int {
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

// headerEnd returns the byte offset of the blank-line separator that ends the
// header block, or -1 when absent.
func headerEnd(raw []byte) int {
	if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 {
		return i
	}
	if i := bytes.Index(raw, []byte("\n\n")); i >= 0 {
		return i
	}
	return -1
}

// isUTF8OrASCII reports whether s is valid UTF-8 (fast path using range).
func isUTF8OrASCII(s string) bool {
	for _, r := range s {
		if r == '�' {
			return !hasRawInvalidUTF8(s)
		}
	}
	return true
}

// hasRawInvalidUTF8 performs strict byte-level UTF-8 validation.
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
