package backup

import (
	"context"
	"database/sql"
	"fmt"
	"sync"

	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

func init() {
	RegisterAdapter(func(s store.Store) (Backend, bool) {
		ss, ok := s.(*storesqlite.Store)
		if !ok {
			return nil, false
		}
		return &sqliteBackend{
			db:    storesqlite.DBHandle(ss),
			wmu:   storesqlite.WriterMu(ss),
			blobs: ss.Blobs(),
		}, true
	})
}

// sqliteBackend implements Backend against a SQLite-backed store.
// Streams use the underlying *sql.DB directly so backup work does not
// queue behind metadata writers; restore takes the writer mutex so
// bulk inserts respect the single-writer discipline.
type sqliteBackend struct {
	db    *sql.DB
	wmu   *sync.Mutex
	blobs store.Blobs
}

func (b *sqliteBackend) Kind() string       { return "sqlite" }
func (b *sqliteBackend) Blobs() store.Blobs { return b.blobs }

func (b *sqliteBackend) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	if err := b.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v); err != nil {
		return 0, fmt.Errorf("sqlite: schema version: %w", err)
	}
	return v, nil
}

func (b *sqliteBackend) IsEmpty(ctx context.Context) (bool, error) {
	for _, t := range TableNames {
		var n int
		if err := b.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+t).Scan(&n); err != nil {
			return false, fmt.Errorf("sqlite: count %s: %w", t, err)
		}
		if n != 0 {
			return false, nil
		}
	}
	return true, nil
}

func (b *sqliteBackend) TruncateAll(ctx context.Context) error {
	b.wmu.Lock()
	defer b.wmu.Unlock()
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin truncate: %w", err)
	}
	defer tx.Rollback()
	// SQLite has no TRUNCATE; DELETE works the same with cascade
	// disabled (we issue them in reverse FK order). Reset
	// AUTOINCREMENT counters via sqlite_sequence so subsequent
	// inserts start at 1.
	for i := len(TableNames) - 1; i >= 0; i-- {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+TableNames[i]); err != nil {
			return fmt.Errorf("sqlite: delete %s: %w", TableNames[i], err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sqlite_sequence`); err != nil {
		// sqlite_sequence may not exist if no AUTOINCREMENT table was
		// ever populated; ignore.
		_ = err
	}
	return tx.Commit()
}

func (b *sqliteBackend) Snapshot(ctx context.Context) (Source, error) {
	// SQLite WAL gives readers a stable snapshot via BEGIN, but we
	// use BEGIN IMMEDIATE for consistency with the writer side and so
	// the snapshot tx fences out further writes from this connection.
	// A read-only DEFERRED transaction is sufficient for cross-table
	// consistency on WAL.
	tx, err := b.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("sqlite: begin snapshot: %w", err)
	}
	return &sqliteSource{tx: tx}, nil
}

func (b *sqliteBackend) Restore(ctx context.Context) (Sink, error) {
	b.wmu.Lock()
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		b.wmu.Unlock()
		return nil, fmt.Errorf("sqlite: begin restore: %w", err)
	}
	return &sqliteSink{tx: tx, wmu: b.wmu}, nil
}

// sqliteSource is the Source for sqliteBackend snapshots. It delegates
// row enumeration to the generic reflection engine in engine.go.
type sqliteSource struct {
	tx     *sql.Tx
	closed bool
}

func (s *sqliteSource) CountRows(ctx context.Context, table string) (int64, error) {
	var n int64
	if err := s.tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
		return 0, fmt.Errorf("sqlite count %s: %w", table, err)
	}
	return n, nil
}

// EnumerateRows delegates to the generic engine. All type quirks (bool↔int,
// nullable pointers, []byte nullability) are handled by engine.go driven by
// struct tags on the Row types in rows.go.
func (s *sqliteSource) EnumerateRows(ctx context.Context, table string, fn func(row any) error) error {
	if _, ok := tableReg[table]; !ok {
		return fmt.Errorf("sqlite: unknown table %q", table)
	}
	return genericEnumerate(ctx, s.tx, table, fn)
}

func (s *sqliteSource) EnumerateBlobHashes(ctx context.Context, fn func(hash string, size int64) error) error {
	// Collect the hashes referenced by message blobs and the queue
	// body / headers blobs. blob_refs may carry rows the messages /
	// queue tables no longer reference (refcount 0 awaiting GC); we
	// include them so a backup taken during the GC grace window is
	// faithful to disk.
	rs, err := s.tx.QueryContext(ctx, `SELECT hash, size FROM blob_refs ORDER BY hash`)
	if err != nil {
		return fmt.Errorf("sqlite: enumerate blobs: %w", err)
	}
	defer rs.Close()
	for rs.Next() {
		var h string
		var size int64
		if err := rs.Scan(&h, &size); err != nil {
			return err
		}
		if err := fn(h, size); err != nil {
			return err
		}
	}
	return rs.Err()
}

func (s *sqliteSource) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	return s.tx.Rollback()
}

// sqliteSink implements Sink against a sqlite writer transaction. Insert
// delegates to the generic engine in engine.go.
type sqliteSink struct {
	tx     *sql.Tx
	wmu    *sync.Mutex
	closed bool
}

// Insert delegates to the generic engine. All type quirks are handled by
// engine.go driven by struct tags on the Row types in rows.go.
func (s *sqliteSink) Insert(ctx context.Context, table string, row any) error {
	if _, ok := tableReg[table]; !ok {
		return fmt.Errorf("sqlite sink: unknown table %q", table)
	}
	return genericInsert(ctx, s.tx, table, row)
}

func (s *sqliteSink) Commit(ctx context.Context) error {
	if s.closed {
		return nil
	}
	s.closed = true
	defer s.wmu.Unlock()
	return s.tx.Commit()
}

func (s *sqliteSink) Rollback(ctx context.Context) error {
	if s.closed {
		return nil
	}
	s.closed = true
	defer s.wmu.Unlock()
	return s.tx.Rollback()
}
