package store

import (
	"context"
	"errors"
	"io"
	"time"
)

// Sentinel errors returned by Store implementations. Callers use
// errors.Is to classify failures; backends wrap these with additional
// context using fmt.Errorf("...: %w", err).
var (
	// ErrNotFound is returned when a lookup finds no row matching the
	// request (GetPrincipalByEmail, GetMessage, Blobs.Get, ...).
	ErrNotFound = errors.New("store: not found")

	// ErrConflict is returned when an optimistic-concurrency or
	// uniqueness constraint prevents an insert or update (duplicate
	// alias, MODSEQ UNCHANGEDSINCE failure, unique email).
	ErrConflict = errors.New("store: conflict")

	// ErrQuotaExceeded is returned when a write would push a principal
	// past its configured quota (REQ-STORE-50/51).
	ErrQuotaExceeded = errors.New("store: quota exceeded")
)

// Store is the composite handle every subsystem consumes to reach
// persistent state. Backends compose the three sub-interfaces
// (Metadata, Blobs, FTS) that cover the three logical stores described
// in docs/architecture/02-storage-architecture.md §Three stores.
//
// Implementations are constructed once at server startup and shared
// across all goroutines; every method is safe for concurrent use.
//
// ExampleStore in the package doc comment shows the expected usage
// pattern: call Meta for structured reads and writes, Blobs for body
// bytes, FTS for search, and Close on shutdown.
type Store interface {
	// Meta returns the metadata repository.
	Meta() Metadata
	// Blobs returns the content-addressed blob store.
	Blobs() Blobs
	// FTS returns the full-text search index.
	FTS() FTS
	// Close flushes in-memory state and releases backend resources. Must
	// be called exactly once during shutdown; subsequent calls return nil.
	Close() error
}

