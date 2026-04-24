package store

import (
	"time"
)

// PrincipalID uniquely identifies a Principal within a Herold deployment.
// It is assigned by the metadata store on insert and never reused.
type PrincipalID uint64

// OIDCProviderID uniquely identifies a configured OIDC provider within a
// Herold deployment. The identity is the operator-chosen provider Name
// (stable across restarts, single string column in the schema); it is
// typed as its own alias so method signatures stay readable without a
// schema-level change to the existing oidc_providers table.
type OIDCProviderID string

// AuditLogID is the primary key of an audit-log entry. Monotonically
// increasing across the store; never reused after a row is deleted
// (in practice rows are only pruned by retention, never UPDATEd).
type AuditLogID uint64

// MailboxID uniquely identifies a Mailbox owned by a Principal. Stable
// for the life of the mailbox; not reused after the mailbox is deleted.
type MailboxID uint64

// MessageID uniquely identifies a message row (a specific placement of a
// blob into a mailbox). Distinct from the content-addressed BlobRef; one
// blob may be referenced by many MessageIDs (fanout / copy).
type MessageID uint64

// AliasID uniquely identifies an alias mapping.
type AliasID uint64

// APIKeyID uniquely identifies an API key.
type APIKeyID uint64

// UID is an IMAP message UID, unique within a mailbox for the lifetime of
// its UIDVALIDITY epoch. 32-bit per RFC 3501.
type UID uint32

// ModSeq is the IMAP CONDSTORE/QRESYNC modification sequence per mailbox,
// strictly increasing (RFC 7162). Also used as the per-mailbox monotonic
// version counter in our metadata store.
type ModSeq uint64

// UIDValidity is the IMAP UIDVALIDITY value for a mailbox (RFC 3501). It
// is immutable for the life of the mailbox in the normal case; bumped only
// on catastrophic events (mailbox deleted and recreated with same name).
type UIDValidity uint32

// ChangeSeq is the per-principal monotonic sequence in the state-change
// feed. See docs/architecture/05-sync-and-state.md.
type ChangeSeq uint64

// PrincipalKind labels a Principal as an individual user, a group, or a
// service account. Phase 1 uses only PrincipalKindUser; other kinds are
// defined so that protocol code does not grow ad-hoc branches later.
type PrincipalKind uint8

const (
	// PrincipalKindUnknown is the zero value; it must never appear in a
	// stored Principal and is returned only by uninitialized decoders.
	PrincipalKindUnknown PrincipalKind = iota
	// PrincipalKindUser is a human end-user principal with a mailbox.
	PrincipalKindUser
	// PrincipalKindGroup is an address that fans out to multiple members.
	PrincipalKindGroup
	// PrincipalKindService is a non-human API consumer (send API, webhook).
	PrincipalKindService
)

// PrincipalFlags is a bitfield of boolean attributes on a Principal.
type PrincipalFlags uint32

const (
	// PrincipalFlagDisabled suppresses authentication and delivery to this
	// principal without deleting it.
	PrincipalFlagDisabled PrincipalFlags = 1 << iota
	// PrincipalFlagIgnoreDownloadLimits exempts this principal from the
	// per-principal download bandwidth cap (REQ-STORE-24).
	PrincipalFlagIgnoreDownloadLimits
	// PrincipalFlagAdmin grants administrative privileges on the admin API.
	PrincipalFlagAdmin
	// PrincipalFlagTOTPEnabled indicates the principal has confirmed a
	// TOTP enrolment (the directory promotes pending -> enabled on the
	// first valid code). Stored separately from TOTPSecret so the secret
	// can remain opaque to the store and so enrolment state is cheap to
	// query without decoding the secret envelope.
	PrincipalFlagTOTPEnabled
)

// Has reports whether f includes mask (every bit in mask is set in f).
// The zero-bit case is a no-op true: a caller asking whether no flags
// are set always succeeds.
func (f PrincipalFlags) Has(mask PrincipalFlags) bool {
	return f&mask == mask
}

