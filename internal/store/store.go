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

	// ErrInvalidArgument is returned when an input argument fails the
	// store's typed validation (e.g. a malformed Mailbox.Color hex
	// literal). Distinct from ErrConflict (which is reserved for
	// uniqueness or optimistic-concurrency collisions) so callers can
	// surface the difference to clients.
	ErrInvalidArgument = errors.New("store: invalid argument")

	// ErrTooManyFilters is returned by InsertTaggedAddressFilter when
	// the principal already holds MaxTaggedAddressFiltersPerPrincipal
	// rows (REQ-TAG-11). Distinct from ErrQuotaExceeded so the JMAP /
	// REST surface can map this to a `too_many_filters` error token
	// without conflating it with the storage quota.
	ErrTooManyFilters = errors.New("store: too many filters")

	// ErrTooManyDismissals is returned by InsertTaggedAddressDismissal
	// when the principal already holds
	// MaxTaggedAddressDismissalsPerPrincipal rows (REQ-TAG-11).
	ErrTooManyDismissals = errors.New("store: too many dismissals")

	// ErrRateLimited is returned by RotateIdentityVerificationToken
	// when the resend rate limit (cooldown or daily cap, REQ-IDENT-36)
	// would be violated. Callers surface this as HTTP 429 with a
	// Retry-After header. The store consults its own clock to compute
	// the window state; the cooldown/cap thresholds are supplied as
	// arguments so the operator's tunables flow in from sysconfig.
	ErrRateLimited = errors.New("store: rate limited")

	// ErrTooManyShares is returned by FileShares.Create when the
	// principal already holds the configured MaxSharesPerPrincipal
	// active+pending shares (REQ-SHARE-12). Distinct from
	// ErrQuotaExceeded so the JMAP surface can map this to a
	// "too_many_shares" error token.
	ErrTooManyShares = errors.New("store: too many shares")

	// ErrShareNotConfirmable is returned by FileShares.Confirm when the
	// share is in state revoked or does not exist, and therefore cannot
	// be transitioned to active. Confirming an already-active share is
	// idempotent (returns nil). REQ-SHARE-21.
	ErrShareNotConfirmable = errors.New("store: share not confirmable")

	// ErrShareExpiryNotShortenable is returned by UpdateFileShareExpiry
	// when the requested newExpiry is not strictly earlier than the
	// current ExpiresAt (i.e. the caller is trying to extend or keep
	// the expiry unchanged). REQ-SHARE-42.
	ErrShareExpiryNotShortenable = errors.New("store: share expiry not shortenable")

	// ErrShareMaxDownloadsNotLowerable is returned by
	// UpdateFileShareMaxDownloads when the requested newMax is not
	// strictly less than the current MaxDownloads, or when newMax is
	// below the existing DownloadCount. REQ-SHARE-42.
	ErrShareMaxDownloadsNotLowerable = errors.New("store: share max-downloads not lowerable")
)

