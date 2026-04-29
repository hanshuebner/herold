package email

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"strings"
	"time"

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
		ID:         jmapIDFromMessage(m.ID),
		BlobID:     m.Blob.Hash,
		ThreadID:   threadIDForMessage(m),
		MailboxIDs: mailboxIDs,
		Keywords:   keywordsFromMessage(m),
		Size:       m.Size,
		ReceivedAt: rfc3339UTC(m.ReceivedAt),
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

// renderFull returns the full-bodied Email rendering, parsing the body
// blob to assemble bodyStructure, bodyValues, textBody/htmlBody and
// attachments. truncateAt clamps each bodyValue's value field; the
// caller passes 0 for "no truncation".
func renderFull(
	ctx context.Context,
	blobs store.Blobs,
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
	parsed, err := parser(bytes.NewReader(body))
	if err != nil {
		// Treat parse errors as "metadata-only render"; clients still
		// see size, blobId, mailboxIds, keywords, and envelope fields.
		return out, nil
	}
	bs, values, textParts, htmlParts, attParts := walkParts(parsed.Body, truncateAt, m.Blob.Hash)
	out.BodyStructure = bs
	out.BodyValues = values
	out.TextBody = textParts
	out.HTMLBody = htmlParts
	out.Attachments = attParts
	out.HasAttachment = len(attParts) > 0
	out.Preview = previewFromValues(values, textParts, 256)
	return out, nil
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
// blobId values in the form "<msgBlobHash>/p<partId>".
func walkParts(root mailparse.Part, truncateAt int, msgBlobHash string) (
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
		size := int64(len(p.Bytes))
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
		text := p.Text
		truncated := false
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
		case strings.EqualFold(out.Type, "text/plain"):
			textParts = append(textParts, out)
		case strings.EqualFold(out.Type, "text/html"):
			htmlParts = append(htmlParts, out)
		default:
			// Treat as inline non-text -- RFC 8621 puts it in attachments.
			attParts = append(attParts, out)
		}
		return out
	}
	bs := walk(root)
	return &bs, values, textParts, htmlParts, attParts
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