// Principal is an authenticatable identity owning mailboxes, aliases, and
// configuration. It maps to the principals table in the metadata store.
type Principal struct {
	// ID is the stable primary key assigned by the store.
	ID PrincipalID
	// Kind classifies the principal (user, group, service).
	Kind PrincipalKind
	// CanonicalEmail is the principal's primary address in lowercase.
	CanonicalEmail string
	// DisplayName is an optional human-friendly label.
	DisplayName string
	// PasswordHash is the Argon2id-encoded hash of the principal's
	// password (REQ-AUTH-20 / REQ-NFR-100). Empty for principals that
	// authenticate only via OIDC.
	PasswordHash string
	// TOTPSecret holds the encrypted TOTP shared secret, or nil if TOTP
	// is not enrolled. The envelope format is the directory subsystem's
	// responsibility; the store treats it as opaque bytes.
	TOTPSecret []byte
	// QuotaBytes is the mailbox quota ceiling (REQ-STORE-50); 0 means
	// unlimited.
	QuotaBytes int64
	// Flags is the bitfield of PrincipalFlag* toggles.
	Flags PrincipalFlags
	// CreatedAt is the instant the principal row was inserted.
	CreatedAt time.Time
	// UpdatedAt is the instant of the most recent mutation to the row.
	UpdatedAt time.Time
}

// Alias maps an email address to a target Principal. See REQ-STORE
// (aliases table in docs/architecture/02-storage-architecture.md §Schema).
type Alias struct {
	// ID is the stable primary key.
	ID AliasID
	// LocalPart is the local portion of the alias address (before '@').
	LocalPart string
	// Domain is the domain portion of the alias address (after '@').
	Domain string
	// TargetPrincipal is the principal receiving mail sent to this alias.
	TargetPrincipal PrincipalID
	// ExpiresAt is non-nil when the alias is time-limited; the store
	// treats expired aliases as non-existent for routing purposes.
	ExpiresAt *time.Time
	// CreatedAt is the insert instant.
	CreatedAt time.Time
}

// Domain is a DNS domain the server considers local (inbound mail for
// addresses in this domain is delivered locally).
type Domain struct {
	// Name is the lowercase domain (no trailing dot).
	Name string
	// IsLocal is true when this server is authoritative for mail in the
	// domain. False rows are reserved for future "known remote" uses.
	IsLocal bool
	// CreatedAt is the insert instant.
	CreatedAt time.Time
}

// MailboxAttributes is a bitfield encoding IMAP SPECIAL-USE attributes and
// related per-mailbox booleans (RFC 6154 §2 + implementation extensions).
type MailboxAttributes uint32

const (
	// MailboxAttrInbox marks the principal's INBOX.
	MailboxAttrInbox MailboxAttributes = 1 << iota
	// MailboxAttrSent marks the \Sent SPECIAL-USE folder.
	MailboxAttrSent
	// MailboxAttrDrafts marks the \Drafts SPECIAL-USE folder.
	MailboxAttrDrafts
	// MailboxAttrTrash marks the \Trash SPECIAL-USE folder.
	MailboxAttrTrash
	// MailboxAttrJunk marks the \Junk SPECIAL-USE folder.
	MailboxAttrJunk
	// MailboxAttrArchive marks the \Archive SPECIAL-USE folder.
	MailboxAttrArchive
	// MailboxAttrFlagged marks the \Flagged virtual folder.
	MailboxAttrFlagged
	// MailboxAttrSubscribed records IMAP SUBSCRIBE state (RFC 3501 §6.3.6).
	MailboxAttrSubscribed
)

// Mailbox is a named folder owned by a Principal. Mailboxes form a
// hierarchy via ParentID; the root has ParentID == 0.
type Mailbox struct {
	// ID is the stable primary key.
	ID MailboxID
	// PrincipalID is the owning principal.
	PrincipalID PrincipalID
	// ParentID is the containing mailbox; 0 means a top-level mailbox.
	ParentID MailboxID
	// Name is the mailbox name as seen by the client (RFC 3501 mailbox
	// name, utf-8-encoded per RFC 6855). The metadata store does not
	// canonicalize; callers must.
	Name string
	// Attributes is the SPECIAL-USE + subscription bitfield.
	Attributes MailboxAttributes
	// UIDValidity is the IMAP UIDVALIDITY for this mailbox.
	UIDValidity UIDValidity
	// UIDNext is the UID that will be assigned to the next APPEND /
	// delivered message (RFC 3501 §7.3.1).
	UIDNext UID
	// HighestModSeq is the maximum ModSeq of any message in the mailbox
	// (RFC 7162 §3.1.1), advanced atomically with each mutation.
	HighestModSeq ModSeq
	// CreatedAt is the insert instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the most recent mutation.
	UpdatedAt time.Time
}

