package sqlitetest_test

import (
	"context"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite/sqlitetest"
)

// TestOpen_ReadyToUse exercises the happy path: the returned Store
// must be usable for a real metadata insert. This protects against a
// future refactor that breaks template-restore -- e.g. a migration
// that inserts caller-specific rows would taint the template.
func TestOpen_ReadyToUse(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	s := sqlitetest.Open(t, clk)
	pid, err := s.Meta().InsertPrincipal(context.Background(), store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@example.local",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	if pid.ID == 0 {
		t.Fatalf("InsertPrincipal returned zero ID")
	}
}

// TestOpen_TwoStoresIndependent confirms that two stores opened in
// the same test process are isolated -- a row written in one must
// not appear in the other.
func TestOpen_TwoStoresIndependent(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	a := sqlitetest.Open(t, clk)
	b := sqlitetest.Open(t, clk)
	_, err := a.Meta().InsertPrincipal(context.Background(), store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "only-in-a@example.local",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal(a): %v", err)
	}
	if _, err := b.Meta().GetPrincipalByEmail(context.Background(), "only-in-a@example.local"); err == nil {
		t.Fatalf("b.GetPrincipalByEmail: row leaked from a")
	}
}
