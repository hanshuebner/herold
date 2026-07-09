package extimg

// InternalizeReader is the Phase 2c streaming rewrite (REQ-STORE-17/19).
// It avoids the enmime ReadEnvelope + Builder round-trip that materialises
// every MIME part (including large attachments) in RAM.
//
// Design:
//   - src is an io.ReaderAt over the assembled message bytes passed to
//     mailparse.Parse.
//   - For the text/html leaf: Part.Text (already in RAM, bounded by 1 MiB)
//     is rewritten via rewriteHTML / RewriteForPlaceholder, QP-encoded.
//   - For every other leaf: raw CTE-encoded bytes are streamed verbatim via
//     Part.RawBody, never decoded or re-encoded.
//   - Multipart containers are reconstructed from the parsed Part tree using
//     original boundary strings from the Content-Type header.
//   - New fetched images are appended as inline parts.
//
// MIME reconstruction contract:
//
//	buildPartContent returns (partHeaders []string, bodyReaders []io.Reader, err)
//	where partHeaders are the MIME part header lines (each ending with \r\n)
//	and bodyReaders stream the body content. Both callers use this differently:
//
//	  buildMsgReaders (top-level): emits all message headers from msg.Headers,
//	  plus MIME-Version, Content-Type and CTE from partHeaders, then \r\n,
//	  then bodyReaders.
//
//	  buildMultipartBodyReaders (child): emits --boundary\r\n, then partHeaders,
//	  then \r\n (blank line between part headers and part body), then bodyReaders.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/mailparse"
)

// InternalizeReader rewrites the message referenced by src using the
// pre-parsed mailparse.Message for structure. Returns a streaming io.Reader
// of the rebuilt message, the audit summary, and any fatal error.
func InternalizeReader(
	ctx context.Context,
	src io.ReaderAt,
	srcSize int64,
	msg mailparse.Message,
	cfg Config,
	verdict DKIMVerdict,
) (io.Reader, AuditSummary, error) {
	cfg.resolveOptional()
	if err := cfg.validate(); err != nil {
		return io.NewSectionReader(src, 0, srcSize), AuditSummary{
			Mode: cfg.Mode, OriginalSize: int(srcSize),
			RewrittenSize: int(srcSize), NotEligibleReason: err.Error(),
		}, nil
	}
	start := time.Now()
	sum := AuditSummary{
		Mode:          cfg.Mode,
		OriginalSize:  int(srcSize),
		RewrittenSize: int(srcSize),
		FailureCounts: map[FetchOutcome]int{},
	}

	if cfg.Mode == ModePassthrough {
		sum.NotEligibleReason = "mode=passthrough"
		sum.WallClock = time.Since(start)
		return io.NewSectionReader(src, 0, srcSize), sum, nil
	}

	htmlPart, found := findHTMLLeaf(msg.Body)
	if !found || htmlPart.Text == "" {
		sum.NotEligibleReason = "no_html_body"
		sum.WallClock = time.Since(start)
		return io.NewSectionReader(src, 0, srcSize), sum, nil
	}
	sum.HTMLPartsScanned = 1

	htmlBytes := []byte(htmlPart.Text)
	cands, cerr := extractCandidates(htmlBytes)
	if cerr != nil {
		sum.ParseError = cerr.Error()
		sum.WallClock = time.Since(start)
		return io.NewSectionReader(src, 0, srcSize), sum, nil
	}
	sum.Candidates = len(cands)
	if len(cands) == 0 {
		sum.NotEligibleReason = "no_external_refs"
		sum.WallClock = time.Since(start)
		return io.NewSectionReader(src, 0, srcSize), sum, nil
	}

	fetchCtx, cancel := context.WithTimeout(ctx, cfg.PerMessageTimeout)
	defer cancel()

	fetcher := NewFetcher(cfg)
	results := fetchAllBudgeted(fetchCtx, fetcher, cfg, cands, &sum)

	cidMap := make(map[string]string, len(results))
	var inlines []inlinePart
	for _, r := range results {
		if r.Outcome != FetchOK {
			sum.Failed++
			sum.FailureCounts[r.Outcome]++
			continue
		}
		cid := newContentID()
		cidMap[r.URL] = cid
		inlines = append(inlines, inlinePart{cid: cid, ct: r.ContentType, body: r.Bytes})
		sum.Internalized++
	}
	if len(inlines) == 0 {
		sum.NotEligibleReason = "no_successful_fetches"
	}

	rewrittenHTML, err := rewriteHTML(htmlBytes, cidMap)
	if err != nil {
		sum.ParseError = err.Error()
		sum.WallClock = time.Since(start)
		return io.NewSectionReader(src, 0, srcSize), sum, nil
	}

	if sum.Failed > 0 {
		// See the matching comment in internalize.go's Internalize:
		// retain the failed URLs + pre-placeholder HTML server-side
		// (issue #162) before the placeholder pass below discards the
		// only copy of that association.
		if failedCands, ferr := extractCandidates(rewrittenHTML); ferr == nil {
			urls := make([]string, len(failedCands))
			for i, c := range failedCands {
				urls[i] = c.URL
			}
			sum.FailedURLs = urls
			sum.FailedImageTemplate = append([]byte(nil), rewrittenHTML...)
		}
		if placeheld, perr := RewriteForPlaceholder(rewrittenHTML); perr == nil {
			rewrittenHTML = placeheld
			sum.Placeholdered = sum.Failed
		}
	}

	outReaders, err := buildMsgReaders(src, msg, rewrittenHTML, inlines, cfg, verdict)
	if err != nil {
		sum.ParseError = err.Error()
		sum.WallClock = time.Since(start)
		return io.NewSectionReader(src, 0, srcSize), sum, nil
	}

	sum.Modified = true
	sum.RewrittenSize = 0 // caller updates from blob size after Blobs().Put
	sum.WallClock = time.Since(start)
	return io.MultiReader(outReaders...), sum, nil
}

