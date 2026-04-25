package store

import (
	"time"
)

// This file declares the Phase 2 typed entities consumed by Wave 2.1
// subsystems (queue, outbound SMTP, mail-auth signer, ACME, DNS plugins,
// autodns, webhooks, DMARC report ingest, mailbox ACL, JMAP-state
// counters, TLS-RPT). They are kept here so the Phase 1 surface in
// types.go stays narrow and unmoved; subsystems written against Phase 1
// only need import the types they reach for.
//
// Identifier conventions follow the Phase 1 patterns: typed uint64 IDs
// for primary keys assigned by the metadata store; enums as small
// unsigned integers; bitfields where a single column is the natural
// representation; opaque byte slices for raw key material so the store
// stays oblivious to envelope formats.

// -- Queue ------------------------------------------------------------

// QueueItemID identifies one outbound queue row. Per-recipient: a
// single submission with N recipients yields N rows that share an
// EnvelopeID. Stable for the life of the row; not reused after the
// row is deleted.
type QueueItemID uint64

// EnvelopeID groups queue rows that originated from one submission.
// Used to correlate per-recipient delivery results back into a single
// DSN. Generated on submission (UUID-like opaque string); the store
// treats it as an opaque text key.
type EnvelopeID string

// QueueState is the lifecycle marker for a queue row.
type QueueState uint8

// QueueState values. Stored as small ints; new values append.
const (
	// QueueStateUnknown is the zero value and must not be persisted.
	QueueStateUnknown QueueState = iota
	// QueueStateQueued is "freshly enqueued, due now-or-later".
	QueueStateQueued
	// QueueStateDeferred is "tried and rescheduled" (transient failure,
	// 4xx response or DNS-temp). The scheduler uses NextAttemptAt to
	// decide when to pick it up again.
	QueueStateDeferred
	// QueueStateInflight is "claimed by a worker" — exactly one
	// scheduler should attempt delivery at a time. Recovery on crash:
	// the operator reschedules stuck inflight rows manually (Phase 2.x
	// adds a watchdog).
	QueueStateInflight
	// QueueStateDone is the terminal success state.
	QueueStateDone
	// QueueStateFailed is the terminal failure state (5xx or expiry).
	// The DSN generator reads these rows.
	QueueStateFailed
	// QueueStateHeld is operator-paused. The scheduler skips held rows
	// until ReleaseQueueItem moves them back to Queued.
	QueueStateHeld
)

// String returns the canonical lowercase token for q. Used in admin
// output and tests; not persisted.
func (q QueueState) String() string {
	switch q {
	case QueueStateQueued:
		return "queued"
	case QueueStateDeferred:
		return "deferred"
	case QueueStateInflight:
		return "inflight"
	case QueueStateDone:
		return "done"
	case QueueStateFailed:
		return "failed"
	case QueueStateHeld:
		return "held"
	default:
		return "unknown"
	}
}

// DSNNotifyFlags is a bitmask encoding RFC 3461 NOTIFY values for one
// queue row. Stored as a small integer; "NEVER" wins exclusively (per
// RFC 3461 §4.1) — when DSNNotifyNever is set, no DSN is generated
// regardless of which other bits are present.
type DSNNotifyFlags uint8

// DSNNotifyFlags values (RFC 3461 §4.1).
const (
	// DSNNotifyNone is the zero value: no NOTIFY parameter was supplied,
	// so the receiver applies its default policy (success-suppress,
	// failure-deliver, delay-deliver).
	DSNNotifyNone DSNNotifyFlags = 0
	// DSNNotifySuccess requests a delivery-success DSN.
	DSNNotifySuccess DSNNotifyFlags = 1 << iota
	// DSNNotifyFailure requests a delivery-failure DSN.
	DSNNotifyFailure
	// DSNNotifyDelay requests a delayed-delivery DSN.
	DSNNotifyDelay
	// DSNNotifyNever suppresses any DSN. Mutually exclusive with the
	// others (RFC 3461 §4.1); when set, the rest are ignored.
	DSNNotifyNever
)

// DSNRet selects how much of the original message is returned in a DSN
// (RFC 3461 §4.3 RET parameter).
type DSNRet uint8

// DSNRet values.
const (
	// DSNRetUnspecified is the zero value: the receiver picks (HDRS).
	DSNRetUnspecified DSNRet = iota
	// DSNRetFull asks for the full message in the DSN.
	DSNRetFull
	// DSNRetHeaders asks for headers only (the common case).
	DSNRetHeaders
)

