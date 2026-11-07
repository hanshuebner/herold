// Package backends opens the store implementations a cross-package test
// runs against: always SQLite, plus Postgres when HEROLD_PG_DSN names a
// throwaway database (the Postgres seam truncates every table it
// touches, so the DSN must never point at data worth keeping).
package backends

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// Opener opens a fresh, empty store for one test.
type Opener func(t *testing.T) store.Store

// Backend names one store implementation and how to open it.
type Backend struct {
	Name string
	Open Opener
}

// All returns the SQLite backend and, when HEROLD_PG_DSN is set, the
// Postgres backend.
func All() []Backend {
	out := []Backend{{Name: "sqlite", Open: openSQLite}}
	if dsn := os.Getenv("HEROLD_PG_DSN"); dsn != "" {
		out = append(out, Backend{Name: "postgres", Open: func(t *testing.T) store.Store {
			return openPG(t, dsn)
		}})
	}
	return out
}

// Each runs fn as a subtest per available backend.
func Each(t *testing.T, fn func(t *testing.T, st store.Store)) {
	t.Helper()
	for _, b := range All() {
		t.Run(b.Name, func(t *testing.T) {
			fn(t, b.Open(t))
		})
	}
}

func testClock() clock.Clock {
	return clock.NewFake(time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC))
}

func openSQLite(t *testing.T) store.Store {
	t.Helper()
	st, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), nil, testClock())
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func openPG(t *testing.T, dsn string) store.Store {
	t.Helper()
	st, err := storepg.Open(context.Background(), dsn, filepath.Join(t.TempDir(), "blobs"), nil, testClock())
	if err != nil {
		t.Fatalf("storepg.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if tr, ok := st.(interface {
		TruncateAll(ctx context.Context) error
	}); ok {
		if err := tr.TruncateAll(context.Background()); err != nil {
			t.Fatalf("storepg TruncateAll: %v", err)
		}
	}
	return st
}
