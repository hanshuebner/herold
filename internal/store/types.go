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
// feed. See docs/design/server/architecture/05-sync-and-state.md.
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
	// SeenAddressesEnabled controls whether the server seeds SeenAddress rows
	// for this principal (REQ-SET-15). Defaults to true; set to false to
	// disable seeding and immediately purge all existing rows.
	SeenAddressesEnabled bool
	// AvatarBlobHash is the BLAKE3 hex hash of the principal's profile-
	// picture blob (REQ-SET-03b). Empty when no picture is set. Maintained
	// as the writethrough target of Identity/set on the synthesised default
	// identity so cross-user lookups (chat, mail thread headers) can resolve
	// the picture without leaking the in-process identity overlay.
	AvatarBlobHash string
	// AvatarBlobSize is the byte size of the avatar blob; zero when unset.
	AvatarBlobSize int64
	// XFaceEnabled controls whether outbound messages from this principal's
	// default identity carry the legacy X-Face / modern Face headers
	// (REQ-SET-03b / REQ-MAIL-45). Default false.
	XFaceEnabled bool
	// ClientlogTelemetryEnabled is the per-user override for client-log
	// behavioural telemetry (REQ-OPS-208, REQ-CLOG-06). nil means the
	// principal inherits the system-config default
	// ([clientlog.defaults].telemetry_enabled). A non-nil value takes
	// precedence over the system default in either direction. Only
	// kind=log and kind=vital events are gated; kind=error is always sent.
	ClientlogTelemetryEnabled *bool
	// CreatedAt is the instant the principal row was inserted.
	CreatedAt time.Time
	// UpdatedAt is the instant of the most recent mutation to the row.
	UpdatedAt time.Time
}

// Alias maps an email address to a target Principal. See REQ-STORE
// (aliases table in docs/design/server/architecture/02-storage-architecture.md §Schema).
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
	// Color is the optional JMAP-only mailbox colour extension
	// (REQ-PROTO-56 / REQ-STORE-34). When non-nil it is a hex literal
	// of the form "#RRGGBB" (six hex digits, leading '#'); nil means
	// unset and clients render their own default. Not advertised on
	// the IMAP wire — IMAP has no keyword for mailbox colour.
	Color *string
	// SortOrder is the JMAP Mailbox.sortOrder property (RFC 8621 §2.1).
	// Lower values sort first; 0 is the default. Clients use this to
	// reorder the mailbox list independently of the name.
	SortOrder uint32
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

// MessageMailbox holds the per-(message, mailbox) state as required by
// RFC 9051 §2.3.1.1 and REQ-STORE-36..38. One row in this struct
// corresponds to one row in the message_mailboxes join table.
//
// A message that lives in N mailboxes has N MessageMailbox entries,
// each with an independent UID, MODSEQ, flags, and keyword set.
// Callers that operate on a single mailbox (IMAP, most JMAP paths)
// receive the relevant entry pre-joined; multi-mailbox callers (JMAP
// mailboxIds rendering) iterate over Message.Mailboxes.
type MessageMailbox struct {
	// MessageID is the owning message (join key).
	MessageID MessageID
	// MailboxID is the containing mailbox (join key).
	MailboxID MailboxID
	// UID is the IMAP UID within MailboxID. Per-mailbox, not global.
	UID UID
	// ModSeq is the IMAP CONDSTORE modification sequence for this
	// (message, mailbox) pair. A flag change in mailbox A does not
	// bump the ModSeq in mailbox B.
	ModSeq ModSeq
	// Flags is the system-flag bitfield for this mailbox membership.
	Flags MessageFlags
	// Keywords is the list of user-defined IMAP keyword flags for this
	// mailbox membership (lowercase, per RFC 5788).
	Keywords []string
	// SnoozedUntil is the wake-up deadline for the JMAP snooze
	// extension (REQ-PROTO-49). Atomicity invariant: non-nil iff
	// Keywords contains "$snoozed". Enforced at the store boundary;
	// direct callers use SetSnooze.
	SnoozedUntil *time.Time
}