// QueueItem is one outbound queue row. The schema stores one row per
// recipient (rcpt_to) so DSNs, retries, and per-recipient backoff are
// trivially per-row; the EnvelopeID column groups recipients from the
// same submission for DSN correlation.
type QueueItem struct {
	// ID is the assigned primary key.
	ID QueueItemID
	// PrincipalID is the submitting principal, or 0 when the row was
	// produced by a forwarding path (Sieve redirect, alias fanout) that
	// does not carry a principal context.
	PrincipalID PrincipalID
	// MailFrom is the SMTP MAIL FROM address ("<>" for null sender on
	// bounces). Lowercased on insert.
	MailFrom string
	// RcptTo is the single recipient address this row represents.
	// Lowercased on insert.
	RcptTo string
	// EnvelopeID groups per-recipient rows from one submission.
	EnvelopeID EnvelopeID
	// BodyBlobHash is the BLAKE3 hex digest of the canonicalised body
	// blob. The blob refcount is incremented on enqueue; decremented in
	// CompleteQueueItem and DeleteQueueItem (terminal transitions).
	BodyBlobHash string
	// HeadersBlobHash is the optional pre-rendered headers blob. When
	// non-empty, the outbound SMTP worker streams headers from this
	// blob instead of synthesizing them — used by the signer to commit
	// to a specific header set across retries.
	HeadersBlobHash string
	// State is the lifecycle marker.
	State QueueState
	// Attempts is the number of delivery attempts so far (0 on
	// enqueue).
	Attempts int32
	// LastAttemptAt is the instant of the most recent attempt; zero
	// when Attempts == 0.
	LastAttemptAt time.Time
	// NextAttemptAt is the earliest instant the scheduler may retry.
	// On enqueue it equals CreatedAt; on Reschedule it advances per
	// the backoff policy (computed by the caller).
	NextAttemptAt time.Time
	// LastError carries the most recent error string (truncated to a
	// small column on the backend); empty when Attempts == 0 or the
	// last attempt succeeded.
	LastError string
	// DSNNotify is the RFC 3461 NOTIFY bitmask.
	DSNNotify DSNNotifyFlags
	// DSNRet is the RFC 3461 RET parameter.
	DSNRet DSNRet
	// DSNEnvID is the RFC 3461 ENVID parameter (echoed back in DSNs);
	// empty when the submitter did not supply one.
	DSNEnvID string
	// DSNOrcpt is the RFC 3461 ORCPT parameter; empty if absent.
	DSNOrcpt string
	// IdempotencyKey, when non-empty, makes EnqueueMessage idempotent:
	// a second insert with the same key returns the existing row's ID
	// and ErrConflict. Empty disables the check.
	IdempotencyKey string
	// CreatedAt is the insert instant.
	CreatedAt time.Time
}

// QueueFilter narrows a ListQueueItems read. Zero values mean "no
// constraint"; Limit is capped at 1000 server-side.
type QueueFilter struct {
	// State, when non-zero, restricts to that lifecycle state.
	State QueueState
	// PrincipalID, when non-zero, restricts to that submitter.
	PrincipalID PrincipalID
	// EnvelopeID, when non-empty, restricts to that submission group.
	EnvelopeID EnvelopeID
	// RecipientDomain, when non-empty, restricts to rows whose RcptTo
	// has this domain (case-insensitive).
	RecipientDomain string
	// Limit caps the result set (default 1000, max 1000).
	Limit int
	// AfterID is the keyset cursor: rows with ID > AfterID.
	AfterID QueueItemID
}

// -- DKIM keys --------------------------------------------------------

// DKIMKeyID identifies one row in the dkim_keys table.
type DKIMKeyID uint64

// DKIMAlgorithm enumerates supported DKIM signing algorithms. The
// values mirror the a= tag tokens defined in RFC 6376 / RFC 8463.
type DKIMAlgorithm uint8

// DKIMAlgorithm values.
const (
	// DKIMAlgorithmUnknown is the zero value and must not be persisted.
	DKIMAlgorithmUnknown DKIMAlgorithm = iota
	// DKIMAlgorithmRSASHA256 corresponds to "rsa-sha256" (RFC 6376).
	DKIMAlgorithmRSASHA256
	// DKIMAlgorithmEd25519SHA256 corresponds to "ed25519-sha256" (RFC 8463).
	DKIMAlgorithmEd25519SHA256
)

// String returns the wire-form a= token for d.
func (d DKIMAlgorithm) String() string {
	switch d {
	case DKIMAlgorithmRSASHA256:
		return "rsa-sha256"
	case DKIMAlgorithmEd25519SHA256:
		return "ed25519-sha256"
	default:
		return "unknown"
	}
}

// DKIMKeyStatus encodes the rotation lifecycle of a DKIM key.
type DKIMKeyStatus uint8

// DKIMKeyStatus values.
const (
	// DKIMKeyStatusUnknown is the zero value and must not be persisted.
	DKIMKeyStatusUnknown DKIMKeyStatus = iota
	// DKIMKeyStatusActive is "use this key for new signatures".
	DKIMKeyStatusActive
	// DKIMKeyStatusRetiring is "do not sign with this key any more,
	// but the DNS TXT must remain published until the rotation grace
	// window elapses so receivers can still verify in-flight mail".
	DKIMKeyStatusRetiring
	// DKIMKeyStatusRetired is "fully retired, DNS TXT removed". Kept
	// for audit; safe to GC after a long retention window.
	DKIMKeyStatusRetired
)

