package storepg

import (
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PoolHandle returns the underlying pgxpool.Pool for use by
// diagnostic tooling (internal/diag/backup, restore, migrate). The
// pool is the same one the metadata layer drives; concurrent readers
// from outside the package are safe but writers MUST take WriterMu
// to preserve the single-writer discipline.
//
// Exposed exclusively so the backup/restore tooling can stream rows
// and perform raw inserts (id-preserving restore). Application code
// MUST NOT call this; STANDARDS §1 rule 3 (storage-centric) requires
// every state change to flow through Metadata.
func PoolHandle(s *Store) *pgxpool.Pool {
	if s == nil {
		return nil
	}
	return s.pool
}

// WriterMu returns the writer mutex for the same purpose as
// PoolHandle. Diag tools take this lock when bulk-loading rows so
// they don't race normal application writers.
func WriterMu(s *Store) *sync.Mutex { return s.writerMu }
