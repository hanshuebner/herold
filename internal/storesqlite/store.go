package storesqlite

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storeblobfs"
)

// Store is the SQLite implementation of store.Store. It wires a single
// *sql.DB (serving both readers and writers), a writer mutex (SQLite is
// single-writer at the engine level, but the driver's BUSY retry
// cascades under contention; holding writerMu is cheap insurance), a
// filesystem blob store, and an FTS stub. Returned by Open.
//
// randReader is the entropy source used for non-secret-but-opaque
// IMAP UIDVALIDITY low-byte salting. Production paths get
// crypto/rand.Reader; tests inject a deterministic reader (via the
// rs argument to Open) so UIDVALIDITY values are reproducible across
// runs.
type Store struct {
	db         *sql.DB
	writerMu   *sync.Mutex
	logger     *slog.Logger
	clock      clock.Clock
	blobs      *storeblobfs.Store
	meta       *metadata
	fts        *ftsStub
	randReader io.Reader
	bulkImport bool // when true, Close runs PRAGMA wal_checkpoint(TRUNCATE) before closing the DB

	// internalizeNotify is called (non-blocking, best-effort) after a
	// row commits with internalize_pending = 1. The internalize-worker
	// (REQ-EXTIMG-BG-08) registers its Notify here at startup so a
	// freshly-imported message wakes the drain loop without a polling
	// delay. Nil during normal test setup; set via
	// SetInternalizeNotifyHook.
	internalizeNotifyMu sync.RWMutex
	internalizeNotify   func()

	closeOnce sync.Once
	closeErr  error
}

// SetInternalizeNotifyHook registers fn as the wake-up callback for
// the extimg-internalize-worker. Mark / Insert paths call fn after
// committing a row with internalize_pending = 1. Pass nil to unregister
// (used at shutdown so the worker's channel is not held alive by the
// store).
func (s *Store) SetInternalizeNotifyHook(fn func()) {
	s.internalizeNotifyMu.Lock()
	s.internalizeNotify = fn
	s.internalizeNotifyMu.Unlock()
}

// fireInternalizeNotify invokes the registered hook with a snapshot of
// the current pointer so concurrent SetInternalizeNotifyHook callers do
// not race the wake-up.
func (s *Store) fireInternalizeNotify() {
	s.internalizeNotifyMu.RLock()
	fn := s.internalizeNotify
	s.internalizeNotifyMu.RUnlock()
	if fn != nil {
		fn()
	}
}

// Meta returns the metadata repository.
func (s *Store) Meta() store.Metadata { return s.meta }

// Blobs returns the filesystem blob store. Wrapped in an adapter so the
// concrete *storeblobfs.Store is not exposed through the store.Blobs
// interface (callers do not reach into blobs.GC from the store
// surface).
func (s *Store) Blobs() store.Blobs { return s.blobs }

// FTS returns the FTS stub. Production FTS ships as internal/storefts.
func (s *Store) FTS() store.FTS { return s.fts }

// Close releases DB and blob resources. Safe to call multiple times;
// only the first call does work.
//
// In BulkImport mode the autocheckpoint pragma is disabled so the WAL
// can grow monotonically during the import; Close runs an explicit
// PRAGMA wal_checkpoint(TRUNCATE) before closing so the next process
// to open the file does not have to walk a multi-GB WAL.
func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		if s.bulkImport && s.db != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			if _, err := s.db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
				if s.logger != nil {
					s.logger.Warn("storesqlite: final wal_checkpoint failed",
						"err", err.Error())
				}
			}
		}
		s.closeErr = s.db.Close()
	})
	return s.closeErr
}
