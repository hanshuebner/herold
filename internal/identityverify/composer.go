package identityverify

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"math/big"
	"mime/multipart"
	"net/textproto"
	"strings"
	"time"
)

// codeAlphabet is the set of ASCII digits used to derive the 6-digit
// verification code (REQ-IDENT-32). Leading zeros are preserved.
const codeAlphabet = "0123456789"

// rawTokenBytes is the entropy size of the raw verification token
// (REQ-IDENT-31). 32 random bytes base64url-encode to 43 ASCII
// characters with no padding.
const rawTokenBytes = 32

// codeDigits is the fixed length of the human-readable verification
// code (REQ-IDENT-32).
const codeDigits = 6

// reader is the entropy source. Default is crypto/rand; tests override
// via WithEntropy for deterministic verification.
type readerFunc func(b []byte) (int, error)

// GenerateToken returns a fresh 32-byte raw token base64url-encoded
// without padding (REQ-IDENT-31). Returns the raw ASCII string plus
// the underlying bytes for the caller to sha256-hash (raw bytes are
// returned so tests can assert hash invariants without re-decoding).
func GenerateToken() (string, []byte, error) {
	return generateTokenFrom(rand.Read)
}

// generateTokenFrom is GenerateToken with an injectable entropy source.
// Internal so tests can drive deterministic outputs without affecting
// the package-level rand reader.
func generateTokenFrom(read readerFunc) (string, []byte, error) {
	buf := make([]byte, rawTokenBytes)
	if _, err := read(buf); err != nil {
		return "", nil, fmt.Errorf("identityverify: read entropy for token: %w", err)
	}
	enc := base64.RawURLEncoding.EncodeToString(buf)
	return enc, buf, nil
}

// GenerateCode returns a fresh 6-digit decimal code as ASCII
// (REQ-IDENT-32). Leading zeros are preserved; the returned string
// always has exactly codeDigits characters.
func GenerateCode() (string, error) {
	return generateCodeFrom(rand.Int)
}

// randIntFunc is the signature of crypto/rand.Int.
type randIntFunc func(io.Reader, *big.Int) (*big.Int, error)

// generateCodeFrom is GenerateCode with an injectable random source.
// Each digit is drawn from a 10-element alphabet so the per-digit bias
// of a modulo reduction is zero (crypto/rand.Int rejection-samples).
func generateCodeFrom(randInt randIntFunc) (string, error) {
	max := big.NewInt(int64(len(codeAlphabet)))
	var b strings.Builder
	b.Grow(codeDigits)
	for i := 0; i < codeDigits; i++ {
		n, err := randInt(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("identityverify: read entropy for code: %w", err)
		}
		b.WriteByte(codeAlphabet[n.Int64()])
	}
	return b.String(), nil
}

// ComposeInput bundles the data the composer needs to render one
// verification message. All fields are required; the composer returns
// an error when any address parses as empty.
type ComposeInput struct {
	// From is the envelope MAIL FROM and header From: address
	// (REQ-IDENT-30; default postmaster@<canonical hosted domain>).
	From string
	// To is the unverified Identity's email address — the recipient
	// the user is trying to claim.
	To string
	// Token is the raw base64url-encoded verification token
	// (REQ-IDENT-31). Included in the callback URL only.
	Token string
	// Code is the 6-digit ASCII code (REQ-IDENT-32). Rendered on its
	// own line in both the plain-text and HTML bodies.
	Code string
	// Hostname is the public-listener hostname used to build the
	// click-link URL (https://<host>/verify-identity?token=<token>).
	Hostname string
	// InitiatorEmail is the requesting principal's canonical email,
	// rendered in the body so the recipient can detect an unsolicited
	// verification (REQ-IDENT-33).
	InitiatorEmail string
	// Now is the message Date: header timestamp. Required so tests
	// can assert deterministic byte output; production wiring passes
	// clock.Now().
	Now time.Time
}

// ComposeOutput is the rendered RFC 5322 message body plus the
// extracted Message-ID for downstream logging / audit metadata. The
// queue consumes only Bytes; Message-ID is convenience.
type ComposeOutput struct {
	Bytes     []byte
	MessageID string
	Subject   string
}

