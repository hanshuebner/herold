package email

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/textproto"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// buildEmailFromProperties assembles an RFC 5322 message from the inline
// creation properties the Suite sends (bodyValues + textBody/htmlBody
// plus header-level properties). This is the "construct from properties"
// path defined by RFC 8621 §4.6.
//
// blobs and ctx are used to fetch the raw bytes for blob-referenced
// attachment parts (populated from bodyStructure subParts with a blobId).
// blobs may be nil when no attachment parts are present.
//
// The returned []byte is a fully-formed RFC 5322 message ready to be
// stored in the blob store.
func buildEmailFromProperties(ctx context.Context, blobs store.Blobs, in emailCreateInput, now time.Time, hostname string) ([]byte, error) {
	var buf bytes.Buffer

	// --- top-level headers ---

	// Date
	writeLiteralHeader(&buf, "Date", now.UTC().Format(time.RFC1123Z))

	// Message-ID: auto-generate if absent.
	msgID := ""
	if len(in.MessageID) > 0 && in.MessageID[0] != "" {
		msgID = ensureAngleBrackets(in.MessageID[0])
	} else {
		msgID = generateMessageID(hostname)
	}
	writeLiteralHeader(&buf, "Message-ID", msgID)

	// From
	if len(in.From) > 0 {
		writeLiteralHeader(&buf, "From", formatAddresses(in.From))
	}

	// To / Cc / Bcc / Reply-To
	if len(in.To) > 0 {
		writeLiteralHeader(&buf, "To", formatAddresses(in.To))
	}
	if len(in.Cc) > 0 {
		writeLiteralHeader(&buf, "Cc", formatAddresses(in.Cc))
	}
	if len(in.Bcc) > 0 {
		writeLiteralHeader(&buf, "Bcc", formatAddresses(in.Bcc))
	}
	if len(in.ReplyTo) > 0 {
		writeLiteralHeader(&buf, "Reply-To", formatAddresses(in.ReplyTo))
	}

	// Subject: use the inline subject if set; fall back to in.Subject
	// (the top-level create field).
	subj := ""
	if in.Subject != nil {
		subj = *in.Subject
	}
	if subj != "" {
		writeLiteralHeader(&buf, "Subject", mime.QEncoding.Encode("utf-8", subj))
	}

	// Sent date: use the SentAt field (JMAP sentAt) if supplied; otherwise
	// the Date header above already covers the sent time.
	if in.SentAt != nil && *in.SentAt != "" {
		if t, err := time.Parse(time.RFC3339, *in.SentAt); err == nil {
			// Overwrite the Date header by writing another one — the
			// simple approach is to write it after, which makes a
			// duplicate. Instead we carry only one Date header. Rebuild:
			buf.Reset()
			writeLiteralHeader(&buf, "Date", t.UTC().Format(time.RFC1123Z))
			writeLiteralHeader(&buf, "Message-ID", msgID)
			if len(in.From) > 0 {
				writeLiteralHeader(&buf, "From", formatAddresses(in.From))
			}
			if len(in.To) > 0 {
				writeLiteralHeader(&buf, "To", formatAddresses(in.To))
			}
			if len(in.Cc) > 0 {
				writeLiteralHeader(&buf, "Cc", formatAddresses(in.Cc))
			}
			if len(in.Bcc) > 0 {
				writeLiteralHeader(&buf, "Bcc", formatAddresses(in.Bcc))
			}
			if len(in.ReplyTo) > 0 {
				writeLiteralHeader(&buf, "Reply-To", formatAddresses(in.ReplyTo))
			}
			if subj != "" {
				writeLiteralHeader(&buf, "Subject", mime.QEncoding.Encode("utf-8", subj))
			}
		}
	}

	// In-Reply-To / References
	if len(in.InReplyTo) > 0 {
		writeLiteralHeader(&buf, "In-Reply-To", ensureAngleBrackets(in.InReplyTo[0]))
	}
	if len(in.References) > 0 {
		parts := make([]string, len(in.References))
		for i, r := range in.References {
			parts[i] = ensureAngleBrackets(r)
		}
		writeLiteralHeader(&buf, "References", strings.Join(parts, " "))
	}

	// MIME-Version
	writeLiteralHeader(&buf, "MIME-Version", "1.0")

	// --- body ---

	// Resolve which parts we have.
	textPartID := resolvePartID(in.TextBody)
	htmlPartID := resolvePartID(in.HtmlBody)

	textVal, hasText := bodyValueFor(in.BodyValues, textPartID)
	htmlVal, hasHTML := bodyValueFor(in.BodyValues, htmlPartID)

	hasInlines := len(in.inlineParts) > 0 && blobs != nil
	hasAttachments := len(in.attachmentParts) > 0 && blobs != nil

	switch {
	case hasInlines && hasAttachments:
		// multipart/mixed wrapping multipart/related (alt + inlines) + attachments.
		if err := writeMultipartMixedWithRelated(ctx, blobs, &buf, textVal, htmlVal, hasText, hasHTML, in.inlineParts, in.attachmentParts); err != nil {
			return nil, fmt.Errorf("bodybuild: multipart/mixed+related: %w", err)
		}
	case hasInlines:
		// multipart/related wraps the alternative body + inline parts.
		// Per RFC 2387 §3, the root is multipart/related and the first
		// subpart is the body (multipart/alternative when both text and html
		// are present; text/html otherwise).
		if err := writeMultipartRelated(ctx, blobs, &buf, textVal, htmlVal, hasText, hasHTML, in.inlineParts); err != nil {
			return nil, fmt.Errorf("bodybuild: multipart/related: %w", err)
		}
	case hasAttachments:
		// multipart/mixed wraps the body part(s) + attachment(s).
		if err := writeMultipartMixed(ctx, blobs, &buf, textVal, htmlVal, hasText, hasHTML, in.attachmentParts); err != nil {
			return nil, fmt.Errorf("bodybuild: multipart/mixed: %w", err)
		}
	default:
		switch {
		case hasText && hasHTML:
			// multipart/alternative: text first (RFC 1341 §7.2 ordering: the
			// last alternative is the preferred one; text/plain first, html last
			// so html-capable clients use html).
			if err := writeMultipartAlternative(&buf, textVal, htmlVal); err != nil {
				return nil, fmt.Errorf("bodybuild: multipart: %w", err)
			}
		case hasText:
			writeTextPart(&buf, textVal, "text/plain")
		case hasHTML:
			writeTextPart(&buf, htmlVal, "text/html")
		default:
			// Empty body: just write the blank line.
			buf.WriteString("\r\n")
		}
	}

	return buf.Bytes(), nil
}