// Metadata is the typed repository over structured state: principals,
// mailboxes, messages, aliases, the state-change feed, and related
// tables. It is explicitly not a key/value store (STANDARDS.md §1 rule
// 7). New methods are added as real callers arrive; the surface here is
// the Phase 1 minimum.
//
// Every method takes a context.Context for cancellation and deadline
// propagation (STANDARDS.md §5). ctx is always the first parameter.
// Methods return ErrNotFound / ErrConflict / ErrQuotaExceeded at the
// sentinel's documented site; other errors are wrapped with fmt.Errorf.
type Metadata interface {
	// GetPrincipalByID returns the principal with the given ID.
	// Returns ErrNotFound if no such principal exists.
	GetPrincipalByID(ctx context.Context, id PrincipalID) (Principal, error)

	// GetPrincipalByEmail returns the principal whose CanonicalEmail
	// matches the lowercased argument. Returns ErrNotFound if no match.
	GetPrincipalByEmail(ctx context.Context, email string) (Principal, error)

	// InsertPrincipal writes a new principal row. The returned Principal
	// carries the assigned ID and store-resolved timestamps. Returns
	// ErrConflict on duplicate CanonicalEmail.
	InsertPrincipal(ctx context.Context, p Principal) (Principal, error)

	// UpdatePrincipal writes the mutable fields of p back to the store.
	// The ID must identify an existing row. Returns ErrNotFound if the
	// principal was deleted between read and write.
	UpdatePrincipal(ctx context.Context, p Principal) error

	// GetMailboxByID returns a single mailbox. Returns ErrNotFound if
	// no such mailbox exists.
	GetMailboxByID(ctx context.Context, id MailboxID) (Mailbox, error)

	// ListMailboxes returns the mailboxes owned by principalID in
	// lexicographic Name order. Returns an empty slice (nil) and nil
	// error if the principal has no mailboxes.
	ListMailboxes(ctx context.Context, principalID PrincipalID) ([]Mailbox, error)

	// InsertMailbox creates a new mailbox. The returned Mailbox carries
	// the assigned ID, freshly-generated UIDValidity, and store-resolved
	// timestamps. Returns ErrConflict on duplicate (principal_id, name).
	InsertMailbox(ctx context.Context, m Mailbox) (Mailbox, error)

	// DeleteMailbox removes the mailbox and all contained messages,
	// appends the corresponding StateChange entries, and decrements blob
	// refcounts for the deleted messages' blobs in the same transaction.
	// Returns ErrNotFound if the mailbox does not exist.
	DeleteMailbox(ctx context.Context, id MailboxID) error

	// GetMessage returns a single message row. Returns ErrNotFound if
	// the message does not exist.
	GetMessage(ctx context.Context, id MessageID) (Message, error)

	// InsertMessage inserts a message into its mailbox (msg.MailboxID),
	// allocating a fresh UID and ModSeq, advancing Mailbox.UIDNext and
	// Mailbox.HighestModSeq, appending a ChangeKindMessageCreated entry
	// to the principal's state-change feed, and incrementing the blob
	// refcount — all in a single transaction. Returns the allocated UID
	// and ModSeq. Returns ErrQuotaExceeded if the insert would exceed
	// the owning principal's quota.
	InsertMessage(ctx context.Context, msg Message) (UID, ModSeq, error)

	// UpdateMessageFlags applies flagAdd and flagClear (bitfield deltas)
	// plus keyword additions and removals to the message, bumps its
	// ModSeq and the mailbox's HighestModSeq, and appends a
	// ChangeKindMessageUpdated entry — atomically. unchangedSince, when
	// non-zero, implements IMAP STORE UNCHANGEDSINCE (RFC 7162 §3.1.3):
	// the update is rejected with ErrConflict if the message's current
	// ModSeq exceeds it. Returns the new ModSeq on success.
	UpdateMessageFlags(
		ctx context.Context,
		id MessageID,
		flagAdd, flagClear MessageFlags,
		keywordAdd, keywordClear []string,
		unchangedSince ModSeq,
	) (ModSeq, error)

	// ExpungeMessages removes the named messages from their mailbox,
	// decrements blob refcounts, bumps HighestModSeq, and appends
	// ChangeKindMessageDestroyed entries — atomically. Silently skips
	// IDs that are already gone; returns ErrNotFound only if every ID is
	// absent.
	ExpungeMessages(ctx context.Context, mailboxID MailboxID, ids []MessageID) error

	// UpdateMailboxModseqAndAppendChange is the low-level escape hatch
	// used by protocol code that has already computed a multi-row
	// mutation and needs to advance HighestModSeq and append a
	// StateChange atomically (for example, a bulk STORE over thousands
	// of UIDs grouped at the IMAP layer). The returned ModSeq is the
	// new HighestModSeq and the Seq field on the appended change.
	UpdateMailboxModseqAndAppendChange(
		ctx context.Context,
		mailboxID MailboxID,
		change StateChange,
	) (ModSeq, ChangeSeq, error)

	// ReadChangeFeed returns up to max StateChange entries with Seq
	// strictly greater than fromSeq, in ascending Seq order, for the
	// given principal. When the feed end is reached the returned slice
	// is shorter than max (possibly empty). Consumers persist the last
	// observed Seq as their cursor; see ReadChangeFeedForFTS for the
	// cross-principal variant used by the FTS worker.
	ReadChangeFeed(
		ctx context.Context,
		principalID PrincipalID,
		fromSeq ChangeSeq,
		max int,
	) ([]StateChange, error)

	// InsertAlias creates an alias mapping. Returns ErrConflict on
	// duplicate (local_part, domain).
	InsertAlias(ctx context.Context, a Alias) (Alias, error)

	// ResolveAlias looks up the principal an address routes to. Returns
	// ErrNotFound if no matching alias or canonical address exists.
	ResolveAlias(ctx context.Context, localPart, domain string) (PrincipalID, error)

	// InsertDomain records a local domain. Returns ErrConflict on
	// duplicate Name.
	InsertDomain(ctx context.Context, d Domain) error

	// GetDomain returns the domain row for name, or ErrNotFound.
	GetDomain(ctx context.Context, name string) (Domain, error)

	// ListLocalDomains returns every domain with IsLocal == true.
	ListLocalDomains(ctx context.Context) ([]Domain, error)

	// InsertOIDCProvider records a new OIDC provider configuration.
	// Returns ErrConflict on duplicate Name.
	InsertOIDCProvider(ctx context.Context, p OIDCProvider) error

	// GetOIDCProvider returns the provider row, or ErrNotFound.
	GetOIDCProvider(ctx context.Context, name string) (OIDCProvider, error)

	// ListOIDCProviders returns every configured OIDC provider. v1
	// deployments carry tens of providers at most; no pagination is
	// offered or needed.
	ListOIDCProviders(ctx context.Context) ([]OIDCProvider, error)

	// DeleteOIDCProvider removes a provider and cascades its oidc_links
	// rows in one transaction. Returns ErrNotFound if no provider with
	// the given id exists.
	DeleteOIDCProvider(ctx context.Context, id OIDCProviderID) error

	// LinkOIDC associates a principal with an external OIDC identity.
	// Returns ErrConflict if (provider, subject) is already linked.
	LinkOIDC(ctx context.Context, link OIDCLink) error

	// LookupOIDCLink returns the principal associated with (provider,
	// subject), or ErrNotFound.
	LookupOIDCLink(ctx context.Context, provider, subject string) (OIDCLink, error)

	// UnlinkOIDC removes the oidc_links row for (pid, providerID).
	// Returns ErrNotFound if no such link exists.
	UnlinkOIDC(ctx context.Context, pid PrincipalID, providerID OIDCProviderID) error

	// InsertAPIKey stores an API key row and returns it with the
	// assigned ID and CreatedAt filled.
	InsertAPIKey(ctx context.Context, k APIKey) (APIKey, error)

	// GetAPIKeyByHash returns the key whose Hash matches, or ErrNotFound.
	// Callers compare the client-supplied token against Hash using a
	// constant-time comparison before calling this method's result-
	// dependent paths.
	GetAPIKeyByHash(ctx context.Context, hash string) (APIKey, error)

	// TouchAPIKey updates the LastUsedAt timestamp of a key. Returns
	// ErrNotFound if the key has been revoked since the caller loaded it.
	TouchAPIKey(ctx context.Context, id APIKeyID, at time.Time) error

	// DeletePrincipal removes a principal and every row that belongs to
	// it — aliases, OIDC links, API keys, mailboxes, messages-in-mailboxes,
	// per-principal state-change entries, and per-principal audit-log
	// entries — in a single transaction. Returns ErrNotFound if pid does
	// not exist. Blob refcounts are decremented for every removed
	// message so the blob GC can reclaim bytes once the grace window
	// elapses; the method does not itself call Blobs.Delete.
	DeletePrincipal(ctx context.Context, pid PrincipalID) error

	// ListPrincipals returns principals with ID > after, in ascending ID
	// order, up to limit entries. Callers paginate by feeding the last
	// returned ID back as after; a zero after starts at the first row.
	// A non-positive limit applies the default cap of 1000; any value
	// above 1000 is silently lowered to 1000 to bound memory.
	ListPrincipals(ctx context.Context, after PrincipalID, limit int) ([]Principal, error)

	// GetFTSCursor returns the persisted cursor value for key, or
	// (0, nil) when no row exists (the consumer starts from the
	// beginning). Used by the FTS indexer (key == "fts") and reserved
	// for future change-feed consumers (DKIM report worker, external
	// webhook relays) so one cursors table carries them all.
	GetFTSCursor(ctx context.Context, key string) (uint64, error)

	// SetFTSCursor upserts the cursor value for key. Idempotent: safe to
	// call with the same (key, seq) twice. Returns an error on backend
	// failures but not on a no-op repeat write.
	SetFTSCursor(ctx context.Context, key string, seq uint64) error

	// AppendAuditLog writes entry to the append-only audit log. The
	// store fills entry.ID and persists the passed At timestamp
	// verbatim. Message MUST be pre-redacted; the store does not inspect
	// it. Returns an error on backend failure; otherwise nil.
	AppendAuditLog(ctx context.Context, entry AuditLogEntry) error

	// ListAuditLog returns audit entries matching filter, in ascending
	// ID order, up to filter.Limit (capped at 1000 server-side). An
	// empty filter returns the first page of all entries. Use
	// filter.AfterID to paginate.
	ListAuditLog(ctx context.Context, filter AuditLogFilter) ([]AuditLogEntry, error)

	// GetMailboxByName returns the mailbox owned by pid whose Name
	// matches name case-sensitively (INBOX normalisation is the
	// caller's responsibility). Returns ErrNotFound when no such
	// mailbox exists.
	GetMailboxByName(ctx context.Context, pid PrincipalID, name string) (Mailbox, error)

	// ListMessages returns the messages in mailboxID ordered by UID
	// ascending, subject to the filter's cursor + limit. The returned
	// slice is always a fresh copy safe for the caller to mutate.
	ListMessages(ctx context.Context, mailboxID MailboxID, filter MessageFilter) ([]Message, error)

	// SetMailboxSubscribed toggles the MailboxAttrSubscribed bit on the
	// mailbox and bumps UpdatedAt. Returns ErrNotFound if the mailbox
	// has been deleted.
	SetMailboxSubscribed(ctx context.Context, mailboxID MailboxID, subscribed bool) error

	// RenameMailbox changes the Name of a mailbox. Returns
	// ErrNotFound if the mailbox does not exist and ErrConflict when
	// the new name collides with an existing mailbox for the same
	// principal.
	RenameMailbox(ctx context.Context, mailboxID MailboxID, newName string) error

	// GetSieveScript returns the active Sieve script text for pid, or
	// ("", nil) when no script is on record (the interpreter then
	// falls back to implicit keep). An I/O error on the backend is
	// reported through the error channel; callers collapse any error
	// to implicit-keep delivery.
	GetSieveScript(ctx context.Context, pid PrincipalID) (string, error)

	// SetSieveScript upserts the active Sieve script for pid. An empty
	// text deletes the row so a subsequent GetSieveScript returns
	// ("", nil). RFC 5804 SETACTIVE semantics (multiple named scripts)
	// land in Phase 2 alongside ManageSieve; Phase 1 is one script per
	// principal.
	SetSieveScript(ctx context.Context, pid PrincipalID, text string) error
}