// String returns the canonical token for s.
func (s DKIMKeyStatus) String() string {
	switch s {
	case DKIMKeyStatusActive:
		return "active"
	case DKIMKeyStatusRetiring:
		return "retiring"
	case DKIMKeyStatusRetired:
		return "retired"
	default:
		return "unknown"
	}
}

// DKIMKey is one row in the dkim_keys table: a per-domain per-selector
// signing keypair plus its lifecycle marker.
type DKIMKey struct {
	// ID is the assigned primary key.
	ID DKIMKeyID
	// Domain is the signing domain (lowercased, no trailing dot).
	Domain string
	// Selector is the s= tag value used in the DKIM-Signature header.
	Selector string
	// Algorithm is the signing algorithm.
	Algorithm DKIMAlgorithm
	// PrivateKeyPEM is the PEM-encoded private key. The store treats
	// the bytes as opaque (encryption-at-rest is the operator's
	// responsibility via filesystem / KMS).
	PrivateKeyPEM string
	// PublicKeyB64 is the base64-encoded public key (the literal value
	// that lands in the DNS TXT v=DKIM1; ...; p=<this> record).
	PublicKeyB64 string
	// Status is the rotation lifecycle marker.
	Status DKIMKeyStatus
	// CreatedAt is the insert instant.
	CreatedAt time.Time
	// RotatedAt is the instant the row last changed status; zero when
	// the row is still in its initial state.
	RotatedAt time.Time
}

// -- ACME -------------------------------------------------------------

// ACMEAccountID identifies an ACME account row.
type ACMEAccountID uint64

// ACMEOrderID identifies an ACME order row.
type ACMEOrderID uint64

// ACMEAccount is one row in the acme_accounts table: the client-side
// material for an ACME account binding (RFC 8555 §7.3).
type ACMEAccount struct {
	// ID is the assigned primary key.
	ID ACMEAccountID
	// DirectoryURL is the ACME server's directory endpoint URL.
	DirectoryURL string
	// ContactEmail is the contact mailto: target (operator-supplied).
	ContactEmail string
	// AccountKeyPEM is the PEM-encoded account private key.
	AccountKeyPEM string
	// KID is the ACME server's account URL ("kid" in JOSE). Empty
	// until the account is registered.
	KID string
	// CreatedAt is the insert instant.
	CreatedAt time.Time
}

// ACMEOrderStatus encodes the RFC 8555 §7.1.6 order state machine.
type ACMEOrderStatus uint8

// ACMEOrderStatus values.
const (
	// ACMEOrderStatusUnknown is the zero value and must not be persisted.
	ACMEOrderStatusUnknown ACMEOrderStatus = iota
	// ACMEOrderStatusPending — order accepted, authorizations not yet valid.
	ACMEOrderStatusPending
	// ACMEOrderStatusReady — every authorization is valid; finalize next.
	ACMEOrderStatusReady
	// ACMEOrderStatusProcessing — finalize submitted, CA is issuing.
	ACMEOrderStatusProcessing
	// ACMEOrderStatusValid — certificate ready for download.
	ACMEOrderStatusValid
	// ACMEOrderStatusInvalid — terminal failure; surface error to admin.
	ACMEOrderStatusInvalid
	// ACMEOrderStatusExpired — order outlived its server-side lifetime.
	ACMEOrderStatusExpired
)

// String returns the canonical RFC 8555 token for s.
func (s ACMEOrderStatus) String() string {
	switch s {
	case ACMEOrderStatusPending:
		return "pending"
	case ACMEOrderStatusReady:
		return "ready"
	case ACMEOrderStatusProcessing:
		return "processing"
	case ACMEOrderStatusValid:
		return "valid"
	case ACMEOrderStatusInvalid:
		return "invalid"
	case ACMEOrderStatusExpired:
		return "expired"
	default:
		return "unknown"
	}
}

// ChallengeType enumerates the ACME challenge types Herold supports.
type ChallengeType uint8

// ChallengeType values.
const (
	// ChallengeTypeUnknown is the zero value and must not be persisted.
	ChallengeTypeUnknown ChallengeType = iota
	// ChallengeTypeHTTP01 is RFC 8555 §8.3 http-01.
	ChallengeTypeHTTP01
	// ChallengeTypeTLSALPN01 is RFC 8737 tls-alpn-01.
	ChallengeTypeTLSALPN01
	// ChallengeTypeDNS01 is RFC 8555 §8.4 dns-01.
	ChallengeTypeDNS01
)

// String returns the canonical wire-form token.
func (c ChallengeType) String() string {
	switch c {
	case ChallengeTypeHTTP01:
		return "http-01"
	case ChallengeTypeTLSALPN01:
		return "tls-alpn-01"
	case ChallengeTypeDNS01:
		return "dns-01"
	default:
		return "unknown"
	}
}