// normaliseBodyStructure converts a bodyStructure tree into the
// textBody/htmlBody form that buildEmailFromProperties expects.
// Handles the common cases clients send:
//   - Single leaf text/plain → TextBody
//   - Single leaf text/html → HtmlBody
//   - multipart/alternative with text/plain + text/html children → both
//   - multipart/mixed with text + blob-referenced attachment parts →
//     text parts normalised as above; blob parts stored in attachmentParts
//   - multipart/related with a multipart/alternative child + inline blob
//     parts → text/html via the nested alternative; inline blob parts
//     (disposition=inline with cid) stored in inlineParts
//   - multipart/mixed wrapping a multipart/related (body+inlines) + regular
//     attachments → inlines in inlineParts, attachments in attachmentParts
//
// Unknown or unsupported structures are mapped to a text/plain part
// with an empty body so the create does not error.
func normaliseBodyStructure(in *emailCreateInput) {
	bs := in.BodyStructure
	if bs == nil {
		return
	}
	walkBSNode(in, bs)
}

// walkBSNode recursively walks a single bodyStructure node and classifies
// its contents into in.TextBody, in.HtmlBody, in.inlineParts, and
// in.attachmentParts.
//
// Text/html body parts from the bodyStructure tree are only added when
// the caller has not already populated those lists via the direct
// textBody/htmlBody wire fields. The compose client sends both forms;
// only extracting blob (attachment/inline) parts from the tree and
// skipping duplicates avoids double-registering the same part IDs.
func walkBSNode(in *emailCreateInput, bs *emailBodyStructurePart) {
	ct := strings.ToLower(bs.Type)
	switch {
	case strings.HasPrefix(ct, "multipart/"):
		for i := range bs.SubParts {
			sub := &bs.SubParts[i]
			subCT := strings.ToLower(sub.Type)
			switch {
			case strings.HasPrefix(subCT, "text/plain") && sub.PartID != "":
				// Only add when not already present from the direct textBody field.
				if !hasBodyPartID(in.TextBody, sub.PartID) {
					in.TextBody = append(in.TextBody, emailBodyPart{PartID: sub.PartID, Type: sub.Type})
				}
			case strings.HasPrefix(subCT, "text/html") && sub.PartID != "":
				// Only add when not already present from the direct htmlBody field.
				if !hasBodyPartID(in.HtmlBody, sub.PartID) {
					in.HtmlBody = append(in.HtmlBody, emailBodyPart{PartID: sub.PartID, Type: sub.Type})
				}
			case strings.HasPrefix(subCT, "multipart/"):
				// Nested multipart (e.g. multipart/alternative inside
				// multipart/related or multipart/mixed): recurse to collect
				// text, html, and blob parts from the nested tree.
				walkBSNode(in, sub)
			case sub.BlobID != "":
				// Blob-referenced leaf part: classify by disposition.
				// disposition=inline with a non-empty cid → inline part (goes
				// into multipart/related so cid: refs in the HTML body resolve).
				// Everything else → regular attachment.
				if strings.EqualFold(sub.Disposition, "inline") && sub.Cid != "" {
					in.inlineParts = append(in.inlineParts, *sub)
				} else {
					in.attachmentParts = append(in.attachmentParts, *sub)
				}
			}
		}
	case strings.HasPrefix(ct, "text/plain"):
		if bs.PartID != "" && !hasBodyPartID(in.TextBody, bs.PartID) {
			in.TextBody = append(in.TextBody, emailBodyPart{PartID: bs.PartID, Type: bs.Type})
		}
	case strings.HasPrefix(ct, "text/html"):
		if bs.PartID != "" && !hasBodyPartID(in.HtmlBody, bs.PartID) {
			in.HtmlBody = append(in.HtmlBody, emailBodyPart{PartID: bs.PartID, Type: bs.Type})
		}
	default:
		// Unknown leaf content type: treat as text/plain so we have at
		// least one part. Only applicable to PartID-bearing leaves.
		if bs.PartID != "" && !hasBodyPartID(in.TextBody, bs.PartID) {
			in.TextBody = append(in.TextBody, emailBodyPart{PartID: bs.PartID, Type: "text/plain"})
		}
	}
}

