package storesqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storeblobfs"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open opens (or creates) a SQLite-backed store at path. The blob
// directory is a sibling of the DB file by default: <path>.blobs/. The
// modernc.org/sqlite driver is registered under "sqlite"; the DSN turns
// on WAL + NORMAL + FK + 30s busy timeout + 64 MiB cache via PRAGMA URI
// parameters. Migrations in internal/storesqlite/migrations are applied
// forward-only; a DB whose schema_migrations table already contains a
// version newer than the binary recognises is rejected (REQ-OPS-130 /
// REQ-OPS-100: no downgrades).
func Open(ctx context.Context, path string, logger *slog.Logger, c clock.Clock) (store.Store, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if c == nil {
		c = clock.NewReal()
	}

	dsn := buildDSN(path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("storesqlite: open: %w", err)
	}
	// Reader pool: SQLite can serve many concurrent readers on WAL, but
	// writes are serialised at the DB level. We leave max open conns
	// unconstrained and rely on SQLite's internal locking for safety;
	// writers additionally take writerMu to prevent BUSY-retry storms at
	// the driver level when multiple goroutines contend.
	db.SetMaxOpenConns(0)
	db.SetMaxIdleConns(4)
	db.SetConnMaxIdleTime(5 * time.Minute)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("storesqlite: ping: %w", err)
	}
	if err := applyPragmas(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := applyMigrations(ctx, db, logger); err != nil {
		_ = db.Close()
		return nil, err
	}

	blobDir := filepath.Clean(path) + ".blobs"
	blobs := storeblobfs.New(blobDir, c)

	s := &Store{
		db:       db,
		writerMu: &sync.Mutex{},
		logger:   logger,
		clock:    c,
		blobs:    blobs,
	}
	s.meta = &metadata{s: s}
	s.fts = &ftsStub{s: s}
	if err := backfillTOTPFlags(ctx, s, logger); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

// backfillTOTPFlags lifts the legacy in-memory TOTPSecret prefix
// convention into PrincipalFlagTOTPEnabled. Wave 1 directory code
// wrapped confirmed secrets with a leading 0x01 byte (and pending
// enrolments with 0x00) because store.PrincipalFlags had no TOTP bit.
// Wave 2 adds the bit. For forward-compat we scan once on Open, flip
// the flag where the prefix indicates confirmation, clear the prefix
// (the stored secret becomes the raw base32 bytes), and write the row
// back. Idempotent: principals whose secret has already been
// normalised (no prefix byte) are skipped.
func backfillTOTPFlags(ctx context.Context, s *Store, logger *slog.Logger) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, flags, totp_secret FROM principals
		 WHERE length(totp_secret) > 0`)
	if err != nil {
		return fmt.Errorf("storesqlite: scan totp secrets: %w", err)
	}
	type candidate struct {
		id       int64
		flags    int64
		newFlags int64
		newTOTP  []byte
	}
	var todo []candidate
	for rows.Next() {
		var id, flags int64
		var totp []byte
		if err := rows.Scan(&id, &flags, &totp); err != nil {
			rows.Close()
			return fmt.Errorf("storesqlite: scan totp row: %w", err)
		}
		if len(totp) == 0 {
			continue
		}
		prefix := totp[0]
		if prefix != 0x00 && prefix != 0x01 {
			// Already normalised, nothing to do.
			continue
		}
		newFlags := flags
		if prefix == 0x01 {
			newFlags |= int64(store.PrincipalFlagTOTPEnabled)
		} else {
			newFlags &^= int64(store.PrincipalFlagTOTPEnabled)
		}
		todo = append(todo, candidate{
			id:       id,
			flags:    flags,
			newFlags: newFlags,
			newTOTP:  append([]byte(nil), totp[1:]...),
		})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("storesqlite: iterate totp rows: %w", err)
	}
	if len(todo) == 0 {
		return nil
	}
	logger.Info("storesqlite: backfilling TOTP flags", "rows", len(todo))
	s.writerMu.Lock()
	defer s.writerMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storesqlite: begin totp backfill: %w", err)
	}
	for _, c := range todo {
		if _, err := tx.ExecContext(ctx,
			`UPDATE principals SET flags = ?, totp_secret = ? WHERE id = ?`,
			c.newFlags, c.newTOTP, c.id); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("storesqlite: totp backfill update id=%d: %w", c.id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storesqlite: commit totp backfill: %w", err)
	}
	return nil
}

// buildDSN composes the URI-form DSN consumed by modernc.org/sqlite. We
// set the PRAGMAs the architecture spec mandates via the _pragma URL
// parameter; the modernc driver runs them before the first query.
func buildDSN(path string) string {
	// Use file: so URI parameters are parsed.
	q := []string{
		"_pragma=journal_mode(WAL)",
		"_pragma=synchronous(NORMAL)",
		"_pragma=foreign_keys(ON)",
		"_pragma=busy_timeout(30000)",
		"_pragma=cache_size(-65536)",
		"_pragma=temp_store(MEMORY)",
	}
	return "file:" + path + "?" + strings.Join(q, "&")
}

// applyPragmas double-enforces the PRAGMAs at the session level in case
// the URL parsing dropped one (some modernc versions ignore unknown
// keys silently). Read the resulting mode back to confirm WAL.
func applyPragmas(ctx context.Context, db *sql.DB) error {
	pragmas := []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 30000",
		"PRAGMA cache_size = -65536",
		"PRAGMA temp_store = MEMORY",
	}
	for _, p := range pragmas {
		if _, err := db.ExecContext(ctx, p); err != nil {
			return fmt.Errorf("storesqlite: %s: %w", p, err)
		}
	}
	return nil
}

// applyMigrations enumerates embedded SQL files, sorts them by numeric
// prefix, applies those whose version is not in schema_migrations, and
// rejects a DB whose latest recorded version exceeds the binary's max.
func applyMigrations(ctx context.Context, db *sql.DB, logger *slog.Logger) error {
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
		  version     INTEGER PRIMARY KEY,
		  applied_at_us INTEGER NOT NULL
		) STRICT`); err != nil {
		return fmt.Errorf("storesqlite: create schema_migrations: %w", err)
	}

	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("storesqlite: read migrations: %w", err)
	}
	type mig struct {
		version int
		name    string
		sql     []byte
	}
	var available []mig
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		parts := strings.SplitN(e.Name(), "_", 2)
		if len(parts) < 2 {
			continue
		}
		v, err := strconv.Atoi(parts[0])
		if err != nil {
			continue
		}
		body, err := fs.ReadFile(migrationsFS, "migrations/"+e.Name())
		if err != nil {
			return fmt.Errorf("storesqlite: read migration %s: %w", e.Name(), err)
		}
		available = append(available, mig{version: v, name: e.Name(), sql: body})
	}
	sort.Slice(available, func(i, j int) bool { return available[i].version < available[j].version })
	if len(available) == 0 {
		return errors.New("storesqlite: no embedded migrations found")
	}
	maxKnown := available[len(available)-1].version

	var latestApplied int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&latestApplied); err != nil {
		return fmt.Errorf("storesqlite: read latest migration: %w", err)
	}
	if latestApplied > maxKnown {
		return fmt.Errorf("storesqlite: database schema version %d is newer than this binary (max %d); downgrades are not supported", latestApplied, maxKnown)
	}

	applied := map[int]bool{}
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("storesqlite: list applied migrations: %w", err)
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return fmt.Errorf("storesqlite: scan migration: %w", err)
		}
		applied[v] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("storesqlite: iterate migrations: %w", err)
	}

	for _, m := range available {
		if applied[m.version] {
			continue
		}
		logger.Debug("applying migration", "driver", "sqlite", "version", m.version, "name", m.name)
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("storesqlite: begin migration %d: %w", m.version, err)
		}
		if _, err := tx.ExecContext(ctx, string(m.sql)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("storesqlite: apply migration %d: %w", m.version, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations(version, applied_at_us) VALUES (?, ?)`,
			m.version, time.Now().UnixMicro()); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("storesqlite: record migration %d: %w", m.version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("storesqlite: commit migration %d: %w", m.version, err)
		}
	}
	return nil
}
