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

	hasAttachments := len(in.attachmentParts) > 0 && blobs != nil

	if hasAttachments {
		// multipart/mixed wraps the body part(s) + attachment(s).
		if err := writeMultipartMixed(ctx, blobs, &buf, textVal, htmlVal, hasText, hasHTML, in.attachmentParts); err != nil {
			return nil, fmt.Errorf("bodybuild: multipart/mixed: %w", err)
		}
	} else {
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
//
// Unknown or unsupported structures are mapped to a text/plain part
// with an empty body so the create does not error.
func normaliseBodyStructure(in *emailCreateInput) {
	bs := in.BodyStructure
	if bs == nil {
		return
	}
	ct := strings.ToLower(bs.Type)
	switch {
	case strings.HasPrefix(ct, "multipart/"):
		// Walk children and collect text/plain, text/html, and blob-attached parts.
		for i := range bs.SubParts {
			sub := &bs.SubParts[i]
			subCT := strings.ToLower(sub.Type)
			switch {
			case strings.HasPrefix(subCT, "text/plain") && sub.PartID != "":
				in.TextBody = append(in.TextBody, emailBodyPart{PartID: sub.PartID, Type: sub.Type})
			case strings.HasPrefix(subCT, "text/html") && sub.PartID != "":
				in.HtmlBody = append(in.HtmlBody, emailBodyPart{PartID: sub.PartID, Type: sub.Type})
			case sub.BlobID != "":
				// Blob-referenced part (attachment or inline blob). Store for
				// inclusion in the assembled MIME message.
				in.attachmentParts = append(in.attachmentParts, *sub)
			}
		}
	case strings.HasPrefix(ct, "text/plain"):
		if bs.PartID != "" {
			in.TextBody = append(in.TextBody, emailBodyPart{PartID: bs.PartID, Type: bs.Type})
		}
	case strings.HasPrefix(ct, "text/html"):
		if bs.PartID != "" {
			in.HtmlBody = append(in.HtmlBody, emailBodyPart{PartID: bs.PartID, Type: bs.Type})
		}
	default:
		// Unknown content type: treat as text/plain so we have at least one part.
		if bs.PartID != "" {
			in.TextBody = append(in.TextBody, emailBodyPart{PartID: bs.PartID, Type: "text/plain"})
		}
	}
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
		attData, err := readBlobBytes(ctx, blobs, att.BlobID)
		if err != nil {
			// Skip unreadable blobs rather than aborting the whole create.
			continue
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
		p, err := mw.CreatePart(ph)
		if err != nil {
			return err
		}
		enc := base64.NewEncoder(base64.StdEncoding, p)
		_, _ = enc.Write(attData)
		_ = enc.Close()
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