// hasBodyPartID reports whether parts already contains an entry with the
// given partID.
func hasBodyPartID(parts []emailBodyPart, partID string) bool {
	for i := range parts {
		if parts[i].PartID == partID {
			return true
		}
	}
	return false
}

// writeLiteralHeader writes "Name: Value\r\n" with no additional folding.
// The caller is responsible for encoding the value (e.g. mime.QEncoding
// for non-ASCII subjects).
func writeLiteralHeader(buf *bytes.Buffer, name, value string) {
	buf.WriteString(name)
	buf.WriteString(": ")
	buf.WriteString(value)
	buf.WriteString("\r\n")
}

// writeTextPart writes the Content-Type / Content-Transfer-Encoding
// headers plus a blank line and the QP-encoded body for a single-part
// message.
func writeTextPart(buf *bytes.Buffer, value, contentType string) {
	writeLiteralHeader(buf, "Content-Type", contentType+"; charset=utf-8")
	writeLiteralHeader(buf, "Content-Transfer-Encoding", "quoted-printable")
	buf.WriteString("\r\n")
	qpBuf := &bytes.Buffer{}
	w := quotedprintable.NewWriter(qpBuf)
	_, _ = w.Write([]byte(value))
	_ = w.Close()
	buf.Write(qpBuf.Bytes())
}

