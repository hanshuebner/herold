package protowebhook

import (
	"github.com/hanshuebner/herold/internal/store"
)

// Payload is the JSON shape POSTed for a mail-arrival event. Field
// names match REQ-HOOK-10/20/30 verbatim so receivers can be written
// against the spec without reading source.
type Payload struct {
	ID          string      `json:"id"`
	Event       string      `json:"event"`
	WebhookID   string      `json:"webhook_id"`
	OccurredAt  string      `json:"occurred_at"`
	PrincipalID string      `json:"principal_id"`
	MailboxID   string      `json:"mailbox_id"`
	MessageID   string      `json:"message_id"`
	Envelope    Envelope    `json:"envelope"`
	Body        Body        `json:"body"`
	AuthResults AuthResults `json:"auth_results"`
	// RouteTag carries the value the directory.resolve_rcpt plugin
	// returned at RCPT TO time when the recipient was synthetic
	// (REQ-DIR-RCPT-07, REQ-HOOK-02 synthetic match).  Empty when the
	// recipient was a real principal or no plugin set the field.
	//
	// TODO(3.5c-coord): the dispatcher reads this from the change-feed
	// entry once Track A surfaces synthetic-recipient session state on
	// the message row; until then this stays empty.
	RouteTag string `json:"route_tag,omitempty"`
	// Attachments is the list of attachment metadata; populated when
	// the message has any non-text non-multipart parts.  Each entry
	// carries a fetch URL so receivers download the bytes out-of-band.
	// Always present in extracted mode (REQ-HOOK-EXTRACTED-01); also
	// emitted in inline / url modes to match the spec's wire shape
	// (REQ-HOOK-21).
	Attachments []Attachment `json:"attachments,omitempty"`
	// RawRFC822URL is the signed fetch URL for the original
	// message/rfc822 bytes.  Always available in extracted mode so
	// receivers that need MIME can still get to it; populated in
	// inline / url modes per the existing contract.
	RawRFC822URL string `json:"raw_rfc822_url,omitempty"`
}

// Envelope mirrors REQ-HOOK-10 — a small projection of the cached
// store.Envelope plus the recipient list. Bcc is omitted by design.
type Envelope struct {
	From    string   `json:"from"`
	To      []string `json:"to,omitempty"`
	Subject string   `json:"subject"`
}

// Body is the content section. The precise shape depends on Mode:
//   - "inline"    — Inline carries the raw RFC 5322 body, base64.
//   - "fetch_url" — FetchURL carries a signed URL the receiver GETs.
//   - "extracted" — Text / TextOrigin / TextTruncated carry the
//     server-rendered plain text per REQ-HOOK-EXTRACTED-01..02.
//
// TextOrigin is REQUIRED in extracted mode and OPTIONAL otherwise; it
// is omitted from inline / url payloads to preserve the Phase-2 wire
// contract.
type Body struct {
	Mode          string      `json:"mode"`
	Inline        *InlineBody `json:"inline,omitempty"`
	FetchURL      *FetchURL   `json:"fetch_url,omitempty"`
	Text          string      `json:"text,omitempty"`
	TextOrigin    string      `json:"text_origin,omitempty"`
	TextTruncated bool        `json:"text_truncated,omitempty"`
}

// InlineBody carries the RFC 5322 body bytes base64-encoded. Size is
// the original (pre-encoding) byte count — receivers do not have to
// decode just to learn the size.
type InlineBody struct {
	RawBase64 string `json:"raw_base64"`
	Size      int64  `json:"size"`
}

// FetchURL is the signed URL the receiver GETs to retrieve the body.
type FetchURL struct {
	URL       string `json:"url"`
	ExpiresAt string `json:"expires_at"`
}

// Attachment is the per-part metadata emitted for every non-text
// non-multipart leaf in the parsed MIME tree (REQ-HOOK-21).  Bytes are
// never inlined; the receiver fetches via FetchURL.
type Attachment struct {
	Filename    string `json:"filename,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Size        int64  `json:"size"`
	FetchURL    string `json:"fetch_url"`
}

// AuthResults is the projected mail-auth verdict shape, four
// short string fields. We project from the stored Envelope's verdict
// columns (currently absent in Phase 1's store types — the existing
// Authentication-Results header is the only source of truth and it
// lives in the message body). For now we return wire tokens parsed off
// the Envelope's cached data when we have it; otherwise empty.
type AuthResults struct {
	DKIM        string `json:"dkim,omitempty"`
	SPF         string `json:"spf,omitempty"`
	DMARC       string `json:"dmarc,omitempty"`
	SpamVerdict string `json:"spam_verdict,omitempty"`
}

// extractAuthResults projects whatever auth verdicts the store has
// cached for the message into the wire shape. The store does not yet
// carry a structured AuthResults column; we leave the fields empty
// when the data is not on hand. A future wave wires the parsed
// Authentication-Results header through here without payload-shape
// churn — the JSON keys are stable.
func extractAuthResults(_ store.Message) AuthResults {
	return AuthResults{}
}