// Blobs is the content-addressed blob surface: one object per canonical
// message body, identified by BLAKE3 hex hash (REQ-STORE-10..16).
//
// Writes are atomic at the filesystem level (temp + fsync + rename, see
// docs/architecture/02-storage-architecture.md §Writes). Reads are
// streamed; Get returns an io.ReadCloser the caller must Close.
type Blobs interface {
	// Put writes the bytes read from r as a new blob, canonicalizing
	// CRLF line endings before hashing (REQ-STORE-10). Returns the
	// BlobRef identifying the stored content. Idempotent: storing the
	// same canonical content twice returns the same hash and leaves the
	// existing file untouched.
	Put(ctx context.Context, r io.Reader) (BlobRef, error)

	// Get opens the blob for streaming read. Returns ErrNotFound if the
	// blob does not exist. The caller must Close the returned reader.
	Get(ctx context.Context, hash string) (io.ReadCloser, error)

	// Stat returns the blob size and refcount. Returns ErrNotFound if
	// the blob is not present in the blob store (irrespective of any
	// metadata rows that may still reference it).
	Stat(ctx context.Context, hash string) (size int64, refs int, err error)

	// Delete removes the blob from the underlying filesystem. It is the
	// GC worker's responsibility to call Delete only on blobs whose
	// refcount is zero and whose grace window has elapsed
	// (REQ-STORE-12). Returns ErrNotFound if the blob is already gone;
	// does not return ErrConflict on positive refcount (refcounts live
	// in Metadata, not here).
	Delete(ctx context.Context, hash string) error
}