// writeMultipartAlternative writes a multipart/alternative body with
// text/plain first and text/html second.
func writeMultipartAlternative(buf *bytes.Buffer, textVal, htmlVal string) error {
	// We need to write the Content-Type header with the boundary before
	// the multipart writer can render. Build the body into a temporary
	// buffer first so we know the boundary.
	var bodyBuf bytes.Buffer
	mw := multipart.NewWriter(&bodyBuf)

	// text/plain part
	textHeaders := textproto.MIMEHeader{}
	textHeaders.Set("Content-Type", "text/plain; charset=utf-8")
	textHeaders.Set("Content-Transfer-Encoding", "quoted-printable")
	textPart, err := mw.CreatePart(textHeaders)
	if err != nil {
		return err
	}
	qw := quotedprintable.NewWriter(textPart)
	if _, err := qw.Write([]byte(textVal)); err != nil {
		return err
	}
	if err := qw.Close(); err != nil {
		return err
	}

	// text/html part
	htmlHeaders := textproto.MIMEHeader{}
	htmlHeaders.Set("Content-Type", "text/html; charset=utf-8")
	htmlHeaders.Set("Content-Transfer-Encoding", "quoted-printable")
	htmlPart, err := mw.CreatePart(htmlHeaders)
	if err != nil {
		return err
	}
	qw = quotedprintable.NewWriter(htmlPart)
	if _, err := qw.Write([]byte(htmlVal)); err != nil {
		return err
	}
	if err := qw.Close(); err != nil {
		return err
	}

	if err := mw.Close(); err != nil {
		return err
	}

	// Write the Content-Type header with boundary into the top-level buffer.
	writeLiteralHeader(buf, "Content-Type",
		"multipart/alternative; boundary="+fmt.Sprintf("%q", mw.Boundary()))
	buf.WriteString("\r\n")
	buf.Write(bodyBuf.Bytes())
	return nil
}

// writeMultipartMixed assembles a multipart/mixed body containing the
// inline text/html body part(s) followed by blob-referenced attachment
// parts (RFC 8621 §4.6 attachment assembly).
func writeMultipartMixed(ctx context.Context, blobs store.Blobs, buf *bytes.Buffer, textVal, htmlVal string, hasText, hasHTML bool, attachments []emailBodyStructurePart) error {
	var bodyBuf bytes.Buffer
	mw := multipart.NewWriter(&bodyBuf)

	// Write the inline body as the first part.
	switch {
	case hasText && hasHTML:
		// Nest as multipart/alternative.
		altBoundary := generateBoundary()
		altHeaders := textproto.MIMEHeader{}
		altHeaders.Set("Content-Type", "multipart/alternative; boundary="+altBoundary)
		altPart, err := mw.CreatePart(altHeaders)
		if err != nil {
			return err
		}
		if err := writeMultipartAlternativeInto(altPart, altBoundary, textVal, htmlVal); err != nil {
			return err
		}
	case hasText:
		ph := textproto.MIMEHeader{}
		ph.Set("Content-Type", "text/plain; charset=utf-8")
		ph.Set("Content-Transfer-Encoding", "quoted-printable")
		p, err := mw.CreatePart(ph)
		if err != nil {
			return err
		}
		qw := quotedprintable.NewWriter(p)
		_, _ = qw.Write([]byte(textVal))
		_ = qw.Close()
	case hasHTML:
		ph := textproto.MIMEHeader{}
		ph.Set("Content-Type", "text/html; charset=utf-8")
		ph.Set("Content-Transfer-Encoding", "quoted-printable")
		p, err := mw.CreatePart(ph)
		if err != nil {
			return err
		}
		qw := quotedprintable.NewWriter(p)
		_, _ = qw.Write([]byte(htmlVal))
		_ = qw.Close()
	}

	// Append blob-referenced attachment parts.
	for _, att := range attachments {
		if err := writeBlobPartInto(ctx, blobs, mw, att); err != nil {
			// Skip unreadable blobs rather than aborting the whole create.
			continue
		}
	}

	if err := mw.Close(); err != nil {
		return err
	}

	writeLiteralHeader(buf, "Content-Type",
		"multipart/mixed; boundary="+fmt.Sprintf("%q", mw.Boundary()))
	buf.WriteString("\r\n")
	buf.Write(bodyBuf.Bytes())
	return nil
}

