package storepg

import (
	"log/slog"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storeblobfs"
)

// Store is the Postgres implementation of store.Store. Writers take
// writerMu before opening a transaction; readers use the pool directly.
// The writer mutex is conservative insurance against surprising
// interleavings in multi-statement transactions that assume a consistent
// view across multiple reads; pgx gives us that already under REPEATABLE
// READ, but the Wave 1 surface uses READ COMMITTED for liveness and we
// serialise writers here to mirror the SQLite backend's single-writer
// discipline.
type Store struct {
	pool     *pgxpool.Pool
	writerMu *sync.Mutex
	logger   *slog.Logger
	clock    clock.Clock
	blobs    *storeblobfs.Store
	meta     *metadata
	fts      *ftsStub

	closeOnce sync.Once
}

// Meta returns the metadata repository.
func (s *Store) Meta() store.Metadata { return s.meta }

// Blobs returns the filesystem blob store.
func (s *Store) Blobs() store.Blobs { return s.blobs }

// FTS returns the FTS stub.
func (s *Store) FTS() store.FTS { return s.fts }

// Close releases the connection pool. Safe to call more than once.
func (s *Store) Close() error {
	s.closeOnce.Do(func() { s.pool.Close() })
	return nil
}
