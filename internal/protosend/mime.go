package protosend

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"mime/quotedprintable"
	"net/mail"
	"sort"
	"strings"
	"time"
)

// builtMessage is the result of assembling an RFC 5322 message from a
// structured /send request: full bytes plus the generated Message-ID
// (without angle brackets).
type builtMessage struct {
	bytes     []byte
	messageID string
}

// buildStructuredMessage renders an RFC 5322 message from req for the
// given source domain and now. The shape:
//
//   - text-only       -> single text/plain part with QP encoding.
//   - html-only       -> single text/html part with QP encoding.
//   - text + html     -> multipart/alternative with both parts.
//
// The function deliberately keeps the surface narrow: no attachments
// (REQ-SEND-10 lists attachments but v1 ships text/html only; raw
// upload covers attachment-heavy senders). Custom headers from
// req.Message.Body.Headers are merged on top of the generated set with
// the request taking precedence.
func buildStructuredMessage(req sendRequest, hostname string, now time.Time) (builtMessage, error) {
	domain := hostname
	if at := strings.LastIndexByte(req.Source, '@'); at >= 0 {
		domain = req.Source[at+1:]
	}
	msgID, err := newMessageID(domain)
	if err != nil {
		return builtMessage{}, err
	}
	headers := map[string]string{
		"From":         req.Source,
		"Date":         now.UTC().Format(time.RFC1123Z),
		"Message-ID":   "<" + msgID + ">",
		"Subject":      encodeHeaderValue(req.Message.Subject),
		"MIME-Version": "1.0",
	}
	if to := joinAddresses(req.Destination.ToAddresses); to != "" {
		headers["To"] = to
	}
	if cc := joinAddresses(req.Destination.CCAddresses); cc != "" {
		headers["Cc"] = cc
	}
	if req.ConfigurationSet != "" {
		headers["X-Herold-Configuration-Set"] = req.ConfigurationSet
	}
	for _, t := range req.Tags {
		// Tag names have a constrained alphabet (REQ-SEND-13). Reject
		// names that would corrupt the header set; the handler validates
		// before we get here, but we defensively skip empties.
		if t.Name == "" {
			continue
		}
		headers["X-Herold-Tag-"+sanitizeHeaderName(t.Name)] = t.Value
	}
	// Caller-supplied headers override. Reject hop-by-hop control
	// headers (the queue / signer owns them).
	for k, v := range req.Message.Body.Headers {
		canon := canonHeaderName(k)
		if isReservedHeader(canon) {
			continue
		}
		headers[canon] = v
	}

	var buf bytes.Buffer
	hasText := req.Message.Body.Text != ""
	hasHTML := req.Message.Body.HTML != ""

	switch {
	case hasText && hasHTML:
		boundary, err := newBoundary()
		if err != nil {
			return builtMessage{}, err
		}
		headers["Content-Type"] = `multipart/alternative; boundary="` + boundary + `"`
		writeHeaders(&buf, headers)
		buf.WriteString("\r\n")
		writeAlternativePart(&buf, boundary, "text/plain; charset=utf-8", req.Message.Body.Text)
		writeAlternativePart(&buf, boundary, "text/html; charset=utf-8", req.Message.Body.HTML)
		fmt.Fprintf(&buf, "--%s--\r\n", boundary)
	case hasHTML:
		headers["Content-Type"] = "text/html; charset=utf-8"
		headers["Content-Transfer-Encoding"] = "quoted-printable"
		writeHeaders(&buf, headers)
		buf.WriteString("\r\n")
		writeQP(&buf, req.Message.Body.HTML)
	default:
		// text-only or empty body; even empty bodies get a text/plain
		// stanza so receivers don't see a header-only message.
		headers["Content-Type"] = "text/plain; charset=utf-8"
		headers["Content-Transfer-Encoding"] = "quoted-printable"
		writeHeaders(&buf, headers)
		buf.WriteString("\r\n")
		writeQP(&buf, req.Message.Body.Text)
	}
	return builtMessage{bytes: buf.Bytes(), messageID: msgID}, nil
}

// inspectRawMessage scans raw RFC 5322 bytes and returns the existing
// Message-ID (or empty if absent). It also returns the full message
// bytes with prepended Date / Message-ID / From if those headers are
// missing. The current function only mints Message-ID and Date when
// missing; From is left to the caller (REQ-SEND-02 spec).
func inspectRawMessage(raw []byte, hostname, defaultFrom string, now time.Time) ([]byte, string, error) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, "", fmt.Errorf("parse raw message: %w", err)
	}
	existingMsgID := stripAngleBrackets(msg.Header.Get("Message-ID"))
	existingDate := msg.Header.Get("Date")
	existingFrom := msg.Header.Get("From")

	var prepend bytes.Buffer
	if existingMsgID == "" {
		domain := hostname
		if at := strings.LastIndexByte(defaultFrom, '@'); at >= 0 {
			domain = defaultFrom[at+1:]
		}
		mid, err := newMessageID(domain)
		if err != nil {
			return nil, "", err
		}
		fmt.Fprintf(&prepend, "Message-ID: <%s>\r\n", mid)
		existingMsgID = mid
	}
	if existingDate == "" {
		fmt.Fprintf(&prepend, "Date: %s\r\n", now.UTC().Format(time.RFC1123Z))
	}
	if existingFrom == "" && defaultFrom != "" {
		fmt.Fprintf(&prepend, "From: %s\r\n", defaultFrom)
	}
	if prepend.Len() == 0 {
		return raw, existingMsgID, nil
	}
	out := make([]byte, 0, prepend.Len()+len(raw))
	out = append(out, prepend.Bytes()...)
	out = append(out, raw...)
	return out, existingMsgID, nil
}