// writeBlobPartInto writes one blob-referenced MIME part (attachment or
// inline) into the given multipart.Writer. Content-Type, Content-Transfer-
// Encoding, Content-Disposition, and (for inline parts with a cid)
// Content-ID headers are written. Inline parts omit the filename parameter
// from Content-Disposition when no name is set, per RFC 2183.
//
// Returns an error if the blob cannot be fetched; the caller typically
// skips that part rather than aborting the whole assembly.
func writeBlobPartInto(ctx context.Context, blobs store.Blobs, mw *multipart.Writer, att emailBodyStructurePart) error {
	attData, err := readBlobBytes(ctx, blobs, att.BlobID)
	if err != nil {
		return err
	}
	ct := att.Type
	if ct == "" {
		ct = "application/octet-stream"
	}
	ph := textproto.MIMEHeader{}
	if att.Name != "" {
		ph.Set("Content-Type", ct+"; name="+mime.QEncoding.Encode("utf-8", att.Name))
	} else {
		ph.Set("Content-Type", ct)
	}
	ph.Set("Content-Transfer-Encoding", "base64")
	disp := att.Disposition
	if disp == "" {
		disp = "attachment"
	}
	if att.Name != "" {
		ph.Set("Content-Disposition", disp+"; filename="+mime.QEncoding.Encode("utf-8", att.Name))
	} else {
		ph.Set("Content-Disposition", disp)
	}
	// Write Content-ID for inline parts that carry a cid. The header value
	// must be wrapped in angle brackets per RFC 2392 §3 so cid: URLs of the
	// form "cid:<id>" resolve to "Content-ID: <<id>>" on the wire.
	if strings.EqualFold(disp, "inline") && att.Cid != "" {
		cid := att.Cid
		if !strings.HasPrefix(cid, "<") {
			cid = "<" + cid + ">"
		}
		ph.Set("Content-ID", cid)
	}
	p, err := mw.CreatePart(ph)
	if err != nil {
		return err
	}
	enc := base64.NewEncoder(base64.StdEncoding, p)
	_, _ = enc.Write(attData)
	_ = enc.Close()
	return nil
}

