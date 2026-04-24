package fixtures

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// BackendFactory opens a fresh Store for one sub-test. The returned
// cleanup is registered via t.Cleanup by Run, so callers do not need to
// close it manually.
type BackendFactory func(t *testing.T) store.Store

// Backend describes a backend leg to run a scenario against.
type Backend struct {
	Name    string
	Factory BackendFactory
	// Skip is non-nil when the backend cannot run (e.g. Postgres with
	// no DSN). Run surfaces it via t.Skipf.
	Skip string
}

// Backends returns the backend legs for Phase-1 parameterisation. The
// SQLite leg is always available; the Postgres leg is populated when
// HEROLD_PG_DSN is exported and reports a Skip reason otherwise so
// Run can emit a single t.Skipf.
//
// Postgres reuses the same destructive-database contract as
// internal/storepg/storepg_test.go: callers MUST point HEROLD_PG_DSN
// at a throwaway database; the factory opens a fresh Store per sub-
// test and relies on unique-per-run identifier choices inside the
// scenarios to avoid cross-leg collisions. Re-using the same database
// across runs is supported only when the caller TRUNCATEs beforehand.
func Backends() []Backend {
	out := []Backend{{
		Name: "sqlite",
		Factory: func(t *testing.T) store.Store {
			t.Helper()
			dir := t.TempDir()
			s, err := storesqlite.Open(
				context.Background(),
				filepath.Join(dir, "meta.db"),
				nil,
				NewFakeClock(),
			)
			if err != nil {
				t.Fatalf("storesqlite.Open: %v", err)
			}
			return s
		},
	}}
	dsn := os.Getenv("HEROLD_PG_DSN")
	if dsn == "" {
		out = append(out, Backend{
			Name: "postgres",
			Skip: "HEROLD_PG_DSN not set; skipping Postgres backend leg",
		})
	} else if !strings.Contains(dsn, "postgres") {
		out = append(out, Backend{
			Name: "postgres",
			Skip: "HEROLD_PG_DSN does not look like a Postgres DSN",
		})
	} else {
		out = append(out, Backend{
			Name: "postgres",
			Factory: func(t *testing.T) store.Store {
				t.Helper()
				blobDir := t.TempDir()
				s, err := storepg.Open(
					context.Background(),
					dsn,
					filepath.Join(blobDir, "blobs"),
					nil,
					NewFakeClock(),
				)
				if err != nil {
					t.Fatalf("storepg.Open: %v", err)
				}
				// Best-effort truncation via the test-only hook so this
				// sub-test starts on empty tables. The interface is
				// exported only under a _test.go build tag in storepg,
				// so we only try it; when it fails (release build) we
				// fall through and rely on unique data + the caller's
				// promise that the DSN points at a throwaway DB.
				type truncator interface {
					TruncateAll(ctx context.Context) error
				}
				if tr, ok := s.(truncator); ok {
					if err := tr.TruncateAll(context.Background()); err != nil {
						t.Logf("postgres truncate (best-effort): %v", err)
					}
				}
				return s
			},
		})
	}
	return out
}

// Run invokes fn once per registered backend as a sub-test named after
// the backend. Sub-tests whose factory is nil (skipped backends) emit
// t.Skipf with the recorded reason.
func Run(t *testing.T, fn func(t *testing.T, newStore BackendFactory)) {
	t.Helper()
	for _, b := range Backends() {
		b := b
		t.Run(b.Name, func(t *testing.T) {
			if b.Skip != "" {
				t.Skipf("%s", b.Skip)
			}
			fn(t, b.Factory)
		})
	}
}

// withStore wraps newStore so the test closes the store on cleanup.
// The factory returns a ready Store; the wrapper hands its Close to
// t.Cleanup so sub-tests need not remember.
func withStore(t *testing.T, newStore BackendFactory) store.Store {
	t.Helper()
	s := newStore(t)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// Prepare is shorthand for newStore + withStore used inside the e2e
// test bodies so they do not repeat the cleanup registration.
func Prepare(t *testing.T, newStore BackendFactory) store.Store {
	t.Helper()
	return withStore(t, newStore)
}