// ACMEOrder is one row in the acme_orders table. Finalized orders are
// kept for audit so the admin surface can render the issuance history;
// live orders carry the cursor that the ACME state machine reads.
type ACMEOrder struct {
	// ID is the assigned primary key.
	ID ACMEOrderID
	// AccountID is the owning ACMEAccount.
	AccountID ACMEAccountID
	// Hostnames is the SAN list the order covers.
	Hostnames []string
	// Status is the current RFC 8555 status.
	Status ACMEOrderStatus
	// OrderURL is the server-assigned order resource URL.
	OrderURL string
	// FinalizeURL is the order's finalize endpoint.
	FinalizeURL string
	// CertificateURL is set once Status reaches Valid.
	CertificateURL string
	// ChallengeType is the chosen challenge family for this order.
	ChallengeType ChallengeType
	// UpdatedAt is the instant Status last changed.
	UpdatedAt time.Time
	// Error carries the last RFC 8555 problem-document detail when
	// Status is Invalid; empty otherwise.
	Error string
}

// ACMECert is the active certificate material for one hostname. The
// hostname is the natural primary key (one cert per hostname); the
// renewal worker reads ListACMECertsExpiringBefore to schedule rolls.
type ACMECert struct {
	// Hostname is the SAN this cert chain serves (lowercased).
	Hostname string
	// ChainPEM is the full PEM chain (leaf + intermediates).
	ChainPEM string
	// PrivateKeyPEM is the matching private key.
	PrivateKeyPEM string
	// NotBefore is the leaf's NotBefore from x509 parsing.
	NotBefore time.Time
	// NotAfter is the leaf's NotAfter; the renewal worker schedules
	// rolls relative to this column.
	NotAfter time.Time
	// Issuer is the leaf's issuer CN (operator-friendly label).
	Issuer string
	// OrderID is the producing order's ID, or 0 when the cert was
	// imported from outside ACME (admin-uploaded).
	OrderID ACMEOrderID
}

// -- Webhooks ---------------------------------------------------------

// WebhookID identifies one webhook subscription row.
type WebhookID uint64

// WebhookOwnerKind classifies who owns a webhook subscription.
type WebhookOwnerKind uint8

// WebhookOwnerKind values.
const (
	// WebhookOwnerUnknown is the zero value and must not be persisted.
	WebhookOwnerUnknown WebhookOwnerKind = iota
	// WebhookOwnerDomain — the subscription fires for any mail
	// arriving in the named domain.
	WebhookOwnerDomain
	// WebhookOwnerPrincipal — the subscription fires only for mail
	// delivered to this principal.
	WebhookOwnerPrincipal
)

// String returns the canonical lowercase token.
func (w WebhookOwnerKind) String() string {
	switch w {
	case WebhookOwnerDomain:
		return "domain"
	case WebhookOwnerPrincipal:
		return "principal"
	default:
		return "unknown"
	}
}

// DeliveryMode selects whether the webhook payload contains the
// message body inline or just an admin URL the receiver fetches.
type DeliveryMode uint8

// DeliveryMode values.
const (
	// DeliveryModeUnknown is the zero value and must not be persisted.
	DeliveryModeUnknown DeliveryMode = iota
	// DeliveryModeInline embeds the message body in the POST body.
	DeliveryModeInline
	// DeliveryModeFetchURL embeds only an admin URL; the receiver
	// fetches the body separately.
	DeliveryModeFetchURL
)

// String returns the canonical lowercase token.
func (d DeliveryMode) String() string {
	switch d {
	case DeliveryModeInline:
		return "inline"
	case DeliveryModeFetchURL:
		return "fetch_url"
	default:
		return "unknown"
	}
}

// RetryPolicy is the per-webhook retry configuration. JSON-marshaled
// into the retry_policy_json column so future fields are additive
// without a migration.
type RetryPolicy struct {
	// MaxAttempts caps the number of HTTP attempts before the
	// dispatcher gives up. Zero means "use the system default".
	MaxAttempts int `json:"max_attempts,omitempty"`
	// InitialBackoffMS is the first retry delay in milliseconds.
	InitialBackoffMS int `json:"initial_backoff_ms,omitempty"`
	// MaxBackoffMS caps the exponential-backoff ceiling.
	MaxBackoffMS int `json:"max_backoff_ms,omitempty"`
	// JitterMS is the +/- jitter applied to each delay.
	JitterMS int `json:"jitter_ms,omitempty"`
}

// Webhook is one row in the webhooks table.
type Webhook struct {
	// ID is the assigned primary key.
	ID WebhookID
	// OwnerKind classifies the owner (domain or principal).
	OwnerKind WebhookOwnerKind
	// OwnerID is the owner's natural identifier — the domain name when
	// OwnerKind is Domain, the stringified PrincipalID when Principal.
	OwnerID string
	// TargetURL is the receiving HTTPS endpoint.
	TargetURL string
	// HMACSecret is the shared secret the dispatcher uses to sign each
	// payload. Stored verbatim.
	HMACSecret []byte
	// DeliveryMode selects inline vs fetch-url payload shape.
	DeliveryMode DeliveryMode
	// RetryPolicy controls retry behaviour.
	RetryPolicy RetryPolicy
	// Active gates dispatching; inactive rows survive for audit.
	Active bool
	// CreatedAt / UpdatedAt are the row lifecycle timestamps.
	CreatedAt time.Time
	UpdatedAt time.Time
}

