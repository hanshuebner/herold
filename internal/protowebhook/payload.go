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
}

// Envelope mirrors REQ-HOOK-10 — a small projection of the cached
// store.Envelope plus the recipient list. Bcc is omitted by design.
type Envelope struct {
	From    string   `json:"from"`
	To      []string `json:"to,omitempty"`
	Subject string   `json:"subject"`
}

// Body is the content section. Exactly one of Inline / FetchURL is
// populated, matching Mode.
type Body struct {
	Mode     string      `json:"mode"`
	Inline   *InlineBody `json:"inline,omitempty"`
	FetchURL *FetchURL   `json:"fetch_url,omitempty"`
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
