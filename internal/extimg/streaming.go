package extimg

// InternalizeReader is the Phase 2c streaming rewrite (REQ-STORE-17/19).
// It avoids the enmime ReadEnvelope + Builder round-trip that materialises
// every MIME part (including large attachments) in RAM.
//
// Design:
//   - src is an io.ReaderAt over the assembled message bytes passed to
//     mailparse.Parse.
//   - Every text/html leaf in the message (not just the first) is scanned
//     for external image references and rewritten via rewriteHTML /
//     RewriteForPlaceholder, QP-encoded, using its own Part.Text (already
//     in RAM, bounded by 1 MiB).
//   - For every other leaf: raw CTE-encoded bytes are streamed verbatim via
//     Part.RawBody, never decoded or re-encoded.
//   - Multipart containers are reconstructed from the parsed Part tree using
//     original boundary strings from the Content-Type header.
//   - New fetched images are appended as inline parts exactly once each
//     (issue #324): at the lowest multipart/related container that
//     encloses every HTML leaf referencing that image, or -- when no
//     such container exists -- at a synthetic multipart/related wrapped
//     around the lowest common ancestor of those leaves. Every
//     referencing HTML leaf's src is rewritten to the same cid:.
//
// MIME reconstruction contract:
//
//	buildPartContent / buildMultipartBody return (bodyReaders, contentType,
//	contentTransferEncoding, err). Both callers use this differently:
//
//	  buildMsgReaders (top-level): emits all message headers from msg.Headers,
//	  plus MIME-Version, Content-Type and CTE from partHeaders, then \r\n,
//	  then bodyReaders. If the message root itself is the placement point
//	  for an image group, the whole body is wrapped in a synthetic
//	  multipart/related first.
//
//	  buildChildPartBlock (child): returns a self-contained block (its own
//	  Content-Type/CTE header lines + blank line + body); the caller
//	  prepends --boundary\r\n. If the child's own path is the placement
//	  point for an image group not already consumed by an enclosing
//	  multipart/related, the block is wrapped in a synthetic
//	  multipart/related before being returned.
//
// Placement bookkeeping: `groups` maps a tree path (see pathKey) to the
// inline image parts to emit there. buildMultipartBody consumes (deletes)
// its own path's entry directly when the container is multipart/related.
// Every other node consults `groups` for its own path after building its
// normal content and wraps if an entry remains -- so each entry is
// consumed exactly once, guaranteeing each image is emitted exactly once.

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

// htmlLeafRef is one text/html leaf found in the parsed Part tree, along
// with its path (the sequence of child indices from the message root).
type htmlLeafRef struct {
	Path []int
	Part mailparse.Part
}

// collectHTMLLeaves walks p depth-first, left-to-right, and returns every
// text/html leaf (Children == 0) with non-empty decoded Text, in document
// order. This traversal order must match the rebuild walk in
// buildMultipartBody/buildPartContent exactly, since InternalizeReader
// correlates leafHTML[i] to the i-th HTML leaf visited during rebuild via
// a monotonic cursor rather than by re-matching Part values.
func collectHTMLLeaves(p mailparse.Part, path []int) []htmlLeafRef {
	if len(p.Children) == 0 {
		if strings.EqualFold(p.ContentType, "text/html") && p.Text != "" {
			return []htmlLeafRef{{Path: append([]int(nil), path...), Part: p}}
		}
		return nil
	}
	var out []htmlLeafRef
	for i, c := range p.Children {
		out = append(out, collectHTMLLeaves(c, appendPath(path, i))...)
	}
	return out
}

// appendPath returns a new path with i appended, never aliasing path's
// backing array (siblings in the same recursion frame must not share
// storage).
func appendPath(path []int, i int) []int {
	out := make([]int, len(path)+1)
	copy(out, path)
	out[len(path)] = i
	return out
}

// pathKey renders a tree path as a map key. Distinct paths (including the
// root, nil) always render distinctly.
func pathKey(path []int) string {
	return fmt.Sprint(path)
}