func writeHeaders(buf *bytes.Buffer, h map[string]string) {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(buf, "%s: %s\r\n", k, h[k])
	}
}

func writeAlternativePart(buf *bytes.Buffer, boundary, contentType, body string) {
	fmt.Fprintf(buf, "--%s\r\n", boundary)
	fmt.Fprintf(buf, "Content-Type: %s\r\n", contentType)
	fmt.Fprintf(buf, "Content-Transfer-Encoding: quoted-printable\r\n\r\n")
	writeQP(buf, body)
}

func writeQP(buf *bytes.Buffer, s string) {
	w := quotedprintable.NewWriter(buf)
	_, _ = w.Write([]byte(s))
	_ = w.Close()
	// Ensure trailing CRLF so the next boundary / EOM lands cleanly.
	if !bytes.HasSuffix(buf.Bytes(), []byte("\r\n")) {
		buf.WriteString("\r\n")
	}
}

// joinAddresses formats addresses for an RFC 5322 header. Empty input
// returns "".
func joinAddresses(addrs []string) string {
	if len(addrs) == 0 {
		return ""
	}
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		out = append(out, a)
	}
	return strings.Join(out, ", ")
}

// encodeHeaderValue applies RFC 2047 encoded-word encoding when the
// input contains non-ASCII bytes; otherwise returns it verbatim.
func encodeHeaderValue(s string) string {
	for _, r := range s {
		if r > 0x7e || r < 0x20 {
			// Use base64 encoded-word for safety.
			return "=?utf-8?B?" + base64URLOk(s) + "?="
		}
	}
	return s
}

func base64URLOk(s string) string {
	// stdlib mime.BEncoding.Encode is correct here, but importing mime
	// for one function bloats the dep graph minimally; explicit base64
	// keeps the function readable.
	const tbl = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	src := []byte(s)
	var out strings.Builder
	for i := 0; i < len(src); i += 3 {
		var b [3]byte
		n := copy(b[:], src[i:])
		out.WriteByte(tbl[b[0]>>2])
		out.WriteByte(tbl[((b[0]&0x03)<<4)|(b[1]>>4)])
		if n > 1 {
			out.WriteByte(tbl[((b[1]&0x0f)<<2)|(b[2]>>6)])
		} else {
			out.WriteByte('=')
		}
		if n > 2 {
			out.WriteByte(tbl[b[2]&0x3f])
		} else {
			out.WriteByte('=')
		}
	}
	return out.String()
}

// canonHeaderName uppercases the first letter and each letter following
// a '-' (RFC 5322 §2.2 says headers are case-insensitive but we keep
// the canonical capitalization for readability).
func canonHeaderName(k string) string {
	b := []byte(strings.ToLower(k))
	upper := true
	for i := range b {
		if upper && b[i] >= 'a' && b[i] <= 'z' {
			b[i] -= 32
		}
		upper = b[i] == '-'
	}
	return string(b)
}

// sanitizeHeaderName strips characters that are illegal in an HTTP /
// RFC 5322 header field name (anything outside the printable ASCII
// alnum + '-' set). Used for X-Herold-Tag-<name>.
func sanitizeHeaderName(s string) string {
	var out strings.Builder
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			out.WriteRune(r)
		default:
			out.WriteRune('-')
		}
	}
	return out.String()
}

// isReservedHeader reports whether the canonicalised header name is
// owned by the queue / signer and may not be supplied by the caller.
func isReservedHeader(canon string) bool {
	switch canon {
	case "From", "Date", "Message-Id", "Mime-Version",
		"Content-Type", "Content-Transfer-Encoding",
		"Dkim-Signature", "Arc-Authentication-Results",
		"Arc-Message-Signature", "Arc-Seal",
		"Received":
		return true
	}
	return false
}

func stripAngleBrackets(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "<")
	s = strings.TrimSuffix(s, ">")
	return s
}

func newMessageID(domain string) (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("message-id: %w", err)
	}
	if domain == "" {
		domain = "localhost"
	}
	return hex.EncodeToString(b[:]) + "@" + strings.ToLower(domain), nil
}

func newBoundary() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("boundary: %w", err)
	}
	return "herold_" + hex.EncodeToString(b[:]), nil
}