// -- DMARC reports ----------------------------------------------------

// DMARCReportID identifies one ingested DMARC aggregate report.
type DMARCReportID uint64

// DMARCRowID identifies one parsed row inside a DMARC report.
type DMARCRowID uint64

// DMARCReport is one row in the dmarc_reports_raw table: the metadata
// of an ingested aggregate report. The XML body itself lives in the
// blob store and is referenced by XMLBlobHash.
type DMARCReport struct {
	// ID is the assigned primary key.
	ID DMARCReportID
	// ReceivedAt is the instant the SMTP receiver took ownership of
	// the report email.
	ReceivedAt time.Time
	// ReporterEmail is the From: of the report message.
	ReporterEmail string
	// ReporterOrg is the org_name from the report XML (or the
	// reporter's domain when the XML lacks one).
	ReporterOrg string
	// ReportID is the report-id from the XML; combined with
	// ReporterOrg it deduplicates re-deliveries.
	ReportID string
	// Domain is the domain the report covers (the policy_published
	// domain).
	Domain string
	// DateBegin / DateEnd is the report's date_range.
	DateBegin time.Time
	DateEnd   time.Time
	// XMLBlobHash references the raw report XML in the blob store.
	XMLBlobHash string
	// ParsedOK is false when the parser rejected the XML.
	ParsedOK bool
	// ParseError carries the parse error string when ParsedOK is
	// false; empty otherwise.
	ParseError string
}

// DMARCRow is one parsed record from a DMARC aggregate report.
// Disposition uses mailauth.DMARCDisposition for the alignment
// vocabulary; the int code is stored to keep the schema isomorphic.
type DMARCRow struct {
	// ID is the assigned primary key.
	ID DMARCRowID
	// ReportID is the owning DMARCReport.
	ReportID DMARCReportID
	// SourceIP is the originating IP from the row.
	SourceIP string
	// Count is the message count grouped at this row.
	Count int64
	// Disposition is the action the reporter applied.
	Disposition int32
	// SPFAligned / DKIMAligned are the alignment booleans.
	SPFAligned  bool
	DKIMAligned bool
	// SPFResult / DKIMResult are the wire-form result tokens.
	SPFResult  string
	DKIMResult string
	// HeaderFrom is the RFC 5322.From domain.
	HeaderFrom string
	// EnvelopeFrom is the RFC 5321.MailFrom domain (empty when absent
	// from the report).
	EnvelopeFrom string
	// EnvelopeTo is the rcpt domain (empty when absent).
	EnvelopeTo string
}

// DMARCReportFilter narrows a ListDMARCReports read.
type DMARCReportFilter struct {
	// Domain restricts to reports covering this policy domain.
	Domain string
	// Since / Until restrict by DateBegin range; zero means "no bound".
	Since time.Time
	Until time.Time
	// Limit caps the result set (default 1000, max 1000).
	Limit int
	// AfterID is the keyset cursor.
	AfterID DMARCReportID
}

// DMARCAggregateRow is one row of pre-canned aggregate output for the
// admin REST surface (DMARCAggregate).
type DMARCAggregateRow struct {
	// HeaderFrom is the RFC 5322.From domain.
	HeaderFrom string
	// Disposition is the disposition (numeric, mirroring DMARCRow).
	Disposition int32
	// Count is the sum of message counts for this group.
	Count int64
	// PassedSPF / PassedDKIM are the per-method pass counts.
	PassedSPF  int64
	PassedDKIM int64
}

// -- Mailbox ACL ------------------------------------------------------

// MailboxACLID identifies one row in the mailbox_acl table.
type MailboxACLID uint64

// ACLRights is a bitmask encoding RFC 4314 IMAP rights. Each bit maps
// to one letter from the RFC 4314 §2.1 vocabulary; the 11 currently
// allocated rights fit in a 16-bit mask with room for future use.
type ACLRights uint16

// ACLRights bits — RFC 4314 §2.1.
const (
	// ACLRightLookup ("l") — visible to LIST/LSUB.
	ACLRightLookup ACLRights = 1 << iota
	// ACLRightRead ("r") — open the mailbox and read messages.
	ACLRightRead
	// ACLRightSeen ("s") — store \Seen state.
	ACLRightSeen
	// ACLRightWrite ("w") — store flags other than \Seen and \Deleted.
	ACLRightWrite
	// ACLRightInsert ("i") — insert messages (APPEND, COPY into).
	ACLRightInsert
	// ACLRightPost ("p") — submit via SMTP to the mailbox's submission address.
	ACLRightPost
	// ACLRightCreateMailbox ("k") — create child mailboxes.
	ACLRightCreateMailbox
	// ACLRightDeleteMailbox ("x") — delete the mailbox.
	ACLRightDeleteMailbox
	// ACLRightDeleteMessage ("t") — set \Deleted.
	ACLRightDeleteMessage
	// ACLRightExpunge ("e") — perform EXPUNGE.
	ACLRightExpunge
	// ACLRightAdmin ("a") — administer the ACL itself.
	ACLRightAdmin
)

