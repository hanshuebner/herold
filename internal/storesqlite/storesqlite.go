package storesqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io"
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
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storeblobfs"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Options carries optional operator-supplied SQLite pragma overrides.
// Zero values mean "use the built-in defaults hardcoded in buildDSN /
// applyPragmas".
type Options struct {
	// CacheSize overrides PRAGMA cache_size. Negative = KiB; positive =
	// pages; zero = leave the default (-65536 = 64 MiB).
	CacheSize int
	// WALAutocheckpoint overrides PRAGMA wal_autocheckpoint. Zero = leave
	// the SQLite default (1000 pages).
	WALAutocheckpoint int
	// BulkImport, when true, opens the database in a configuration tuned
	// for one-shot bulk inserts (the gmail importer is the canonical
	// caller). The effects:
	//
	//   - synchronous = OFF (skip the WAL fsync at each commit)
	//   - wal_autocheckpoint raised to 10000 pages so checkpoints don't
	//     stall the import loop every few seconds
	//   - cache_size raised to 256 MiB so the b-tree pages and
	//     change-feed pages stay hot
	//
	// Trade-off: synchronous=OFF means a host-OS crash or power loss
	// during the import may corrupt the database. Process-level crashes
	// (kill -9, panic) remain safe — SQLite still flushes when the
	// process exits cleanly. Operators MUST run a fresh-DB or
	// known-recoverable target when using this mode; the importer's
	// idempotency lets a re-run pick up where a crashed run left off,
	// which is the standard recovery path.
	BulkImport bool
}

// Open opens (or creates) a SQLite-backed store at path. The blob
// directory is a sibling of the DB file by default: <path>.blobs/. The
// modernc.org/sqlite driver is registered under "sqlite"; the DSN turns
// on WAL + NORMAL + FK + 30s busy timeout + 64 MiB cache via PRAGMA URI
// parameters. Migrations in internal/storesqlite/migrations are applied
// forward-only; a DB whose schema_migrations table already contains a
// version newer than the binary recognises is rejected (REQ-OPS-130 /
// REQ-OPS-100: no downgrades).
//
// Production callers route through this entrypoint, which uses
// crypto/rand for the IMAP UIDVALIDITY salt. Tests inject a
// deterministic source via OpenWithRand. The two-entrypoint shape
// keeps the public Open signature stable across the Wave-3 codebase.
func Open(ctx context.Context, path string, logger *slog.Logger, c clock.Clock) (store.Store, error) {
	return OpenWithOptions(ctx, path, logger, c, Options{}, nil)
}

// OpenWithOpts is Open with explicit pragma Options (operator knobs from
// sysconfig). Tests or callers that only need rand injection and do not
// care about pragma overrides should use Open or OpenWithRand.
func OpenWithOpts(ctx context.Context, path string, logger *slog.Logger, c clock.Clock, opts Options) (store.Store, error) {
	return OpenWithOptions(ctx, path, logger, c, opts, nil)
}

// OpenWithRand is Open with an explicit entropy source for the
// non-secret IMAP UIDVALIDITY salt. A nil rs falls back to
// crypto/rand.Reader. STANDARDS §8.5: tests inject a deterministic
// reader (e.g. bytes.NewReader of a fixed corpus) so UIDVALIDITY
// values are reproducible across runs.
func OpenWithRand(ctx context.Context, path string, logger *slog.Logger, c clock.Clock, rs io.Reader) (store.Store, error) {
	return OpenWithOptions(ctx, path, logger, c, Options{}, rs)
}

// OpenWithOptions is the canonical implementation shared by Open,
// OpenWithOpts, and OpenWithRand.
func OpenWithOptions(ctx context.Context, path string, logger *slog.Logger, c clock.Clock, opts Options, rs io.Reader) (store.Store, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if c == nil {
		c = clock.NewReal()
	}
	if rs == nil {
		rs = rand.Reader
	}

	dsn := buildDSN(path, opts)
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
	if err := applyPragmas(ctx, db, opts); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := applyMigrations(ctx, db, logger); err != nil {
		_ = db.Close()
		return nil, err
	}

	blobDir := filepath.Clean(path) + ".blobs"
	blobs := storeblobfs.New(blobDir, c)
	if opts.BulkImport {
		blobs.SetSkipFsync(true)
	}

	s := &Store{
		db:         db,
		writerMu:   &sync.Mutex{},
		logger:     logger,
		clock:      c,
		blobs:      blobs,
		randReader: rs,
		bulkImport: opts.BulkImport,
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
	logger.Info("storesqlite: backfilling TOTP flags", "activity", observe.ActivitySystem, "rows", len(todo))
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
//
// BulkImport mode replaces synchronous=NORMAL with synchronous=OFF and
// raises cache_size to 256 MiB; see Options.BulkImport for the
// rationale and the corruption trade-off.
func buildDSN(path string, opts Options) string {
	sync := "NORMAL"
	cacheKB := 65536
	if opts.BulkImport {
		sync = "OFF"
		cacheKB = 262144
	}
	q := []string{
		"_pragma=journal_mode(WAL)",
		"_pragma=synchronous(" + sync + ")",
		"_pragma=foreign_keys(ON)",
		"_pragma=busy_timeout(30000)",
		fmt.Sprintf("_pragma=cache_size(-%d)", cacheKB),
		"_pragma=temp_store(MEMORY)",
	}
	return "file:" + path + "?" + strings.Join(q, "&")
}

// applyPragmas double-enforces the PRAGMAs at the session level in case
// the URL parsing dropped one (some modernc versions ignore unknown
// keys silently). Read the resulting mode back to confirm WAL.
// opts overrides cache_size and wal_autocheckpoint when non-zero.
func applyPragmas(ctx context.Context, db *sql.DB, opts Options) error {
	cacheSize := -65536
	if opts.BulkImport {
		cacheSize = -262144
	}
	if opts.CacheSize != 0 {
		cacheSize = opts.CacheSize
	}
	sync := "NORMAL"
	if opts.BulkImport {
		sync = "OFF"
	}
	pragmas := []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = " + sync,
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 30000",
		fmt.Sprintf("PRAGMA cache_size = %d", cacheSize),
		"PRAGMA temp_store = MEMORY",
	}
	walAutocheckpoint := opts.WALAutocheckpoint
	if opts.BulkImport && walAutocheckpoint == 0 {
		// BulkImport disables autocheckpoint entirely so the import
		// loop is never stalled mid-batch by SQLite folding the WAL
		// into the main DB. Trade-off: the WAL grows monotonically
		// during the import (potentially several GB on a 13 GB
		// takeout) and is collapsed by an explicit
		// PRAGMA wal_checkpoint(TRUNCATE) on Store.Close.
		pragmas = append(pragmas, "PRAGMA wal_autocheckpoint = 0")
	} else if walAutocheckpoint != 0 {
		pragmas = append(pragmas, fmt.Sprintf("PRAGMA wal_autocheckpoint = %d", walAutocheckpoint))
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
		logger.Debug("applying migration", "activity", observe.ActivitySystem, "driver", "sqlite", "version", m.version, "name", m.name)
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