// Store is the composite handle every subsystem consumes to reach
// persistent state. Backends compose the three sub-interfaces
// (Metadata, Blobs, FTS) that cover the three logical stores described
// in docs/design/server/architecture/02-storage-architecture.md §Three stores.
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

	// GetMessage returns a single message row with its full Mailboxes
	// slice populated. Returns ErrNotFound if the message does not exist.
	GetMessage(ctx context.Context, id MessageID) (Message, error)

	// InsertMessage inserts a message row (msg) together with one or more
	// per-(message, mailbox) rows (targets). Each target entry carries the
	// MailboxID, UID, ModSeq, Flags, and Keywords for that mailbox
	// membership; the store allocates fresh UIDs and MODSEQs and fills
	// the UID/ModSeq fields in the returned slice.
	//
	// Atomically: inserts messages row, inserts N message_mailboxes rows,
	// advances each mailbox's highest_uid / highest_modseq, appends N
	// (EntityKindEmail, ChangeOpCreated) state-change entries, and
	// increments the blob refcount by 1 (regardless of N).
	//
	// msg.PrincipalID must be set. Returns ErrQuotaExceeded if the insert
	// would exceed the principal's quota. Returns the first target's UID
	// and ModSeq for backward-compatibility with single-mailbox callers.
	InsertMessage(ctx context.Context, msg Message, targets []MessageMailbox) (UID, ModSeq, error)

	// InsertMessages inserts a batch of messages in a single
	// transaction so the underlying SQLite WAL fsync (or Postgres COMMIT
	// round-trip) amortises across the batch instead of firing per
	// message. All-or-nothing: if any item fails the entire batch rolls
	// back. Same atomic semantics as InsertMessage applied N times in a
	// row, just under a shared transaction.
	//
	// Returns one result per item in the same order. The result's
	// (UID, ModSeq) are taken from the item's first target — matching
	// the single-target convention of InsertMessage's return shape.
	//
	// Used by the bulk gmail importer (REQ-IMPORT-29). Real-time
	// SMTP / JMAP delivery callers should keep using InsertMessage one
	// at a time so a single bad message doesn't take down a batch's
	// worth of unrelated delivery work.
	InsertMessages(ctx context.Context, items []InsertMessageItem, opts InsertMessagesOptions) ([]InsertMessageResult, error)

	// RethreadPrincipal walks every message of principalID in
	// internal-date order and assigns thread_id by looking up each
	// message's In-Reply-To / References headers against the
	// principal's own message-id index. Used after a bulk import where
	// InsertMessages was called with SkipThreading=true.
	//
	// Single transaction, single fsync at end. Returns the count of
	// messages whose thread_id was changed (0 means no work was needed
	// because every message already had a thread_id).
	RethreadPrincipal(ctx context.Context, principalID PrincipalID) (int, error)

	// AddMessageToMailbox adds an existing message (by msgID) to
	// mailboxID, allocating a fresh UID and ModSeq for that mailbox.
	// Returns the allocated UID and ModSeq. Returns ErrNotFound if msgID
	// does not exist. Returns ErrConflict if the message is already in
	// that mailbox (idempotency: callers should check first).
	AddMessageToMailbox(ctx context.Context, msgID MessageID, mailboxID MailboxID) (UID, ModSeq, error)

	// RemoveMessageFromMailbox removes the (msgID, mailboxID) membership
	// row. If the message has no remaining memberships the messages row
	// and blob refcount are cleaned up in the same transaction. Returns
	// ErrNotFound if the row is absent. Does NOT return ErrNotFound when
	// the message row itself still exists in other mailboxes — only the
	// membership is gone.
	RemoveMessageFromMailbox(ctx context.Context, msgID MessageID, mailboxID MailboxID) error

	// UpdateMessageFlags applies flagAdd and flagClear (bitfield deltas)
	// plus keyword additions and removals to the per-(message, mailbox)
	// row identified by (id, mailboxID), bumps its ModSeq and the
	// mailbox's HighestModSeq, and appends an (EntityKindEmail,
	// ChangeOpUpdated) entry — atomically. Flag changes in one mailbox do
	// not affect other mailbox memberships.
	// unchangedSince, when non-zero, implements IMAP STORE UNCHANGEDSINCE
	// (RFC 7162 §3.1.3): the update is rejected with ErrConflict if the
	// message's current ModSeq in that mailbox exceeds it. Returns the
	// new ModSeq on success.
	UpdateMessageFlags(
		ctx context.Context,
		id MessageID,
		mailboxID MailboxID,
		flagAdd, flagClear MessageFlags,
		keywordAdd, keywordClear []string,
		unchangedSince ModSeq,
	) (ModSeq, error)

	// ReplaceMessageBody swaps the body blob and envelope cache for an
	// existing message, preserving its MessageID, mailbox memberships,
	// flags, and keywords. Used by JMAP Email/set update on draft messages
	// when the patch supplies new body content (bodyValues / bodyStructure
	// / textBody / htmlBody) or new header fields (subject, from, to, …).
	//
	// Atomically: updates messages.{blob_hash, blob_size, size, env_*},
	// decrements the old blob refcount, increments the new blob refcount,
	// adjusts the principal's used_bytes by (newSize - oldSize), bumps each
	// mailbox membership's modseq + the mailbox's highest_modseq, and
	// appends one (EntityKindEmail, ChangeOpUpdated) state-change entry per
	// mailbox membership.
	//
	// The caller must already have written the new body via Blobs().Put;
	// ref must point at that newly-written blob. Returns ErrNotFound if id
	// no longer exists. Returns ErrQuotaExceeded if (newSize - oldSize) > 0
	// and the principal would exceed its quota.
	ReplaceMessageBody(
		ctx context.Context,
		id MessageID,
		ref BlobRef,
		size int64,
		env Envelope,
	) error

	// ExpungeMessages removes the named messages from their mailbox,
	// decrements blob refcounts, bumps HighestModSeq, and appends
	// (EntityKindEmail, ChangeOpDestroyed) entries — atomically.
	// Silently skips IDs that are already gone; returns ErrNotFound only
	// if every ID is absent.
	ExpungeMessages(ctx context.Context, mailboxID MailboxID, ids []MessageID) error

	// MoveMessage atomically moves the (msgID, fromMailboxID) membership
	// to targetMailboxID: deletes the source message_mailboxes row and
	// inserts a new row in the target mailbox with a fresh UID and ModSeq.
	// The messages row ID (and therefore the JMAP Email id) is preserved.
	// Returns ErrNotFound if the (msgID, fromMailboxID) membership is absent.
	MoveMessage(ctx context.Context, msgID MessageID, fromMailboxID, targetMailboxID MailboxID) error

	// UpdateMessageThreadID sets the thread_id of message msgID to
	// threadID. Used by the post-import thread re-linking pass when
	// emails are imported out of order (replies before originals).
	// Does NOT bump HighestModSeq or add change-feed entries — thread
	// re-linking is transparent to IMAP clients.
	UpdateMessageThreadID(ctx context.Context, msgID MessageID, threadID uint64) error

	// MarkMessageInternalizePending sets the per-message
	// internalize_pending flag to 1. Importers call this for every
	// message that has an external HTML image reference, telling the
	// JMAP Email/get path to rewrite the body on first read
	// (17-external-images.md REQ-EXTIMG-91 / REQ-EXTIMG-97). Returns
	// ErrNotFound when msgID is absent.
	MarkMessageInternalizePending(ctx context.Context, msgID MessageID) error

	// ClearMessageInternalizePending sets the flag back to 0 without
	// touching the body. Called by the on-demand rewrite path after a
	// rewrite attempt — successful or not — to prevent retry storms
	// (REQ-EXTIMG-94). ReplaceMessageBody clears the flag implicitly
	// in the same transaction; this method is for the failure /
	// no-op cases. Returns ErrNotFound when msgID is absent.
	ClearMessageInternalizePending(ctx context.Context, msgID MessageID) error

	// ListMessagesWithInternalizePending returns up to limit MessageIDs
	// in DESCENDING order (newest first) with id < beforeID, scanning
	// only rows with internalize_pending = 1. Pass beforeID = 0 to
	// start from the newest pending message; the implementation treats
	// the zero value as a sentinel for "no upper bound" (effectively
	// MAX_INT64). Used by tests that need deterministic id ordering;
	// production callers (the extimg internalize-worker) moved to
	// ListMessagesWithInternalizePendingByReceivedAt
	// (REQ-EXTIMG-BG-INTERNAL-80..81) so newly-arrived mail wins on
	// received_at_us rather than on the message-id assigned at insert
	// time -- the two diverge during a bulk import.
	ListMessagesWithInternalizePending(ctx context.Context, beforeID MessageID, limit int) ([]MessageID, error)

	// ListMessagesWithInternalizePendingByReceivedAt returns up to
	// `limit` PendingMessageRefs where internalize_pending = 1 AND
	// (received_at_us, id) lexicographically less than (beforeReceivedAtUs,
	// beforeMessageID), ordered DESC. Pass beforeReceivedAtUs = 0
	// (treated as the maximum sentinel by the implementation) and
	// beforeMessageID = 0 to start from the most-recent pending
	// message. The worker maintains an in-memory cursor as the
	// (received_at_us, id) pair of the lowest item from the previous
	// batch (the rows are returned descending so the last item is the
	// lowest cursor value).
	//
	// REQ-EXTIMG-BG-INTERNAL-80..81. Production caller (the extimg
	// internalize-worker) uses this method; the legacy id-ordered
	// ListMessagesWithInternalizePending stays for tests that need
	// deterministic id ordering.
	ListMessagesWithInternalizePendingByReceivedAt(
		ctx context.Context,
		beforeReceivedAtUs int64,
		beforeMessageID MessageID,
		limit int,
	) ([]PendingMessageRef, error)

	// CountInternalizePending returns the number of messages owned by
	// principalID with internalize_pending = 1. Used by the JMAP
	// session capability descriptor (REQ-EXTIMG-BG-04) so the suite
	// SPA can show a "images being processed" notice during the
	// import-drain phase.
	CountInternalizePending(ctx context.Context, principalID PrincipalID) (uint64, error)

	// CountInternalizePendingTotal returns the number of rows with
	// internalize_pending = 1 across every principal. Used by the
	// internalize-worker observability surface (REQ-EXTIMG-BG-INTERNAL-52)
	// so the operator sees the backlog magnitude at boot and via the
	// periodic progress beacon. Cheaper than per-principal aggregation
	// at scale: a single SELECT COUNT(*) over the partial index, no
	// principal enumeration.
	CountInternalizePendingTotal(ctx context.Context) (uint64, error)

	// SetMessageBodyMeta writes precomputed body metadata for the message
	// identified by id: sets preview, has_attachment, and flips
	// body_meta_computed to true (1) in a single atomic UPDATE. Idempotent:
	// calling again with updated values is safe and overwrites the prior
	// state. Returns ErrNotFound when id no longer exists.
	SetMessageBodyMeta(ctx context.Context, id MessageID, preview string, hasAttachment bool) error

	// ListMessagesNeedingBodyMeta returns up to limit MessageIDs in
	// DESCENDING order (newest first, highest ID first) with id < beforeID,
	// scanning only rows where body_meta_computed = 0. Pass beforeID = 0 to
	// start from the newest uncomputed message (the implementation treats 0
	// as a sentinel for "no upper bound", effectively MAX_INT64). The partial
	// index idx_messages_body_meta_pending makes this scan cheap regardless
	// of table size. Used by the background sweep worker that fills in
	// Preview + HasAttachment without touching protocol handlers.
	ListMessagesNeedingBodyMeta(ctx context.Context, beforeID MessageID, limit int) ([]MessageID, error)

	// -- Blob part index (re #46) -----------------------------------------

	// PutBlobPartIndex upserts the serialised MIME part index for a blob.
	// The store treats partsJSON as opaque bytes; the caller owns the schema.
	// Idempotent on blobHash: a second call overwrites version, json, and
	// computedAtUS. computedAtUS is wall-clock microseconds from the
	// caller's injected clock.
	PutBlobPartIndex(ctx context.Context, blobHash string, version int, partsJSON []byte, computedAtUS int64) error

	// GetBlobPartIndex returns the stored index version and JSON for blobHash.
	// Returns ErrNotFound when no row exists.
	GetBlobPartIndex(ctx context.Context, blobHash string) (version int, partsJSON []byte, err error)

	// DeleteBlobPartIndex removes the index row for blobHash. Returns nil
	// when the row is absent (idempotent; a future blob-GC pass calls this
	// when reaping a source blob).
	DeleteBlobPartIndex(ctx context.Context, blobHash string) error

	// ListMessagesNeedingPartIndex returns up to limit MessageIDs, newest
	// first (id DESC), for messages whose blob_hash has no blob_part_index
	// row at index_version >= minVersion. beforeID pages the sweep: pass 0
	// to start from the newest message (treated as MAX_INT64 internally);
	// subsequent pages pass the last returned ID. Mirrors
	// ListMessagesNeedingBodyMeta's paging contract exactly.
	ListMessagesNeedingPartIndex(ctx context.Context, beforeID MessageID, minVersion int, limit int) ([]MessageID, error)

	// AppendStateChange writes a single change-feed row directly,
	// honouring the supplied PrincipalID, Kind, EntityID,
	// ParentEntityID, Op and Cause. It is intended for synthetic /
	// non-entity-bound rows -- v1 use site is the extimg
	// internalize-worker driving the InternalizeStatus push channel
	// (REQ-EXTIMG-BG-INTERNAL-22). The store assigns Seq atomically;
	// callers do not pre-compute it. Cause defaults to user when the
	// caller leaves it empty.
	AppendStateChange(ctx context.Context, change StateChange) error

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
	// given principal. Default cause filter: cause = 'user' only. JMAP
	// /changes / EventSource and any consumer that mirrors the
	// user-visible state of an entity must call this method;
	// ReadChangeFeedAll is the explicit opt-in for consumers (IMAP
	// IDLE, FTS, seen-address) that need every cause. Un-migrated
	// callers therefore fail closed (less data, not more) per
	// REQ-EXTIMG-BG-INTERNAL-11. When the feed end is reached the
	// returned slice is shorter than max (possibly empty); consumers
	// persist the last observed Seq as their cursor. See
	// ReadChangeFeedForFTS for the cross-principal variant used by the
	// FTS worker.
	ReadChangeFeed(
		ctx context.Context,
		principalID PrincipalID,
		fromSeq ChangeSeq,
		max int,
	) ([]StateChange, error)

	// ReadChangeFeedAll is the cause-blind read used by IMAP IDLE
	// (CONDSTORE / FETCH must observe every byte-level mutation), the
	// FTS-feed reader, and the seen-address indexer
	// (REQ-EXTIMG-BG-INTERNAL-13/-14). The returned rows include
	// cause = 'background' entries written by the extimg
	// internalize-worker (REQ-EXTIMG-BG-INTERNAL-15) alongside the
	// regular cause = 'user' rows; otherwise the read shape is
	// identical to ReadChangeFeed.
	ReadChangeFeedAll(
		ctx context.Context,
		principalID PrincipalID,
		fromSeq ChangeSeq,
		max int,
	) ([]StateChange, error)

	// GetMaxChangeSeqForKind returns the highest Seq in the change feed
	// for the given principal and entity kind, or 0 when no matching row
	// exists. Filters cause = 'user' only (REQ-EXTIMG-BG-INTERNAL-10):
	// background-cause writes from the extimg internalize-worker do not
	// advance the JMAP-visible state, mirroring the ReadChangeFeed
	// default. Used by JMAP Foo/changes and Foo/set to derive per-type
	// state strings directly from the change feed, so that mutations made
	// outside the JMAP layer (e.g. IMAP APPEND, principal provisioning)
	// are reflected in state strings without a separate counter.
	GetMaxChangeSeqForKind(
		ctx context.Context,
		principalID PrincipalID,
		kind EntityKind,
	) (ChangeSeq, error)

	// GetMaxChangeSeqForPrincipal returns the highest Seq in the
	// change feed for the principal across all entity kinds, or 0
	// when the principal has no rows. Used by the JMAP eventsource
	// handler to prime its cursor at connect time so it does not
	// re-walk the entire historical feed (which on a freshly-imported
	// mailbox is 100k+ rows and emits a StateChange per coalesce
	// window for every batch — visible to clients as a sustained
	// burst of /changes calls for several minutes after each restart).
	GetMaxChangeSeqForPrincipal(
		ctx context.Context,
		principalID PrincipalID,
	) (ChangeSeq, error)

	// QueryEmailFast runs the SQL-pushable subset of the JMAP
	// Email/query plan: filter by mailbox membership, received-at and
	// size ranges; sort by a numeric column; paginate; optionally
	// count. Returns the matched message IDs (in sort order) along
	// with their thread IDs in a parallel slice; the JMAP layer uses
	// the thread IDs to apply collapseThreads without round-tripping
	// to the store. Total is meaningful only when CalculateTotal is
	// set, in which case it is the COUNT(*) for the same WHERE clause.
	//
	// This call is the principal-scoped fast path that replaces the
	// "list every accessible message, filter in Go, sort in Go,
	// paginate in Go" loop in the JMAP query handler. Predicates the
	// store cannot evaluate (hasAttachment, body, non-cached headers,
	// thread-keyword aggregation) are not representable in
	// EmailQueryFastOpts; the JMAP layer must keep the slow path for
	// those.
	//
	// Pagination semantics mirror RFC 8620 §5.5 with two narrowings:
	// negative Position is rejected (caller falls back), and Anchor
	// is not supported (caller falls back). When Limit is 0, no row
	// cap is applied beyond the store's internal hard limit.
	QueryEmailFast(
		ctx context.Context,
		principalID PrincipalID,
		opts EmailQueryFastOpts,
	) (EmailQueryFastResult, error)

	// ListThreadsByKeys returns the per-message thread-membership rows
	// for the supplied thread keys, scoped to principalID. The result
	// is grouped by key; each value is the list of (MessageID,
	// ReceivedAt) pairs for messages in that thread, in unspecified
	// order (JMAP Thread/get sorts before rendering).
	//
	// Key semantics mirror the JMAP wire form: threaded messages match
	// when their stored thread_id is in keys; un-threaded messages
	// (thread_id == 0) match when their MessageID is in keys, with the
	// returned Key set equal to the MessageID. Both branches collapse
	// into the same map slot when an un-threaded message id equals a
	// real thread_id, which is consistent with how the email renderer
	// emits thread ids (render.go: zero thread_id → "t<message_id>").
	//
	// Used by JMAP Thread/get (fetch the requested threads) and
	// Thread/changes (classify thread set-membership after a change
	// feed pass) to avoid the per-call full-account envelope scan
	// the legacy listAllMessages path performed.
	//
	// Empty keys returns an empty map without executing SQL.
	ListThreadsByKeys(
		ctx context.Context,
		principalID PrincipalID,
		keys []uint64,
	) (map[uint64][]ThreadMessageRow, error)

	// InsertAlias creates an alias mapping. Returns ErrConflict on
	// duplicate (local_part, domain).
	InsertAlias(ctx context.Context, a Alias) (Alias, error)

	// ResolveAlias looks up the principal an address routes to. Returns
	// ErrNotFound if no matching alias or canonical address exists.
	ResolveAlias(ctx context.Context, localPart, domain string) (PrincipalID, error)

	// ListAliases returns every alias row whose Domain matches, in
	// ascending LocalPart order. An empty domain argument returns every
	// alias. Expired rows are included so the admin surface can display
	// them; callers filter by ExpiresAt where required.
	ListAliases(ctx context.Context, domain string) ([]Alias, error)

	// DeleteAlias removes the alias with the given ID. Returns
	// ErrNotFound if no such row exists.
	DeleteAlias(ctx context.Context, id AliasID) error

	// InsertDomain records a local domain. Returns ErrConflict on
	// duplicate Name.
	InsertDomain(ctx context.Context, d Domain) error

	// GetDomain returns the domain row for name, or ErrNotFound.
	GetDomain(ctx context.Context, name string) (Domain, error)

	// ListLocalDomains returns every domain with IsLocal == true.
	ListLocalDomains(ctx context.Context) ([]Domain, error)

	// DeleteDomain removes a domain row. Returns ErrNotFound if no such
	// row exists. Callers are responsible for refusing the delete when
	// aliases still reference the domain; the store does not enforce
	// that constraint so bootstrap tooling can tear down a half-built
	// deployment.
	DeleteDomain(ctx context.Context, name string) error

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

	// ListAPIKeysByPrincipal returns every API key belonging to pid, in
	// ascending ID order. The Hash column is returned verbatim; the
	// plaintext key is never recoverable from this call.
	ListAPIKeysByPrincipal(ctx context.Context, pid PrincipalID) ([]APIKey, error)

	// DeleteAPIKey removes a single API key row. Returns ErrNotFound if
	// the key was already revoked.
	DeleteAPIKey(ctx context.Context, id APIKeyID) error

	// DeleteOneShotAPIKeysByPrincipal deletes every API key owned by pid
	// whose one_shot flag is set (migration 0060).  Called by the TOTP
	// confirm handler after a successful enrollment so the bootstrap /
	// recovery key is automatically consumed (REQ-AUTH-44, re #21).
	// Returns the number of rows deleted; zero is not an error.
	DeleteOneShotAPIKeysByPrincipal(ctx context.Context, pid PrincipalID) (int64, error)

	// ListOIDCLinksByPrincipal returns every OIDC link owned by pid, in
	// ascending ProviderName order. The returned slice is empty (nil)
	// when the principal has no linked identities.
	ListOIDCLinksByPrincipal(ctx context.Context, pid PrincipalID) ([]OIDCLink, error)

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

	// SearchPrincipalsByText returns principals whose DisplayName contains
	// prefix (case-insensitive, anywhere) or whose CanonicalEmail local-part
	// starts with prefix (case-insensitive). Results are sorted: display-name
	// matches first, then email-local-part matches; within each group,
	// alphabetical by DisplayName then CanonicalEmail. limit caps the result;
	// values <= 0 are treated as 1 and values > 1000 are clamped to 1000.
	// Used exclusively by Principal/query with a textPrefix filter
	// (docs/design/web/architecture/07-chat-protocol.md).
	SearchPrincipalsByText(ctx context.Context, prefix string, limit int) ([]Principal, error)

	// SearchPrincipalsByTextInDomain is identical to SearchPrincipalsByText
	// but restricts results to principals whose canonical email belongs to
	// domain (case-insensitive exact match against the domain part). When
	// domain is empty (or whitespace-only), the method behaves identically to
	// SearchPrincipalsByText with no domain restriction. Used by
	// Directory/search to provide compose-window autocomplete scoped to the
	// caller's email domain.
	SearchPrincipalsByTextInDomain(ctx context.Context, prefix, domain string, limit int) ([]Principal, error)

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

	// CountMessages returns the total and unread (\Seen-not-set) message
	// counts for mailboxID, computed by a single SQL aggregate against
	// message_mailboxes. Hits the (mailbox_id, uid) index. Used by
	// JMAP Mailbox/get to populate totalEmails / unreadEmails without
	// decoding every Message row — at the scale of a freshly-imported
	// Gmail archive (100k+ rows in the largest folder) the row-decode
	// path turns Mailbox/get into a multi-second CPU hog.
	CountMessages(ctx context.Context, mailboxID MailboxID) (total, unread int64, err error)

	// SetMailboxSubscribed toggles the MailboxAttrSubscribed bit on the
	// mailbox and bumps UpdatedAt. Returns ErrNotFound if the mailbox
	// has been deleted.
	SetMailboxSubscribed(ctx context.Context, mailboxID MailboxID, subscribed bool) error

	// RenameMailbox changes the Name of a mailbox. Returns
	// ErrNotFound if the mailbox does not exist and ErrConflict when
	// the new name collides with an existing mailbox for the same
	// principal. Appends an (EntityKindMailbox, ChangeOpUpdated) entry
	// to the principal's change feed.
	RenameMailbox(ctx context.Context, mailboxID MailboxID, newName string) error

	// MoveMailbox sets the parent of mailboxID to newParentID (0 means
	// top-level). Returns ErrNotFound when the mailbox is missing.
	// Appends an (EntityKindMailbox, ChangeOpUpdated) change-feed entry.
	MoveMailbox(ctx context.Context, mailboxID MailboxID, newParentID MailboxID) error

	// SetMailboxSortOrder updates the sortOrder column (RFC 8621 §2.1).
	// Returns ErrNotFound when the mailbox is missing.
	// Appends an (EntityKindMailbox, ChangeOpUpdated) change-feed entry.
	SetMailboxSortOrder(ctx context.Context, mailboxID MailboxID, sortOrder uint32) error

	// SetMailboxColor updates the optional Mailbox.Color extension
	// (REQ-PROTO-56 / REQ-STORE-34). A nil color clears the value;
	// a non-nil color must match the form "#RRGGBB" (six hex digits,
	// leading '#') or the call returns ErrInvalidArgument. Returns
	// ErrNotFound when the mailbox has been deleted.
	SetMailboxColor(ctx context.Context, mailboxID MailboxID, color *string) error

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

	// GetUserSieveScript returns the user-written Sieve script half for
	// pid. The user-written half is the script the user edits via
	// ManageSieve or the JMAP Sieve surface; the managed-rules compiler
	// prepends its compiled preamble before persisting the effective
	// (combined) script. Returns ("", nil) when no user script is on record.
	GetUserSieveScript(ctx context.Context, pid PrincipalID) (string, error)

	// SetUserSieveScript upserts the user-written Sieve script half for
	// pid. After updating, callers must recompile the managed-rules
	// preamble and call SetSieveScript with the combined effective script.
	// An empty text clears the user-written half (the effective script
	// then contains only the managed-rules preamble, or nothing).
	SetUserSieveScript(ctx context.Context, pid PrincipalID, text string) error

	// -- ManageSieve named scripts (RFC 5804) -------------------------
	//
	// The methods below operate on the multi-script set ManageSieve
	// exposes. They are independent of GetSieveScript / SetSieveScript:
	// the runtime delivery path continues to read the legacy single-
	// script slot, and SetActiveSieveScript copies the chosen named
	// script's text into that slot so the runtime never sees a
	// partially-migrated state.

	// ListSieveScripts returns every named script for pid in stable
	// (name-ascending) order. Empty slice + nil error means the
	// principal has no scripts.
	ListSieveScripts(ctx context.Context, pid PrincipalID) ([]NamedSieveScript, error)

	// GetSieveScriptByName returns the named script's text. Returns
	// ErrNotFound when no script with that name exists for pid.
	GetSieveScriptByName(ctx context.Context, pid PrincipalID, name string) (string, error)

	// PutSieveScriptByName upserts (pid, name) → text. The active
	// flag is preserved on update; on insert the new row is inactive.
	// Use SetActiveSieveScript to flip the active script.
	PutSieveScriptByName(ctx context.Context, pid PrincipalID, name, text string) error

	// DeleteSieveScriptByName removes the named script. ErrConflict is
	// returned when the named script is currently active (the caller
	// must SETACTIVE "" or SETACTIVE another name first); ErrNotFound
	// is returned when no such script exists.
	DeleteSieveScriptByName(ctx context.Context, pid PrincipalID, name string) error

	// RenameSieveScript renames a script for pid. ErrNotFound is
	// returned when oldName does not exist; ErrConflict is returned
	// when newName already exists. The active flag (if oldName was
	// active) is preserved.
	RenameSieveScript(ctx context.Context, pid PrincipalID, oldName, newName string) error

	// SetActiveSieveScript flips the active script for pid to name.
	// An empty name clears the active script (no script runs at
	// delivery). Both legs synchronously update the legacy
	// sieve_scripts slot so the runtime path's GetSieveScript reflects
	// the change immediately. ErrNotFound is returned when the
	// requested name does not exist.
	SetActiveSieveScript(ctx context.Context, pid PrincipalID, name string) error

	// GetActiveSieveScriptName returns the name of the currently-
	// active script, or ("", false, nil) when none is active.
	GetActiveSieveScriptName(ctx context.Context, pid PrincipalID) (string, bool, error)

	// -- Phase 2 outbound queue ---------------------------------------

	// EnqueueMessage inserts one queue row and increments the body
	// blob's refcount in the same transaction. When item.IdempotencyKey
	// is non-empty and a row with that key already exists, the existing
	// row's ID is returned together with ErrConflict; the caller treats
	// that as "already enqueued, here's the prior id".
	EnqueueMessage(ctx context.Context, item QueueItem) (QueueItemID, error)

	// ClaimDueQueueItems atomically transitions up to max queued or
	// deferred rows whose NextAttemptAt <= now to QueueStateInflight,
	// stamps LastAttemptAt = now, and returns them. The returned slice
	// length is at most max and may be empty when nothing is due. The
	// scheduler MUST be exactly one goroutine; the store does not
	// fence concurrent claimers.
	ClaimDueQueueItems(ctx context.Context, now time.Time, max int) ([]QueueItem, error)

	// CompleteQueueItem transitions an inflight row to its terminal
	// state — done on success, failed on permanent failure — and
	// decrements the body blob refcount. errMsg is persisted into the
	// last_error column when success is false. Returns ErrNotFound if
	// the row is missing.
	CompleteQueueItem(ctx context.Context, id QueueItemID, success bool, errMsg string) error

	// RescheduleQueueItem transitions an inflight row to deferred,
	// bumps Attempts, sets NextAttemptAt = nextAttempt, and stores
	// errMsg as last_error. Returns ErrNotFound if the row is gone.
	RescheduleQueueItem(ctx context.Context, id QueueItemID, nextAttempt time.Time, errMsg string) error

	// HoldQueueItem moves a row to QueueStateHeld. Returns ErrNotFound
	// when missing. Idempotent: holding an already-held row is a no-op.
	HoldQueueItem(ctx context.Context, id QueueItemID) error

	// ReleaseQueueItem moves a held row back to QueueStateQueued and
	// resets NextAttemptAt to the clock's now.
	ReleaseQueueItem(ctx context.Context, id QueueItemID) error

	// DeleteQueueItem removes a row, decrementing the body blob's
	// refcount. Operator force-delete; not used by normal lifecycle.
	DeleteQueueItem(ctx context.Context, id QueueItemID) error

	// GetQueueItem returns one row, or ErrNotFound.
	GetQueueItem(ctx context.Context, id QueueItemID) (QueueItem, error)

	// ListQueueItems applies the filter and returns matching rows in
	// ascending ID order. Caps at 1000 rows; callers paginate via
	// filter.AfterID.
	ListQueueItems(ctx context.Context, filter QueueFilter) ([]QueueItem, error)

	// CountQueueByState returns a per-state population map suitable
	// for an admin dashboard. States not present in the table appear
	// in the map with value 0.
	CountQueueByState(ctx context.Context) (map[QueueState]int, error)

	// EarliestNextAttempt returns the minimum NextAttemptAt among rows
	// in QueueStateQueued or QueueStateDeferred, and ok=true. When no
	// such rows exist it returns the zero time and ok=false. The
	// scheduler uses this to arm its poll timer for exactly when the
	// next retry is due rather than sleeping a fixed pollInterval.
	EarliestNextAttempt(ctx context.Context) (t time.Time, ok bool, err error)

	// -- Phase 2 DKIM keys --------------------------------------------

	// UpsertDKIMKey inserts or updates a (Domain, Selector) row. On
	// duplicate the Status, RotatedAt, PrivateKeyPEM, PublicKeyB64,
	// and Algorithm columns are overwritten; CreatedAt is preserved.
	UpsertDKIMKey(ctx context.Context, key DKIMKey) error

	// GetActiveDKIMKey returns the active key for domain. The
	// selector is opaque to the caller (the signer reads it from the
	// returned row). Returns ErrNotFound when no active key exists.
	GetActiveDKIMKey(ctx context.Context, domain string) (DKIMKey, error)

	// ListDKIMKeys returns every key for domain (any status), in
	// ascending Selector order.
	ListDKIMKeys(ctx context.Context, domain string) ([]DKIMKey, error)

	// RotateDKIMKey atomically retires (Domain, oldSelector) and
	// upserts newKey as Active. Both rows land in one tx so the signer
	// never observes a window with no active key.
	RotateDKIMKey(ctx context.Context, domain, oldSelector string, newKey DKIMKey) error

	// -- Phase 2 ACME -------------------------------------------------

	// UpsertACMEAccount inserts or updates an ACME account row keyed
	// by (DirectoryURL, ContactEmail).
	UpsertACMEAccount(ctx context.Context, acc ACMEAccount) (ACMEAccount, error)

	// GetACMEAccount returns the account row for (directoryURL,
	// contactEmail) or ErrNotFound.
	GetACMEAccount(ctx context.Context, directoryURL, contactEmail string) (ACMEAccount, error)

	// ListACMEAccounts returns every account row in ascending ID
	// order.
	ListACMEAccounts(ctx context.Context) ([]ACMEAccount, error)

	// InsertACMEOrder inserts a new in-flight order row and returns
	// it with the assigned ID.
	InsertACMEOrder(ctx context.Context, order ACMEOrder) (ACMEOrder, error)

	// UpdateACMEOrder writes the mutable fields of order back. The ID
	// must identify an existing row. Returns ErrNotFound when missing.
	UpdateACMEOrder(ctx context.Context, order ACMEOrder) error

	// GetACMEOrder returns one order, or ErrNotFound.
	GetACMEOrder(ctx context.Context, id ACMEOrderID) (ACMEOrder, error)

	// ListACMEOrdersByStatus returns orders with the given status in
	// ascending UpdatedAt order; the renewer worker reads the
	// "pending" / "ready" / "processing" sets.
	ListACMEOrdersByStatus(ctx context.Context, status ACMEOrderStatus) ([]ACMEOrder, error)

	// UpsertACMECert inserts or replaces the cert row keyed by
	// Hostname.
	UpsertACMECert(ctx context.Context, cert ACMECert) error

	// GetACMECert returns the cert for hostname, or ErrNotFound.
	GetACMECert(ctx context.Context, hostname string) (ACMECert, error)

	// ListACMECertsExpiringBefore returns every cert whose NotAfter
	// is strictly before t. The renewer schedules rolls from this
	// list.
	ListACMECertsExpiringBefore(ctx context.Context, t time.Time) ([]ACMECert, error)

	// -- Phase 2 webhooks ---------------------------------------------

	// InsertWebhook stores a new subscription and returns it with
	// assigned ID and timestamps.
	InsertWebhook(ctx context.Context, w Webhook) (Webhook, error)

	// UpdateWebhook persists the mutable fields (target URL, mode,
	// secret, retry policy, active flag). Returns ErrNotFound on
	// missing ID.
	UpdateWebhook(ctx context.Context, w Webhook) error

	// DeleteWebhook removes a subscription. Returns ErrNotFound when
	// already gone.
	DeleteWebhook(ctx context.Context, id WebhookID) error

	// GetWebhook returns one subscription by ID, or ErrNotFound.
	GetWebhook(ctx context.Context, id WebhookID) (Webhook, error)

	// ListWebhooks returns every subscription with the given owner.
	// Pass an empty kind/id to list all subscriptions.
	ListWebhooks(ctx context.Context, kind WebhookOwnerKind, ownerID string) ([]Webhook, error)

	// ListActiveWebhooksForDomain returns every active webhook whose
	// owner is the given domain (WebhookOwnerDomain) plus every
	// active webhook whose owner is a principal with a canonical
	// address in that domain (WebhookOwnerPrincipal). Used by the
	// mail-arrival dispatcher.
	ListActiveWebhooksForDomain(ctx context.Context, domain string) ([]Webhook, error)

	// -- Phase 2 DMARC -------------------------------------------------

	// InsertDMARCReport writes the report header row and rows in one
	// transaction; report-level dedup uses the unique
	// (ReporterOrg, ReportID) pair and returns ErrConflict on a
	// repeat. The caller supplies the parsed DMARCReport (with
	// XMLBlobHash already pointing at the stored XML); rows is
	// written as-is.
	InsertDMARCReport(ctx context.Context, report DMARCReport, rows []DMARCRow) (DMARCReportID, error)

	// GetDMARCReport returns the report header and its rows.
	GetDMARCReport(ctx context.Context, id DMARCReportID) (DMARCReport, []DMARCRow, error)

	// ListDMARCReports returns reports matching filter, in ascending
	// ID order, capped at 1000.
	ListDMARCReports(ctx context.Context, filter DMARCReportFilter) ([]DMARCReport, error)

	// DMARCAggregate returns a pre-cannedaggregate of every row whose
	// owning report covers domain and falls within [since, until].
	// One row per (HeaderFrom, Disposition); admin REST renders
	// directly from this surface.
	DMARCAggregate(ctx context.Context, domain string, since, until time.Time) ([]DMARCAggregateRow, error)

	// -- Phase 2 mailbox ACL ------------------------------------------

	// SetMailboxACL upserts one ACL row for (mailboxID, principalID).
	// principalID == nil encodes the RFC 4314 "anyone" pseudo-row.
	// rights replaces any prior mask wholesale (RFC 4314 SETACL
	// semantics, not an additive merge).
	SetMailboxACL(ctx context.Context, mailboxID MailboxID, principalID *PrincipalID, rights ACLRights, grantedBy PrincipalID) error

	// GetMailboxACL returns every ACL row for mailboxID. Anyone rows
	// (PrincipalID nil) come first.
	GetMailboxACL(ctx context.Context, mailboxID MailboxID) ([]MailboxACL, error)

	// ListMailboxesAccessibleBy returns every mailbox whose ACL grants
	// pid the lookup right (or has an "anyone" row with lookup). The
	// owning principal's mailboxes are NOT auto-included; the caller
	// composes them with ListMailboxes when "all" semantics are
	// needed.
	ListMailboxesAccessibleBy(ctx context.Context, pid PrincipalID) ([]Mailbox, error)

	// RemoveMailboxACL deletes the ACL row for (mailboxID, principalID).
	// principalID == nil targets the "anyone" row. Returns ErrNotFound
	// when the row is missing.
	RemoveMailboxACL(ctx context.Context, mailboxID MailboxID, principalID *PrincipalID) error

	// -- Phase 2 JMAP states ------------------------------------------

	// GetJMAPStates returns the per-principal counter row, creating a
	// zero-valued row on first access.
	GetJMAPStates(ctx context.Context, pid PrincipalID) (JMAPStates, error)

	// IncrementJMAPState atomically bumps one of the per-principal
	// counters (mailbox/email/thread/...) and returns the new value.
	// The store creates the row on first access.
	IncrementJMAPState(ctx context.Context, pid PrincipalID, kind JMAPStateKind) (int64, error)

	// -- Phase 2 TLS-RPT ----------------------------------------------

	// AppendTLSRPTFailure stores one failure row for the reporter to
	// roll up later.
	AppendTLSRPTFailure(ctx context.Context, f TLSRPTFailure) error

	// ListTLSRPTFailures returns every failure for policyDomain in
	// [since, until], in ascending RecordedAt order.
	ListTLSRPTFailures(ctx context.Context, policyDomain string, since, until time.Time) ([]TLSRPTFailure, error)

	// -- Phase 2 JMAP EmailSubmission ---------------------------------

	// InsertEmailSubmission persists row. The caller MUST set ID
	// (typically the EnvelopeID stringified). Returns ErrConflict on a
	// duplicate ID.
	InsertEmailSubmission(ctx context.Context, row EmailSubmissionRow) error

	// GetEmailSubmission returns one row by id, or ErrNotFound.
	GetEmailSubmission(ctx context.Context, id string) (EmailSubmissionRow, error)

	// ListEmailSubmissions returns rows owned by principal, filtered
	// per filter. Default sort is SendAtUs ascending. Caps at 1000;
	// callers paginate via filter.AfterID.
	ListEmailSubmissions(ctx context.Context, principal PrincipalID, filter EmailSubmissionFilter) ([]EmailSubmissionRow, error)

	// UpdateEmailSubmissionUndoStatus replaces UndoStatus on the row
	// identified by id. Returns ErrNotFound when the row is missing.
	UpdateEmailSubmissionUndoStatus(ctx context.Context, id, undoStatus string) error

	// DeleteEmailSubmission removes the row. Returns ErrNotFound when
	// the row is already gone. The caller is responsible for ensuring
	// terminal-state semantics (RFC 8621 §5.5); the store does not
	// gate destroys on UndoStatus.
	DeleteEmailSubmission(ctx context.Context, id string) error

	// -- Phase 2 JMAP Identity overlay -------------------------------

	// InsertJMAPIdentity persists a new identity overlay row. The
	// caller assigns ID; ErrConflict on duplicate (id) or
	// (principal_id, email) collisions, depending on backend rules.
	InsertJMAPIdentity(ctx context.Context, row JMAPIdentity) error

	// GetJMAPIdentity returns one row by id, or ErrNotFound.
	GetJMAPIdentity(ctx context.Context, id string) (JMAPIdentity, error)

	// ListJMAPIdentities returns the principal's overlay rows in
	// ascending CreatedAtUs / ID order. Default identities are NOT
	// returned — they are synthesised by the JMAP layer.
	ListJMAPIdentities(ctx context.Context, principal PrincipalID) ([]JMAPIdentity, error)

	// UpdateJMAPIdentity replaces the mutable fields (Name, ReplyToJSON,
	// BccJSON, TextSignature, HTMLSignature) on the row identified by
	// row.ID. Returns ErrNotFound when the row is missing.
	UpdateJMAPIdentity(ctx context.Context, row JMAPIdentity) error

	// DeleteJMAPIdentity removes the row identified by id. Returns
	// ErrNotFound when the row is missing.
	DeleteJMAPIdentity(ctx context.Context, id string) error

	// SetDefaultJMAPIdentity makes the identity owned by principalID the
	// principal's default, enforcing the single-default invariant
	// (REQ-IDENT-70). In one transaction it clears is_default on every
	// persisted jmap_identities row owned by the principal and, when
	// identityID is a persisted overlay row, sets is_default on that
	// row. Passing the synthesised default's wire id ("default") clears
	// every persisted row's flag, which makes the synthesised default
	// the principal's default (it has no row). Returns ErrNotFound when
	// identityID names a persisted row that does not exist or is not
	// owned by principalID.
	SetDefaultJMAPIdentity(ctx context.Context, principalID PrincipalID, identityID string) error

	// -- Phase 2 Identity verification (REQ-IDENT-01..91) ------------

	// IssueIdentityVerificationToken writes the verification trio
	// (token hash, code hash, expiry) on the identity row in a single
	// atomic UPDATE (REQ-IDENT-30..32). Refused with ErrConflict when
	// a token is already live on the row; the caller uses
	// ResetIdentityVerificationToken for the resend path. Returns
	// ErrNotFound when the identity row is missing.
	IssueIdentityVerificationToken(ctx context.Context, identityID string, tokenHash, codeHash []byte, expiresAtUs int64) error

	// ResetIdentityVerificationToken overwrites the verification trio
	// on the identity row (REQ-IDENT-37, resend rotates the token).
	// Distinct from IssueIdentityVerificationToken in that the
	// previous token, if any, is silently replaced rather than rejected.
	// Caller enforces the rate-limit (REQ-IDENT-36). Returns ErrNotFound
	// when the identity row is missing.
	ResetIdentityVerificationToken(ctx context.Context, identityID string, tokenHash, codeHash []byte, expiresAtUs int64) error

	// MarkIdentityVerified sets verified_at_us = now and clears the
	// verification trio on the identity row (REQ-IDENT-40..43). The
	// transition is idempotent at the wire layer; this store method
	// always writes verified_at_us and clears the token columns. Returns
	// ErrNotFound when the identity row is missing.
	MarkIdentityVerified(ctx context.Context, identityID string) error

	// UnmarkIdentityVerified clears verified_at_us (sets it to NULL) on
	// the identity row (REQ-IDENT-51). Used by the admin CLI to revert a
	// verified Identity to unverified — for example, to revoke a
	// compromised identity. Does NOT touch the verification token trio:
	// callers that want to also clear a pending token call
	// ClearIdentityVerificationToken separately. Returns ErrNotFound when
	// the identity row is missing.
	UnmarkIdentityVerified(ctx context.Context, identityID string) error

	// ClearIdentityVerificationToken clears the verification trio on
	// the identity row without touching verified_at_us. Used after a
	// successful verification (called inside MarkIdentityVerified for
	// callers that want to keep the operations distinct) and by the GC
	// pass for expired tokens whose Identity row is still inside the
	// 7-day unverified-purge window (REQ-IDENT-35). Returns ErrNotFound
	// when the identity row is missing.
	ClearIdentityVerificationToken(ctx context.Context, identityID string) error

	// GetIdentityByVerificationTokenHash returns the identity row whose
	// verification_token_hash matches tokenHash exactly (REQ-IDENT-40).
	// The match is global because the link callback only carries the
	// raw token, not the identity id. Returns ErrNotFound when no row
	// matches; callers MUST treat that case identically to a token-
	// expired or already-verified state for user-facing error rendering.
	GetIdentityByVerificationTokenHash(ctx context.Context, tokenHash []byte) (JMAPIdentity, error)

	// GetIdentityByVerificationCodeHash returns the identity row
	// identified by identityID iff its verification_code_hash matches
	// codeHash (REQ-IDENT-41). The lookup is identity-scoped so that
	// 6-digit codes may repeat across identities without ambiguity;
	// the URL the suite uses for the code-entry endpoint carries the
	// identity id. Returns ErrNotFound when the identity is missing or
	// the code does not match.
	GetIdentityByVerificationCodeHash(ctx context.Context, identityID string, codeHash []byte) (JMAPIdentity, error)

	// ListUnverifiedIdentitiesOlderThan returns identity rows whose
	// verified_at_us IS NULL AND created_at_us < before (REQ-IDENT-35).
	// Used by the GC pass to destroy stale unverified identities after
	// the 7-day window. Results are not strictly bounded but in normal
	// operation the count is tiny (single-digit per principal). Order
	// is ascending created_at_us.
	ListUnverifiedIdentitiesOlderThan(ctx context.Context, before time.Time) ([]JMAPIdentity, error)

	// ListExpiredVerificationTokens returns identity rows whose
	// verification_token_expires_at_us IS NOT NULL AND
	// verification_token_expires_at_us < before (REQ-IDENT-35). The
	// caller wipes the verification trio via
	// ClearIdentityVerificationToken; the Identity row itself remains
	// (the longer 7-day GC of unverified rows is a separate pass).
	// Order is ascending verification_token_expires_at_us.
	ListExpiredVerificationTokens(ctx context.Context, before time.Time) ([]JMAPIdentity, error)

	// GetVerificationResendStats returns the per-Identity resend
	// bookkeeping used by the rate-limit gate (REQ-IDENT-36):
	//   - LastIssuedAtUs is the unix-micros instant the most recent
	//     verification token was issued (zero before first issuance).
	//   - WindowStartedAtUs is the unix-micros anchor of the active
	//     24h resend-count window (zero before first issuance).
	//   - WindowCount is the number of issuances inside the active
	//     window (zero before first issuance).
	// Returns ErrNotFound when the identity row is missing.
	GetVerificationResendStats(ctx context.Context, identityID string) (VerificationResendStats, error)

	// RotateIdentityVerificationToken replaces the verification trio
	// on the identity row AND updates the resend bookkeeping in a
	// single transaction (REQ-IDENT-36, REQ-IDENT-37). The store
	// computes the new window state using its injected clock:
	//   - if (now - window_started) >= 24h, the window resets
	//     (window_started=now, window_count=1).
	//   - otherwise, window_count is incremented.
	// The rate-limit gate is consulted atomically inside the
	// transaction:
	//   - if (now - last_issued) < cooldown, returns ErrRateLimited.
	//   - if (incoming window_count) > dailyCap, returns ErrRateLimited.
	// Pass cooldown=0 / dailyCap=0 to disable that arm of the gate;
	// negative values are treated as zero. Returns ErrNotFound when
	// the identity row is missing.
	RotateIdentityVerificationToken(
		ctx context.Context,
		identityID string,
		tokenHash, codeHash []byte,
		expiresAtUs int64,
		cooldown time.Duration,
		dailyCap int,
	) error

	// -- Tagged addresses (REQ-TAG-10..11, REQ-TAG-30..32) ------------

	// GetTaggedAddressFilter returns the filter row for the
	// (principalID, baseIdentityID, suffix) tuple. The suffix is
	// matched case-insensitively after lower-casing both sides
	// (REQ-TAG-02). Returns ErrNotFound when no match exists.
	GetTaggedAddressFilter(ctx context.Context, principalID PrincipalID, baseIdentityID, suffix string) (TaggedAddressFilter, error)

	// ListTaggedAddressFiltersForPrincipal returns every filter row
	// owned by principalID, ordered by (base_identity_id, suffix) for
	// stable SPA rendering. Returns an empty slice (nil) and nil error
	// when the principal has no filters.
	ListTaggedAddressFiltersForPrincipal(ctx context.Context, principalID PrincipalID) ([]TaggedAddressFilter, error)

	// InsertTaggedAddressFilter creates a new tagged_address_filters
	// row from f. The store generates a random opaque ID when f.ID is
	// empty, normalises f.Suffix to lower-case, and stamps
	// CreatedAt / UpdatedAt to the store clock. Returns ErrConflict on
	// duplicate (principal, base_identity, suffix) and ErrTooManyFilters
	// when the principal already holds
	// MaxTaggedAddressFiltersPerPrincipal rows (REQ-TAG-11). Returns
	// ErrInvalidArgument when f.Action is not one of the allowed
	// TaggedAddressAction* constants.
	InsertTaggedAddressFilter(ctx context.Context, f TaggedAddressFilter) error

	// UpdateTaggedAddressFilter changes the action + label_name on the
	// filter row identified by id and bumps updated_at. Only those two
	// columns are mutable; suffix / base_identity / principal are
	// immutable identity columns. Returns ErrNotFound when the row is
	// missing and ErrInvalidArgument when action is not one of the
	// allowed TaggedAddressAction* constants.
	UpdateTaggedAddressFilter(ctx context.Context, id, action, labelName string) error

	// DeleteTaggedAddressFilter removes the filter row identified by id
	// and atomically deletes any matching tagged_address_dismissals
	// row for the same (principal, base_identity, suffix) tuple per
	// REQ-TAG-61. Returns the deleted row's (suffix, baseIdentityID,
	// principalID) so the caller can emit the appropriate Identity/
	// changes push event without a second read. Returns ErrNotFound
	// when the filter row is missing; the dismissal-side delete is
	// best-effort and is not an error if no dismissal exists.
	DeleteTaggedAddressFilter(ctx context.Context, id string) (suffix, baseIdentityID string, principalID PrincipalID, err error)

	// InsertTaggedAddressDismissal creates a new
	// tagged_address_dismissals row from d. The store normalises
	// d.Suffix to lower-case and stamps DismissedAt to the store clock.
	// Idempotent on duplicate (principal, base_identity, suffix):
	// returns nil. Returns ErrTooManyDismissals when the principal
	// already holds MaxTaggedAddressDismissalsPerPrincipal rows
	// (REQ-TAG-11).
	InsertTaggedAddressDismissal(ctx context.Context, d TaggedAddressDismissal) error

	// DeleteTaggedAddressDismissal removes the dismissal row identified
	// by (principalID, baseIdentityID, suffix). Idempotent: no error if
	// no row matches. The suffix is lower-cased before comparison
	// (REQ-TAG-02).
	DeleteTaggedAddressDismissal(ctx context.Context, principalID PrincipalID, baseIdentityID, suffix string) error

	// ListTaggedAddressDismissalsForPrincipal returns every dismissal
	// row owned by principalID, ordered by (base_identity_id, suffix)
	// for stable SPA rendering. Returns an empty slice (nil) and nil
	// error when the principal has no dismissals.
	ListTaggedAddressDismissalsForPrincipal(ctx context.Context, principalID PrincipalID) ([]TaggedAddressDismissal, error)

	// HasTaggedAddressFilterOrDismissal reports whether the
	// (principalID, baseIdentityID, suffix) tuple matches an existing
	// filter row, a dismissal row, or both. Used by the SPA banner gate
	// to decide whether to surface the per-message prompt without
	// loading the full rows. The suffix is lower-cased before
	// comparison (REQ-TAG-02). Returns no error when both flags are
	// false (i.e., the user has neither registered nor dismissed the
	// suffix yet).
	HasTaggedAddressFilterOrDismissal(ctx context.Context, principalID PrincipalID, baseIdentityID, suffix string) (hasFilter, hasDismissal bool, err error)

	// LookupTaggedAddressFilterForRecipient returns the tagged-address
	// filter that matches an inbound recipient (REQ-TAG-20 step 2). The
	// caller has already split the envelope RCPT TO into (baseEmail,
	// suffix) via the taggedaddr package; this method finds the Identity
	// row whose email matches baseEmail (case-insensitively) AND owns a
	// tagged_address_filters row with the given suffix for principalID.
	//
	// The lookup joins jmap_identities and tagged_address_filters in a
	// single round trip — the inbound pipeline runs this once per local
	// recipient with a `+`-suffix on every delivered message, so the
	// query must be index-friendly. Both indexes already exist:
	// idx_jmap_identities_principal (scoping the email scan) and
	// idx_tagged_address_filters_principal_identity (scoping the suffix
	// match).
	//
	// Returns ErrNotFound when no Identity carries baseEmail or no
	// filter matches the (Identity, suffix) tuple. The synthesised
	// default identity is NEVER returned because it has no
	// jmap_identities row (REQ-TAG-10 stores base_identity_id as a real
	// FK; tag filters against the default identity are unrepresentable
	// without first materialising the row, which the REST/JMAP create
	// path does on demand).
	LookupTaggedAddressFilterForRecipient(ctx context.Context, principalID PrincipalID, baseEmail, suffix string) (TaggedAddressFilter, error)

	// MaterializeDefaultIdentity ensures that a real jmap_identities row
	// exists for the principal's synthesised default identity
	// (REQ-AUTH-EXT-SUBMIT-01). The default identity is normally a
	// synthesised value not stored in jmap_identities; this method
	// creates a persisted row (idempotently) so that a FK-backed
	// identity_submission row can reference it. The returned int64 is
	// the numeric ID that was assigned to the identity row (consistent
	// with the wire form: decimal string of the row's ROWID / sequence).
	// Calling this method twice with the same principalID returns the
	// same id both times without creating a second row.
	//
	// The materialised id is stable across the principal's lifetime; once
	// written, callers MAY hold references to it. Any future export /
	// import tool MUST preserve jmap_identities.id values verbatim across
	// the round-trip AND carry the corresponding identity_submission rows
	// alongside, so cross-table references survive. Failing to do so will
	// orphan submission rows from their identities at import time.
	MaterializeDefaultIdentity(ctx context.Context, principalID PrincipalID) (string, error)

	// UpsertIdentitySubmission creates or replaces the submission config
	// for sub.IdentityID. The IdentityID must already exist in
	// jmap_identities (either as an overlay row or after
	// MaterializeDefaultIdentity). Returns ErrNotFound when the identity
	// row is absent.
	UpsertIdentitySubmission(ctx context.Context, sub IdentitySubmission) error

	// GetIdentitySubmission returns the submission config for identityID.
	// Returns ErrNotFound when no row is present (i.e., the identity
	// uses herold's default outbound queue).
	GetIdentitySubmission(ctx context.Context, identityID string) (IdentitySubmission, error)

	// DeleteIdentitySubmission removes the submission config for
	// identityID. Returns ErrNotFound when absent.
	DeleteIdentitySubmission(ctx context.Context, identityID string) error

	// ListIdentitySubmissionsDue returns rows whose refresh_due_us is
	// non-null and <= before (i.e., the OAuth token sweeper should
	// refresh these). Results are ordered by refresh_due_us ascending.
	ListIdentitySubmissionsDue(ctx context.Context, before time.Time) ([]IdentitySubmission, error)

	// CountOAuthIdentitySubmissions returns the total number of
	// identity_submission rows whose submit_auth_method is 'oauth2',
	// regardless of refresh_due_us. Used by the sweeper to keep the
	// herold_external_submission_active_identities gauge accurate between
	// refresh windows (REQ-AUTH-EXT-SUBMIT-09).
	CountOAuthIdentitySubmissions(ctx context.Context) (int, error)

	// -- Phase 2 JMAP snooze (REQ-PROTO-49) ---------------------------

	// ListDueSnoozedMessages returns messages whose SnoozedUntil <= now
	// (UTC) AND whose Keywords contains "$snoozed", in ascending
	// SnoozedUntil order. Used by the wake-up worker. limit caps the
	// batch; callers loop until the slice is empty. A non-positive
	// limit applies the default cap of 1000 server-side.
	ListDueSnoozedMessages(ctx context.Context, now time.Time, limit int) ([]Message, error)

	// SetSnooze sets or clears the snoozed-until / "$snoozed" keyword
	// pair on the (msgID, mailboxID) per-mailbox row, atomically inside
	// one transaction. when==nil clears both; when non-nil sets both.
	// The mailbox's HighestModSeq is bumped and a (EntityKindEmail,
	// ChangeOpUpdated) row is appended to the state-change feed in the
	// same tx. Returns the message's new ModSeq. Returns ErrNotFound when
	// the (msgID, mailboxID) membership is gone.
	SetSnooze(ctx context.Context, msgID MessageID, mailboxID MailboxID, when *time.Time) (ModSeq, error)

	// -- Phase 2 LLM categorisation (REQ-FILT-200..221) ---------------

	// GetCategorisationConfig returns the per-account categoriser
	// configuration row for pid, seeding a row with the documented
	// defaults (REQ-FILT-201, 210, 211) when the principal has no row
	// yet. The seed write is best-effort: a backend that cannot
	// persist the seed row still returns the in-memory defaults so
	// the caller can run the categoriser. Returns ErrNotFound only on
	// a backend lookup failure; "row absent" is not an error.
	GetCategorisationConfig(ctx context.Context, pid PrincipalID) (CategorisationConfig, error)

	// UpdateCategorisationConfig upserts the per-account categoriser
	// row. UpdatedAtUs on the supplied struct is ignored — the store
	// stamps it with the current Clock instant.
	// When the Prompt field differs from the stored value, DerivedCategories
	// is cleared to nil as part of the same write (REQ-FILT-217 invalidation
	// rule). Callers that only want to refill DerivedCategories should use
	// SetDerivedCategories instead.
	UpdateCategorisationConfig(ctx context.Context, cfg CategorisationConfig) error

	// SetDerivedCategories persists the categories slice derived from the most
	// recent successful classifier response (REQ-FILT-217). It is a targeted
	// write: only the derived_categories_json column is updated; all other
	// config fields are left unchanged. Categories are lowercase ASCII
	// dash-separated names; bounded to MaxDerivedCategoryEntries entries and
	// MaxDerivedCategoryNameBytes bytes per name. Writes are de-duplicated by
	// callers: this method is called only when the new slice differs from the
	// currently persisted one.
	//
	// expectedEpoch must equal the DerivedCategoriesEpoch the caller read from
	// GetCategorisationConfig before starting the classifier call. If the stored
	// epoch has been incremented in the interim (because UpdateCategorisationConfig
	// ran a prompt change), the UPDATE is a no-op and the method returns
	// (false, nil). This prevents stale classifier results from overwriting the
	// NULL that a prompt change wrote. Multiple concurrent in-flight classifier
	// calls with the same expected epoch are also safe: exactly one wins and
	// the others are silently dropped — correct behaviour, since only the latest
	// persisted categories are meaningful once the prompt has not changed.
	SetDerivedCategories(ctx context.Context, pid PrincipalID, categories []string, expectedEpoch int64) (bool, error)

	// SetLLMClassification upserts the per-message LLM classification
	// record (REQ-FILT-66 / REQ-FILT-216). The record is written once at
	// delivery time; subsequent calls are silently treated as upserts so
	// re-classification (REQ-FILT-220) can overwrite. Nil pointer fields
	// on rec leave the corresponding columns unchanged when updating
	// (except that the entire spam or category sub-record is replaced
	// atomically).
	SetLLMClassification(ctx context.Context, rec LLMClassificationRecord) error

	// GetLLMClassification returns the classification record for msgID,
	// or ErrNotFound when no record exists (e.g. the classifier was not
	// run, or the message pre-dates this feature).
	GetLLMClassification(ctx context.Context, msgID MessageID) (LLMClassificationRecord, error)

	// BatchGetLLMClassifications returns the classification records for
	// every id in msgIDs. Absent entries (no classification run) are
	// omitted from the returned map; callers treat a missing key as
	// "no classification available". The returned map is keyed by
	// MessageID.
	BatchGetLLMClassifications(ctx context.Context, msgIDs []MessageID) (map[MessageID]LLMClassificationRecord, error)

	// -- Phase 2 Wave 2.6 JMAP for Contacts (REQ-PROTO-55) -----------

	// InsertAddressBook persists a new address book and returns the
	// assigned ID. When ab.IsDefault is true, any prior default address
	// book for the same principal is flipped to false in the same tx.
	// Appends a (EntityKindAddressBook, ChangeOpCreated) state-change
	// row. Returns ErrConflict on duplicate (principal_id, name).
	InsertAddressBook(ctx context.Context, ab AddressBook) (AddressBookID, error)

	// GetAddressBook returns one row by id. Returns ErrNotFound when
	// the row is missing.
	GetAddressBook(ctx context.Context, id AddressBookID) (AddressBook, error)

	// ListAddressBooks applies filter and returns rows in ascending ID
	// order. Caps at 1000 server-side.
	ListAddressBooks(ctx context.Context, filter AddressBookFilter) ([]AddressBook, error)

	// UpdateAddressBook persists the mutable fields (Name, Description,
	// Color, SortOrder, IsSubscribed, IsDefault, RightsMask) on the row
	// identified by ab.ID, bumps ModSeq and UpdatedAt, and appends a
	// (EntityKindAddressBook, ChangeOpUpdated) state-change row in the
	// same tx. Flipping IsDefault to true unsets any prior default.
	// Returns ErrNotFound when the row is missing.
	UpdateAddressBook(ctx context.Context, ab AddressBook) error

	// DeleteAddressBook removes the row identified by id (cascading to
	// every contact owned by the book) and appends a
	// (EntityKindAddressBook, ChangeOpDestroyed) row plus one
	// (EntityKindContact, ChangeOpDestroyed) row per contact removed —
	// all in one tx. Returns ErrNotFound when the row is missing.
	DeleteAddressBook(ctx context.Context, id AddressBookID) error

	// DefaultAddressBook returns the default address book for
	// principalID, or ErrNotFound when the principal has no default
	// (no row with is_default = true).
	DefaultAddressBook(ctx context.Context, principalID PrincipalID) (AddressBook, error)

	// InsertContact persists a new contact row. The caller is
	// responsible for populating the denormalised columns (DisplayName,
	// GivenName, Surname, OrgName, PrimaryEmail, SearchBlob) from the
	// JSContact JSON. Appends a (EntityKindContact, ChangeOpCreated)
	// state-change row. Returns ErrConflict on duplicate (address_book_id,
	// uid).
	InsertContact(ctx context.Context, c Contact) (ContactID, error)

	// GetContact returns one row by id, or ErrNotFound.
	GetContact(ctx context.Context, id ContactID) (Contact, error)

	// ListContacts applies filter and returns rows in ascending ID
	// order. Caps at 1000 server-side.
	ListContacts(ctx context.Context, filter ContactFilter) ([]Contact, error)

	// UpdateContact persists the mutable fields (JSContactJSON +
	// denormalised columns) on the row identified by c.ID, bumps
	// ModSeq and UpdatedAt, and appends a (EntityKindContact,
	// ChangeOpUpdated) state-change row in the same tx. Returns
	// ErrNotFound when the row is missing.
	UpdateContact(ctx context.Context, c Contact) error

	// DeleteContact removes the row identified by id and appends a
	// (EntityKindContact, ChangeOpDestroyed) state-change row in the
	// same tx. Returns ErrNotFound when the row is missing.
	DeleteContact(ctx context.Context, id ContactID) error

	// -- Phase 2 Wave 2.7 JMAP for Calendars (REQ-PROTO-54) ----------

	// InsertCalendar persists a new calendar and returns the assigned
	// ID. When c.IsDefault is true, any prior default calendar for the
	// same principal is flipped to false in the same tx. Appends a
	// (EntityKindCalendar, ChangeOpCreated) state-change row.
	InsertCalendar(ctx context.Context, c Calendar) (CalendarID, error)

	// GetCalendar returns one row by id. Returns ErrNotFound when the
	// row is missing.
	GetCalendar(ctx context.Context, id CalendarID) (Calendar, error)

	// ListCalendars applies filter and returns rows in ascending ID
	// order. Caps at 1000 server-side.
	ListCalendars(ctx context.Context, filter CalendarFilter) ([]Calendar, error)

	// UpdateCalendar persists the mutable fields (Name, Description,
	// Color, SortOrder, IsSubscribed, IsDefault, IsVisible, TimeZoneID,
	// RightsMask) on the row identified by c.ID, bumps ModSeq and
	// UpdatedAt, and appends a (EntityKindCalendar, ChangeOpUpdated)
	// state-change row in the same tx. Flipping IsDefault to true
	// unsets any prior default. Returns ErrNotFound when the row is
	// missing.
	UpdateCalendar(ctx context.Context, c Calendar) error

	// DeleteCalendar removes the row identified by id (cascading to
	// every event owned by the calendar) and appends a
	// (EntityKindCalendar, ChangeOpDestroyed) row plus one
	// (EntityKindCalendarEvent, ChangeOpDestroyed) row per event
	// removed — all in one tx. Returns ErrNotFound when the row is
	// missing.
	DeleteCalendar(ctx context.Context, id CalendarID) error

	// DefaultCalendar returns the default calendar for principalID, or
	// ErrNotFound when the principal has no default (no row with
	// is_default = true).
	DefaultCalendar(ctx context.Context, principalID PrincipalID) (Calendar, error)

	// InsertCalendarEvent persists a new calendar event row. The
	// caller is responsible for populating the denormalised columns
	// (Start, End, IsRecurring, RRuleJSON, Summary, OrganizerEmail,
	// Status) from the JSCalendar JSON. Appends a
	// (EntityKindCalendarEvent, ChangeOpCreated) state-change row.
	// Returns ErrConflict on duplicate (calendar_id, uid).
	InsertCalendarEvent(ctx context.Context, e CalendarEvent) (CalendarEventID, error)

	// GetCalendarEvent returns one row by id, or ErrNotFound.
	GetCalendarEvent(ctx context.Context, id CalendarEventID) (CalendarEvent, error)

	// GetCalendarEventByUID returns the event identified by the
	// (calendarID, uid) natural key — the iMIP RSVP-path lookup. Returns
	// ErrNotFound when no such row exists.
	GetCalendarEventByUID(ctx context.Context, calendarID CalendarID, uid string) (CalendarEvent, error)

	// ListCalendarEvents applies filter and returns rows in ascending
	// ID order. Caps at 1000 server-side.
	ListCalendarEvents(ctx context.Context, filter CalendarEventFilter) ([]CalendarEvent, error)

	// UpdateCalendarEvent persists the mutable fields (JSCalendarJSON +
	// denormalised columns) on the row identified by e.ID, bumps
	// ModSeq and UpdatedAt, and appends a (EntityKindCalendarEvent,
	// ChangeOpUpdated) state-change row in the same tx. Returns
	// ErrNotFound when the row is missing.
	UpdateCalendarEvent(ctx context.Context, e CalendarEvent) error

	// DeleteCalendarEvent removes the row identified by id and appends
	// a (EntityKindCalendarEvent, ChangeOpDestroyed) state-change row
	// in the same tx. Returns ErrNotFound when the row is missing.
	DeleteCalendarEvent(ctx context.Context, id CalendarEventID) error

	// -- Phase 2 Wave 2.8 chat subsystem (REQ-CHAT-*) -----------------

	// InsertChatConversation persists a new conversation row and
	// appends a (EntityKindConversation, ChangeOpCreated) state-change
	// row.
	InsertChatConversation(ctx context.Context, c ChatConversation) (ConversationID, error)

	// GetChatConversation returns one row by id, or ErrNotFound.
	GetChatConversation(ctx context.Context, id ConversationID) (ChatConversation, error)

	// ListChatConversations applies filter and returns rows in
	// ascending ID order. Caps at 1000 server-side.
	ListChatConversations(ctx context.Context, filter ChatConversationFilter) ([]ChatConversation, error)

	// UpdateChatConversation persists the mutable fields (Name, Topic,
	// LastMessageAt, MessageCount, IsArchived) on the row identified
	// by c.ID, bumps ModSeq and UpdatedAt, and appends a
	// (EntityKindConversation, ChangeOpUpdated) state-change row in
	// the same tx. Returns ErrNotFound when the row is missing.
	UpdateChatConversation(ctx context.Context, c ChatConversation) error

	// DeleteChatConversation removes the row identified by id
	// (cascading to every membership and message owned by the
	// conversation via FK ON DELETE CASCADE) and appends per-row
	// destroyed state-change entries plus the conversation destroyed
	// row — all in one tx. Returns ErrNotFound when the row is missing.
	DeleteChatConversation(ctx context.Context, id ConversationID) error

	// InsertChatMembership persists a new membership row. The unique
	// (conversation_id, principal_id) constraint surfaces as
	// ErrConflict on duplicate. Appends a (EntityKindMembership,
	// ChangeOpCreated) state-change row with ParentEntityID =
	// conversation_id.
	InsertChatMembership(ctx context.Context, m ChatMembership) (MembershipID, error)

	// GetChatMembership returns the membership row for the given
	// (conversationID, principalID) pair, or ErrNotFound.
	GetChatMembership(ctx context.Context, conversationID ConversationID, principalID PrincipalID) (ChatMembership, error)

	// ListChatMembershipsByConversation returns every membership for
	// conversationID in ascending ID order.
	ListChatMembershipsByConversation(ctx context.Context, conversationID ConversationID) ([]ChatMembership, error)

	// ListChatMembershipsByPrincipal returns every membership owned by
	// principalID in ascending ID order — the "what conversations am I
	// in" projection.
	ListChatMembershipsByPrincipal(ctx context.Context, principalID PrincipalID) ([]ChatMembership, error)

	// UpdateChatMembership persists the mutable fields (Role,
	// LastReadMessageID, IsMuted, MuteUntil, NotificationsSetting) on
	// the row identified by m.ID, bumps ModSeq, and appends a
	// (EntityKindMembership, ChangeOpUpdated) row in the same tx.
	// Returns ErrNotFound when the row is missing.
	UpdateChatMembership(ctx context.Context, m ChatMembership) error

	// DeleteChatMembership removes the row identified by id and
	// appends a (EntityKindMembership, ChangeOpDestroyed) state-change
	// row in the same tx. Returns ErrNotFound when the row is missing.
	DeleteChatMembership(ctx context.Context, id MembershipID) error

	// InsertChatMessage persists a new chat message row, advances the
	// owning conversation's denormalised LastMessageAt and
	// MessageCount, and appends a (EntityKindChatMessage,
	// ChangeOpCreated) state-change row with ParentEntityID =
	// conversation_id — all in one tx. The reactions / attachments
	// JSON caps are enforced server-side; oversized payloads return
	// ErrInvalidArgument.
	InsertChatMessage(ctx context.Context, m ChatMessage) (ChatMessageID, error)

	// GetChatMessage returns one row by id, or ErrNotFound.
	GetChatMessage(ctx context.Context, id ChatMessageID) (ChatMessage, error)

	// ListChatMessages applies filter and returns rows in ascending ID
	// order. Caps at 1000 server-side.
	ListChatMessages(ctx context.Context, filter ChatMessageFilter) ([]ChatMessage, error)

	// UpdateChatMessage persists the mutable fields (BodyText, BodyHTML,
	// BodyFormat, ReactionsJSON, AttachmentsJSON, MetadataJSON,
	// EditedAt) on the row identified by m.ID, bumps ModSeq, and
	// appends a (EntityKindChatMessage, ChangeOpUpdated) state-change
	// row in the same tx. Reaction-cap validation runs server-side;
	// oversized payloads return ErrInvalidArgument. Returns
	// ErrNotFound when the row is missing.
	UpdateChatMessage(ctx context.Context, m ChatMessage) error

	// SoftDeleteChatMessage flips the soft-delete bit on a chat
	// message (REQ-CHAT-21): sets DeletedAt to the clock instant,
	// clears BodyText / BodyHTML, leaves the row in place so threading
	// and read-receipt offsets stay stable. Bumps ModSeq and appends a
	// (EntityKindChatMessage, ChangeOpUpdated) state-change row.
	// Returns ErrNotFound when the row is missing; idempotent on an
	// already-deleted row.
	SoftDeleteChatMessage(ctx context.Context, id ChatMessageID) error

	// SetChatReaction atomically toggles one reactor's presence in the
	// reactions JSON for emoji on the message identified by msgID:
	// when present is true the principal is added; when false they
	// are removed; toggling away from non-existent state is a no-op.
	// Bumps ModSeq and appends a (EntityKindChatMessage,
	// ChangeOpUpdated) state-change row when the JSON actually
	// changes. Returns ErrNotFound when the message is missing,
	// ErrInvalidArgument when emoji is malformed or would push the
	// reaction caps over the documented limits.
	SetChatReaction(ctx context.Context, msgID ChatMessageID, emoji string, principalID PrincipalID, present bool) error

	// InsertChatBlock records a block. The composite primary key
	// (blocker, blocked) makes a duplicate insert ErrConflict.
	// Inserting a self-block (blocker == blocked) returns
	// ErrInvalidArgument.
	InsertChatBlock(ctx context.Context, b ChatBlock) error

	// DeleteChatBlock removes the (blocker, blocked) row. Returns
	// ErrNotFound when the row is already gone.
	DeleteChatBlock(ctx context.Context, blocker, blocked PrincipalID) error

	// ListChatBlocksBy returns every block row whose blocker_principal_id
	// equals blocker, in ascending blocked_principal_id order.
	ListChatBlocksBy(ctx context.Context, blocker PrincipalID) ([]ChatBlock, error)

	// IsBlocked reports whether blocker has blocked blocked.
	IsBlocked(ctx context.Context, blocker, blocked PrincipalID) (bool, error)

	// LastReadAt returns the (LastReadMessageID, JoinedAt) pair from
	// the membership row identifying principalID's read pointer in
	// conversationID. Returns ErrNotFound when the principal is not a
	// member of the conversation.
	LastReadAt(ctx context.Context, principalID PrincipalID, conversationID ConversationID) (*ChatMessageID, time.Time, error)

	// CountChatMessagesUnread returns the number of non-deleted messages
	// in conversationID whose sender_principal_id != viewerPID and
	// whose id > *lastReadMessageID. When lastReadMessageID is nil,
	// every non-self, non-deleted message in the conversation counts.
	// Self-sent and system messages are excluded so the unread badge
	// reflects messages the viewer has not yet seen from someone else.
	//
	// This is the authoritative server-computed unread tally surfaced
	// on the JMAP wire as Conversation.unreadCount. It cannot be
	// derived from MessageCount alone because chat_messages.id is a
	// global autoincrement, not a per-conversation counter, so naively
	// comparing lastReadMessageID against MessageCount silently breaks
	// once any conversation has had a real markRead.
	CountChatMessagesUnread(ctx context.Context, conversationID ConversationID, viewerPID PrincipalID, lastReadMessageID *ChatMessageID) (int, error)

	// ChatPrincipalCanReadBlob reports whether principalID is a
	// member of any conversation that contains a non-deleted chat
	// message referencing blobHash — either via attachments_json
	// (`"blob_hash":"<hash>"` substring) or via an inline reference
	// in body_html (the suite embeds image attachments as
	// `<img src="/jmap/download/.../<hash>/...">`). Used by the
	// JMAP /jmap/download endpoint as a fallback authorisation path
	// when the URL's accountId does not match the requester: chat
	// blobs are intentionally shared across the conversation's
	// members, so the strict mail-style accountId check cannot stand
	// alone for chat-embedded images.
	//
	// Implementations may scan with a substring predicate; blobHash
	// MUST be a hex-encoded BLAKE3 digest validated by the caller
	// before calling this method (the implementations do not
	// re-validate to keep the predicate sargable).
	ChatPrincipalCanReadBlob(ctx context.Context, principalID PrincipalID, blobHash string) (bool, error)

	// FirstReceivedToForBlob returns the canonical envelope RCPT TO
	// (REQ-FLOW-33) for the first message_mailboxes row that ties
	// principalID's owned message blob (matched by blobHash) to one
	// of their mailboxes with a non-empty received_to. Returns the
	// empty string when no such row exists — pre-feature memberships,
	// non-inbound origins (Email/set, copy, move from non-fanout
	// sources), or a blob that does not belong to a message owned by
	// principalID. The caller (the /jmap/download handler) uses this
	// to inject the synthetic X-Herold-Recipient header into a
	// full-message blob being streamed back to the principal
	// (REQ-FLOW-34). blobHash MUST be a hex-encoded BLAKE3 digest;
	// implementations may rely on a direct equality lookup and do
	// not re-validate the input.
	FirstReceivedToForBlob(ctx context.Context, principalID PrincipalID, blobHash string) (string, error)

	// SetLastRead advances the per-membership LastReadMessageID
	// pointer (REQ-CHAT-30) to msgID, bumps ModSeq, and appends a
	// (EntityKindMembership, ChangeOpUpdated) state-change row. Returns
	// ErrNotFound when the membership is missing.
	SetLastRead(ctx context.Context, principalID PrincipalID, conversationID ConversationID, msgID ChatMessageID) error

	// GetChatAccountSettings returns the per-principal chat defaults
	// (Phase 2 Wave 2.9.6 REQ-CHAT-20 / REQ-CHAT-92). When no row has
	// been persisted for principalID, returns a zero-value row carrying
	// PrincipalID = principalID,
	// DefaultRetentionSeconds = ChatDefaultRetentionSeconds (0; never
	// expire), and DefaultEditWindowSeconds =
	// ChatDefaultEditWindowSeconds (900s; 15 min). Never returns
	// ErrNotFound — the absent row is the implicit default.
	GetChatAccountSettings(ctx context.Context, principalID PrincipalID) (ChatAccountSettings, error)

	// UpsertChatAccountSettings writes the account-defaults row for
	// settings.PrincipalID, inserting on first call and updating on
	// subsequent ones. Resolved CreatedAt and UpdatedAt are not echoed
	// back to the caller; subsequent Get returns the persisted instants.
	UpsertChatAccountSettings(ctx context.Context, settings ChatAccountSettings) error

	// HardDeleteChatMessage removes the chat_messages row identified by
	// id (Phase 2 Wave 2.9.6 REQ-CHAT-92 retention). Distinct from
	// SoftDeleteChatMessage: the row is GONE from the table, the
	// owning conversation's MessageCount and LastMessageAt are
	// recomputed from the surviving rows, and a
	// (EntityKindChatMessage, ChangeOpDestroyed) state-change row is
	// appended. Returns ErrNotFound when the message has already been
	// removed.
	HardDeleteChatMessage(ctx context.Context, id ChatMessageID) error

	// ListChatConversationsForRetention returns up to limit conversation
	// rows whose RetentionSeconds is non-nil and positive, in ascending
	// ID order starting after afterID. Used by the retention sweeper to
	// page through the per-conversation override set.
	ListChatConversationsForRetention(ctx context.Context, afterID ConversationID, limit int) ([]ChatConversation, error)

	// ListChatAccountSettingsForRetention returns up to limit
	// account-defaults rows whose DefaultRetentionSeconds > 0, in
	// ascending principal-id order starting after afterID. Used by the
	// retention sweeper to page through accounts that have opted into a
	// global retention policy.
	ListChatAccountSettingsForRetention(ctx context.Context, afterID PrincipalID, limit int) ([]ChatAccountSettings, error)

	// FindDMBetween returns the canonical DM conversation between
	// principals a and b (a != b) together with its current membership
	// rows. The third return value is true when a DM was found. When
	// multiple legacy duplicate rows exist the one with the smallest id
	// is returned. Returns (zero, nil, false, nil) when no DM exists.
	//
	// The check searches both the chat_dm_pairs index (populated for new
	// DMs after migration 0034) and falls back to a membership JOIN for
	// pre-migration rows.
	FindDMBetween(ctx context.Context, a, b PrincipalID) (ChatConversation, []ChatMembership, bool, error)

	// InsertDMConversation creates a new DM conversation between creator
	// and other atomically: it inserts the chat_conversations row, both
	// chat_memberships rows (creator as owner, other as member), and the
	// chat_dm_pairs uniqueness row in a single transaction. If a
	// concurrent insert races on the same (creator, other) pair and wins
	// the unique constraint, InsertDMConversation returns ErrConflict so
	// the caller can retry with FindDMBetween to obtain the winner.
	//
	// name is the pre-computed display name for the conversation row
	// (resolved from the other principal's display name by the handler).
	// now is the timestamp to use for CreatedAt / UpdatedAt / JoinedAt.
	InsertDMConversation(ctx context.Context, creator, other PrincipalID, name string, now time.Time) (ChatConversation, []ChatMembership, error)

	// GetBlobRef returns the metadata-side blob_refs row for hash:
	// (size, ref_count). Returns ErrNotFound when no row exists for the
	// hash. The blob-store sweeper is responsible for evicting rows
	// whose ref_count has fallen to zero out-of-band; this method
	// reports the persisted state and does not itself mutate it.
	// Exposed primarily for test assertions and future GC tooling.
	GetBlobRef(ctx context.Context, hash string) (size int64, refCount int64, err error)

	// -- Phase 3 Wave 3.5c inbound attachment policy (REQ-FLOW-ATTPOL-01) -

	// GetInboundAttachmentPolicy returns the effective inbound
	// attachment policy for the lowercased "local@domain" address. The
	// lookup tries the per-recipient row first and falls back to the
	// recipient's domain row; an absent row at both levels returns the
	// implicit default {AttPolicyAccept, ""}. Never returns ErrNotFound
	// — the absent row is the implicit default. address must be
	// lowercased and contain exactly one '@'; malformed input yields
	// the implicit default rather than an error so callers do not have
	// to guard the SMTP path.
	GetInboundAttachmentPolicy(ctx context.Context, address string) (InboundAttachmentPolicyRow, error)

	// SetInboundAttachmentPolicyRecipient upserts the per-recipient
	// policy row for the lowercased address. Passing AttPolicyUnset
	// deletes the row so a subsequent GetInboundAttachmentPolicy falls
	// back to the domain row.
	SetInboundAttachmentPolicyRecipient(ctx context.Context, address string, row InboundAttachmentPolicyRow) error

	// SetInboundAttachmentPolicyDomain upserts the per-domain policy
	// row for the lowercased domain (no leading dot). Passing
	// AttPolicyUnset deletes the row.
	SetInboundAttachmentPolicyDomain(ctx context.Context, domain string, row InboundAttachmentPolicyRow) error

	// -- Phase 3 Wave 3.8a JMAP PushSubscription (REQ-PROTO-120..122) -

	// InsertPushSubscription persists a new push-subscription row and
	// returns the assigned ID. Appends a (EntityKindPushSubscription,
	// ChangeOpCreated) state-change row in the same tx so JMAP
	// EventSource consumers see the subscription churn for clients
	// that watch the type.
	InsertPushSubscription(ctx context.Context, ps PushSubscription) (PushSubscriptionID, error)

	// GetPushSubscription returns the row by id, or ErrNotFound when
	// the row is missing.
	GetPushSubscription(ctx context.Context, id PushSubscriptionID) (PushSubscription, error)

	// ListPushSubscriptionsByPrincipal returns every subscription
	// owned by pid, in ascending ID order. Empty slice (nil) when the
	// principal has no subscriptions.
	ListPushSubscriptionsByPrincipal(ctx context.Context, pid PrincipalID) ([]PushSubscription, error)

	// UpdatePushSubscription persists the mutable fields (Verified,
	// VerificationCode, Expires, Types, NotificationRulesJSON,
	// QuietHoursStartLocal, QuietHoursEndLocal, QuietHoursTZ) on the
	// row identified by ps.ID and bumps UpdatedAt. Appends a
	// (EntityKindPushSubscription, ChangeOpUpdated) state-change row
	// in the same tx. Returns ErrNotFound when the row is missing.
	UpdatePushSubscription(ctx context.Context, ps PushSubscription) error

	// DeletePushSubscription removes the row identified by id and
	// appends a (EntityKindPushSubscription, ChangeOpDestroyed) state-
	// change row in the same tx. Returns ErrNotFound when the row is
	// missing.
	DeletePushSubscription(ctx context.Context, id PushSubscriptionID) error

	// -- Phase 3 Wave 3.2 SES inbound dedupe (REQ-HOOK-SES-02) --------

	// IsSESSeen returns true when messageID is present in the
	// ses_seen_messages table within the configured retention window.
	IsSESSeen(ctx context.Context, messageID string) (bool, error)

	// InsertSESSeen records messageID as seen at seenAt.  Duplicate
	// inserts are silently ignored (UPSERT ON CONFLICT DO NOTHING).
	InsertSESSeen(ctx context.Context, messageID string, seenAt time.Time) error

	// GCOldSESSeen deletes ses_seen_messages rows whose seen_at is
	// strictly before cutoff.  Intended to be called periodically by
	// the SES inbound handler.
	GCOldSESSeen(ctx context.Context, cutoff time.Time) error

	// -- Phase 3 Wave 3.9 Email reactions (REQ-PROTO-100..103) ---------

	// AddEmailReaction inserts a (emailID, emoji, principalID) row at
	// createdAt. Duplicate inserts (same triple) are silently ignored —
	// the reaction is already present so the call is a no-op.
	AddEmailReaction(ctx context.Context, emailID MessageID, emoji string, principalID PrincipalID, createdAt time.Time) error

	// RemoveEmailReaction deletes the (emailID, emoji, principalID) row.
	// Returns nil when the row is absent (idempotent).
	RemoveEmailReaction(ctx context.Context, emailID MessageID, emoji string, principalID PrincipalID) error

	// ListEmailReactions returns all reactions on emailID as a two-level
	// map: emoji → set of reacting principal ids. Returns an empty map
	// (never nil) when no reactions exist.
	ListEmailReactions(ctx context.Context, emailID MessageID) (map[string]map[PrincipalID]struct{}, error)

	// BatchListEmailReactions returns reactions for every id in emailIDs.
	// The outer map key is the MessageID; absent entries mean no reactions.
	// Used by Email/get to batch-load reactions without N+1 round-trips.
	BatchListEmailReactions(ctx context.Context, emailIDs []MessageID) (map[MessageID]map[string]map[PrincipalID]struct{}, error)

	// GetMessageByMessageIDHeader looks up a message in the mailbox of
	// principalID whose cached Envelope.MessageID equals msgIDHeader (the
	// RFC 5322 Message-ID header value, without angle brackets). Returns
	// the first matching message, or ErrNotFound. Used by the inbound
	// reaction handler to locate the original email by its Message-ID.
	GetMessageByMessageIDHeader(ctx context.Context, principalID PrincipalID, msgIDHeader string) (Message, error)

	// GetMessageByBlobHash looks up a message owned by principalID whose
	// content-addressed body hash (blob_hash column) equals blobHash (the
	// canonical-CRLF BLAKE3 of the message bytes, the same value
	// BlobRef.Hash carries). Returns the first matching message, or
	// ErrNotFound. This is the content-hash dedup fallback used by the IMAP
	// importers when a message has no usable Message-ID header
	// (REQ-IMAP-IMP-30 / REQ-IMPORT-05): two messages whose bodies
	// canonicalise to the same bytes share a hash, so a message already
	// mirrored is not duplicated on a later pass. blobHash must be non-empty.
	GetMessageByBlobHash(ctx context.Context, principalID PrincipalID, blobHash string) (Message, error)

	// ListPrincipalBlobHashes returns the distinct blob_hash values
	// owned by principalID in arbitrary order. Used by the bulk gmail
	// importer to seed a content-addressed dedup set
	// (REQ-IMPORT-05). The hash is the canonical-CRLF BLAKE3 of the
	// message bytes (REQ-STORE-10), the same key BlobRef.Hash carries.
	// Content-hash dedup is correct in the face of senders that reuse
	// Message-ID headers across distinct messages (notably spam).
	// Not for user-facing callers — the result can be large
	// (one entry per unique message blob) on big mailboxes.
	ListPrincipalBlobHashes(ctx context.Context, principalID PrincipalID) ([]string, error)

	// -- Phase 3 Wave 3.10 ShortcutCoachStat (REQ-PROTO-110..112) --------

	// AppendCoachEvents inserts one or more CoachEvent rows for
	// principalID. Each event records a batch of invocations at
	// occurredAt. Implementations MUST set RecordedAt to the server
	// clock; callers supply OccurredAt from the client-reported
	// timestamp. Non-empty slices are inserted atomically.
	AppendCoachEvents(ctx context.Context, events []CoachEvent) error

	// GetCoachStat returns the aggregated CoachStat for one
	// (principalID, action) pair, using now as the reference point for
	// the 14d / 90d sliding windows. Returns a zero CoachStat (not
	// ErrNotFound) when no events or dismissal rows exist — the absence
	// of data is a valid stat with all counters zero.
	GetCoachStat(ctx context.Context, principalID PrincipalID, action string, now time.Time) (CoachStat, error)

	// ListCoachStats returns CoachStat rows for every action that has
	// at least one event or a dismissal row for principalID. The result
	// is sorted by action lexicographically. now is the window reference
	// point.
	ListCoachStats(ctx context.Context, principalID PrincipalID, now time.Time) ([]CoachStat, error)

	// UpsertCoachDismiss sets (or updates) the coach_dismiss row for
	// (principalID, action). dismissCount replaces the stored value;
	// dismissUntil replaces the stored value (nil clears it).
	UpsertCoachDismiss(ctx context.Context, d CoachDismiss) error

	// DestroyCoachStat removes all coach_events and the coach_dismiss
	// row for (principalID, action). Idempotent: no error when the rows
	// are already absent.
	DestroyCoachStat(ctx context.Context, principalID PrincipalID, action string) error

	// DestroyAllCoachStats removes every coach_events row and every
	// coach_dismiss row for principalID. Used by the "Reset coach data"
	// path (REQ-COACH-72).
	DestroyAllCoachStats(ctx context.Context, principalID PrincipalID) error

	// GCCoachEvents deletes coach_events rows whose occurred_at is
	// strictly before cutoff. Intended to be called periodically (90-day
	// retention window in production). Returns the number of rows deleted.
	GCCoachEvents(ctx context.Context, cutoff time.Time) (int64, error)

	// -- Wave 3.15 ManagedRule (REQ-FLT-01..31) -----------------------

	// InsertManagedRule creates a new managed_rules row. The returned
	// ManagedRule carries the server-assigned ID and timestamps. Returns
	// ErrInvalidArgument when the Conditions or Actions JSON is invalid.
	InsertManagedRule(ctx context.Context, rule ManagedRule) (ManagedRule, error)

	// UpdateManagedRule replaces the mutable fields (Name, Enabled,
	// SortOrder, Conditions, Actions) of the row identified by rule.ID.
	// Returns ErrNotFound when the row is absent or owned by a different
	// principal. Returns ErrInvalidArgument for invalid Conditions/Actions.
	UpdateManagedRule(ctx context.Context, rule ManagedRule) (ManagedRule, error)

	// DeleteManagedRule removes the row. Returns ErrNotFound when the row
	// does not exist or is owned by a different principal.
	DeleteManagedRule(ctx context.Context, id ManagedRuleID, pid PrincipalID) error

	// GetManagedRule returns one row by id. Returns ErrNotFound when the
	// row does not exist or is owned by a different principal.
	GetManagedRule(ctx context.Context, id ManagedRuleID, pid PrincipalID) (ManagedRule, error)

	// ListManagedRules returns the principal's rules in (sort_order, id)
	// ascending order, subject to filter.
	ListManagedRules(ctx context.Context, pid PrincipalID, filter ManagedRuleFilter) ([]ManagedRule, error)

	// -- REQ-MAIL-11e..m: seen-addresses history -----------------------

	// UpsertSeenAddress atomically inserts or updates the
	// (principalID, email) seen-address row.  On insert, firstSeenAt
	// and lastUsedAt are set to the current instant; on update,
	// lastUsedAt is refreshed, sendCount is incremented by sendDelta,
	// and receivedCount is incremented by receiveDelta.  When
	// displayName is non-empty it overwrites the stored value.
	//
	// After the upsert, if the principal now has more than 500 rows the
	// single oldest-by-lastUsedAt row is deleted in the same
	// transaction (server-side 500-entry cap, REQ-MAIL-11e).
	//
	// Returns the upserted row and isNew=true when the row was inserted
	// rather than updated.
	UpsertSeenAddress(ctx context.Context, principalID PrincipalID, email, displayName string, sendDelta, receiveDelta int64) (SeenAddress, bool, error)

	// ListSeenAddressesByPrincipal returns up to limit seen-address rows
	// for principalID, ordered by last_used_at DESC. A non-positive limit
	// applies the 500-row cap as the default.
	ListSeenAddressesByPrincipal(ctx context.Context, principalID PrincipalID, limit int) ([]SeenAddress, error)

	// GetSeenAddressByEmail returns the seen-address row for the given
	// (principalID, email) pair, or ErrNotFound.
	GetSeenAddressByEmail(ctx context.Context, principalID PrincipalID, email string) (SeenAddress, error)

	// DestroySeenAddress removes the row identified by id owned by
	// principalID.  Returns ErrNotFound when the row is absent or owned
	// by a different principal.
	DestroySeenAddress(ctx context.Context, principalID PrincipalID, id SeenAddressID) error

	// DestroySeenAddressByEmail removes the row for (principalID, email).
	// Returns ErrNotFound when the row is absent.
	DestroySeenAddressByEmail(ctx context.Context, principalID PrincipalID, email string) error

	// PurgeSeenAddressesByPrincipal removes every seen-address row for
	// principalID (privacy-toggle-off path, REQ-MAIL-11m).  Returns the
	// number of rows deleted.
	PurgeSeenAddressesByPrincipal(ctx context.Context, principalID PrincipalID) (int, error)

	// -- REQ-SET-03b: per-identity avatar blob refcounting --

	// IncRefBlob increments the ref_count for hash in blob_refs by 1.
	// If no row exists for hash yet, one is inserted with ref_count=1.
	// size is ignored when a row already exists. This is the caller-
	// managed refcount path for objects that are not messages (e.g.
	// identity avatar blobs); message blobs are managed atomically
	// inside InsertMessage / ExpungeMessages.
	IncRefBlob(ctx context.Context, hash string, size int64) error

	// DecRefBlob decrements the ref_count for hash in blob_refs by 1,
	// refusing to drive it below zero (same guard as the internal decRef
	// used by message paths). A no-op when hash is empty or the row does
	// not exist; logs a warning at the store level.
	DecRefBlob(ctx context.Context, hash string) error

	// -- REQ-OPS-206: client-log ring buffer ---------------------------

	// AppendClientLog inserts one event row into the clientlog ring-buffer
	// table for the given slice.  The caller supplies a fully-enriched row
	// (server_ts, clock_skew_ms, user_id, etc.); the store assigns the ID.
	// Errors are non-fatal: the handler counts them and continues to the
	// slog / OTLP fan-out legs.
	AppendClientLog(ctx context.Context, row ClientLogRow) error

	// ListClientLogByCursor returns up to opts.Limit rows matching
	// opts.Filter in descending ID order (newest first).  The opaque
	// cursor from a prior call resumes the page; an empty cursor starts
	// from the newest row.  Returns the rows and a next-page cursor (empty
	// string when there are no further rows).
	ListClientLogByCursor(ctx context.Context, opts ClientLogCursorOptions) (rows []ClientLogRow, nextCursor string, err error)

	// ListClientLogByRequestID returns every row whose request_id equals
	// requestID, in descending ID order.  Used by the timeline endpoint
	// (REQ-ADM-231).  Returns an empty slice when no rows match.
	ListClientLogByRequestID(ctx context.Context, requestID string) ([]ClientLogRow, error)

	// EvictClientLog deletes the oldest rows from the named slice that
	// exceed either the row-count cap or the age limit.  At most
	// opts.BatchSize rows are deleted per call to bound lock pressure on
	// SQLite.  Returns the number of rows deleted.
	EvictClientLog(ctx context.Context, opts ClientLogEvictOptions) (deleted int, err error)

	// -- REQ-OPS-208 / REQ-CLOG-06: session rows for per-session state ---

	// UpsertSession inserts or updates the session row identified by
	// s.SessionID. On conflict (same session_id) all mutable columns are
	// overwritten, so a refresh call is a plain Upsert. The store does not
	// validate that ExpiresAt is in the future; callers enforce that.
	UpsertSession(ctx context.Context, s SessionRow) error

	// GetSession returns the session row for sessionID. Returns
	// ErrNotFound when no row exists for that key (the session was never
	// persisted or has been evicted).
	GetSession(ctx context.Context, sessionID string) (SessionRow, error)

	// DeleteSession removes the session row for sessionID. Returns
	// ErrNotFound when the row is absent. Used by logout.
	DeleteSession(ctx context.Context, sessionID string) error

	// UpdateSessionTelemetry sets the clientlog_telemetry_enabled column
	// on an existing session row. Returns ErrNotFound when the session
	// does not exist (already expired or never created). This is called
	// by the self-service telemetry endpoint after it has updated the
	// principal row, so the session row stays in sync without a full
	// Upsert.
	UpdateSessionTelemetry(ctx context.Context, sessionID string, enabled bool) error

	// UpdateSessionLastSeen sets the last_seen_at_us column on an
	// existing session row to atMicros (REQ-AUTH-72, issue #12 slice 3).
	// Returns ErrNotFound when the session does not exist. Called by
	// the session resolver on every accepted authenticated request to
	// slide the idle-timeout deadline forward. atMicros is taken as a
	// trusted "now" from the caller so the store layer stays free of
	// clock dependencies.
	UpdateSessionLastSeen(ctx context.Context, sessionID string, atMicros int64) error

	// EvictExpiredSessions deletes all session rows whose expires_at_us
	// is <= nowMicros. Returns the number of rows deleted. Intended for
	// a periodic background sweeper; safe to call concurrently.
	EvictExpiredSessions(ctx context.Context, nowMicros int64) (deleted int, err error)

	// ClearExpiredLivetail sets clientlog_livetail_until_us = NULL on all
	// session rows where clientlog_livetail_until_us <= nowMicros. Returns
	// the number of rows updated. Intended for a periodic background
	// sweeper; the comparison also happens at read time so this is a
	// cosmetic cleanup only (REQ-OPS-211).
	ClearExpiredLivetail(ctx context.Context, nowMicros int64) (cleared int, err error)

	// -- Attachment shares (REQ-SHARE-01..23) --------------------------

	// CreateFileShare creates a file_shares row in state pending. The
	// store generates the capability token (CSPRNG, >= 128 bits,
	// URL-safe base64), installs an incRef on the blob, enforces the
	// per-principal count cap (FileSharesConfig.MaxSharesPerPrincipal)
	// and the dedup-aware share quota
	// (FileSharesConfig.ShareQuotaPerPrincipal) inside the same
	// transaction. REQ-SHARE-11b, REQ-SHARE-12, REQ-SHARE-50.
	//
	// Returns ErrTooManyShares when the count cap is exceeded.
	// Returns ErrQuotaExceeded when adding this blob's distinct size
	// would exceed the principal's share quota.
	// Returns ErrInvalidArgument when required fields are empty.
	CreateFileShare(ctx context.Context, req FileShareCreate) (FileShare, error)

	// ConfirmFileShare transitions a pending share to active, resetting
	// expires_at to now+DefaultTTL clamped to now+MaxTTL. The source
	// argument carries the optional message back-reference (REQ-SHARE-04):
	// when source is non-zero, source_message_id, source_subject, and
	// source_recipients are persisted atomically with the state transition.
	// A zero-value source leaves the columns NULL. Re-confirming an already
	// active share is idempotent (returns the current row, nil error); the
	// source columns are updated only when source is non-zero so a re-confirm
	// with an empty source does not wipe a previously-stored context.
	// Returns ErrShareNotConfirmable when the share is revoked or does not
	// exist. REQ-SHARE-04, REQ-SHARE-20, REQ-SHARE-21.
	ConfirmFileShare(ctx context.Context, principalID PrincipalID, id string, cfg FileSharesConfig, source FileShareSource) (FileShare, error)

	// RevokeFileShare transitions a share to revoked and stamps
	// revoked_at. Returns ErrNotFound when the share does not exist or
	// is owned by a different principal. REQ-SHARE-22.
	RevokeFileShare(ctx context.Context, principalID PrincipalID, id string) error

	// DestroyFileShare deletes the file_shares row and decrements the
	// blob refcount. Returns ErrNotFound when the share does not exist
	// or is owned by a different principal. REQ-SHARE-21.
	DestroyFileShare(ctx context.Context, principalID PrincipalID, id string) error

	// GetFileShareByID returns the file_shares row identified by the
	// capability token id, regardless of state. The public download
	// path is the sole caller; it performs serveability decisions
	// itself (REQ-SHARE-11e). Returns ErrNotFound when no row matches.
	GetFileShareByID(ctx context.Context, id string) (FileShare, error)

	// ListFileSharesByPrincipal returns file_shares rows owned by
	// principalID, ordered by created_at_us DESC (newest first),
	// subject to filter. REQ-SHARE-40.
	ListFileSharesByPrincipal(ctx context.Context, principalID PrincipalID, filter FileShareListFilter) ([]FileShare, error)

	// RecordFileShareDownload atomically increments download_count and
	// sets last_downloaded_at to now for the share identified by id.
	// Returns the post-increment row so the caller can enforce
	// max_downloads without a race. Returns ErrNotFound when the row
	// does not exist. REQ-SHARE-30, REQ-SHARE-44.
	RecordFileShareDownload(ctx context.Context, id string) (FileShare, error)

	// SweepFileShares deletes:
	//   - pending shares whose created_at_us < now-pending_ttl
	//   - active shares whose expires_at_us < now
	//   - revoked shares whose revoked_at_us < now-revoked_grace
	// Each deletion decrements the blob refcount. Returns per-reason
	// counts. REQ-SHARE-23.
	SweepFileShares(ctx context.Context, now time.Time, cfg FileSharesConfig) (SweepStats, error)

	// IsFileShareBlobReferenced returns true when any non-deleted
	// file_shares row has blob_hash = hash. Used by the blob-GC
	// liveness callback (REQ-STORE-12 interaction, REQ-SHARE-01) to
	// extend the referenced set beyond message blobs.
	IsFileShareBlobReferenced(ctx context.Context, hash string) (bool, error)

	// UpdateFileShareExpiry shortens the expiry on an active or pending
	// share owned by principalID. newExpiry must be in the future and
	// strictly earlier than the share's current ExpiresAt (only
	// shortening is allowed; extending is rejected).
	//
	// Returns ErrNotFound when the share does not exist or is owned by a
	// different principal.
	// Returns ErrShareNotConfirmable when the share is in state revoked.
	// Returns ErrShareExpiryNotShortenable when newExpiry >= current
	// ExpiresAt or newExpiry is not in the future.
	// REQ-SHARE-42.
	UpdateFileShareExpiry(ctx context.Context, principalID PrincipalID, id string, newExpiry time.Time) error

	// UpdateFileShareMaxDownloads lowers the max-downloads cap on an
	// active or pending share owned by principalID. newMax must be
	// strictly less than the current MaxDownloads value and must not be
	// below the current DownloadCount. When the current MaxDownloads is
	// nil (unlimited), lowering to any newMax >= DownloadCount is
	// allowed.
	//
	// Returns ErrNotFound when the share does not exist or is owned by a
	// different principal.
	// Returns ErrShareNotConfirmable when the share is in state revoked.
	// Returns ErrShareMaxDownloadsNotLowerable when newMax >= current
	// MaxDownloads or newMax < current DownloadCount.
	// REQ-SHARE-42.
	UpdateFileShareMaxDownloads(ctx context.Context, principalID PrincipalID, id string, newMax int64) error

	// -- IMAP import (issue #25, REQ-IMAP-IMP-02, -15..19, -34, -42, -70, -74) --

	// CreateIMAPImportAccount creates a new imapimport_account row. The
	// state defaults to IMAPImportAccountStateEnabled when zero-valued.
	// CredentialCT must carry the "v1:" prefix (REQ-IMAP-IMP-70).
	// Returns ErrInvalidArgument when the credential validation fails.
	CreateIMAPImportAccount(ctx context.Context, create IMAPImportAccountCreate) (IMAPImportAccount, error)

	// UpdateIMAPImportAccount replaces mutable fields on an existing
	// imapimport_account row owned by update.PrincipalID. When
	// update.CredentialCT is non-nil it replaces the sealed credential
	// and is validated. Returns ErrNotFound when the (id, principal_id)
	// row is absent. Returns ErrInvalidArgument on credential validation
	// failure.
	UpdateIMAPImportAccount(ctx context.Context, update IMAPImportAccountUpdate) (IMAPImportAccount, error)

	// GetIMAPImportAccount returns the account row identified by id.
	// Returns ErrNotFound when absent.
	GetIMAPImportAccount(ctx context.Context, id string) (IMAPImportAccount, error)

	// ListIMAPImportAccountsByPrincipal returns all accounts owned by
	// principalID in ascending created_at order.
	ListIMAPImportAccountsByPrincipal(ctx context.Context, principalID PrincipalID) ([]IMAPImportAccount, error)

	// ListEnabledIMAPImportAccounts returns all accounts with state ==
	// "enabled" across all principals in ascending created_at order.
	// Used by the worker scheduler at startup and on config-change
	// events (REQ-IMAP-IMP-25).
	ListEnabledIMAPImportAccounts(ctx context.Context) ([]IMAPImportAccount, error)

	// DeleteIMAPImportAccount removes the account row and (via ON DELETE
	// CASCADE) all folder_map, folder_cursor, and message_state rows
	// associated with it. Returns ErrNotFound when the (id, principalID)
	// row is absent.
	DeleteIMAPImportAccount(ctx context.Context, principalID PrincipalID, id string) error

	// SetIMAPImportAccountState updates the state, last_error, and
	// optionally last_success_at on an existing account row. When
	// lastSuccessAt is non-nil the column is advanced; when nil the
	// column is left unchanged (COALESCE semantics). Returns ErrNotFound
	// when id is absent.
	SetIMAPImportAccountState(ctx context.Context, id string, state IMAPImportAccountState, lastError string, lastSuccessAt *time.Time) error

	// GetIMAPImportFolderMap returns the folder-name mapping entries for
	// accountID in ascending upstream_folder order. Returns an empty
	// slice (nil) and nil error when no entries exist.
	GetIMAPImportFolderMap(ctx context.Context, accountID string) ([]IMAPImportFolderMapEntry, error)

	// SetIMAPImportFolderMap replaces the entire folder map for accountID
	// in one transaction: delete all existing rows, then insert entries.
	// Passing nil or an empty slice clears the mapping.
	SetIMAPImportFolderMap(ctx context.Context, accountID string, entries []IMAPImportFolderMapEntry) error

	// GetIMAPImportFolderCursor returns the sync cursor for the
	// (accountID, upstreamFolder) pair. The bool return is false when no
	// cursor row exists yet (the folder has not been synced).
	GetIMAPImportFolderCursor(ctx context.Context, accountID, upstreamFolder string) (IMAPImportFolderCursor, bool, error)

	// UpsertIMAPImportFolderCursor inserts or updates the sync cursor for
	// the (accountID, upstreamFolder) pair identified inside cursor.
	// Idempotent: calling with the same values is a no-op.
	UpsertIMAPImportFolderCursor(ctx context.Context, cursor IMAPImportFolderCursor) error

	// GetIMAPImportMessageState returns the per-message sync state for
	// the (accountID, upstreamFolder, upstreamUID) triple. The bool
	// return is false when no row exists (message not yet imported or
	// its state was not recorded).
	GetIMAPImportMessageState(ctx context.Context, accountID, upstreamFolder string, upstreamUID uint32) (IMAPImportMessageState, bool, error)

	// GetIMAPImportMessageStateByMessage looks up the message state by
	// the herold-side (accountID, heroldMessageID) pair — the write-back
	// path (REQ-IMAP-IMP-34 / REQ-IMAP-IMP-42). The bool return is
	// false when no row exists.
	GetIMAPImportMessageStateByMessage(ctx context.Context, accountID string, heroldMessageID MessageID) (IMAPImportMessageState, bool, error)

	// UpsertIMAPImportMessageState inserts or updates the per-message
	// sync state. On conflict (accountID, upstreamFolder, upstreamUID)
	// the herold-side IDs and LastSyncedFlags are replaced.
	UpsertIMAPImportMessageState(ctx context.Context, state IMAPImportMessageState) error
}