// ACLRightsAll is every defined right combined; convenience for the
// owner grant the store seeds when the principal first touches their
// mailbox under JMAP/IMAP.
const ACLRightsAll = ACLRightLookup | ACLRightRead | ACLRightSeen |
	ACLRightWrite | ACLRightInsert | ACLRightPost | ACLRightCreateMailbox |
	ACLRightDeleteMailbox | ACLRightDeleteMessage | ACLRightExpunge |
	ACLRightAdmin

// CanRead reports whether the mask grants enough to open and read.
// Convenience helper: callers do not need to remember which bits the
// IMAP SELECT flow needs.
func (r ACLRights) CanRead() bool {
	return r&(ACLRightLookup|ACLRightRead) == (ACLRightLookup | ACLRightRead)
}

// CanWrite reports whether the mask grants flag mutation (excluding
// \Seen and \Deleted, which have their own bits).
func (r ACLRights) CanWrite() bool { return r&ACLRightWrite != 0 }

// CanInsert reports whether the mask grants APPEND / COPY-target.
func (r ACLRights) CanInsert() bool { return r&ACLRightInsert != 0 }

// CanExpunge reports whether the mask grants EXPUNGE.
func (r ACLRights) CanExpunge() bool { return r&ACLRightExpunge != 0 }

// CanAdmin reports whether the mask grants ACL administration.
func (r ACLRights) CanAdmin() bool { return r&ACLRightAdmin != 0 }

// MailboxACL is one row in the mailbox_acl table. PrincipalID may be
// nil to encode the RFC 4314 "anyone" pseudo-identifier (a single row
// per mailbox with PrincipalID == nil grants the listed rights to
// every authenticated principal).
type MailboxACL struct {
	// ID is the assigned primary key.
	ID MailboxACLID
	// MailboxID is the mailbox the ACL applies to.
	MailboxID MailboxID
	// PrincipalID is the grantee, or nil for "anyone".
	PrincipalID *PrincipalID
	// Rights is the granted rights mask.
	Rights ACLRights
	// GrantedBy is the principal who set this row (for audit).
	GrantedBy PrincipalID
	// CreatedAt is the insert instant.
	CreatedAt time.Time
}

// -- JMAP states ------------------------------------------------------

// JMAPStateKind enumerates the JMAP object types whose state strings
// the store tracks per-principal. JMAP exposes opaque state strings to
// clients; we store the integers and let callers stringify.
type JMAPStateKind uint8

// JMAPStateKind values. Order matches the column layout in the
// jmap_states table so a single SELECT returns the whole row.
const (
	// JMAPStateKindUnknown is the zero value and must not be persisted.
	JMAPStateKindUnknown JMAPStateKind = iota
	// JMAPStateKindMailbox tracks JMAP Mailbox object changes.
	JMAPStateKindMailbox
	// JMAPStateKindEmail tracks JMAP Email object changes.
	JMAPStateKindEmail
	// JMAPStateKindThread tracks JMAP Thread object changes.
	JMAPStateKindThread
	// JMAPStateKindIdentity tracks JMAP Identity changes.
	JMAPStateKindIdentity
	// JMAPStateKindEmailSubmission tracks JMAP EmailSubmission changes.
	JMAPStateKindEmailSubmission
	// JMAPStateKindVacationResponse tracks JMAP VacationResponse changes.
	JMAPStateKindVacationResponse
	// JMAPStateKindSieve tracks JMAP Sieve datatype changes
	// (REQ-PROTO-53 / RFC 9007). Bumped on every successful Sieve/set.
	JMAPStateKindSieve
	// JMAPStateKindAddressBook tracks JMAP AddressBook changes
	// (REQ-PROTO-55 / RFC 9553). Bumped on every AddressBook/set
	// mutation.
	JMAPStateKindAddressBook
	// JMAPStateKindContact tracks JMAP Contact changes (REQ-PROTO-55 /
	// RFC 9553). Bumped on every Contact/set mutation.
	JMAPStateKindContact
)

// JMAPStates is the per-principal row of JMAP object-scoped state
// counters. JMAP returns each as an opaque string ("State"); the store
// stores them as int64 and increments atomically per-kind on every
// relevant mutation. The counters parallel the existing per-principal
// ChangeSeq feed but are JMAP-object-scoped so clients can reason
// about drift per object type.
type JMAPStates struct {
	// PrincipalID is the owning principal.
	PrincipalID PrincipalID
	// Mailbox is the JMAP Mailbox state.
	Mailbox int64
	// Email is the JMAP Email state.
	Email int64
	// Thread is the JMAP Thread state.
	Thread int64
	// Identity is the JMAP Identity state.
	Identity int64
	// EmailSubmission is the JMAP EmailSubmission state.
	EmailSubmission int64
	// VacationResponse is the JMAP VacationResponse state.
	VacationResponse int64
	// Sieve is the JMAP Sieve datatype state (REQ-PROTO-53).
	Sieve int64
	// AddressBook is the JMAP AddressBook state (REQ-PROTO-55).
	AddressBook int64
	// Contact is the JMAP Contact state (REQ-PROTO-55).
	Contact int64
	// UpdatedAt is the instant of the most recent increment.
	UpdatedAt time.Time
}