// writeMultipartRelated assembles a multipart/related body per RFC 2387.
// The root type is multipart/related; the first subpart is the body
// (multipart/alternative when both text and html are present; text/html
// when only html; text/plain when only text). The remaining subparts are
// the inline blob parts (images etc.) referenced by cid: in the HTML body.
func writeMultipartRelated(ctx context.Context, blobs store.Blobs, buf *bytes.Buffer, textVal, htmlVal string, hasText, hasHTML bool, inlines []emailBodyStructurePart) error {
	var bodyBuf bytes.Buffer
	mw := multipart.NewWriter(&bodyBuf)

	// First part: the body (alternative or plain text or html).
	switch {
	case hasText && hasHTML:
		altBoundary := generateBoundary()
		altHeaders := textproto.MIMEHeader{}
		altHeaders.Set("Content-Type", "multipart/alternative; boundary="+altBoundary)
		altPart, err := mw.CreatePart(altHeaders)
		if err != nil {
			return err
		}
		if err := writeMultipartAlternativeInto(altPart, altBoundary, textVal, htmlVal); err != nil {
			return err
		}
	case hasText:
		ph := textproto.MIMEHeader{}
		ph.Set("Content-Type", "text/plain; charset=utf-8")
		ph.Set("Content-Transfer-Encoding", "quoted-printable")
		p, err := mw.CreatePart(ph)
		if err != nil {
			return err
		}
		qw := quotedprintable.NewWriter(p)
		_, _ = qw.Write([]byte(textVal))
		_ = qw.Close()
	case hasHTML:
		ph := textproto.MIMEHeader{}
		ph.Set("Content-Type", "text/html; charset=utf-8")
		ph.Set("Content-Transfer-Encoding", "quoted-printable")
		p, err := mw.CreatePart(ph)
		if err != nil {
			return err
		}
		qw := quotedprintable.NewWriter(p)
		_, _ = qw.Write([]byte(htmlVal))
		_ = qw.Close()
	}

	// Remaining parts: inline blob parts.
	for _, il := range inlines {
		if err := writeBlobPartInto(ctx, blobs, mw, il); err != nil {
			// Skip unreadable blobs.
			continue
		}
	}

	if err := mw.Close(); err != nil {
		return err
	}

	writeLiteralHeader(buf, "Content-Type",
		"multipart/related; boundary="+fmt.Sprintf("%q", mw.Boundary()))
	buf.WriteString("\r\n")
	buf.Write(bodyBuf.Bytes())
	return nil
}

// writeMultipartMixedWithRelated assembles a multipart/mixed body that
// contains a multipart/related inner part (body + inline images) as the
// first child, followed by regular attachment parts. This is the structure
// required when the message has both inline images (referenced by cid: in
// the HTML body) and regular file attachments.
func writeMultipartMixedWithRelated(ctx context.Context, blobs store.Blobs, buf *bytes.Buffer, textVal, htmlVal string, hasText, hasHTML bool, inlines, attachments []emailBodyStructurePart) error {
	var outerBuf bytes.Buffer
	outerMW := multipart.NewWriter(&outerBuf)

	// First outer part: the multipart/related (body + inlines).
	var relBuf bytes.Buffer
	if err := writeMultipartRelated(ctx, blobs, &relBuf, textVal, htmlVal, hasText, hasHTML, inlines); err != nil {
		return err
	}
	// Extract the Content-Type from the beginning of relBuf so we can set
	// it as the sub-part header. writeMultipartRelated writes it as a
	// top-level header; for nesting we need to pass it to CreatePart.
	relCT := extractFirstHeaderValue(relBuf.Bytes(), "Content-Type")
	relHeaders := textproto.MIMEHeader{}
	if relCT != "" {
		relHeaders.Set("Content-Type", relCT)
	} else {
		relHeaders.Set("Content-Type", "multipart/related")
	}
	relPart, err := outerMW.CreatePart(relHeaders)
	if err != nil {
		return err
	}
	// Write the body of the related part (everything after the headers).
	relBody := skipHeaders(relBuf.Bytes())
	if _, err := relPart.Write(relBody); err != nil {
		return err
	}

	// Remaining outer parts: regular attachments.
	for _, att := range attachments {
		if err := writeBlobPartInto(ctx, blobs, outerMW, att); err != nil {
			// Skip unreadable blobs.
			continue
		}
	}

	if err := outerMW.Close(); err != nil {
		return err
	}

	writeLiteralHeader(buf, "Content-Type",
		"multipart/mixed; boundary="+fmt.Sprintf("%q", outerMW.Boundary()))
	buf.WriteString("\r\n")
	buf.Write(outerBuf.Bytes())
	return nil
}

// extractFirstHeaderValue extracts the value of the first occurrence of a
// named header from a raw RFC 5322 message prefix (header block only).
// Returns "" if the header is not found. Used by writeMultipartMixedWithRelated
// to re-use the boundary produced by writeMultipartRelated.
func extractFirstHeaderValue(raw []byte, name string) string {
	prefix := strings.ToLower(name) + ":"
	for _, line := range strings.Split(string(raw), "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), prefix) {
			return strings.TrimSpace(line[len(prefix):])
		}
		if line == "" {
			// End of headers.
			break
		}
	}
	return ""
}