// MessageFlags is the bitfield of IMAP system flags (RFC 3501 §2.3.2).
// Keyword flags — user-defined labels — are carried separately in
// Message.Keywords to preserve their names.
type MessageFlags uint16

const (
	// MessageFlagSeen corresponds to the IMAP \Seen flag.
	MessageFlagSeen MessageFlags = 1 << iota
	// MessageFlagAnswered corresponds to \Answered.
	MessageFlagAnswered
	// MessageFlagFlagged corresponds to \Flagged.
	MessageFlagFlagged
	// MessageFlagDeleted corresponds to \Deleted.
	MessageFlagDeleted
	// MessageFlagDraft corresponds to \Draft.
	MessageFlagDraft
	// MessageFlagRecent corresponds to \Recent (RFC 3501, removed in IMAP4rev2
	// but retained for RFC 3501 compatibility).
	MessageFlagRecent
)

// Envelope is the cached RFC 5322 envelope summary used by IMAP STATUS /
// FETCH ENVELOPE (RFC 3501 §7.4.2) and JMAP Email properties without
// re-parsing the blob body.
type Envelope struct {
	// Subject is the decoded Subject header.
	Subject string
	// From is the raw From header value (list in display form).
	From string
	// To is the raw To header value.
	To string
	// Cc is the raw Cc header value.
	Cc string
	// Bcc is the raw Bcc header value.
	Bcc string
	// ReplyTo is the raw Reply-To header value.
	ReplyTo string
	// MessageID is the decoded Message-ID header (without angle brackets).
	MessageID string
	// InReplyTo is the decoded In-Reply-To header.
	InReplyTo string
	// Date is the parsed Date header; zero value if absent or unparsable.
	Date time.Time
}

// Message is one message row within a mailbox. The message body is stored
// once by content hash (Blob) and referenced here by hash.
type Message struct {
	// ID is the stable primary key.
	ID MessageID
	// MailboxID is the containing mailbox.
	MailboxID MailboxID
	// UID is the IMAP UID within that mailbox.
	UID UID
	// ModSeq is the IMAP CONDSTORE modification sequence assigned at the
	// last mutation of this message in its mailbox.
	ModSeq ModSeq
	// Flags is the system-flag bitfield.
	Flags MessageFlags
	// Keywords is the list of user-defined IMAP keyword flags (lowercase,
	// per RFC 5788). Nil for messages with no keywords.
	Keywords []string
	// InternalDate is the IMAP INTERNALDATE (RFC 3501 §2.3.3) — the
	// instant the server took ownership of the message.
	InternalDate time.Time
	// ReceivedAt is the instant the message was accepted by the receiving
	// transport; equals InternalDate for locally-delivered mail.
	ReceivedAt time.Time
	// Size is the RFC 822 size of the message body in bytes.
	Size int64
	// Blob is the content-addressed reference to the message body.
	Blob BlobRef
	// ThreadID groups this message with related messages per RFC 5256
	// (REFERENCES algorithm). 0 means the message is not yet threaded.
	ThreadID uint64
	// Envelope is the cached parsed envelope (for STATUS / FETCH without
	// touching the blob).
	Envelope Envelope
}

// MessageFilter narrows a ListMessages read. Zero values mean "no
// constraint"; Limit is capped at 1000 server-side regardless of
// caller input.
type MessageFilter struct {
	// AfterUID is the keyset cursor: only messages with UID > AfterUID
	// are returned. Callers paginate by setting AfterUID to the last
	// UID seen in the previous page.
	AfterUID UID
	// Limit caps the number of returned rows. 0 applies the default
	// cap of 1000. Values above 1000 are silently lowered to 1000.
	Limit int
	// WithEnvelope, when true, requests that the backend populate
	// Message.Envelope. Callers that only need UID/Flags skip this to
	// avoid the extra columns. Backends are free to ignore it and
	// always populate the envelope; the flag is a permission to skip,
	// not a requirement.
	WithEnvelope bool
}