// FTSChange is the cross-principal change record delivered to the
// background FTS indexing worker. Unlike StateChange, which is
// per-principal for sync consumers, FTSChange carries the minimal fields
// the indexer needs and is strictly ordered by Seq across the whole
// store (so one cursor tracks all FTS work).
type FTSChange struct {
	// Seq is the store-global monotonic sequence used as the indexer's
	// durable cursor.
	Seq uint64
	// PrincipalID is the owning principal.
	PrincipalID PrincipalID
	// MailboxID is the affected mailbox.
	MailboxID MailboxID
	// MessageID is the affected message.
	MessageID MessageID
	// Kind classifies the change (same vocabulary as StateChange).
	Kind ChangeKind
	// ProducedAt is the transaction commit instant from the injected
	// Clock.
	ProducedAt time.Time
}

// MessageRef is the minimal identifier returned from a search hit:
// enough to fetch the full message from Metadata without carrying the
// whole row through the index layer.
type MessageRef struct {
	// MessageID is the hit's message row ID.
	MessageID MessageID
	// MailboxID is the containing mailbox (useful for per-mailbox
	// IMAP SEARCH scoping before a Metadata round trip).
	MailboxID MailboxID
	// Score is the backend-defined relevance score; higher is more
	// relevant. Comparable only within a single Query call.
	Score float64
}

