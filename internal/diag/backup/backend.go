package backup

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/hanshuebner/herold/internal/store"
)

// Backend is the storage capability the backup, restore, and migrate
// packages drive. It abstracts the per-table row enumeration and
// row-insert paths so the same orchestration code works across
// SQLite, Postgres, and the in-memory fakestore. Each
// store.Store-implementing backend ships an adapter that returns one
// of these from BackendFor.
//
// Snapshot semantics: BeginSnapshot opens a read-only transaction
// (REPEATABLE READ on Postgres; BEGIN IMMEDIATE on SQLite WAL) so
// concurrent writes during a backup do not produce torn rows.
// CommitSnapshot / RollbackSnapshot release the snapshot.
//
// Restore semantics: BeginRestore opens a writer transaction. The
// caller emits InsertRow calls for every table (in FK-respecting
// order) then CommitRestore. ResetIdentities, when supported, bumps
// the backend's IDENTITY / AUTOINCREMENT counters to one past the
// largest restored ID so subsequent application writes don't collide.
// TruncateAll is the implementation behind ModeReplace.
type Backend interface {
	// Kind returns "sqlite", "postgres", or "fakestore". Recorded in
	// the manifest.
	Kind() string

	// SchemaVersion returns the highest applied migration version in
	// the underlying store. The orchestrator stamps it into the
	// manifest and refuses cross-backend migration when source and
	// target disagree.
	SchemaVersion(ctx context.Context) (int, error)

	// Snapshot opens a read-only snapshot transaction. The returned
	// Source enumerates rows for each TableNames entry. Close releases
	// the snapshot.
	Snapshot(ctx context.Context) (Source, error)

	// Restore opens a writer transaction. The returned Sink accepts
	// InsertRow calls per table; Commit / Rollback finalise.
	Restore(ctx context.Context) (Sink, error)

	// TruncateAll empties every backed-up table. Used by ModeReplace.
	// Must respect FK ordering (or use CASCADE).
	TruncateAll(ctx context.Context) error

	// IsEmpty reports whether every backed-up table has zero rows.
	// Used by ModeFresh to refuse restore into a non-empty target.
	IsEmpty(ctx context.Context) (bool, error)

	// Blobs returns the underlying store.Blobs. Carried on the
	// Backend so the orchestrator does not need to plumb the original
	// store through.
	Blobs() store.Blobs
}

// Source enumerates rows from one snapshot transaction. Rows arrive
// in the table's canonical order (typically primary-key ascending);
// callers stream them straight to JSONL without buffering whole
// tables into memory.
type Source interface {
	// CountRows returns the total row count for table. Used to size
	// progress bars.
	CountRows(ctx context.Context, table string) (int64, error)

	// EnumerateRows calls fn once per row in table. fn receives a
	// pointer to one of the typed Row structs from rows.go (the
	// concrete type matches the table name; see rowsForTable). fn
	// returning a non-nil error stops iteration with that error.
	EnumerateRows(ctx context.Context, table string, fn func(row any) error) error

	// EnumerateBlobHashes streams every (hash, size) pair the source
	// considers part of the backup. Implementations return blobs
	// referenced by the messages or queue tables.
	EnumerateBlobHashes(ctx context.Context, fn func(hash string, size int64) error) error

	// Close releases the snapshot. Idempotent.
	Close() error
}

// Sink writes rows into a target backend within one writer
// transaction. Insert is called once per row in FK-respecting order;
// callers bracket the whole restore with Commit/Rollback.
type Sink interface {
	// Insert writes one row to table. row's concrete type must match
	// the table (DomainRow for "domains", PrincipalRow for
	// "principals", ...).
	Insert(ctx context.Context, table string, row any) error

	// Commit finalises the transaction. Subsequent Insert calls
	// return an error.
	Commit(ctx context.Context) error

	// Rollback aborts the transaction. Idempotent; safe to call after
	// Commit.
	Rollback(ctx context.Context) error
}

// ErrUnsupported indicates a Backend cannot be derived from the
// provided store.Store (no registered adapter knows its concrete
// type). Callers surface this as an operator-friendly message.
var ErrUnsupported = errors.New("backup: store does not expose a backup capability")

// adapterFn turns a store.Store into a Backend or returns
// ErrUnsupported. Backends register one such adapter at init time so
// the diag packages stay decoupled from the concrete backend imports.
type adapterFn func(s store.Store) (Backend, bool)

var adapters []adapterFn

// RegisterAdapter installs an adapter resolver. Each backend's
// internal/diag/backup adapter file calls this from init().
func RegisterAdapter(fn adapterFn) {
	adapters = append(adapters, fn)
}

// BackendFor returns the Backend for s. Returns ErrUnsupported when
// no registered adapter recognises s.
func BackendFor(s store.Store) (Backend, error) {
	if s == nil {
		return nil, fmt.Errorf("%w: nil store", ErrUnsupported)
	}
	for _, ad := range adapters {
		if b, ok := ad(s); ok {
			return b, nil
		}
	}
	return nil, ErrUnsupported
}

// rowsForTable returns a freshly-allocated zero value of the row
// struct for table. The Source uses it to type-allocate during
// EnumerateRows; the Sink uses the type assertion in Insert. Returns
// (nil, false) for unknown tables.
func rowsForTable(table string) (any, bool) {
	switch table {
	case "domains":
		return &DomainRow{}, true
	case "principals":
		return &PrincipalRow{}, true
	case "oidc_providers":
		return &OIDCProviderRow{}, true
	case "oidc_links":
		return &OIDCLinkRow{}, true
	case "api_keys":
		return &APIKeyRow{}, true
	case "aliases":
		return &AliasRow{}, true
	case "sieve_scripts":
		return &SieveScriptRow{}, true
	case "mailboxes":
		return &MailboxRow{}, true
	case "messages":
		return &MessageRow{}, true
	case "mailbox_acl":
		return &MailboxACLRow{}, true
	case "state_changes":
		return &StateChangeRow{}, true
	case "audit_log":
		return &AuditLogRow{}, true
	case "cursors":
		return &CursorRow{}, true
	case "queue":
		return &QueueRow{}, true
	case "dkim_keys":
		return &DKIMKeyRow{}, true
	case "acme_accounts":
		return &ACMEAccountRow{}, true
	case "acme_orders":
		return &ACMEOrderRow{}, true
	case "acme_certs":
		return &ACMECertRow{}, true
	case "webhooks":
		return &WebhookRow{}, true
	case "dmarc_reports_raw":
		return &DMARCReportRow{}, true
	case "dmarc_rows":
		return &DMARCRowRow{}, true
	case "jmap_states":
		return &JMAPStateRow{}, true
	case "jmap_email_submissions":
		return &JMAPEmailSubmissionRow{}, true
	case "jmap_identities":
		return &JMAPIdentityRow{}, true
	case "tlsrpt_failures":
		return &TLSRPTFailureRow{}, true
	case "address_books":
		return &AddressBookRow{}, true
	case "contacts":
		return &ContactRow{}, true
	case "blob_refs":
		return &BlobRefRow{}, true
	}
	return nil, false
}

// closeWithErr closes c and returns its error preferring the
// caller-supplied existing error.
func closeWithErr(c io.Closer, prev error) error {
	cerr := c.Close()
	if prev != nil {
		return prev
	}
	return cerr
}
