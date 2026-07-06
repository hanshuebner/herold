package store

import "time"

// This file declares the types used by the admin message-research surface
// (REQ-ADM-306). The surface is a retrospective per-message tracer that
// joins three sources: received mail from the messages store, inbound
// accept/reject/defer trail from the system_events ring-buffer, and
// outbound send outcomes from the queue table.
//
// Envelope metadata and disposition ONLY — never message bodies or
// attachment content (REQ-ADM-306 security constraint).

// AdminMessageFilter narrows a SearchAdminMessages read. All fields are
// AND-combined; zero values are unconstrained.
type AdminMessageFilter struct {
	// Sender, when non-empty, restricts to messages where the From header
	// contains this substring (case-insensitive).
	Sender string
	// Recipient, when non-empty, restricts to messages where the To header
	// contains this substring (case-insensitive).
	Recipient string
	// MessageID, when non-empty, matches messages with this exact
	// Message-ID header value (case-insensitive).
	MessageID string
	// Subject, when non-empty, restricts to messages where the Subject
	// header contains this substring (case-insensitive).
	Subject string
	// DateFrom, when non-zero, restricts to messages with received_at >= DateFrom.
	DateFrom time.Time
	// DateTo, when non-zero, restricts to messages with received_at < DateTo.
	DateTo time.Time
	// Domains, when non-nil, restricts to messages whose principal's email
	// domain matches any entry in the slice (REQ-ADM-307 scope). nil =
	// unrestricted (super-admin). Non-nil empty = no results (fail-closed).
	Domains []string
	// Limit caps the result set. 0 = default (100). Max 1000.
	Limit int
	// BeforeReceivedUs is the keyset cursor: restrict to messages with
	// received_at_us < BeforeReceivedUs. 0 = no cursor.
	BeforeReceivedUs int64
}

// AdminMessageHit is one result from SearchAdminMessages. It carries the
// message envelope and disposition — never body content (REQ-ADM-306).
type AdminMessageHit struct {
	// MessageID is the store primary key.
	MessageID MessageID
	// PrincipalID is the owning principal.
	PrincipalID PrincipalID
	// ReceivedAt is the instant the message was accepted by the server.
	ReceivedAt time.Time
	// Envelope contains the message envelope fields (no body content).
	Envelope Envelope
	// MailboxName is the name of the primary delivery mailbox (the first
	// mailbox, by mailbox_id, that the message was placed into).
	MailboxName string
	// IsJunk is true when any mailbox the message resides in carries the
	// Junk special-use attribute (REQ-ADM-306 disposition signal).
	IsJunk bool
	// SpamVerdict is the classifier verdict from llm_classifications
	// ("ham", "spam", "suspect", "unclassified"); nil when the spam
	// classifier was not run for this message.
	SpamVerdict *string
	// SpamConfidence is the [0,1] confidence score from the spam
	// classifier; nil when the classifier was not run.
	SpamConfidence *float64
}