// Message is the mailbox-independent metadata for a delivered message.
// The message body is stored once by content hash (Blob) and referenced
// by hash. Per-(message, mailbox) state (UID, MODSEQ, flags, keywords)
// lives in MessageMailbox rows accessible via the Mailboxes field.
//
// For single-mailbox read paths (IMAP, most JMAP paths), the store
// populates the convenience fields MailboxID / UID / ModSeq / Flags /
// Keywords / SnoozedUntil from the first (or only) Mailboxes entry so
// existing callers do not need to change. Multi-mailbox callers should
// iterate Mailboxes directly.
type Message struct {
	// ID is the stable primary key.
	ID MessageID
	// PrincipalID is the owning principal (denormalised for query speed).
	// Required on insert; populated on read.
	PrincipalID PrincipalID

	// -- Convenience fields populated from the first Mailboxes entry ---
	// These are valid when the Message was returned by a mailbox-scoped
	// query (ListMessages, GetMessage with a mailbox context). Do not
	// rely on them when iterating a multi-mailbox response.

	// MailboxID is the first (or only) mailbox this message belongs to.
	MailboxID MailboxID
	// UID is the IMAP UID within MailboxID.
	UID UID
	// ModSeq is the IMAP CONDSTORE modification sequence for MailboxID.
	ModSeq ModSeq
	// Flags is the system-flag bitfield for MailboxID.
	Flags MessageFlags
	// Keywords is the list of user-defined IMAP keyword flags for MailboxID.
	Keywords []string
	// SnoozedUntil is the snooze deadline for MailboxID.
	SnoozedUntil *time.Time

	// -- Mailbox-independent fields ------------------------------------

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

	// -- Multi-mailbox membership (REQ-STORE-36) -----------------------

	// Mailboxes is the full set of per-(message, mailbox) rows for this
	// message, populated when the caller requests multi-mailbox data
	// (e.g. JMAP Email/get with mailboxIds). In single-mailbox paths the
	// slice contains exactly one entry (matching the convenience fields
	// above). May be nil when the caller has not requested it.
	Mailboxes []MessageMailbox
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
	// ReceivedBefore, when non-nil, restricts results to messages whose
	// InternalDate is strictly before this instant. Used by the trash
	// retention sweeper (REQ-STORE-90) to page through aged-out messages
	// without loading the full mailbox.
	ReceivedBefore *time.Time
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

// EntityKind names a JMAP-style datatype on the change feed. The set is
// open — v1 emits only the email/mailbox kinds; future kinds (e.g.
// addressbook, card, calendar, event) extend additively per
// docs/design/server/architecture/05-sync-and-state.md §Forward-compatibility.
//
// Consumers MUST dispatch on the string value (e.g.
// `change.Kind == EntityKindEmail`) and ignore unknown kinds, so a
// later wave can add a new datatype without touching the dispatch
// core in protoimap, protojmap, or the FTS worker.
type EntityKind string

const (
	// EntityKindMailbox is a JMAP `Mailbox` (also IMAP folder).
	EntityKindMailbox EntityKind = "mailbox"
	// EntityKindEmail is a JMAP `Email` (an IMAP message row).
	EntityKindEmail EntityKind = "email"
	// EntityKindEmailSubmission is a JMAP `EmailSubmission` row.
	EntityKindEmailSubmission EntityKind = "email_submission"
	// EntityKindIdentity is a JMAP `Identity` row.
	EntityKindIdentity EntityKind = "identity"
	// EntityKindVacationResponse is a JMAP `VacationResponse` row.
	EntityKindVacationResponse EntityKind = "vacation_response"
	// EntityKindAddressBook is a JMAP `AddressBook` row (REQ-PROTO-55,
	// RFC 9553). The state-change feed carries (Kind, EntityID =
	// AddressBookID, ParentEntityID = 0, Op).
	EntityKindAddressBook EntityKind = "address_book"
	// EntityKindContact is a JMAP `Contact` row (RFC 9553 JSContact).
	// The feed carries (Kind, EntityID = ContactID, ParentEntityID =
	// AddressBookID, Op) so per-book IMAP-IDLE-style filters dispatch
	// without a join.
	EntityKindContact EntityKind = "contact"
	// EntityKindCalendar is a JMAP `Calendar` row (REQ-PROTO-54,
	// RFC 8984). The state-change feed carries (Kind, EntityID =
	// CalendarID, ParentEntityID = 0, Op).
	EntityKindCalendar EntityKind = "calendar"
	// EntityKindCalendarEvent is a JMAP `CalendarEvent` row
	// (RFC 8984 JSCalendar). The feed carries (Kind, EntityID =
	// CalendarEventID, ParentEntityID = CalendarID, Op) so per-calendar
	// CalDAV-style filters dispatch without a join.
	EntityKindCalendarEvent EntityKind = "calendar_event"
	// EntityKindPushSubscription is a JMAP `PushSubscription` row
	// (REQ-PROTO-120 / RFC 8620 §7.2). The feed carries (Kind,
	// EntityID = PushSubscriptionID, ParentEntityID = 0, Op) so per-
	// principal subscription churn flows through the same change-feed
	// shape every other datatype uses; clients that subscribe to
	// "PushSubscription" via EventSource see their own administrative
	// changes (other devices that the same user logged in from).
	EntityKindPushSubscription EntityKind = "push_subscription"
	// EntityKindSeenAddress is a `SeenAddress` row (REQ-MAIL-11e..m).
	// The feed carries (Kind, EntityID = SeenAddressID, ParentEntityID = 0, Op).
	// Clients that watch SeenAddress via EventSource observe their own
	// autocomplete-history mutations (seed-on-send, seed-on-receive,
	// auto-promotion, privacy purge).
	EntityKindSeenAddress EntityKind = "seen_address"
)

// ChangeOp is the operation kind on a StateChange row. Distinct from
// EntityKind so consumers can dispatch on (kind, op) pairs (e.g. only
// the email-created case warrants an FTS index pass).
type ChangeOp uint8

const (
	// ChangeOpUnknown is the zero value; it must never be persisted and
	// is returned only by uninitialized decoders.
	ChangeOpUnknown ChangeOp = iota
	// ChangeOpCreated signals a new entity (delivery, APPEND, JMAP set
	// create, mailbox create).
	ChangeOpCreated
	// ChangeOpUpdated signals a mutation to an existing entity (STORE,
	// rename, JMAP set update).
	ChangeOpUpdated
	// ChangeOpDestroyed signals deletion (EXPUNGE, JMAP destroy,
	// mailbox delete).
	ChangeOpDestroyed
)

// StateChange is one entry in a principal's monotonic state-change feed.
// Consumers (IMAP IDLE, JMAP push, FTS worker) read ranges of this feed
// with ReadChangeFeed to observe committed mutations.
//
// The row is intentionally datatype-agnostic. The triple (Kind,
// EntityID, ParentEntityID) names the affected entity; per-type sync
// auxiliaries (IMAP UID, MODSEQ for emails) live on the type's own
// tables and are joined in by the consumer when needed. This keeps the
// JMAP `Foo/changes` dispatch path uniform across types and lets new
// datatypes be added additively per
// docs/design/server/architecture/05-sync-and-state.md §Forward-compatibility.
type StateChange struct {
	// Seq is the principal-local monotonic sequence number. Strictly
	// increasing for a given PrincipalID; never reused after compaction.
	Seq ChangeSeq
	// PrincipalID is the owning principal.
	PrincipalID PrincipalID
	// Kind names the JMAP datatype of the affected entity (e.g.
	// EntityKindEmail). Consumers MUST dispatch on this string and
	// ignore kinds they do not handle.
	Kind EntityKind
	// EntityID is the opaque id of the affected entity within Kind's
	// namespace (e.g. a MessageID when Kind == EntityKindEmail, a
	// MailboxID when Kind == EntityKindMailbox).
	EntityID uint64
	// ParentEntityID is an optional containing-entity id used by some
	// kinds — for EntityKindEmail it carries the MailboxID so per-
	// mailbox IMAP IDLE filters can dispatch without a join. Zero when
	// the kind has no natural parent.
	ParentEntityID uint64
	// Op classifies the mutation (created / updated / destroyed).
	Op ChangeOp
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
	// ScopeJSON is the JSON-encoded list of auth.Scope values the key
	// grants (REQ-AUTH-SCOPE-04). The store layer treats it as opaque
	// text; callers in internal/protoadmin and internal/admin parse +
	// validate the contents. The schema migration (0016) backfills
	// existing rows with '["admin"]' so legacy keys retain their
	// implicit pre-3.6 capability.
	ScopeJSON string
	// AllowedFromAddresses is the optional per-key allowlist of
	// sender addresses (REQ-SEND-30 / REQ-SEND-12).  When non-empty
	// the key may only send as one of these addresses.  The store
	// persists them as a JSON array; nil / empty means "no constraint".
	AllowedFromAddresses []string
	// AllowedFromDomains is the optional per-key allowlist of sender
	// domains (REQ-SEND-30).  When non-empty the from address must
	// belong to one of these domains.  Applied after AllowedFromAddresses.
	AllowedFromDomains []string
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

// SessionRow is one row in the sessions table. Each row is keyed on
// session_id, which matches the CSRFToken embedded in the signed session
// cookie (authsession.Session.CSRFToken). The row holds per-session state
// that cannot live in the stateless HMAC-signed cookie payload.
//
// clientlog_telemetry_enabled is the resolved effective flag: the store
// computes it once at session creation (principal override ?? system default)
// and caches it here so the clientlog ingest handler can call
// TelemetryGate.IsEnabled(sessionID) without a principal lookup.
//
// clientlog_livetail_until is the expiry for admin live-tail mode
// (REQ-OPS-211 / REQ-ADM-232); nil means live-tail is inactive.
type SessionRow struct {
	// SessionID is the CSRF token from the signed session cookie. It is
	// opaque to the store and serves only as the lookup key.
	SessionID string
	// PrincipalID is the owning principal. Used for cascade-delete on
	// DeletePrincipal and for auditing.
	PrincipalID PrincipalID
	// CreatedAt is the instant the session row was inserted.
	CreatedAt time.Time
	// ExpiresAt is the session expiry deadline (matches the cookie TTL).
	// Expired rows are not automatically evicted by the store; a sweeper
	// goroutine or VACUUM handles that. Readers treat expired rows as
	// not-found.
	ExpiresAt time.Time
	// ClientlogTelemetryEnabled is the effective resolved telemetry flag
	// at session creation / last refresh. Always non-nil (computed from
	// the principal's override and the system default passed by the
	// directory layer).
	ClientlogTelemetryEnabled bool
	// ClientlogLivetailUntil, when non-nil, marks the end of an active
	// admin live-tail session (REQ-OPS-211 / REQ-ADM-232). A nil value
	// means live-tail is inactive for this session.
	ClientlogLivetailUntil *time.Time
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
