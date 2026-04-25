package storesqlite

import (
	"database/sql"
	"sync"
)

// DBHandle returns the underlying *sql.DB for use by diagnostic
// tooling (internal/diag/backup, restore, migrate). The handle is the
// same connection pool the metadata layer drives, so callers MUST
// take WriterMu when issuing writes from outside the package to keep
// the single-writer discipline. Read-only callers may issue queries
// directly.
//
// Exposed exclusively so the backup/restore tooling can stream rows
// and perform raw inserts (id-preserving restore). Application code
// MUST NOT call this; STANDARDS §1 rule 3 (storage-centric) requires
// every state change to flow through Metadata.
func DBHandle(s *Store) *sql.DB {
	if s == nil {
		return nil
	}
	return s.db
}

// WriterMu returns the writer mutex for the same purpose as DBHandle.
// Diag tools take this lock when bulk-loading rows so they don't
// race normal application writers.
func WriterMu(s *Store) *sync.Mutex { return s.writerMu }
