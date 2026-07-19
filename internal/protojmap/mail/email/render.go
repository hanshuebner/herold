package email

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"strings"
	"sync"
	"time"

	"github.com/hanshuebner/herold/internal/extimg"
	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/store"
)

// rfc3339UTC formats t in RFC 3339 UTC form per RFC 8621 §1.2 (Date
// values are UTC, second resolution, with the "Z" suffix).
func rfc3339UTC(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02T15:04:05Z")
}

// renderEmailMetadata projects a store.Message onto the cheap subset
// of the JMAP Email object: id, blobId, threadId, mailboxIds, keywords,
// size, receivedAt, plus the cached envelope fields.
//
// blobs and bodyValues are NOT populated here -- Email/get's "properties"
// hint drives the optional render in renderEmailFull.
//
// mailboxIds is populated from m.Mailboxes when that slice is non-empty
// (the multi-mailbox M:N case). For single-mailbox paths where Mailboxes
// is nil or empty, the convenience field m.MailboxID is used as a fallback.
func renderEmailMetadata(m store.Message) jmapEmail {
	mailboxIDs := make(map[jmapID]bool, max(1, len(m.Mailboxes)))
	if len(m.Mailboxes) > 0 {
		for _, mm := range m.Mailboxes {
			mailboxIDs[jmapIDFromMailbox(mm.MailboxID)] = true
		}
	} else {
		mailboxIDs[jmapIDFromMailbox(m.MailboxID)] = true
	}
	out := jmapEmail{
		ID:                 jmapIDFromMessage(m.ID),
		BlobID:             m.Blob.Hash,
		ThreadID:           threadIDForMessage(m),
		MailboxIDs:         mailboxIDs,
		Keywords:           keywordsFromMessage(m),
		Size:               m.Size,
		ReceivedAt:         rfc3339UTC(m.ReceivedAt),
		InternalizePending: m.InternalizePending,
		FailedImageCount:   m.FailedImageCount,
	}
	if m.SnoozedUntil != nil {
		s := rfc3339UTC(*m.SnoozedUntil)
		out.SnoozedUntil = &s
	}
	if m.Envelope.Subject != "" {
		out.Subject = m.Envelope.Subject
	}
	if !m.Envelope.Date.IsZero() {
		out.SentAt = rfc3339UTC(m.Envelope.Date)
	}
	if m.Envelope.From != "" {
		out.From = parseAddressList(m.Envelope.From)
	}
	if m.Envelope.To != "" {
		out.To = parseAddressList(m.Envelope.To)
	}
	if m.Envelope.Cc != "" {
		out.Cc = parseAddressList(m.Envelope.Cc)
	}
	if m.Envelope.Bcc != "" {
		out.Bcc = parseAddressList(m.Envelope.Bcc)
	}
	if m.Envelope.ReplyTo != "" {
		out.ReplyTo = parseAddressList(m.Envelope.ReplyTo)
	}
	if m.Envelope.MessageID != "" {
		out.MessageID = []string{m.Envelope.MessageID}
	}
	if m.Envelope.InReplyTo != "" {
		out.InReplyTo = []string{m.Envelope.InReplyTo}
	}
	// Serve preview and hasAttachment from precomputed metadata when
	// available. The caller (renderOne) still dispatches to
	// renderFullWithProperties when body-only properties are requested or
	// when BodyMetaComputed is false; this path is the zero-blob fast lane.
	if m.BodyMetaComputed {
		out.Preview = m.Preview
		out.HasAttachment = m.HasAttachment
	}
	return out
}

// threadIDForMessage formats the threadId per RFC 8621 §4.1. v1 lifts
// store.Message.ThreadID directly; messages whose ThreadID is 0 (not yet
// threaded) collapse to the message id, so the JMAP Thread object is
// always at minimum the singleton "{this email}" thread.
func threadIDForMessage(m store.Message) jmapID {
	if m.ThreadID == 0 {
		return "t" + jmapIDFromMessage(m.ID)
	}
	return "t" + fmt.Sprintf("%d", m.ThreadID)
}

// parseAddressList parses an RFC 5322 address-list header into JMAP
// EmailAddress objects. Malformed input falls through to a single
// best-effort entry with name=raw and email empty so clients still see
// the operator-visible value.
func parseAddressList(raw string) []jmapAddress {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	addrs, err := mail.ParseAddressList(raw)
	if err != nil {
		return []jmapAddress{{Name: raw}}
	}
	out := make([]jmapAddress, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, jmapAddress{Name: a.Name, Email: a.Address})
	}
	return out
}