// BlobRef names a content-addressed blob in the blob store. A blob's
// identity is its BLAKE3 hash of the canonicalized message bytes (RFC 5322
// CRLF-normalized). Hash is lowercase hex.
type BlobRef struct {
	// Hash is the lowercase hex BLAKE3 digest of the canonical bytes.
	Hash string
	// Size is the length of the stored blob in bytes.
	Size int64
}

// ChangeKind classifies an entry in the per-principal state-change feed.
// See docs/architecture/05-sync-and-state.md.
type ChangeKind uint8

const (
	// ChangeKindUnknown is the zero value and must not be persisted.
	ChangeKindUnknown ChangeKind = iota
	// ChangeKindMessageCreated signals a new message row (delivery,
	// APPEND, JMAP Email/set create).
	ChangeKindMessageCreated
	// ChangeKindMessageUpdated signals a mutation to flags, keywords, or
	// mailbox placement (STORE, COPY/MOVE target).
	ChangeKindMessageUpdated
	// ChangeKindMessageDestroyed signals an EXPUNGE or JMAP destroy.
	ChangeKindMessageDestroyed
	// ChangeKindMailboxCreated signals a new mailbox.
	ChangeKindMailboxCreated
	// ChangeKindMailboxUpdated signals a mailbox rename or attribute change.
	ChangeKindMailboxUpdated
	// ChangeKindMailboxDestroyed signals mailbox deletion.
	ChangeKindMailboxDestroyed
)

// StateChange is one entry in a principal's monotonic state-change feed.
// Consumers (IMAP IDLE, JMAP push, FTS worker) read ranges of this feed
// with ReadChangeFeed to observe committed mutations.
type StateChange struct {
	// Seq is the principal-local monotonic sequence number. Strictly
	// increasing for a given PrincipalID; never reused after compaction.
	Seq ChangeSeq
	// PrincipalID is the owning principal.
	PrincipalID PrincipalID
	// Kind classifies the change (see ChangeKind*).
	Kind ChangeKind
	// MailboxID is the affected mailbox, or 0 for principal-scoped events.
	MailboxID MailboxID
	// MessageID is the affected message, or 0 for mailbox-scoped events.
	MessageID MessageID
	// MessageUID is the IMAP UID of the affected message within its
	// mailbox, or 0 for non-message events.
	MessageUID UID
	// ProducedAt is the instant the mutation's transaction committed,
	// captured from the injected Clock (not the wall clock).
	ProducedAt time.Time
}

// OIDCProvider describes a configured external OIDC issuer usable for
// per-user federation (REQ-AUTH-50..58).
type OIDCProvider struct {
	// Name is the operator-chosen short identifier (e.g. "google").
	Name string
	// IssuerURL is the provider's OIDC issuer URL.
	IssuerURL string
	// ClientID is the OAuth2 client identifier.
	ClientID string
	// ClientSecretRef is an opaque reference to the secret (env, file, or
	// external KMS); the store never stores secret material inline.
	ClientSecretRef string
	// Scopes is the set of OAuth2 scopes requested at sign-in.
	Scopes []string
	// AutoProvision permits creating a local principal on first
	// successful federation.
	AutoProvision bool
	// CreatedAt is the insert instant.
	CreatedAt time.Time
}

// OIDCLink associates a local Principal with an external OIDC identity,
// keyed by provider + subject.
type OIDCLink struct {
	// PrincipalID is the local principal.
	PrincipalID PrincipalID
	// ProviderName is the OIDCProvider.Name.
	ProviderName string
	// Subject is the provider's "sub" claim for this identity.
	Subject string
	// EmailAtProvider is the email the provider reported (for display and
	// audit); not used for routing.
	EmailAtProvider string
	// LinkedAt is the instant the link was established.
	LinkedAt time.Time
}

// APIKey is a long-lived token granting scoped API access to a Principal.
// Defined now for scheme stability; REST admin surface lands in a later wave.
type APIKey struct {
	// ID is the stable primary key.
	ID APIKeyID
	// PrincipalID is the owning principal.
	PrincipalID PrincipalID
	// Hash is the Argon2id / HMAC hash of the token. The plaintext token
	// is shown to the operator exactly once at creation time.
	Hash string
	// Name is the operator-supplied label for the key.
	Name string
	// CreatedAt is the insert instant.
	CreatedAt time.Time
	// LastUsedAt is the most recent successful authentication instant
	// using this key; zero value if never used.
	LastUsedAt time.Time
}

