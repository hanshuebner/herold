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

	// GetMaxChangeSeqForKind returns the highest Seq in the change feed
	// for the given principal and entity kind, or 0 when no matching row
	// exists. Used by JMAP Foo/changes and Foo/set to derive per-type
	// state strings directly from the change feed, so that mutations made
	// outside the JMAP layer (e.g. IMAP APPEND, principal provisioning)
	// are reflected in state strings without a separate counter.
	GetMaxChangeSeqForKind(
		ctx context.Context,
		principalID PrincipalID,
		kind EntityKind,
	) (ChangeSeq, error)

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
	UpdateCategorisationConfig(ctx context.Context, cfg CategorisationConfig) error

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
}

// Blobs is the content-addressed blob surface: one object per canonical
// message body, identified by BLAKE3 hex hash (REQ-STORE-10..16).
//
// Writes are atomic at the filesystem level (temp + fsync + rename, see
// docs/design/server/architecture/02-storage-architecture.md §Writes). Reads are
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