// findHTMLLeaf returns the first text/html leaf with non-empty decoded Text.
func findHTMLLeaf(p mailparse.Part) (mailparse.Part, bool) {
	if len(p.Children) == 0 {
		if strings.EqualFold(p.ContentType, "text/html") && p.Text != "" {
			return p, true
		}
		return mailparse.Part{}, false
	}
	for _, c := range p.Children {
		if part, ok := findHTMLLeaf(c); ok {
			return part, ok
		}
	}
	return mailparse.Part{}, false
}

// buildMsgReaders assembles the complete rewritten message as a slice of
// io.Readers. The message structure is:
//
//	[non-structural message headers]
//	MIME-Version: 1.0\r\n
//	Content-Type: <topCT>\r\n
//	[Content-Transfer-Encoding: <topCTE>\r\n — if non-empty]
//	Authentication-Results: ...\r\n
//	X-Herold-Body-Modified: ...\r\n
//	\r\n
//	[body readers]
func buildMsgReaders(
	src io.ReaderAt,
	msg mailparse.Message,
	rewrittenHTML []byte,
	inlines []inlinePart,
	cfg Config,
	verdict DKIMVerdict,
) ([]io.Reader, error) {
	// Build body first to learn topCT and topCTE.
	bodyReaders, topCT, topCTE, err := buildPartContent(src, msg.Body, rewrittenHTML, inlines)
	if err != nil {
		return nil, err
	}

	var hdrBuf bytes.Buffer

	// Emit non-structural, non-dropped top-level message headers.
	for _, key := range msg.Headers.Keys() {
		if isMsgDroppedHeader(key, cfg) {
			continue
		}
		for _, v := range msg.Headers.GetAll(key) {
			fmt.Fprintf(&hdrBuf, "%s: %s\r\n", key, v)
		}
	}

	hdrBuf.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&hdrBuf, "Content-Type: %s\r\n", topCT)
	if topCTE != "" {
		fmt.Fprintf(&hdrBuf, "Content-Transfer-Encoding: %s\r\n", topCTE)
	}

	host := cfg.HostHeader
	if host == "" {
		host = "herold"
	}
	fmt.Fprintf(&hdrBuf, "Authentication-Results: %s\r\n", buildAuthResults(host, verdict))
	hdrBuf.WriteString("X-Herold-Body-Modified: image-internalization\r\n")
	hdrBuf.WriteString("\r\n") // header/body separator

	var out []io.Reader
	out = append(out, &hdrBuf)
	out = append(out, bodyReaders...)
	return out, nil
}

// isMsgDroppedHeader returns true for top-level message headers that the
// streaming rebuilder replaces or omits.
func isMsgDroppedHeader(name string, cfg Config) bool {
	switch strings.ToLower(name) {
	case "mime-version", "content-type", "content-transfer-encoding":
		// Re-emitted by buildMsgReaders from topCT/topCTE.
		return true
	case "authentication-results":
		// Re-stamped by buildMsgReaders.
		return true
	case "dkim-signature":
		return cfg.DKIM != DKIMKeep
	}
	return false
}

