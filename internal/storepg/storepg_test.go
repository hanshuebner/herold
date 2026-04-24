package storepg_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/store/storetest"
	"github.com/hanshuebner/herold/internal/storepg"
)

// getDSN returns a Postgres DSN for running integration tests, and a
// bool indicating whether tests should run. The CI matrix job sets
// HEROLD_PG_DSN (preferred). HEROLD_PG_TESTS=1 with testcontainers-go
// on the host is the alternate path; we keep the gate simple (DSN
// only) to avoid dragging docker in by default and skip cleanly when
// neither is set.
func getDSN(t *testing.T) (string, bool) {
	t.Helper()
	if dsn := os.Getenv("HEROLD_PG_DSN"); dsn != "" {
		return dsn, true
	}
	if testing.Short() {
		return "", false
	}
	return "", false
}

func openStore(t *testing.T, dsn string) (store.Store, func()) {
	t.Helper()
	// Use a unique database-schema prefix per test by scoping via a
	// dedicated database if the DSN's host is controllable; otherwise
	// reset per-test state by wiping known tables in a rollback-only
	// tx. Simplest portable approach: create a temporary schema and
	// set search_path on the pgxpool via the DSN's search_path option.
	// For Wave 1 we accept a destructive test DB: callers MUST point
	// HEROLD_PG_DSN at a throwaway database. We DROP+CREATE the public
	// schema tables (only the ones we manage) before each test by
	// issuing a TRUNCATE ... RESTART IDENTITY CASCADE.
	blobDir := t.TempDir()
	s, err := storepg.Open(
		context.Background(),
		dsn,
		filepath.Join(blobDir, "blobs"),
		nil,
		clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
	)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := truncateTables(s); err != nil {
		_ = s.Close()
		t.Fatalf("truncate: %v", err)
	}
	return s, func() { _ = s.Close() }
}

// truncateTables wipes row state between tests while leaving the
// schema (and therefore the schema_migrations table) intact. Only
// known tables are touched.
func truncateTables(s store.Store) error {
	// We need raw access to exec a TRUNCATE; storepg does not expose
	// its pool publicly, so we issue a no-op writing method via the
	// Metadata surface. For the harness we instead use an exposed
	// TruncateAll test helper.
	tr, ok := s.(interface {
		TruncateAll(ctx context.Context) error
	})
	if !ok {
		return nil
	}
	return tr.TruncateAll(context.Background())
}

func TestCompliance(t *testing.T) {
	dsn, ok := getDSN(t)
	if !ok {
		t.Skip("HEROLD_PG_DSN not set; skipping Postgres integration tests (set HEROLD_PG_DSN or pass -run with a provided DB)")
	}
	if !strings.Contains(dsn, "postgres") {
		t.Skipf("HEROLD_PG_DSN=%q does not look like a Postgres DSN", dsn)
	}
	storetest.Run(t, func(t *testing.T) (store.Store, func()) {
		return openStore(t, dsn)
	})
}

func TestMigrationIdempotency(t *testing.T) {
	dsn, ok := getDSN(t)
	if !ok {
		t.Skip("HEROLD_PG_DSN not set")
	}
	// First open runs migrations; close; second open is a no-op.
	s1, err := storepg.Open(context.Background(), dsn, t.TempDir(), nil, clock.NewReal())
	if err != nil {
		t.Fatalf("Open #1: %v", err)
	}
	_ = s1.Close()
	storetest.RunMigrationIdempotency(t, func(t *testing.T) store.Store {
		s, err := storepg.Open(context.Background(), dsn, t.TempDir(), nil, clock.NewReal())
		if err != nil {
			t.Fatalf("Open #2: %v", err)
		}
		return s
	})
}
