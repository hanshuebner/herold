// Package sqlitetest provides test-only helpers that open a herold
// SQLite store without paying the per-test migration cost.
//
// storesqlite.Open applies the full migration chain on every call (~45 ms
// without the race detector, ~1.6 s with it on an Apple M3). Tests that
// open hundreds of fresh stores per process amplify that cost into
// minutes of CPU under -race -- protoadmin alone opened ~160 stores per
// run before this package existed, costing ~5 wall-minutes of the CI
// sqlite lane.
//
// The helpers in this package amortise the cost: on first call per
// process they build one fully-migrated template DB in a private temp
// dir, then each subsequent call copies that template file to the
// caller-supplied path and opens it. Per-test isolation is unchanged
// (separate DB file, separate connection pool, separate blob dir);
// migrations only run once.
//
// Drop-in usage:
//
//	// before:
//	fs, err := storesqlite.Open(ctx, filepath.Join(t.TempDir(), "store.db"), nil, clk)
//
//	// after:
//	fs, err := sqlitetest.Open(t, clk)
//
// The Store returned by these helpers is identical to a freshly-opened
// store: the schema_migrations table reports the same versions, all
// indices are present, and PRAGMAs are applied at session level on
// every Open. The only observable difference is that no migration SQL
// runs during the test's Open call.
package sqlitetest

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

var (
	templateOnce sync.Once
	templatePath string
	templateErr  error
)

// templateFile returns the path to the per-process migrated template
// DB, building it on first call. The returned path points at a closed,
// checkpointed SQLite file with all embedded migrations applied. The
// file lives under os.MkdirTemp and is reaped when the test process
// exits; we never delete it explicitly because subsequent tests in the
// same process expect it to remain readable for copy().
func templateFile() (string, error) {
	templateOnce.Do(func() {
		dir, err := os.MkdirTemp("", "herold-storesqlite-template-*")
		if err != nil {
			templateErr = fmt.Errorf("sqlitetest: mkdir template: %w", err)
			return
		}
		p := filepath.Join(dir, "template.db")
		// Use a stable anchor clock so any clock-dependent column
		// default in a migration is reproducible. Tests pass their
		// own clock to Open below; the clock used to build the
		// template only affects values written by the migration
		// step (currently none).
		clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
		s, err := storesqlite.Open(context.Background(), p, nil, clk)
		if err != nil {
			templateErr = fmt.Errorf("sqlitetest: build template: %w", err)
			return
		}
		// Close cleanly so WAL is checkpointed into the main DB
		// file. Without this the copy step would have to also copy
		// the -wal and -shm files, which sqlite recreates anyway on
		// the next open.
		if err := s.Close(); err != nil {
			templateErr = fmt.Errorf("sqlitetest: close template: %w", err)
			return
		}
		templatePath = p
	})
	return templatePath, templateErr
}

// copyFile replaces dst with a byte-for-byte copy of src. It is used
// to materialise the per-test DB from the cached template.
func copyFile(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Open returns a fresh store.Store backed by a SQLite DB at
// t.TempDir()/store.db, pre-populated from the per-process migration
// template. It is a drop-in replacement for:
//
//	storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), nil, clk)
//
// Failures fatal the test via tb.Fatalf -- callers do not have to
// handle errors. The store is registered with t.Cleanup so it is
// closed when the test ends; callers do not have to Close it
// explicitly (but may, and a double-close is harmless).
func Open(tb testing.TB, clk clock.Clock) store.Store {
	tb.Helper()
	return OpenAt(tb, filepath.Join(tb.TempDir(), "store.db"), clk)
}

// OpenAt is like Open but writes the per-test DB at a caller-chosen
// path. Use this when the test needs a stable absolute path (e.g. to
// inspect the file from another process) or when t.TempDir() is not
// suitable.
func OpenAt(tb testing.TB, path string, clk clock.Clock) store.Store {
	tb.Helper()
	if clk == nil {
		tb.Fatalf("sqlitetest: Open: clk must not be nil")
	}
	tpl, err := templateFile()
	if err != nil {
		tb.Fatalf("sqlitetest: %v", err)
	}
	if err := copyFile(path, tpl); err != nil {
		tb.Fatalf("sqlitetest: copy template to %s: %v", path, err)
	}
	s, err := storesqlite.Open(context.Background(), path, nil, clk)
	if err != nil {
		tb.Fatalf("sqlitetest: storesqlite.Open: %v", err)
	}
	tb.Cleanup(func() {
		// Best-effort close; tests that already closed the store
		// will see an "already closed" error which is fine to
		// swallow.
		_ = s.Close()
	})
	return s
}

// OpenWithRand is like Open but injects an explicit entropy source for
// the IMAP UIDVALIDITY salt (storesqlite.OpenWithRand). Tests that
// care about deterministic UIDVALIDITY values across runs use this.
func OpenWithRand(tb testing.TB, clk clock.Clock, rs io.Reader) store.Store {
	tb.Helper()
	if clk == nil {
		tb.Fatalf("sqlitetest: OpenWithRand: clk must not be nil")
	}
	if rs == nil {
		tb.Fatalf("sqlitetest: OpenWithRand: rs must not be nil")
	}
	tpl, err := templateFile()
	if err != nil {
		tb.Fatalf("sqlitetest: %v", err)
	}
	path := filepath.Join(tb.TempDir(), "store.db")
	if err := copyFile(path, tpl); err != nil {
		tb.Fatalf("sqlitetest: copy template: %v", err)
	}
	s, err := storesqlite.OpenWithRand(context.Background(), path, nil, clk, rs)
	if err != nil {
		tb.Fatalf("sqlitetest: storesqlite.OpenWithRand: %v", err)
	}
	tb.Cleanup(func() { _ = s.Close() })
	return s
}