// buildPartContent returns (bodyReaders, contentType, contentTransferEncoding, error)
// for a MIME Part. This is the TOP-LEVEL variant: the caller emits Content-Type
// and CTE as message headers, and bodyReaders contains only the raw body bytes
// (no headers, no blank line — those come from the caller).
//
// For multipart parts, bodyReaders is the multipart body (starting with
// --boundary). No CTE is needed (multipart parts have no CTE).
//
// For text/html parts, bodyReaders is the QP-encoded HTML. CTE = "quoted-printable".
//
// For other leaf parts, bodyReaders streams raw body via RawBody. CTE = original.
func buildPartContent(
	src io.ReaderAt,
	p mailparse.Part,
	rewrittenHTML []byte,
	inlines []inlinePart,
) (bodyReaders []io.Reader, contentType string, contentTransferEncoding string, err error) {
	ct := strings.ToLower(strings.TrimSpace(p.ContentType))

	switch {
	case ct == "text/html" && len(p.Children) == 0:
		charset := p.Charset
		if charset == "" {
			charset = "utf-8"
		}
		if len(inlines) > 0 {
			// Wrap in multipart/related sub-container. No top-level CTE needed.
			boundary := sMakeBoundary()
			contentType = fmt.Sprintf("multipart/related; boundary=%q", boundary)
			bodyReaders, err = wrapHTMLInRelated(rewrittenHTML, inlines, charset, boundary)
			if err != nil {
				return nil, "", "", err
			}
			contentTransferEncoding = ""
		} else {
			contentType = fmt.Sprintf("text/html; charset=%q", charset)
			contentTransferEncoding = "quoted-printable"
			bodyReaders, err = encodeHTMLBody(rewrittenHTML)
			if err != nil {
				return nil, "", "", err
			}
		}
		return bodyReaders, contentType, contentTransferEncoding, nil

	case strings.HasPrefix(ct, "multipart/"):
		readers, mct, merr := buildMultipartBody(src, p, rewrittenHTML, inlines)
		if merr != nil {
			return nil, "", "", merr
		}
		return readers, mct, "", nil

	default:
		// Raw leaf. For top-level this is unusual (message/rfc822 or similar).
		rawReader, rerr := p.RawBody(src)
		if rerr != nil {
			return nil, "", "", fmt.Errorf("extimg: top-level RawBody: %w", rerr)
		}
		rawCT := p.Headers.Get("Content-Type")
		if rawCT == "" {
			rawCT = p.ContentType
		}
		if rawCT == "" {
			rawCT = "application/octet-stream"
		}
		return []io.Reader{rawReader}, rawCT, p.ContentTransferEncoding, nil
	}
}

// encodeHTMLBody QP-encodes rewrittenHTML and returns [QP body reader].
func encodeHTMLBody(rewrittenHTML []byte) ([]io.Reader, error) {
	var qpBuf bytes.Buffer
	qpw := quotedprintable.NewWriter(&qpBuf)
	if _, werr := qpw.Write(rewrittenHTML); werr != nil {
		return nil, fmt.Errorf("extimg: qp write: %w", werr)
	}
	if cerr := qpw.Close(); cerr != nil {
		return nil, fmt.Errorf("extimg: qp close: %w", cerr)
	}
	return []io.Reader{&qpBuf}, nil
}

// wrapHTMLInRelated builds a multipart/related body containing the HTML
// plus the inline image parts. Returns the body readers (starting with
// --boundary, no outer Content-Type header) and the Content-Type string.
func wrapHTMLInRelated(rewrittenHTML []byte, inlines []inlinePart, charset, boundary string) ([]io.Reader, error) {
	var qpBuf bytes.Buffer
	qpw := quotedprintable.NewWriter(&qpBuf)
	if _, werr := qpw.Write(rewrittenHTML); werr != nil {
		return nil, fmt.Errorf("extimg: qp write: %w", werr)
	}
	if cerr := qpw.Close(); cerr != nil {
		return nil, fmt.Errorf("extimg: qp close: %w", cerr)
	}

	var buf bytes.Buffer
	// First child: HTML.
	fmt.Fprintf(&buf, "--%s\r\n", boundary)
	fmt.Fprintf(&buf, "Content-Type: text/html; charset=%q\r\n", charset)
	buf.WriteString("Content-Transfer-Encoding: quoted-printable\r\n")
	buf.WriteString("\r\n")
	buf.Write(qpBuf.Bytes())
	buf.WriteString("\r\n")
	// Inline image parts.
	appendInlineParts(&buf, inlines, boundary)
	// Closing boundary.
	fmt.Fprintf(&buf, "--%s--\r\n", boundary)
	return []io.Reader{&buf}, nil
}