// -- TLS-RPT ----------------------------------------------------------

// TLSRPTFailureID identifies one row in the tlsrpt_failures table.
type TLSRPTFailureID uint64

// TLSRPTFailureType enumerates the failure-type vocabulary defined by
// RFC 8460 §4.3.
type TLSRPTFailureType uint8

// TLSRPTFailureType values.
const (
	// TLSRPTFailureUnknown is the zero value and must not be persisted.
	TLSRPTFailureUnknown TLSRPTFailureType = iota
	// TLSRPTFailureMTASTS — MTA-STS policy fetch / parsing failure.
	TLSRPTFailureMTASTS
	// TLSRPTFailureDANE — DANE TLSA validation failure.
	TLSRPTFailureDANE
	// TLSRPTFailureCertificateExpired — peer cert expired.
	TLSRPTFailureCertificateExpired
	// TLSRPTFailureValidation — generic certificate validation failure.
	TLSRPTFailureValidation
	// TLSRPTFailureSTARTTLSNotOffered — peer did not advertise STARTTLS.
	TLSRPTFailureSTARTTLSNotOffered
	// TLSRPTFailureSTARTTLSNegotiation — STARTTLS negotiation failed.
	TLSRPTFailureSTARTTLSNegotiation
	// TLSRPTFailureOther — anything else; FailureCode carries the detail.
	TLSRPTFailureOther
)

// String returns the wire-form RFC 8460 token for t.
func (t TLSRPTFailureType) String() string {
	switch t {
	case TLSRPTFailureMTASTS:
		return "mta-sts"
	case TLSRPTFailureDANE:
		return "dane"
	case TLSRPTFailureCertificateExpired:
		return "certificate-expired"
	case TLSRPTFailureValidation:
		return "validation-failure"
	case TLSRPTFailureSTARTTLSNotOffered:
		return "starttls-not-supported"
	case TLSRPTFailureSTARTTLSNegotiation:
		return "starttls-negotiation-failure"
	case TLSRPTFailureOther:
		return "other"
	default:
		return "unknown"
	}
}

// TLSRPTFailure is one row in the tlsrpt_failures table: a recorded
// outbound TLS failure that the TLS-RPT reporter rolls up into RUA
// payloads.
type TLSRPTFailure struct {
	// ID is the assigned primary key.
	ID TLSRPTFailureID
	// RecordedAt is the instant the failure was observed.
	RecordedAt time.Time
	// PolicyDomain is the recipient domain whose TLS policy was in
	// effect at the time of the failure.
	PolicyDomain string
	// ReceivingMTAHostname is the MX hostname the connection targeted.
	ReceivingMTAHostname string
	// FailureType is the categorical RFC 8460 token.
	FailureType TLSRPTFailureType
	// FailureCode is a short machine-readable identifier; freeform but
	// the reporter uses it to group similar failures in the RUA
	// payload.
	FailureCode string
	// FailureDetailJSON is the per-row detail dictionary serialised
	// as JSON (small, schema-shape lives in the reporter).
	FailureDetailJSON string
}

// -- JMAP EmailSubmission ---------------------------------------------

// EmailSubmissionRow is one persisted JMAP EmailSubmission record. The
// queue rows under EnvelopeID carry the per-recipient delivery state;
// this row carries the JMAP-side metadata (identity, email, thread) plus
// the cached undoStatus the most recent /set update committed. Together
// they let EmailSubmission/get reconstruct the wire object across server
// restarts.
type EmailSubmissionRow struct {
	// ID is the wire-form JMAP id. We store the EnvelopeID stringified
	// so callers can round-trip ids without a separate counter.
	ID string
	// EnvelopeID is the queue submission group this row belongs to.
	// One EmailSubmission ↔ one EnvelopeID by construction.
	EnvelopeID EnvelopeID
	// PrincipalID is the submitting principal.
	PrincipalID PrincipalID
	// IdentityID is the JMAP Identity id the submission was sent under
	// (the wire form, e.g. "default" or a numeric overlay id).
	IdentityID string
	// EmailID is the source Email row.
	EmailID MessageID
	// ThreadID is the JMAP Thread id (the wire form). Empty for
	// orphaned drafts.
	ThreadID string
	// SendAtUs is the scheduled or actual send instant in unix-micros.
	SendAtUs int64
	// CreatedAtUs is the row insert instant in unix-micros.
	CreatedAtUs int64
	// UndoStatus is the JMAP undoStatus token: "pending" / "final" /
	// "canceled". Updated by /set update; rendered straight onto the
	// wire by /get when the queue rows are still in flight.
	UndoStatus string
	// Properties carries any additional RFC 8621 fields the row needs
	// to round-trip across restart but which we do not model with their
	// own column. Small JSON object; opaque to the store.
	Properties []byte
}