// BlobReader is a seekable, range-capable blob handle (REQ-STORE-14):
// sequential streaming plus random access, so callers read headers,
// a bounded excerpt, or one part at a time without buffering the blob.
// The caller must Close the handle when done.
type BlobReader interface {
	io.ReadSeekCloser
	io.ReaderAt
}

// Blobs is the content-addressed blob surface: one object per canonical
// message body, identified by BLAKE3 hex hash (REQ-STORE-10..16).
//
// Writes are atomic at the filesystem level (temp + fsync + rename, see
// docs/design/server/architecture/02-storage-architecture.md §Writes). Reads
// return a seekable, ReaderAt-capable BlobReader (REQ-STORE-14) so callers
// can stream, seek, or range-read without buffering the whole blob.
type Blobs interface {
	// Put writes the bytes read from r as a new blob, canonicalizing
	// CRLF line endings before hashing (REQ-STORE-10). Returns the
	// BlobRef identifying the stored content. Idempotent: storing the
	// same canonical content twice returns the same hash and leaves the
	// existing file untouched.
	Put(ctx context.Context, r io.Reader) (BlobRef, error)

	// Get opens the blob for seekable, range-capable read (REQ-STORE-14).
	// Returns ErrNotFound if the blob does not exist. The caller must
	// Close the returned BlobReader.
	Get(ctx context.Context, hash string) (BlobReader, error)

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
// per-principal for sync consumers, FTSChange is strictly ordered by Seq
// across the whole store (so one cursor tracks all FTS work). It mirrors
// the StateChange row's datatype-agnostic shape: the worker filters on
// (Kind, Op) and treats EntityID / ParentEntityID as opaque ids that it
// interprets per its datatype of interest (currently only
// EntityKindEmail).
type FTSChange struct {
	// Seq is the store-global monotonic sequence used as the indexer's
	// durable cursor.
	Seq uint64
	// PrincipalID is the owning principal.
	PrincipalID PrincipalID
	// Kind names the JMAP datatype of the affected entity. The worker
	// only acts on EntityKindEmail; other kinds flow through harmlessly
	// so the cursor advances and unknown future kinds do not stall it.
	Kind EntityKind
	// EntityID is the opaque entity id within Kind's namespace. For
	// EntityKindEmail this is a MessageID.
	EntityID uint64
	// ParentEntityID is the optional containing-entity id. For
	// EntityKindEmail this is a MailboxID.
	ParentEntityID uint64
	// Op classifies the mutation (created / updated / destroyed).
	Op ChangeOp
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

// PendingMessageRef pairs a MessageID with the received_at_us
// timestamp used as the primary cursor key by the
// ListMessagesWithInternalizePendingByReceivedAt iterator
// (REQ-EXTIMG-BG-INTERNAL-81). The internalize-worker uses both
// fields: received_at_us drives the "newest mail first" ordering and
// the MessageID is the tie-breaker / row identifier passed to
// downstream Get/Replace/Clear methods. Other ordered iterators may
// reuse the type as their needs grow.
type PendingMessageRef struct {
	// ID is the messages.id row identifier.
	ID MessageID
	// ReceivedAtUs is the row's received_at_us value (Unix
	// microseconds). Zero when the message had no header date and the
	// importer / live receiver could not infer one.
	ReceivedAtUs int64
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
	// Subject, From, To, Cc, Body, AttachmentName are per-field term
	// lists. Nil or empty slices are ignored.
	Subject        []string
	From           []string
	To             []string
	Cc             []string
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
// Indexing is asynchronous by design (REQ-STORE-66, docs/design/server/notes/
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