// skipHeaders advances past the header block of a raw RFC 5322 message
// and returns the body. The header block ends at the first blank line
// (CRLF CRLF). If no blank line is found, the full input is returned.
func skipHeaders(raw []byte) []byte {
	sep := []byte("\r\n\r\n")
	idx := bytes.Index(raw, sep)
	if idx < 0 {
		return raw
	}
	return raw[idx+len(sep):]
}

// writeMultipartAlternativeInto writes text/plain + text/html parts into
// an existing io.Writer (for nesting inside multipart/mixed).
func writeMultipartAlternativeInto(w io.Writer, boundary, textVal, htmlVal string) error {
	fmt.Fprintf(w, "--%s\r\n", boundary)
	fmt.Fprintf(w, "Content-Type: text/plain; charset=utf-8\r\n")
	fmt.Fprintf(w, "Content-Transfer-Encoding: quoted-printable\r\n\r\n")
	qw := quotedprintable.NewWriter(w)
	_, _ = qw.Write([]byte(textVal))
	_ = qw.Close()
	fmt.Fprintf(w, "\r\n--%s\r\n", boundary)
	fmt.Fprintf(w, "Content-Type: text/html; charset=utf-8\r\n")
	fmt.Fprintf(w, "Content-Transfer-Encoding: quoted-printable\r\n\r\n")
	qw = quotedprintable.NewWriter(w)
	_, _ = qw.Write([]byte(htmlVal))
	_ = qw.Close()
	fmt.Fprintf(w, "\r\n--%s--\r\n", boundary)
	return nil
}

// generateBoundary returns a random MIME boundary string.
func generateBoundary() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// readBlobBytes fetches a blob by ID and returns its raw bytes.
func readBlobBytes(ctx context.Context, blobs store.Blobs, blobID string) ([]byte, error) {
	rc, err := blobs.Get(ctx, blobID)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, 64<<20))
}

// resolvePartID extracts the first partId from a bodyPart slice (textBody
// or htmlBody). Returns "" if the slice is empty or the first entry has
// no partId.
func resolvePartID(parts []emailBodyPart) string {
	if len(parts) == 0 {
		return ""
	}
	return parts[0].PartID
}

// bodyValueFor looks up partID in the bodyValues map. Returns ("", false)
// when partID is empty or absent.
func bodyValueFor(bv map[string]emailBodyValue, partID string) (string, bool) {
	if partID == "" || len(bv) == 0 {
		return "", false
	}
	v, ok := bv[partID]
	return v.Value, ok
}

// formatAddresses formats a slice of jmapAddress as an RFC 5322
// address-list string. Non-ASCII display names are encoded with RFC 2047.
func formatAddresses(addrs []emailAddress) string {
	parts := make([]string, 0, len(addrs))
	for _, a := range addrs {
		if a.Email == "" {
			continue
		}
		if a.Name == "" {
			parts = append(parts, a.Email)
		} else {
			encoded := mime.QEncoding.Encode("utf-8", a.Name)
			parts = append(parts, fmt.Sprintf("%s <%s>", encoded, a.Email))
		}
	}
	return strings.Join(parts, ", ")
}

// generateMessageID produces a random RFC 5322 Message-ID in angle
// brackets. Callers should supply a real hostname when available.
func generateMessageID(hostname string) string {
	if hostname == "" {
		hostname = "localhost"
	}
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	return "<" + base64.RawURLEncoding.EncodeToString(b) + "@" + hostname + ">"
}

// ensureAngleBrackets wraps s in angle brackets if it does not already
// start with "<".
func ensureAngleBrackets(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	if !strings.HasPrefix(s, "<") {
		return "<" + s + ">"
	}
	return s
}
