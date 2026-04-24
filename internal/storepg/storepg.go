package storepg

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storeblobfs"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open opens a Postgres-backed store using dsn (a libpq-style URL or
// key=value string). blobDir controls where the filesystem blob store
// lives; if blobDir is empty, a default of "./data/blobs" is used. The
// pgxpool is configured with conservative bounds (max 16) and left to
// pgx's defaults for query timeout (the caller's ctx is the source of
// truth). Migrations are applied forward-only; schemas newer than the
// binary are rejected (REQ-OPS-100).
func Open(ctx context.Context, dsn string, blobDir string, logger *slog.Logger, c clock.Clock) (store.Store, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if c == nil {
		c = clock.NewReal()
	}
	if blobDir == "" {
		blobDir = "./data/blobs"
	}

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("storepg: parse dsn: %w", err)
	}
	if cfg.MaxConns < 2 {
		cfg.MaxConns = 16
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("storepg: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("storepg: ping: %w", err)
	}
	if err := applyMigrations(ctx, pool, logger); err != nil {
		pool.Close()
		return nil, err
	}
	blobs := storeblobfs.New(blobDir, c)
	s := &Store{
		pool:     pool,
		writerMu: &sync.Mutex{},
		logger:   logger,
		clock:    c,
		blobs:    blobs,
	}
	s.meta = &metadata{s: s}
	s.fts = &ftsStub{s: s}
	return s, nil
}

// applyMigrations creates schema_migrations and applies embedded SQL
// migrations in numeric order. A database whose latest applied version
// exceeds the binary's max-known is rejected with a clear error.
func applyMigrations(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) error {
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
		  version       INTEGER PRIMARY KEY,
		  applied_at_us BIGINT  NOT NULL
		)`); err != nil {
		return fmt.Errorf("storepg: create schema_migrations: %w", err)
	}

	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("storepg: read migrations: %w", err)
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
			return fmt.Errorf("storepg: read migration %s: %w", e.Name(), err)
		}
		available = append(available, mig{version: v, name: e.Name(), sql: body})
	}
	sort.Slice(available, func(i, j int) bool { return available[i].version < available[j].version })
	if len(available) == 0 {
		return errors.New("storepg: no embedded migrations found")
	}
	maxKnown := available[len(available)-1].version

	var latestApplied int
	if err := pool.QueryRow(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&latestApplied); err != nil {
		return fmt.Errorf("storepg: read latest migration: %w", err)
	}
	if latestApplied > maxKnown {
		return fmt.Errorf("storepg: database schema version %d is newer than this binary (max %d); downgrades are not supported", latestApplied, maxKnown)
	}

	applied := map[int]bool{}
	rows, err := pool.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("storepg: list applied migrations: %w", err)
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return fmt.Errorf("storepg: scan migration: %w", err)
		}
		applied[v] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("storepg: iterate migrations: %w", err)
	}

	for _, m := range available {
		if applied[m.version] {
			continue
		}
		logger.Debug("applying migration", "driver", "postgres", "version", m.version, "name", m.name)
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return fmt.Errorf("storepg: begin migration %d: %w", m.version, err)
		}
		if _, err := tx.Exec(ctx, string(m.sql)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("storepg: apply migration %d: %w", m.version, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations(version, applied_at_us) VALUES ($1, $2)`,
			m.version, time.Now().UnixMicro()); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("storepg: record migration %d: %w", m.version, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("storepg: commit migration %d: %w", m.version, err)
		}
	}
	return nil
}