// ActorKind classifies the source of an audited action. The vocabulary
// is closed so forensic queries (`action by ActorSystem`) do not need to
// string-match freeform labels.
type ActorKind uint8

const (
	// ActorUnknown is the zero value. It must not be used by callers;
	// it appears only when a row predates the audit wiring or when the
	// code path neglected to classify — both are bugs.
	ActorUnknown ActorKind = iota
	// ActorPrincipal is a human user acting under their own principal.
	ActorPrincipal
	// ActorAPIKey is an automated client authenticated via an API key.
	ActorAPIKey
	// ActorSystem is an internal subsystem (scheduled jobs, migrations,
	// startup backfills). ActorID is the fixed string "system".
	ActorSystem
)

// AuditOutcome is the success/failure tag on an audit entry. Most
// mutations care about both outcomes; authentication failures land here
// the same way successes do (REQ-AUTH-62).
type AuditOutcome uint8

const (
	// OutcomeUnknown is the zero value and must not be persisted.
	OutcomeUnknown AuditOutcome = iota
	// OutcomeSuccess is the default for successful actions.
	OutcomeSuccess
	// OutcomeFailure tags a rejected or errored action.
	OutcomeFailure
)

// AuditLogEntry is one row in the append-only audit log (REQ-AUTH-62).
// Callers build the struct and hand it to Metadata.AppendAuditLog; the
// store fills ID and persists the value verbatim. Message MUST be
// pre-redacted (no plaintext passwords, tokens, or TOTP codes); the
// store treats it as opaque human-readable text.
type AuditLogEntry struct {
	// ID is the assigned primary key (populated by the store on append;
	// callers may leave it zero).
	ID AuditLogID
	// At is the event instant supplied by the injected clock. The store
	// uses this value verbatim so tests with a FakeClock are
	// deterministic.
	At time.Time
	// ActorKind classifies the actor (principal / API key / system).
	ActorKind ActorKind
	// ActorID is the actor's stable identifier: "<principal_id>" or
	// "<api_key_id>" or the literal "system". Freeform (string) rather
	// than typed because actors of different kinds collide in one column
	// and forensic reads need the raw token anyway.
	ActorID string
	// Action is the dotted action identifier, e.g. "principal.create",
	// "principal.delete", "totp.enroll", "oidc.link". Keep callers
	// namespaced per subsystem.
	Action string
	// Subject names the entity acted upon, formatted "kind:id" (e.g.
	// "principal:42", "domain:example.test"). Empty for actions that
	// target nothing in particular (admin login, system startup).
	Subject string
	// RemoteAddr is the originating IP for network actions; empty for
	// ActorSystem entries.
	RemoteAddr string
	// Outcome tags success / failure; FAILURE rows carry the reason in
	// Message.
	Outcome AuditOutcome
	// Message is the human-readable summary, already redacted. Secrets
	// MUST NOT appear here.
	Message string
	// Metadata carries typed key/value tags per action. Canonical keys
	// per subsystem live in the caller's package (e.g. directory's TOTP
	// path sets "method=totp", admin emits "target_email=..."). The
	// store serialises the map deterministically (sorted keys) to the
	// underlying column.
	Metadata map[string]string
}

// AuditLogFilter narrows a ListAuditLog read. Unset (zero) fields are
// treated as "no constraint". Limit is capped at 1000 server-side
// regardless of caller input.
type AuditLogFilter struct {
	// PrincipalID, when non-zero, restricts results to entries whose
	// Subject is "principal:<id>" OR whose ActorKind == ActorPrincipal
	// with ActorID == "<id>". This covers both "actions by this user"
	// and "actions upon this user".
	PrincipalID PrincipalID
	// Action, when non-empty, matches the Action column exactly.
	Action string
	// Since, when non-zero, excludes rows with At < Since.
	Since time.Time
	// Until, when non-zero, excludes rows with At >= Until.
	Until time.Time
	// Limit caps the returned slice length. 0 means the default (1000).
	// Values above 1000 are silently lowered to 1000.
	Limit int
	// AfterID is the keyset cursor: return rows with ID > AfterID.
	// Callers paginate by setting AfterID to the ID of the last row
	// from the previous page.
	AfterID AuditLogID
}
