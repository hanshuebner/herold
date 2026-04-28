package email

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/textproto"
	"strings"
	"time"
)

// buildEmailFromProperties assembles an RFC 5322 message from the inline
// creation properties the Suite sends (bodyValues + textBody/htmlBody
// plus header-level properties). This is the "construct from properties"
// path defined by RFC 8621 §4.6.
//
// The returned []byte is a fully-formed RFC 5322 message ready to be
// stored in the blob store.
func buildEmailFromProperties(in emailCreateInput, now time.Time, hostname string) ([]byte, error) {
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

	return buf.Bytes(), nil
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