// lcaPath returns the lowest common ancestor path of leaves[idxs[i]] for
// every i -- the longest path prefix shared by all of them. Returns the
// root path (nil) for an empty idxs, a defensive fallback that should
// never be exercised: every successfully-fetched URL originates from
// extractCandidates on at least one leaf's own text, so its referencing
// set is never empty.
func lcaPath(leaves []htmlLeafRef, idxs []int) []int {
	if len(idxs) == 0 {
		return nil
	}
	lca := append([]int(nil), leaves[idxs[0]].Path...)
	for _, idx := range idxs[1:] {
		lca = commonPrefix(lca, leaves[idx].Path)
	}
	return lca
}

// commonPrefix returns the longest common prefix of a and b.
func commonPrefix(a, b []int) []int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return append([]int(nil), a[:i]...)
}

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

	leaves := collectHTMLLeaves(msg.Body, nil)
	if len(leaves) == 0 {
		sum.NotEligibleReason = "no_html_body"
		sum.WallClock = time.Since(start)
		return io.NewSectionReader(src, 0, srcSize), sum, nil
	}
	sum.HTMLPartsScanned = len(leaves)

	// Extract candidates per leaf (so each leaf's own references are
	// known for placement below) and fetch the message-wide deduplicated
	// union.
	leafCands := make([][]candidate, len(leaves))
	urlSeen := map[string]bool{}
	var allCands []candidate
	for i, lf := range leaves {
		cs, cerr := extractCandidates([]byte(lf.Part.Text))
		if cerr != nil {
			sum.ParseError = cerr.Error()
			sum.WallClock = time.Since(start)
			return io.NewSectionReader(src, 0, srcSize), sum, nil
		}
		leafCands[i] = cs
		for _, c := range cs {
			if !urlSeen[c.URL] {
				urlSeen[c.URL] = true
				allCands = append(allCands, c)
			}
		}
	}
	sum.Candidates = len(allCands)
	if len(allCands) == 0 {
		sum.NotEligibleReason = "no_external_refs"
		sum.WallClock = time.Since(start)
		return io.NewSectionReader(src, 0, srcSize), sum, nil
	}

	fetchCtx, cancel := context.WithTimeout(ctx, cfg.PerMessageTimeout)
	defer cancel()

	fetcher := NewFetcher(cfg)
	results := fetchAllBudgeted(fetchCtx, fetcher, cfg, allCands, &sum)

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

	// Rewrite each leaf's own HTML using the shared cidMap, and collect
	// failed-URL / placeholder bookkeeping across all leaves (the
	// single-leaf equivalent of the matching pass in internalize.go's
	// Internalize).
	leafHTML := make([][]byte, len(leaves))
	var failedURLs []string
	failedSeen := map[string]bool{}
	var failedTemplate []byte
	for i, lf := range leaves {
		rw, rerr := rewriteHTML([]byte(lf.Part.Text), cidMap)
		if rerr != nil {
			sum.ParseError = rerr.Error()
			sum.WallClock = time.Since(start)
			return io.NewSectionReader(src, 0, srcSize), sum, nil
		}
		if sum.Failed > 0 {
			if fc, ferr := extractCandidates(rw); ferr == nil {
				for _, c := range fc {
					if !failedSeen[c.URL] {
						failedSeen[c.URL] = true
						failedURLs = append(failedURLs, c.URL)
					}
				}
				if len(fc) > 0 && failedTemplate == nil {
					// Best-effort representative template (issue #162
					// retry affordance): when multiple HTML leaves
					// still carry failed URLs, the first one found
					// stands in for the whole message, matching
					// RetryFailedImages' existing single-html-template
					// assumption (internal/extimg/retry.go rebuilds via
					// enmime.Builder, which itself only ever produces
					// one HTML body).
					failedTemplate = append([]byte(nil), rw...)
				}
			}
			if placeheld, perr := RewriteForPlaceholder(rw); perr == nil {
				rw = placeheld
			}
		}
		leafHTML[i] = rw
	}
	if sum.Failed > 0 {
		sum.FailedURLs = failedURLs
		sum.FailedImageTemplate = failedTemplate
		sum.Placeholdered = sum.Failed
	}

	// Group successfully-fetched images by the lowest common ancestor of
	// the HTML leaves that reference them (issue #324): each image is
	// placed -- and therefore emitted -- exactly once.
	referencing := map[string][]int{}
	for i, cs := range leafCands {
		for _, c := range cs {
			if cid, ok := cidMap[c.URL]; ok {
				referencing[cid] = append(referencing[cid], i)
			}
		}
	}
	groups := map[string][]inlinePart{}
	for _, in := range inlines {
		lca := lcaPath(leaves, referencing[in.cid])
		key := pathKey(lca)
		groups[key] = append(groups[key], in)
	}

	outReaders, err := buildMsgReaders(src, msg, leafHTML, groups, cfg, verdict)
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
	leafHTML [][]byte,
	groups map[string][]inlinePart,
	cfg Config,
	verdict DKIMVerdict,
) ([]io.Reader, error) {
	cursor := 0
	bodyReaders, topCT, topCTE, err := buildPartContent(src, msg.Body, nil, leafHTML, &cursor, groups)
	if err != nil {
		return nil, err
	}

	// The message root is the placement point for an image group only
	// when every leaf referencing that image sits in a different
	// top-level subtree (their LCA is the root) and the root is not
	// itself already multipart/related -- buildMultipartBody consumes
	// its own group in place when it is. Wrap the whole body once.
	if imgs := groups[pathKey(nil)]; len(imgs) > 0 {
		delete(groups, pathKey(nil))
		inner := assembleSelfContained(topCT, topCTE, bodyReaders)
		topCT, bodyReaders = wrapInRelated(inner, imgs)
		topCTE = ""
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
	path []int,
	leafHTML [][]byte,
	cursor *int,
	groups map[string][]inlinePart,
) (bodyReaders []io.Reader, contentType string, contentTransferEncoding string, err error) {
	ct := strings.ToLower(strings.TrimSpace(p.ContentType))

	switch {
	case ct == "text/html" && len(p.Children) == 0:
		charset := p.Charset
		if charset == "" {
			charset = "utf-8"
		}
		contentType = fmt.Sprintf("text/html; charset=%q", charset)
		contentTransferEncoding = "quoted-printable"
		bodyReaders, err = encodeHTMLBody(nextLeafHTML(leafHTML, cursor))
		if err != nil {
			return nil, "", "", err
		}
		return bodyReaders, contentType, contentTransferEncoding, nil

	case strings.HasPrefix(ct, "multipart/"):
		readers, mct, merr := buildMultipartBody(src, p, path, leafHTML, cursor, groups)
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

// nextLeafHTML returns leafHTML[*cursor] and advances the cursor. The
// cursor tracks which HTML leaf is currently being rendered; it advances
// in the same depth-first order collectHTMLLeaves used to populate
// leafHTML, so the two stay correlated without re-matching Part values.
func nextLeafHTML(leafHTML [][]byte, cursor *int) []byte {
	rw := leafHTML[*cursor]
	*cursor++
	return rw
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

// assembleSelfContained builds a self-contained MIME part block (Content-Type
// header, optional CTE header, blank line, body) from a top-level-style
// (ct, cte, bodyReaders) triple -- the same shape buildChildPartBlock's
// callers already produce -- so it can be wrapped by wrapInRelated like
// any other self-contained block.
func assembleSelfContained(ct, cte string, bodyReaders []io.Reader) []io.Reader {
	var hdrBuf bytes.Buffer
	fmt.Fprintf(&hdrBuf, "Content-Type: %s\r\n", ct)
	if cte != "" {
		fmt.Fprintf(&hdrBuf, "Content-Transfer-Encoding: %s\r\n", cte)
	}
	hdrBuf.WriteString("\r\n")
	out := []io.Reader{&hdrBuf}
	out = append(out, bodyReaders...)
	return out
}

// wrapInRelated wraps innerBlock -- a self-contained MIME part block (its
// own Content-Type/CTE headers + blank line + body, no boundary markers)
// -- as the sole content child of a new multipart/related container,
// followed by images as additional children. Returns the new Content-Type
// value and the body-only readers (starting with --boundary; the caller
// supplies its own Content-Type header line for this new value).
func wrapInRelated(innerBlock []io.Reader, images []inlinePart) (string, []io.Reader) {
	boundary := sMakeBoundary()
	ct := fmt.Sprintf("multipart/related; boundary=%q", boundary)

	var open bytes.Buffer
	fmt.Fprintf(&open, "--%s\r\n", boundary)

	var sep bytes.Buffer
	sep.WriteString("\r\n")

	var imgBuf bytes.Buffer
	appendInlineParts(&imgBuf, images, boundary)

	var closeBuf bytes.Buffer
	fmt.Fprintf(&closeBuf, "--%s--\r\n", boundary)

	body := []io.Reader{&open}
	body = append(body, innerBlock...)
	body = append(body, &sep, &imgBuf, &closeBuf)
	return ct, body
}

// buildMultipartBody builds the body content of a multipart/* part.
// Returns (bodyReaders, contentType, error). bodyReaders starts with the
// first boundary line. The caller emits any wrapping headers.
func buildMultipartBody(
	src io.ReaderAt,
	p mailparse.Part,
	path []int,
	leafHTML [][]byte,
	cursor *int,
	groups map[string][]inlinePart,
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

	for i, child := range p.Children {
		childCT := strings.ToLower(strings.TrimSpace(child.ContentType))
		isHTMLChild := childCT == "text/html" && len(child.Children) == 0
		childPath := appendPath(path, i)

		// Build the child part as a self-contained block (all headers + blank
		// line + body). Then prepend --boundary\r\n.
		childBlock, berr := buildChildPartBlock(src, child, childPath, leafHTML, cursor, groups, isHTMLChild)
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

	// For multipart/related: append any images placed at this exact
	// container (issue #324: consumed here, in place, so no ancestor or
	// descendant re-emits the same image).
	if isRelated {
		if imgs := groups[pathKey(path)]; len(imgs) > 0 {
			delete(groups, pathKey(path))
			var inBuf bytes.Buffer
			appendInlineParts(&inBuf, imgs, boundary)
			out = append(out, &inBuf)
		}
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
//
// After building the child's normal content, checks whether path is the
// placement point for an image group nothing upstream has consumed yet
// (issue #324) -- e.g. the child is the single HTML leaf referencing an
// image, or a non-related container that is the lowest common ancestor of
// several referencing leaves -- and wraps the block in a synthetic
// multipart/related if so.
func buildChildPartBlock(
	src io.ReaderAt,
	p mailparse.Part,
	path []int,
	leafHTML [][]byte,
	cursor *int,
	groups map[string][]inlinePart,
	isHTMLChild bool,
) ([]io.Reader, error) {
	var block []io.Reader
	var err error
	if isHTMLChild {
		block, err = buildHTMLChildBlock(p, leafHTML, cursor)
	} else if len(p.Children) > 0 {
		block, err = buildMultipartChildBlock(src, p, path, leafHTML, cursor, groups)
	} else {
		block, err = buildRawChildBlock(src, p)
	}
	if err != nil {
		return nil, err
	}

	if imgs := groups[pathKey(path)]; len(imgs) > 0 {
		delete(groups, pathKey(path))
		newCT, newBody := wrapInRelated(block, imgs)
		block = assembleSelfContained(newCT, "", newBody)
	}
	return block, nil
}

// buildHTMLChildBlock builds a self-contained MIME part block for a
// text/html leaf: its own rewritten content, QP-encoded, with no
// placement decision -- the caller (buildChildPartBlock) wraps in a
// synthetic multipart/related if this leaf's own path turns out to be an
// image's placement point.
func buildHTMLChildBlock(p mailparse.Part, leafHTML [][]byte, cursor *int) ([]io.Reader, error) {
	charset := p.Charset
	if charset == "" {
		charset = "utf-8"
	}
	var qpBuf bytes.Buffer
	qpw := quotedprintable.NewWriter(&qpBuf)
	if _, werr := qpw.Write(nextLeafHTML(leafHTML, cursor)); werr != nil {
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

// buildMultipartChildBlock builds a self-contained MIME part block for a
// multipart/* child. Returns (CT-header + blank-line + body readers).
func buildMultipartChildBlock(
	src io.ReaderAt,
	p mailparse.Part,
	path []int,
	leafHTML [][]byte,
	cursor *int,
	groups map[string][]inlinePart,
) ([]io.Reader, error) {
	bodyReaders, ct, berr := buildMultipartBody(src, p, path, leafHTML, cursor, groups)
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
