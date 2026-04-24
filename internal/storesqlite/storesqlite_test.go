package storesqlite_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/store/storetest"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

func openStore(t *testing.T) (store.Store, func()) {
	t.Helper()
	dir := t.TempDir()
	s, err := storesqlite.Open(
		context.Background(),
		filepath.Join(dir, "meta.db"),
		nil,
		clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
	)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s, func() { _ = s.Close() }
}

func TestCompliance(t *testing.T) {
	storetest.Run(t, openStore)
}

func TestMigrationIdempotency(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.db")
	// First open — migrations apply.
	s1, err := storesqlite.Open(context.Background(), path, nil, clock.NewReal())
	if err != nil {
		t.Fatalf("Open #1: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close #1: %v", err)
	}
	// Second open — must be a no-op.
	storetest.RunMigrationIdempotency(t, func(t *testing.T) store.Store {
		s2, err := storesqlite.Open(context.Background(), path, nil, clock.NewReal())
		if err != nil {
			t.Fatalf("Open #2: %v", err)
		}
		return s2
	})
}

func TestRejectNewerSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.db")
	s, err := storesqlite.Open(context.Background(), path, nil, clock.NewReal())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = s.Close()

	// Forge a future migration version directly in the DB.
	injected, err := storesqlite.OpenRaw(path)
	if err != nil {
		t.Fatalf("OpenRaw: %v", err)
	}
	if _, err := injected.Exec(`INSERT INTO schema_migrations(version, applied_at_us) VALUES (9999, 0)`); err != nil {
		t.Fatalf("forge: %v", err)
	}
	_ = injected.Close()

	if _, err := storesqlite.Open(context.Background(), path, nil, clock.NewReal()); err == nil {
		t.Fatal("expected Open to reject newer schema, got nil")
	}
}
