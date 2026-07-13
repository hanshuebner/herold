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

// MessageDeliveryDisposition records the mailbox disposition the SMTP
// ingest path decided for a message at the moment it was first stored.
// It is written once, by the delivery caller, alongside the InsertMessage
// call that creates the row (internal/protosmtp/deliver.go) and is never
// recomputed later from current mailbox membership -- a subsequent move,
// refile, or IMAP/JMAP mailbox change does not alter the recorded value.
// This is the fix for re #143: message research previously derived
// "was this junk" from whichever mailboxes the message currently belongs
// to, so filing it out of Junk after the fact silently rewrote the
// retrospective trace.
//
// The two recorded values mirror the only two dispositions the SMTP
// ingest path actually decides between when it resolves Sieve fileinto
// targets: the chosen mailbox either carries the Junk special-use
// attribute or it does not (RFC 5228 fileinto; store.MailboxAttrJunk).
// There is no separate "quarantined" disposition in the ingest path today
// -- DMARC/Sieve reject and defer outcomes never create a messages row at
// all (they surface as system_events "smtp.rcpt.resolve" entries, the
// second message-research source) -- so no such value is defined here.
type MessageDeliveryDisposition string

const (
	// DeliveryDispositionUnknown is the zero value: the row predates
	// disposition recording (migration 0090), or was written by a path
	// that is not an SMTP-ingest decision (JMAP import, IMAP APPEND,
	// bulk mailbox import, Sieve redirect fan-out, RethreadPrincipal
	// rewrites). Rendered as an explicit "not recorded", never
	// back-filled from current mailbox state.
	DeliveryDispositionUnknown MessageDeliveryDisposition = ""
	// DeliveryDispositionInbox means the ingest path filed the message
	// into a mailbox that does not carry the Junk special-use attribute
	// (the ordinary case: Inbox, or any other Sieve fileinto target).
	DeliveryDispositionInbox MessageDeliveryDisposition = "delivered_inbox"
	// DeliveryDispositionJunk means the ingest path filed the message
	// into a mailbox carrying the Junk special-use attribute.
	DeliveryDispositionJunk MessageDeliveryDisposition = "delivered_junk"
)

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
	// Disposition is the recorded-at-ingest delivery disposition (re
	// #143). This is the authoritative, immutable forensic fact: what
	// the SMTP ingest path decided when the message was accepted.
	// DeliveryDispositionUnknown means no value was recorded (row
	// predates migration 0090, or was written by a non-SMTP-ingest
	// path) -- rendered as "not recorded", never inferred from current
	// mailbox state.
	Disposition MessageDeliveryDisposition
	// MailboxName is the name of the message's current primary mailbox
	// (the first mailbox, by mailbox_id, that the message currently
	// belongs to). This is LIVE state: it reflects moves, refiles, and
	// Sieve/IMAP/JMAP mailbox changes made after delivery, and can
	// differ from Disposition. Use Disposition for "what happened at
	// delivery"; use MailboxName only for "where is it now".
	MailboxName string
	// IsJunk is true when any mailbox the message currently resides in
	// carries the Junk special-use attribute. Like MailboxName, this is
	// LIVE state, not the recorded disposition -- see Disposition.
	IsJunk bool
	// SpamVerdict is the classifier verdict from llm_classifications
	// ("ham", "spam", "suspect", "unclassified"); nil when the spam
	// classifier was not run for this message.
	SpamVerdict *string
	// SpamConfidence is the [0,1] confidence score from the spam
	// classifier; nil when the classifier was not run.
	SpamConfidence *float64
}