// Query is the structured FTS query accepted by FTS.Query. Fields are
// AND-combined; each individual field is a user-supplied phrase that
// the backend tokenizes per its configured analyzer (REQ-STORE-64).
//
// The surface is intentionally narrow for Wave 0: Phase 1 needs
// mailbox-scoped keyword searches (IMAP SEARCH) and JMAP Email/query's
// text filter. Richer predicates (date ranges, faceted flags) arrive
// as additional methods when real callers need them — no speculative
// fields here.
type Query struct {
	// MailboxID, if non-zero, restricts the search to a single mailbox.
	// Zero searches all of the principal's mailboxes.
	MailboxID MailboxID
	// Text is a free-text search across all indexed fields.
	Text string
	// Subject, From, To, Body, AttachmentName are per-field term lists.
	// Nil or empty slices are ignored.
	Subject        []string
	From           []string
	To             []string
	Body           []string
	AttachmentName []string
	// Limit caps the result set. 0 means "backend default" (typically
	// 1000). Backends MUST cap at a hard ceiling regardless of caller
	// input.
	Limit int
}

// FTS is the full-text search surface: write path (called from the
// background indexing worker), read path (called from IMAP SEARCH and
// JMAP Email/query), and a change-feed hook the worker polls.
//
// Indexing is asynchronous by design (REQ-STORE-66, docs/notes/
// spike-fts-cadence.md). The recommended cadence is batch=2000 with a
// 500 ms ceiling; see the spike note for supporting measurements.
type FTS interface {
	// IndexMessage writes (or replaces) the FTS document for msg using
	// the already-extracted plain text. The worker performs MIME parsing
	// and attachment extraction upstream; this method assumes text is
	// ready. Idempotent: re-indexing the same MessageID overwrites.
	IndexMessage(ctx context.Context, msg Message, text string) error

	// RemoveMessage deletes the FTS document for id. Idempotent.
	RemoveMessage(ctx context.Context, id MessageID) error

	// Query runs the search against principalID's indexed mailboxes and
	// returns matches in descending Score order, up to q.Limit.
	Query(ctx context.Context, principalID PrincipalID, q Query) ([]MessageRef, error)

	// ReadChangeFeedForFTS is the indexer worker's polling hook: return
	// up to max changes with Seq > cursor, ordered by ascending Seq. The
	// returned Seq values are the worker's durable cursor advance. When
	// the feed end is reached the returned slice is shorter than max;
	// callers sleep briefly and poll again rather than blocking.
	ReadChangeFeedForFTS(
		ctx context.Context,
		cursor uint64,
		max int,
	) ([]FTSChange, error)

	// Commit flushes any in-memory batch to the index backend. Callers
	// trigger it on size OR time (2000 docs OR 500 ms, per the spike).
	// Safe to call on an empty batch; a no-op then.
	Commit(ctx context.Context) error
}