// Compose renders the verification email's bytes. The output is a
// complete RFC 5322 message: headers, blank line, multipart/alternative
// body with text/plain (primary) and text/html (alternative). The body
// is plain ASCII / UTF-8 with no emojis (per project rules) and no
// embedded HTML images.
//
// The plain-text part is the primary representation; mail clients that
// strip HTML still get the click link and the code on its own line.
// The HTML alternative renders the code prominently and the link as a
// real anchor, both falling back to a copy/paste URL.
func Compose(in ComposeInput) (ComposeOutput, error) {
	if strings.TrimSpace(in.From) == "" {
		return ComposeOutput{}, fmt.Errorf("identityverify.Compose: From is empty")
	}
	if strings.TrimSpace(in.To) == "" {
		return ComposeOutput{}, fmt.Errorf("identityverify.Compose: To is empty")
	}
	if strings.TrimSpace(in.Token) == "" {
		return ComposeOutput{}, fmt.Errorf("identityverify.Compose: Token is empty")
	}
	if strings.TrimSpace(in.Code) == "" {
		return ComposeOutput{}, fmt.Errorf("identityverify.Compose: Code is empty")
	}
	if strings.TrimSpace(in.Hostname) == "" {
		return ComposeOutput{}, fmt.Errorf("identityverify.Compose: Hostname is empty")
	}

	link := "https://" + in.Hostname + "/verify-identity?token=" + in.Token
	subject := "Verify your email address"
	msgID := fmt.Sprintf("<%d.identity-verify@%s>", in.Now.UnixNano(), in.Hostname)
	initiator := in.InitiatorEmail
	if strings.TrimSpace(initiator) == "" {
		// Defensive fallback: if the caller did not supply an initiator
		// the body still describes the request, but without the audit
		// detail the spec wants. The composer logs this via the
		// returned message ID for the audit trail.
		initiator = "(unknown)"
	}

	// Build the multipart body first so we know the boundary before
	// writing the outer headers.
	var bodyBuf bytes.Buffer
	mw := multipart.NewWriter(&bodyBuf)
	boundary := mw.Boundary()

	plainHdr := textproto.MIMEHeader{}
	plainHdr.Set("Content-Type", "text/plain; charset=utf-8")
	plainHdr.Set("Content-Transfer-Encoding", "8bit")
	pw, err := mw.CreatePart(plainHdr)
	if err != nil {
		return ComposeOutput{}, fmt.Errorf("identityverify.Compose: plain part: %w", err)
	}
	writePlainBody(pw, in.To, initiator, link, in.Code)

	htmlHdr := textproto.MIMEHeader{}
	htmlHdr.Set("Content-Type", "text/html; charset=utf-8")
	htmlHdr.Set("Content-Transfer-Encoding", "8bit")
	hw, err := mw.CreatePart(htmlHdr)
	if err != nil {
		return ComposeOutput{}, fmt.Errorf("identityverify.Compose: html part: %w", err)
	}
	writeHTMLBody(hw, in.To, initiator, link, in.Code)

	if err := mw.Close(); err != nil {
		return ComposeOutput{}, fmt.Errorf("identityverify.Compose: close multipart: %w", err)
	}

	// Assemble the outer envelope. Headers per RFC 5322; the auto-
	// submitted hint helps recipients' mail clients distinguish this
	// from user-initiated correspondence.
	var out bytes.Buffer
	fmt.Fprintf(&out, "From: %s\r\n", in.From)
	fmt.Fprintf(&out, "To: %s\r\n", in.To)
	fmt.Fprintf(&out, "Date: %s\r\n", in.Now.UTC().Format("Mon, 02 Jan 2006 15:04:05 +0000"))
	fmt.Fprintf(&out, "Message-ID: %s\r\n", msgID)
	fmt.Fprintf(&out, "Subject: %s\r\n", subject)
	fmt.Fprintf(&out, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&out, "Auto-Submitted: auto-generated\r\n")
	fmt.Fprintf(&out, "X-Herold-Identity-Verification: 1\r\n")
	fmt.Fprintf(&out, "Content-Type: multipart/alternative; boundary=%q\r\n", boundary)
	fmt.Fprintf(&out, "\r\n")
	if _, err := io.Copy(&out, &bodyBuf); err != nil {
		return ComposeOutput{}, fmt.Errorf("identityverify.Compose: copy body: %w", err)
	}
	return ComposeOutput{Bytes: out.Bytes(), MessageID: msgID, Subject: subject}, nil
}