// partDimsFromParsed computes intrinsic pixel dimensions for image leaf parts
// by walking an already-parsed message and decoding each image's header.
// rawBody must be the bytes that produced parsed (e.g. the blob with an
// optional synthetic header prepended). The returned map uses the same 1-based
// DFS index as loadPartDims so it can be passed directly to walkParts.
//
// The RawOffset values in the underlying PartIndexEntry structs are relative
// to rawBody. Do not persist these entries when rawBody contains a prepended
// synthetic header (such as X-Herold-Recipient): the offsets would be wrong
// relative to the stored blob. Use writePartIndexBackground for DB persistence.
//
// Returns nil when no image parts have decodable dimensions.
func partDimsFromParsed(parsed mailparse.Message, rawBody []byte) map[int]struct{ W, H int } {
	src := bytes.NewReader(rawBody)
	entries := mailparse.BuildPartIndex(parsed, src)
	m := make(map[int]struct{ W, H int }, len(entries))
	for _, e := range entries {
		if e.Width > 0 || e.Height > 0 {
			m[e.Index] = struct{ W, H int }{e.Width, e.Height}
		}
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// writePartIndexBackground starts a goroutine that parses rawBodyOrig
// (the stored blob bytes without any prepended synthetic headers) and writes
// the v2 part index to meta. Call this after serving dims computed inline via
// partDimsFromParsed so the index is available for future Email/get calls
// without repeating the inline computation.
//
// wg, when non-nil, is incremented before the goroutine starts and decremented
// when it exits. Tests pass the handlerSet's WaitGroup so that test teardown
// can call WaitBackgroundWrites before closing the store, preventing the
// goroutine from racing t.TempDir RemoveAll cleanup.
//
// Errors are silently ignored: the bodymeta worker will recompute the index
// on its next sweep. A 30-second context bounds the goroutine lifetime so it
// cannot leak indefinitely.
func writePartIndexBackground(meta store.Metadata, blobHash string, rawBodyOrig []byte, wg *sync.WaitGroup) {
	if wg != nil {
		wg.Add(1)
	}
	go func() {
		if wg != nil {
			defer wg.Done()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		parsed, err := mailparse.Parse(bytes.NewReader(rawBodyOrig), mailparse.NewParseOptions())
		if err != nil {
			return
		}
		entries := mailparse.BuildPartIndex(parsed, bytes.NewReader(rawBodyOrig))
		indexJSON, err := json.Marshal(entries)
		if err != nil {
			return
		}
		_ = meta.PutBlobPartIndex(ctx, blobHash, mailparse.PartIndexVersion, indexJSON, time.Now().UnixMicro())
	}()
}

// loadPartDims fetches the persisted part index for hash and returns a map
// from 1-based DFS part index to intrinsic pixel dimensions. Returns nil when
// meta is nil, the index is absent, stale (version != PartIndexVersion), or
// unparseable — callers treat nil as "no dimensions available" and omit
// width/height from the rendered EmailBodyPart (re #47).
func loadPartDims(ctx context.Context, meta store.Metadata, hash string) map[int]struct{ W, H int } {
	if meta == nil || hash == "" {
		return nil
	}
	version, partsJSON, err := meta.GetBlobPartIndex(ctx, hash)
	if err != nil || version != mailparse.PartIndexVersion {
		return nil
	}
	var entries []mailparse.PartIndexEntry
	if err := json.Unmarshal(partsJSON, &entries); err != nil {
		return nil
	}
	m := make(map[int]struct{ W, H int }, len(entries))
	for _, e := range entries {
		if e.Width > 0 || e.Height > 0 {
			m[e.Index] = struct{ W, H int }{e.Width, e.Height}
		}
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// renderFull returns the full-bodied Email rendering, parsing the body
// blob to assemble bodyStructure, bodyValues, textBody/htmlBody and
// attachments. truncateAt clamps each bodyValue's value field; the
// caller passes 0 for "no truncation". meta, when non-nil, is used to
// load the persisted part index so intrinsic image dimensions are
// included on image body parts (re #47).
func renderFull(
	ctx context.Context,
	blobs store.Blobs,
	meta store.Metadata,
	m store.Message,
	truncateAt int,
	parser parseFn,
) (jmapEmail, error) {
	out := renderEmailMetadata(m)
	rc, err := blobs.Get(ctx, m.Blob.Hash)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return out, nil
		}
		return jmapEmail{}, fmt.Errorf("email: blob: %w", err)
	}
	defer rc.Close()
	body, err := io.ReadAll(io.LimitReader(rc, 64<<20))
	if err != nil {
		return jmapEmail{}, fmt.Errorf("email: read blob: %w", err)
	}
	// REQ-FLOW-34: prepend X-Herold-Recipient at render time so the
	// header is part of the parsed top-level part. The header is not
	// persisted into the dedup-shared blob; ReceivedTo is empty for
	// pre-feature memberships and for mailbox memberships that did
	// not originate from inbound delivery, in which case Inject is a
	// no-op.
	body = mailparse.InjectXHeroldRecipient(body, m.ReceivedTo)
	parsed, err := parser(bytes.NewReader(body))
	if err != nil {
		if errors.Is(err, mailparse.ErrTooLarge) {
			// The blob exceeds mailparse.DefaultMaxSize: the envelope in
			// out (from renderEmailMetadata/m.Envelope) is already correct
			// -- mailparse.Parse recovers headers even on this path (re
			// #244) -- but the body cannot be walked. Render a defined,
			// non-empty placeholder rather than silently leaving every
			// body field empty.
			return tooLargeBodyPlaceholder(out, m), nil
		}
		// Treat other parse errors as "metadata-only render"; clients
		// still see size, blobId, mailboxIds, keywords, and envelope
		// fields.
		return out, nil
	}
	dims := loadPartDims(ctx, meta, m.Blob.Hash)
	bs, values, textParts, htmlParts, attParts := walkParts(parsed.Body, truncateAt, m.Blob.Hash, dims)
	if m.InternalizePending {
		// REQ-EXTIMG-BG-10: replace external image references in every
		// genuine HTML body part with a placeholder data URI until the
		// background internalize-worker rewrites the blob. Failures
		// fall through silently — the user sees the original HTML
		// rather than a refused render.
		//
		// htmlParts may contain a part promoted by resolveBodyLists's
		// symmetric-fill (RFC 8621 §4.1.4; re #258) whose real content
		// type is text/plain, not text/html (a leaf with no html
		// alternative at its level). That promoted part shares its
		// bodyValue with textBody by partId, so running it through the
		// html.Parse/Render round-trip below would corrupt the plain
		// text in both textBody and htmlBody. Only rewrite parts whose
		// actual type is text/html.
		for _, p := range htmlParts {
			if p.PartID == nil || !strings.EqualFold(p.Type, "text/html") {
				continue
			}
			bv, ok := values[*p.PartID]
			if !ok {
				continue
			}
			rewritten, err := extimg.RewriteForPlaceholder([]byte(bv.Value))
			if err != nil {
				continue
			}
			bv.Value = string(rewritten)
			values[*p.PartID] = bv
		}
	}
	out.BodyStructure = bs
	out.BodyValues = values
	out.TextBody = textParts
	out.HTMLBody = htmlParts
	out.Attachments = attParts
	out.HasAttachment = hasRealAttachment(attParts)
	out.Preview = previewFromValues(values, textParts, 256)
	return out, nil
}

// envelopeIsEmpty reports whether env carries none of the three fields a
// successful parse always populates for a message with any headers at all
// (MessageID/From/Subject). This is the exact fingerprint the pre-fix
// mailparse.Parse-too-large defect (re #244) left behind:
// buildEnvelopeFromParsed on the zero-value mailparse.Message it used to
// return zeroed every field. A message that legitimately lacks a
// Message-ID but has From/Subject does NOT match this and is left
// untouched.
func envelopeIsEmpty(env store.Envelope) bool {
	return env.MessageID == "" && env.From == "" && env.Subject == ""
}

// tooLargeBodyPlaceholder degrades out's body-shaped fields to a defined,
// non-empty placeholder for a message whose blob exceeds
// mailparse.DefaultMaxSize. out already carries the correct envelope
// (subject/from/messageId/...) from renderEmailMetadata; only
// bodyStructure/bodyValues/textBody/preview/hasAttachment are touched
// here, replacing what would otherwise silently stay at their zero value
// (re #244). The raw message remains downloadable via BlobID/"Show
// original" regardless of this placeholder.
func tooLargeBodyPlaceholder(out jmapEmail, m store.Message) jmapEmail {
	const partID = "too-large"
	text := fmt.Sprintf(
		"This message (%s) exceeds the %s size limit and cannot be displayed. Use \"Show original\" to download the raw message.",
		formatMiB(m.Size), formatMiB(mailparse.DefaultMaxSize),
	)
	part := bodyPart{
		PartID: func() *string { s := partID; return &s }(),
		Type:   "text/plain",
		Size:   m.Size,
	}
	out.Preview = text
	out.BodyStructure = &part
	out.TextBody = []bodyPart{part}
	out.HTMLBody = nil
	out.Attachments = nil
	out.BodyValues = map[string]bodyValue{partID: {Value: text, IsTruncated: true}}
	out.HasAttachment = false
	return out
}

// formatMiB renders n bytes as a human-readable MiB figure, e.g. "78.5 MiB".
func formatMiB(n int64) string {
	return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
}

// hasRealAttachment reports whether any part in attParts is a downloadable
// attachment rather than a cid-referenced inline image. Parts whose
// disposition is "inline" are body-embedded images (they carry a Content-ID
// so they are referenced by cid: URLs in the HTML body) and do NOT constitute
// a user-visible attachment. All other parts — explicit "attachment" disposition
// or no disposition — count as real attachments per RFC 8621.
func hasRealAttachment(parts []bodyPart) bool {
	for _, p := range parts {
		if p.Disposition != nil && *p.Disposition == "inline" {
			continue
		}
		return true
	}
	return false
}

// parseFn is the body-parser injection point. v1 calls
// mailparse.Parse with the default ParseOptions; tests inject a fake
// parser to exercise specific parse outcomes without spinning up
// actual blob parsing.
type parseFn func(io.Reader) (mailparse.Message, error)

// defaultParseFn is the production parse function.
func defaultParseFn(r io.Reader) (mailparse.Message, error) {
	return mailparse.Parse(r, mailparse.NewParseOptions())
}

// walkParts builds the bodyStructure tree and the flat textBody /
// htmlBody / attachments lists per RFC 8621 §4.1.4. Truncation
// applies per-bodyValue. msgBlobHash is used to construct per-part
// blobId values in the form "<msgBlobHash>/p<partId>". dims, when
// non-nil, maps 1-based DFS part index to intrinsic pixel dimensions;
// Width/Height are set on image leaf bodyParts when the map has an
// entry for that part (re #47).
func walkParts(root mailparse.Part, truncateAt int, msgBlobHash string, dims map[int]struct{ W, H int }) (
	*bodyPart,
	map[string]bodyValue,
	[]bodyPart,
	[]bodyPart,
	[]bodyPart,
) {
	values := map[string]bodyValue{}
	var textParts []bodyPart
	var htmlParts []bodyPart
	var attParts []bodyPart
	idx := 0
	var walk func(p mailparse.Part) bodyPart
	walk = func(p mailparse.Part) bodyPart {
		idx++
		partID := fmt.Sprintf("%d", idx)
		// Part.Size holds the decoded body length (set by Parse). For text
		// parts it reflects the full decoded size even when Part.Text is
		// truncated. Use it directly; fall back to len(Text) for legacy text
		// parts that may have been built without a Size.
		size := p.Size
		if size == 0 && p.Text != "" {
			size = int64(len(p.Text))
		}
		var name *string
		if p.Filename != "" {
			n := p.Filename
			name = &n
		}
		var charset *string
		if p.Charset != "" {
			c := p.Charset
			charset = &c
		}
		// Populate cid from Content-ID header, stripping angle brackets
		// per RFC 2392 / RFC 8621 §4.1.4.
		var cid *string
		if raw := p.Headers.Get("Content-ID"); raw != "" {
			v := strings.Trim(raw, "<> \t")
			if v != "" {
				cid = &v
			}
		}
		// Compute the JMAP disposition value.
		//
		// Content-Disposition: inline is meaningful on its own only when the
		// part also carries a Content-ID header, because only a CID makes the
		// part addressable by a cid: URL in the HTML body.  A part with
		// Content-Disposition: inline but no Content-ID cannot be referenced
		// inline; it is effectively a regular attachment whose sender chose
		// to hint "display this rather than offer as a download."  Clients
		// (e.g. AttachmentList) use disposition=="inline" to separate the
		// "Inline images" sub-section from the normal attachments section, so
		// stripping the "inline" disposition from CID-less parts ensures they
		// surface in the regular attachment section rather than being hidden
		// under a sub-section the user may not notice.
		var disposition *string
		if d := p.Disposition.String(); d != "" {
			if p.Disposition == mailparse.DispositionInline && cid == nil {
				// No CID: cannot be inline-referenced; treat as regular attachment.
			} else {
				disposition = &d
			}
		}
		out := bodyPart{
			PartID:      &partID,
			Size:        size,
			Type:        strings.ToLower(p.ContentType),
			Charset:     charset,
			Disposition: disposition,
			Name:        name,
			Cid:         cid,
		}
		// Set blobId on every part so clients can download part content.
		if msgBlobHash != "" {
			blobID := msgBlobHash + "/p" + partID
			out.BlobID = &blobID
		}
		// Populate intrinsic image dimensions from the persisted part index
		// (re #47). Only image leaves carry non-zero dimensions in the index,
		// so this is a no-op for multipart containers and non-image leaves.
		if dims != nil {
			if d, ok := dims[idx]; ok {
				if d.W > 0 {
					w := d.W
					out.Width = &w
				}
				if d.H > 0 {
					h := d.H
					out.Height = &h
				}
			}
		}
		for _, hk := range p.Headers.Keys() {
			for _, v := range p.Headers.GetAll(hk) {
				out.Headers = append(out.Headers, bodyPartHeader{Name: hk, Value: v})
			}
		}
		if p.IsMultipart() {
			for _, c := range p.Children {
				out.SubParts = append(out.SubParts, walk(c))
			}
			return out
		}
		// Leaf: record body value and classify into textParts/htmlParts/attParts.
		// p.Text is already capped at the parser's DefaultMaxTextPartBytes;
		// propagate that cap as isTruncated so the client knows the inline
		// value is incomplete and can fetch the full part by blobId (RFC 8621
		// §4.1.4; issue #48). The truncateAt clamp below tightens it further
		// when the client asked for a smaller maxBodyValueBytes.
		text := p.Text
		truncated := p.TextTruncated
		// RFC 3676 reception-side reflow (re #261): a text/plain part
		// carrying format=flowed is served as already-reflowed prose so
		// the client needs no flowed/not-flowed distinction. This runs
		// on the decoded text only -- the raw blob served via BlobID
		// ("Show original") is untouched.
		if strings.EqualFold(p.ContentType, "text/plain") && p.IsFlowed() {
			text = mailparse.ReflowFormatFlowed(text, p.DelSp)
		}
		if truncateAt > 0 && len(text) > truncateAt {
			text = text[:truncateAt]
			truncated = true
		}
		values[partID] = bodyValue{
			Value:             text,
			IsEncodingProblem: len(p.DecodeErrors) > 0,
			IsTruncated:       truncated,
		}
		switch {
		case p.Disposition == mailparse.DispositionAttachment:
			attParts = append(attParts, out)
		case strings.EqualFold(out.Type, "text/plain"), strings.EqualFold(out.Type, "text/html"):
			// textBody/htmlBody membership is computed after the full tree is
			// built, by resolveBodyLists (RFC 8621 §4.1.4; re #258): a leaf's
			// contribution depends on the semantics of its enclosing
			// multipart (alternative vs mixed/related), not on its own type
			// alone.
		default:
			// Treat as inline non-text -- RFC 8621 puts it in attachments.
			attParts = append(attParts, out)
		}
		return out
	}
	bs := walk(root)
	textParts, htmlParts = resolveBodyLists(&bs)
	return &bs, values, textParts, htmlParts, attParts
}

// resolveBodyLists computes Email.textBody and Email.htmlBody (RFC 8621
// §4.1.4) from the already-built bodyPart tree bs. Each multipart's
// contribution is the ordered concatenation of its children's own
// contributions; multipart/alternative instead selects the single best
// text/plain candidate and the single best text/html candidate among its
// children. A leaf (or an alternative) that has no counterpart of the other
// type contributes its one representative to BOTH lists, so a client that
// prefers HTML still sees text-only content and vice versa (re #258).
func resolveBodyLists(bs *bodyPart) (text []bodyPart, html []bodyPart) {
	return contribute(bs)
}

// contribute resolves a single position in the RFC 8621 §4.1.4 traversal --
// a leaf, a multipart/alternative, or any other multipart container -- to
// its complete (text, html) contribution. "Complete" means the symmetric
// fill rule has already been applied: if this position has no
// representative of one type, its representative of the other type is used
// for both lists. This is the function to call for anything that is itself
// a self-contained slot in the body sequence: the message root and every
// child of a concatenating (mixed/related-like) container.
func contribute(p *bodyPart) (text []bodyPart, html []bodyPart) {
	if p.Disposition != nil && *p.Disposition == "attachment" {
		return nil, nil
	}
	if len(p.SubParts) == 0 {
		t, h := leafContribution(p)
		return fillMissing(t, h)
	}
	if strings.EqualFold(p.Type, "multipart/alternative") {
		return contributeAlternative(p.SubParts)
	}
	// multipart/mixed, multipart/related, or any other multipart subtype:
	// transparent concatenation of each child's own (already-filled)
	// contribution, in order.
	for i := range p.SubParts {
		ct, ch := contribute(&p.SubParts[i])
		text = append(text, ct...)
		html = append(html, ch...)
	}
	return text, html
}

// contributeRaw is like contribute, but for a candidate considered inside a
// multipart/alternative: it reports only the GENUINE text/html
// representation reachable from p, without applying the symmetric fill
// contribute() uses for a self-contained slot. This lets contributeAlternative
// tell a genuinely plain-only candidate apart from a genuinely html-only one
// even though, once contribute() is applied on its own, either would be
// filled into both lists. Recursion into a nested multipart/alternative
// still calls the full contribute() (via contributeAlternative), since a
// nested alternative is itself a resolved, self-contained slot.
func contributeRaw(p *bodyPart) (text []bodyPart, html []bodyPart) {
	if p.Disposition != nil && *p.Disposition == "attachment" {
		return nil, nil
	}
	if len(p.SubParts) == 0 {
		return leafContribution(p)
	}
	if strings.EqualFold(p.Type, "multipart/alternative") {
		return contributeAlternative(p.SubParts)
	}
	for i := range p.SubParts {
		ct, ch := contributeRaw(&p.SubParts[i])
		text = append(text, ct...)
		html = append(html, ch...)
	}
	return text, html
}

// leafContribution reports a non-multipart part's own (unfilled) type
// contribution: a text/plain leaf contributes only to text, a text/html
// leaf only to html, and anything else (image/audio/video, inline non-text,
// etc.) contributes to neither -- such parts are already routed to
// attachments by walkParts's leaf switch.
func leafContribution(p *bodyPart) (text []bodyPart, html []bodyPart) {
	switch {
	case strings.EqualFold(p.Type, "text/plain"):
		return []bodyPart{*p}, nil
	case strings.EqualFold(p.Type, "text/html"):
		return nil, []bodyPart{*p}
	default:
		return nil, nil
	}
}

// contributeAlternative implements RFC 8621's multipart/alternative
// selection: the LAST genuine text/plain candidate among children becomes
// the text track's contribution and the LAST genuine text/html candidate
// becomes the html track's (later alternatives are conventionally the more
// preferred/capable rendering). A child that is itself a container (e.g. a
// multipart/related wrapping the HTML representation, per RFC 8621 EXAMPLE
// 5) is resolved recursively via contributeRaw. When only one of the two
// types is present among the alternatives, that single representative is
// is used for BOTH tracks.
func contributeAlternative(children []bodyPart) (text []bodyPart, html []bodyPart) {
	var bestText, bestHTML []bodyPart
	for i := range children {
		c := &children[i]
		if c.Disposition != nil && *c.Disposition == "attachment" {
			continue
		}
		ct, ch := contributeRaw(c)
		if len(ct) > 0 {
			bestText = ct
		}
		if len(ch) > 0 {
			bestHTML = ch
		}
	}
	return fillMissing(bestText, bestHTML)
}

// fillMissing applies RFC 8621 §4.1.4's symmetric fallback: when a position
// has a representative for only one of text/html, that same representative
// is used for the other list too, so a client is never left with an empty
// track just because the message happened to supply only one alternative.
func fillMissing(t []bodyPart, h []bodyPart) ([]bodyPart, []bodyPart) {
	if len(t) == 0 {
		t = h
	}
	if len(h) == 0 {
		h = t
	}
	return t, h
}

// previewFromValues returns the first n runes of the leftmost text body
// value, used as the JMAP "preview" property.
func previewFromValues(values map[string]bodyValue, textParts []bodyPart, n int) string {
	if len(textParts) == 0 {
		return ""
	}
	partID := textParts[0].PartID
	if partID == nil {
		return ""
	}
	v, ok := values[*partID]
	if !ok {
		return ""
	}
	s := strings.TrimSpace(v.Value)
	if len(s) <= n {
		return s
	}
	// Trim at a rune boundary so we never split a multi-byte codepoint.
	if n > 0 && n < len(s) {
		s = s[:n]
		// Walk back to a valid rune boundary.
		for len(s) > 0 && (s[len(s)-1]&0xC0) == 0x80 {
			s = s[:len(s)-1]
		}
	}
	return s
}
