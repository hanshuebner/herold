package storesqlite

import (
	"database/sql"
	"log/slog"
	"sync"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storeblobfs"
)

// Store is the SQLite implementation of store.Store. It wires a single
// *sql.DB (serving both readers and writers), a writer mutex (SQLite is
// single-writer at the engine level, but the driver's BUSY retry
// cascades under contention; holding writerMu is cheap insurance), a
// filesystem blob store, and an FTS stub. Returned by Open.
type Store struct {
	db       *sql.DB
	writerMu *sync.Mutex
	logger   *slog.Logger
	clock    clock.Clock
	blobs    *storeblobfs.Store
	meta     *metadata
	fts      *ftsStub

	closeOnce sync.Once
	closeErr  error
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
func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		s.closeErr = s.db.Close()
	})
	return s.closeErr
}