// EmailSubmissionFilter narrows a ListEmailSubmissions read per RFC 8621
// §5.4 (EmailSubmission/query). All fields are optional; zero values
// mean "no constraint". IdentityIDs / EmailIDs / ThreadIDs are
// disjunctive within each list and AND-combined across lists.
type EmailSubmissionFilter struct {
	// IdentityIDs restricts to rows whose IdentityID is in this list.
	IdentityIDs []string
	// EmailIDs restricts to rows whose EmailID stringifies to one of
	// these values. The string form lets the JMAP layer pass through
	// its wire representation without re-typing.
	EmailIDs []MessageID
	// ThreadIDs restricts to rows whose ThreadID is in this list.
	ThreadIDs []string
	// UndoStatus, when non-empty, restricts to rows whose UndoStatus
	// matches verbatim.
	UndoStatus string
	// Before / After are unix-micros bounds on SendAtUs (RFC 8621 §5.4).
	// Zero means "no bound".
	BeforeUs int64
	AfterUs  int64
	// Limit caps the result set (default 1000, max 1000).
	Limit int
	// AfterID is the keyset cursor; rows whose ID compares strictly
	// greater than AfterID. Empty starts at the first row.
	AfterID string
}

// -- JMAP Identity ----------------------------------------------------

// JMAPIdentity is one persisted JMAP Identity overlay row. The default
// identity is synthesised from the Principal canonical email at read
// time and is NOT stored here; only operator- or user-explicitly-created
// identities land in this table.
type JMAPIdentity struct {
	// ID is the wire-form JMAP id (decimal string for overlay rows).
	ID string
	// PrincipalID is the owning principal.
	PrincipalID PrincipalID
	// Name is the display name shown in the From: header.
	Name string
	// Email is the addr-spec the identity sends from.
	Email string
	// ReplyToJSON is the JMAP "replyTo" array serialised as JSON; nil
	// or empty when the identity has no replyTo override.
	ReplyToJSON []byte
	// BccJSON is the JMAP "bcc" array serialised as JSON; nil or empty
	// when the identity has no bcc override.
	BccJSON []byte
	// TextSignature / HTMLSignature carry the optional signature
	// blocks (RFC 8621 §7.1).
	TextSignature string
	HTMLSignature string
	// Signature is the JMAP Identity.signature extension property
	// (REQ-PROTO-57 / REQ-STORE-35): a single nullable plain-text
	// signature body separate from TextSignature/HTMLSignature.
	// nil means unset; clients populate compose with this value when
	// present. The HTML signature variant is deferred to phase 2 as a
	// separate column.
	Signature *string
	// MayDelete reflects the JMAP mayDelete flag (RFC 8621 §7.4). The
	// synthesised default identity sets this to false; persisted rows
	// default to true.
	MayDelete bool
	// CreatedAtUs / UpdatedAtUs are unix-micros timestamps maintained
	// by the store on insert / update.
	CreatedAtUs int64
	UpdatedAtUs int64
}

// -- LLM categorisation (REQ-FILT-200..231) --------------------------

// CategoryDef is one entry in a CategorisationConfig.CategorySet.
// Names are lowercase ASCII dash-separated tokens (REQ-FILT-201);
// Description is a free-text gloss that lands in the LLM prompt to
// help the model pick between sibling categories.
type CategoryDef struct {
	// Name is the bare category name (e.g. "primary"); the delivery
	// pipeline emits it as the "$category-<Name>" keyword.
	Name string `json:"name"`
	// Description is a one-sentence gloss describing what kind of
	// message belongs in this category. Fed into the system prompt
	// alongside the category name.
	Description string `json:"description"`
}

// CategorisationConfig is the per-account row driving the LLM
// categoriser (REQ-FILT-210/211/213). Endpoint, Model, APIKeyEnv and
// TimeoutSec are nullable so a per-account row can override only the
// knobs the user cares about; nil falls back to the operator-supplied
// default. Prompt and CategorySet are required and seeded with the
// REQ-FILT-201/210 defaults on first read.
type CategorisationConfig struct {
	// PrincipalID is the owning principal.
	PrincipalID PrincipalID
	// Prompt is the system prompt fed to the LLM. Free text;
	// REQ-FILT-211 default approximates Gmail's behaviour.
	Prompt string
	// CategorySet enumerates the categories the LLM may pick from.
	// JSON-serialised in storage; the order is preserved so a "reset
	// to default" control stays stable.
	CategorySet []CategoryDef
	// Endpoint, when non-nil, overrides the operator-default
	// OpenAI-compatible endpoint URL for this account.
	Endpoint *string
	// Model, when non-nil, overrides the operator-default model name.
	Model *string
	// APIKeyEnv, when non-nil, names the environment variable the
	// process reads to obtain a Bearer token; nil means "use the
	// operator default".
	APIKeyEnv *string
	// TimeoutSec is the per-call HTTP timeout in seconds. 0 means
	// "use the operator default" (typically 5).
	TimeoutSec int
	// Enabled gates the categoriser for this principal. The default
	// row is seeded enabled = true (REQ-FILT-200).
	Enabled bool
	// UpdatedAtUs is the unix-micros instant of the most recent write.
	UpdatedAtUs int64
}
