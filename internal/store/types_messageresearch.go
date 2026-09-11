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

// MessageIngestSource records which ingest path produced a messages row
// (re #143, maintainer finding #2 on 2026-09-09). It is written once, by
// the caller, alongside the InsertMessage / InsertMessages call that
// creates the row, and is never recomputed. An operator reviewing
// message research cannot otherwise tell whether a message arrived by
// live SMTP delivery or was pulled in later by an importer.
type MessageIngestSource string

const (
	// IngestSourceUnknown is the zero value: the row predates
	// ingest-source recording (migration 0105), or was written by a
	// caller that has not been updated to set it. Rendered as an
	// explicit "not recorded", never inferred.
	IngestSourceUnknown MessageIngestSource = ""
	// IngestSourceSMTP is live SMTP delivery (internal/protosmtp).
	IngestSourceSMTP MessageIngestSource = "smtp"
	// IngestSourceIMAPImport is the internal/imapimport background sync
	// from an external IMAP account. IngestSourceRef carries the import
	// account name.
	IngestSourceIMAPImport MessageIngestSource = "imap-import"
	// IngestSourceJMAPImport is a JMAP Email/import or Email/set create.
	IngestSourceJMAPImport MessageIngestSource = "jmap-import"
	// IngestSourceIMAPAppend is an IMAP APPEND command.
	IngestSourceIMAPAppend MessageIngestSource = "imap-append"
	// IngestSourceIMAPCopy is an IMAP COPY command creating a new row
	// (a cross-account or cross-principal copy).
	IngestSourceIMAPCopy MessageIngestSource = "imap-copy"
	// IngestSourceMailingListArchive is the mailing-list archiver.
	// IngestSourceRef carries the list address.
	IngestSourceMailingListArchive MessageIngestSource = "mailing-list-archive"
	// IngestSourceGmailImport is the Google Takeout / Gmail bulk import.
	IngestSourceGmailImport MessageIngestSource = "gmail-import"
)

// AdminMessageFilter narrows a SearchAdminMessages read. All fields are
// AND-combined; zero values are unconstrained. There is intentionally no
// subject filter: the Subject header is message content from the
// operator's point of view, not envelope metadata, and REQ-ADM-306
// restricts this surface to envelope metadata and disposition only (re
// #143, maintainer finding #4 on 2026-09-09).
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

// AdminMessageMailbox is one mailbox a message currently sits in, as
// returned by SearchAdminMessages via AdminMessageHit.Mailboxes (re #143,
// maintainer finding #1 on 2026-09-09: a message can sit in several
// mailboxes at once -- e.g. an IMAP import's per-account mailbox
// alongside Archive/Spam copies -- and a single name/flag pair
// misrepresents that).
type AdminMessageMailbox struct {
	// Name is the mailbox display name.
	Name string
	// IsJunk is true when this mailbox carries the Junk special-use
	// attribute (store.MailboxAttrJunk).
	IsJunk bool
}

// AdminMessageHit is one result from SearchAdminMessages. It carries the
// message envelope and disposition — never body content or subject text
// (REQ-ADM-306). SearchAdminMessages never populates Envelope.Subject.
type AdminMessageHit struct {
	// MessageID is the store primary key.
	MessageID MessageID
	// PrincipalID is the owning principal.
	PrincipalID PrincipalID
	// ReceivedAt is the instant the message was accepted by the server.
	ReceivedAt time.Time
	// Envelope contains the message envelope fields (no body content).
	// Subject is intentionally left unset -- see the AdminMessageHit
	// doc comment.
	Envelope Envelope
	// IngestSource records which ingest path produced this row (re
	// #143, maintainer finding #2). IngestSourceUnknown means not
	// recorded, either because the row predates migration 0105 or the
	// write path has not been updated to set it.
	IngestSource MessageIngestSource
	// IngestSourceRef is ingest-path-specific free text alongside
	// IngestSource: the import account name for IngestSourceIMAPImport,
	// the mailing list address for IngestSourceMailingListArchive, empty
	// for every other source.
	IngestSourceRef string
	// Disposition is the recorded-at-ingest delivery disposition (re
	// #143). This is the authoritative, immutable forensic fact: what
	// the SMTP ingest path decided when the message was accepted.
	// DeliveryDispositionUnknown means no value was recorded (row
	// predates migration 0090, or was written by a non-SMTP-ingest
	// path) -- rendered as "not recorded", never inferred from current
	// mailbox state.
	Disposition MessageDeliveryDisposition
	// Mailboxes lists every mailbox the message currently sits in,
	// ordered by name (re #143, maintainer finding #1). This is LIVE
	// state: it reflects moves, refiles, and Sieve/IMAP/JMAP mailbox
	// changes made after delivery, and can differ from Disposition. Use
	// Disposition for "what happened at delivery"; use Mailboxes only
	// for "where is it now".
	Mailboxes []AdminMessageMailbox
	// MailboxName is Mailboxes[0].Name (empty if Mailboxes is empty),
	// kept for compatibility. Derived from Mailboxes -- see its doc
	// comment for the LIVE-state caveat.
	MailboxName string
	// IsJunk is true when any entry in Mailboxes carries the Junk
	// special-use attribute, kept for compatibility. Derived from
	// Mailboxes -- see its doc comment for the LIVE-state caveat.
	IsJunk bool
	// SpamVerdict is the classifier verdict from llm_classifications
	// ("ham", "spam", "suspect", "unclassified"); nil when the spam
	// classifier was not run for this message.
	SpamVerdict *string
	// SpamConfidence is the [0,1] confidence score from the spam
	// classifier; nil when the classifier was not run.
	SpamConfidence *float64
	// SpamReason is llm_classifications.spam_reason: for a genuine
	// ham/spam/suspect verdict, the plugin's own one-sentence
	// explanation; for SpamVerdict == "unclassified" (re #326), the
	// "<class>: <detail>" string recorded at classification time
	// (spam.ReasonClass tokens: timeout, not_configured, unparseable,
	// plugin_error). Nil when the classifier was not run or returned no
	// reason.
	SpamReason *string
}