// buildMultipartBody builds the body content of a multipart/* part.
// Returns (bodyReaders, contentType, error). bodyReaders starts with the
// first boundary line. The caller emits any wrapping headers.
func buildMultipartBody(
	src io.ReaderAt,
	p mailparse.Part,
	rewrittenHTML []byte,
	inlines []inlinePart,
) ([]io.Reader, string, error) {
	rawCT := p.Headers.Get("Content-Type")
	boundary := extractMIMEBoundary(rawCT)
	if boundary == "" {
		// Degenerate: fall back to treating as leaf (no children emittable).
		rawReader, rerr := p.RawBody(src)
		if rerr != nil {
			return nil, "", fmt.Errorf("extimg: multipart RawBody fallback: %w", rerr)
		}
		return []io.Reader{rawReader}, p.ContentType, nil
	}

	ct := strings.ToLower(strings.TrimSpace(p.ContentType))
	isRelated := ct == "multipart/related"

	var out []io.Reader

	for _, child := range p.Children {
		childCT := strings.ToLower(strings.TrimSpace(child.ContentType))
		isHTMLChild := childCT == "text/html" && len(child.Children) == 0

		// Build the child part as a self-contained block (all headers + blank
		// line + body). Then prepend --boundary\r\n.
		childBlock, berr := buildChildPartBlock(src, child, rewrittenHTML, inlines, isHTMLChild)
		if berr != nil {
			return nil, "", berr
		}

		var boundaryBuf bytes.Buffer
		fmt.Fprintf(&boundaryBuf, "--%s\r\n", boundary)
		out = append(out, &boundaryBuf)
		out = append(out, childBlock...)

		// Trailing CRLF before the next boundary (RFC 2046 §5.1.1).
		var sepBuf bytes.Buffer
		sepBuf.WriteString("\r\n")
		out = append(out, &sepBuf)
	}

	// For multipart/related: append newly-fetched inline images.
	if isRelated {
		var inBuf bytes.Buffer
		appendInlineParts(&inBuf, inlines, boundary)
		out = append(out, &inBuf)
	}

	var closeBuf bytes.Buffer
	fmt.Fprintf(&closeBuf, "--%s--\r\n", boundary)
	out = append(out, &closeBuf)

	outCT := rebuildMultipartCT(p, boundary)
	return out, outCT, nil
}

// buildChildPartBlock builds a self-contained MIME part block:
// [part headers (all headers, including Content-Type)] + [\r\n] + [body readers].
// This is for children of a multipart/* container; the caller prepends --boundary\r\n.
func buildChildPartBlock(
	src io.ReaderAt,
	p mailparse.Part,
	rewrittenHTML []byte,
	inlines []inlinePart,
	isHTMLChild bool,
) ([]io.Reader, error) {
	if isHTMLChild {
		return buildHTMLChildBlock(p, rewrittenHTML, inlines)
	}
	if len(p.Children) > 0 {
		return buildMultipartChildBlock(src, p, rewrittenHTML, inlines)
	}
	return buildRawChildBlock(src, p)
}

// buildHTMLChildBlock builds a self-contained MIME part block for a text/html
// leaf. If inlines are present, wraps in a multipart/related sub-container.
// Returns (hdrBlk + blankLine + body readers).
func buildHTMLChildBlock(p mailparse.Part, rewrittenHTML []byte, inlines []inlinePart) ([]io.Reader, error) {
	charset := p.Charset
	if charset == "" {
		charset = "utf-8"
	}

	if len(inlines) == 0 {
		// Standalone HTML: emit CT + CTE + blank + QP body.
		var qpBuf bytes.Buffer
		qpw := quotedprintable.NewWriter(&qpBuf)
		if _, werr := qpw.Write(rewrittenHTML); werr != nil {
			return nil, fmt.Errorf("extimg: qp write: %w", werr)
		}
		if cerr := qpw.Close(); cerr != nil {
			return nil, fmt.Errorf("extimg: qp close: %w", cerr)
		}
		var hdrBuf bytes.Buffer
		fmt.Fprintf(&hdrBuf, "Content-Type: text/html; charset=%q\r\n", charset)
		hdrBuf.WriteString("Content-Transfer-Encoding: quoted-printable\r\n")
		hdrBuf.WriteString("\r\n")
		return []io.Reader{&hdrBuf, &qpBuf}, nil
	}

	// Wrap in multipart/related sub-container.
	boundary := sMakeBoundary()
	var buf bytes.Buffer
	// Part headers for the multipart/related sub-container.
	fmt.Fprintf(&buf, "Content-Type: multipart/related; boundary=%q\r\n", boundary)
	buf.WriteString("\r\n")
	// First child of the sub-container: HTML.
	fmt.Fprintf(&buf, "--%s\r\n", boundary)
	// QP-encode HTML.
	var qpBuf bytes.Buffer
	qpw := quotedprintable.NewWriter(&qpBuf)
	if _, werr := qpw.Write(rewrittenHTML); werr != nil {
		return nil, fmt.Errorf("extimg: qp write: %w", werr)
	}
	if cerr := qpw.Close(); cerr != nil {
		return nil, fmt.Errorf("extimg: qp close: %w", cerr)
	}
	fmt.Fprintf(&buf, "Content-Type: text/html; charset=%q\r\n", charset)
	buf.WriteString("Content-Transfer-Encoding: quoted-printable\r\n")
	buf.WriteString("\r\n")
	buf.Write(qpBuf.Bytes())
	buf.WriteString("\r\n")
	// Inline images.
	appendInlineParts(&buf, inlines, boundary)
	fmt.Fprintf(&buf, "--%s--\r\n", boundary)
	return []io.Reader{&buf}, nil
}