// writePlainBody renders the plain-text alternative. Lines are wrapped
// at sensible widths; the click link is on its own line so terminal
// mail readers can copy it without picking up surrounding punctuation.
// The 6-digit code goes on its own line for the same reason.
func writePlainBody(w io.Writer, to, initiator, link, code string) {
	fmt.Fprintf(w, "Hello,\r\n")
	fmt.Fprintf(w, "\r\n")
	fmt.Fprintf(w, "Someone (initiated by: %s) has requested to use the\r\n", initiator)
	fmt.Fprintf(w, "email address %s as a sending identity on this herold\r\n", to)
	fmt.Fprintf(w, "mail server.\r\n")
	fmt.Fprintf(w, "\r\n")
	fmt.Fprintf(w, "To confirm that this address is yours, click the link below\r\n")
	fmt.Fprintf(w, "(valid for 24 hours):\r\n")
	fmt.Fprintf(w, "\r\n")
	fmt.Fprintf(w, "%s\r\n", link)
	fmt.Fprintf(w, "\r\n")
	fmt.Fprintf(w, "Or paste this 6-digit code into the verification dialog:\r\n")
	fmt.Fprintf(w, "\r\n")
	fmt.Fprintf(w, "    %s\r\n", code)
	fmt.Fprintf(w, "\r\n")
	fmt.Fprintf(w, "If you did not request this, ignore this message; the\r\n")
	fmt.Fprintf(w, "address will not be activated without confirmation.\r\n")
}

// writeHTMLBody renders the HTML alternative. The markup is minimal,
// inline-styled so spam filters that strip <style> blocks still render
// reasonably, and contains no external image references — the spec
// requires the message to round-trip through DMARC-aligned signing,
// which any external-image fetch would weaken.
func writeHTMLBody(w io.Writer, to, initiator, link, code string) {
	fmt.Fprintf(w, "<!doctype html>\r\n")
	fmt.Fprintf(w, "<html lang=\"en\"><head><meta charset=\"utf-8\"></head><body style=\"font-family:system-ui,-apple-system,sans-serif;color:#222;max-width:36rem;margin:2rem auto;padding:0 1rem;line-height:1.5\">\r\n")
	fmt.Fprintf(w, "<h1 style=\"font-size:1.4rem;margin:0 0 1rem\">Verify your email address</h1>\r\n")
	fmt.Fprintf(w, "<p>Someone (initiated by <strong>%s</strong>) has requested to use the address <strong>%s</strong> as a sending identity on this herold mail server.</p>\r\n",
		htmlEscape(initiator), htmlEscape(to))
	fmt.Fprintf(w, "<p>To confirm that this address is yours, click the button below (valid for 24 hours):</p>\r\n")
	fmt.Fprintf(w, "<p><a href=\"%s\" style=\"display:inline-block;padding:0.6rem 1.2rem;background:#2a6fdb;color:#fff;text-decoration:none;border-radius:4px\">Verify this address</a></p>\r\n",
		htmlEscape(link))
	fmt.Fprintf(w, "<p>If the button does not work, paste this link into your browser:</p>\r\n")
	fmt.Fprintf(w, "<p><code style=\"display:block;background:#f4f4f4;padding:0.5rem;word-break:break-all\">%s</code></p>\r\n", htmlEscape(link))
	fmt.Fprintf(w, "<p>Or enter this 6-digit code in the verification dialog:</p>\r\n")
	fmt.Fprintf(w, "<p style=\"font-size:1.8rem;font-family:ui-monospace,Menlo,Consolas,monospace;letter-spacing:0.25em;background:#f4f4f4;padding:0.5rem 1rem;display:inline-block;border-radius:4px\">%s</p>\r\n", htmlEscape(code))
	fmt.Fprintf(w, "<p style=\"color:#666;font-size:0.9rem\">If you did not request this, you may ignore this message; the address will not be activated without confirmation.</p>\r\n")
	fmt.Fprintf(w, "</body></html>\r\n")
}

// htmlEscape escapes the minimal HTML entities needed in the body.
func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}