// buildMultipartChildBlock builds a self-contained MIME part block for a
// multipart/* child. Returns (CT-header + blank-line + body readers).
func buildMultipartChildBlock(
	src io.ReaderAt,
	p mailparse.Part,
	rewrittenHTML []byte,
	inlines []inlinePart,
) ([]io.Reader, error) {
	bodyReaders, ct, berr := buildMultipartBody(src, p, rewrittenHTML, inlines)
	if berr != nil {
		return nil, berr
	}
	var hdrBuf bytes.Buffer
	fmt.Fprintf(&hdrBuf, "Content-Type: %s\r\n", ct)
	hdrBuf.WriteString("\r\n")
	result := []io.Reader{&hdrBuf}
	result = append(result, bodyReaders...)
	return result, nil
}

// buildRawChildBlock builds a self-contained MIME part block for a non-HTML
// leaf child. All headers from p.Headers are preserved verbatim (including
// Content-Type with full parameters). Returns (all-headers + blank-line +
// raw body via SectionReader).
func buildRawChildBlock(src io.ReaderAt, p mailparse.Part) ([]io.Reader, error) {
	var hdrBuf bytes.Buffer
	for _, key := range p.Headers.Keys() {
		for _, v := range p.Headers.GetAll(key) {
			fmt.Fprintf(&hdrBuf, "%s: %s\r\n", key, v)
		}
	}
	hdrBuf.WriteString("\r\n")

	rawReader, rerr := p.RawBody(src)
	if rerr != nil {
		return nil, fmt.Errorf("extimg: RawBody: %w", rerr)
	}
	return []io.Reader{&hdrBuf, rawReader}, nil
}

// appendInlineParts writes MIME parts for newly-fetched inline images into w.
func appendInlineParts(w *bytes.Buffer, inlines []inlinePart, boundary string) {
	for _, in := range inlines {
		fmt.Fprintf(w, "--%s\r\n", boundary)
		fmt.Fprintf(w, "Content-Type: %s\r\n", in.ct)
		w.WriteString("Content-Transfer-Encoding: base64\r\n")
		fmt.Fprintf(w, "Content-ID: <%s>\r\n", in.cid)
		w.WriteString("Content-Disposition: inline\r\n")
		w.WriteString("\r\n")
		enc := base64.StdEncoding.EncodeToString(in.body)
		for len(enc) > 76 {
			w.WriteString(enc[:76])
			w.WriteString("\r\n")
			enc = enc[76:]
		}
		if len(enc) > 0 {
			w.WriteString(enc)
			w.WriteString("\r\n")
		}
		w.WriteString("\r\n")
	}
}

// extractMIMEBoundary extracts the boundary parameter from a Content-Type value.
func extractMIMEBoundary(rawCT string) string {
	_, params, err := mime.ParseMediaType(rawCT)
	if err != nil {
		return ""
	}
	return params["boundary"]
}

// rebuildMultipartCT builds a Content-Type value for a multipart container,
// preserving the original type and boundary.
func rebuildMultipartCT(p mailparse.Part, boundary string) string {
	base := p.ContentType
	if base == "" {
		base = "multipart/mixed"
	}
	if sc := strings.IndexByte(base, ';'); sc >= 0 {
		base = strings.TrimSpace(base[:sc])
	}
	return fmt.Sprintf("%s; boundary=%q", base, boundary)
}

// sMakeBoundary generates a unique MIME boundary for new multipart/related
// wrappers created around HTML + inline images.
func sMakeBoundary() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "herold_" + hex.EncodeToString(b[:])
}
