package identityverify

// sweeper_compliance.go exposes a small helper used by both backends
// (sqlite + postgres) to assert the GC sweep semantics against a
// freshly-opened store. The helper is intentionally not in _test.go
// so backend test packages that live in foo_test packages can import
// it from the production module path. Production callers MUST NOT
// invoke RunSweeperCompliance; it allocates fixtures and is gated
// behind a *testing.T to make accidental misuse a compile error.

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
)

// SweeperComplianceFactory hands out a freshly-opened store and the
// fake clock the store was constructed with. Each call creates a new
// per-test store so the assertions inside RunSweeperCompliance start
// from a clean slate.
type SweeperComplianceFactory func(t *testing.T) (s store.Store, clk *clock.FakeClock, cleanup func())

// RunSweeperCompliance exercises the sweeper against a backend factory.
// The matrix is the same as in sweeper_test.go but reduced to the
// portable subset (Postgres test workflow does not let us call
// internal sqlite helpers directly).
func RunSweeperCompliance(t *testing.T, f SweeperComplianceFactory) {
	t.Helper()
	t.Run("ClearsExpiredTokensKeepsRow", func(t *testing.T) {
		s, clk, cleanup := f(t)
		defer cleanup()
		runClearsExpiredTokensKeepsRow(t, s, clk)
	})
	t.Run("PurgesStaleUnverifiedRows", func(t *testing.T) {
		s, clk, cleanup := f(t)
		defer cleanup()
		runPurgesStaleUnverifiedRows(t, s, clk)
	})
	t.Run("LeavesVerifiedRowsAlone", func(t *testing.T) {
		s, clk, cleanup := f(t)
		defer cleanup()
		runLeavesVerifiedRowsAlone(t, s, clk)
	})
}

func newComplianceSweeper(s store.Store, clk *clock.FakeClock, aud Auditor, purge time.Duration) *Sweeper {
	return NewSweeper(SweeperOptions{
		Store:                s,
		Logger:               slog.New(slog.NewTextHandler(io.Discard, nil)),
		Clock:                clk,
		Auditor:              aud,
		Interval:             time.Hour,
		UnverifiedPurgeAfter: purge,
	})
}

func runClearsExpiredTokensKeepsRow(t *testing.T, s store.Store, clk *clock.FakeClock) {
	t.Helper()
	ctx := context.Background()
	p, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "sweeperA@example.com", DisplayName: "X",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	id := "sw-token-" + t.Name()
	if err := s.Meta().InsertJMAPIdentity(ctx, store.JMAPIdentity{
		ID: id, PrincipalID: p.ID, Email: "a@external.test", MayDelete: true,
		CreatedAtUs: clk.Now().UnixMicro(),
	}); err != nil {
		t.Fatalf("InsertJMAPIdentity: %v", err)
	}
	tok := sha256.Sum256([]byte("tok-" + id))
	code := sha256.Sum256([]byte("code-" + id))
	if err := s.Meta().IssueIdentityVerificationToken(ctx, id, tok[:], code[:],
		clk.Now().Add(-time.Second).UnixMicro()); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	sw := newComplianceSweeper(s, clk, nil, 7*24*time.Hour)
	if err := sw.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	got, err := s.Meta().GetJMAPIdentity(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.VerificationTokenHash != nil || got.VerificationCodeHash != nil ||
		got.VerificationTokenExpiresAtUs != 0 {
		t.Fatalf("trio not cleared: %+v", got)
	}
}

func runPurgesStaleUnverifiedRows(t *testing.T, s store.Store, clk *clock.FakeClock) {
	t.Helper()
	ctx := context.Background()
	p, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "sweeperB@example.com", DisplayName: "X",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	now := clk.Now()
	stale := "sw-stale-" + t.Name()
	fresh := "sw-fresh-" + t.Name()
	if err := s.Meta().InsertJMAPIdentity(ctx, store.JMAPIdentity{
		ID: stale, PrincipalID: p.ID, Email: "stale@external.test", MayDelete: true,
		CreatedAtUs: now.Add(-8 * 24 * time.Hour).UnixMicro(),
	}); err != nil {
		t.Fatalf("InsertJMAPIdentity stale: %v", err)
	}
	if err := s.Meta().InsertJMAPIdentity(ctx, store.JMAPIdentity{
		ID: fresh, PrincipalID: p.ID, Email: "fresh@external.test", MayDelete: true,
		CreatedAtUs: now.Add(-1 * 24 * time.Hour).UnixMicro(),
	}); err != nil {
		t.Fatalf("InsertJMAPIdentity fresh: %v", err)
	}
	sw := newComplianceSweeper(s, clk, nil, 7*24*time.Hour)
	if err := sw.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if _, err := s.Meta().GetJMAPIdentity(ctx, stale); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("stale not purged: err = %v", err)
	}
	if _, err := s.Meta().GetJMAPIdentity(ctx, fresh); err != nil {
		t.Fatalf("fresh destroyed: %v", err)
	}
}

func runLeavesVerifiedRowsAlone(t *testing.T, s store.Store, clk *clock.FakeClock) {
	t.Helper()
	ctx := context.Background()
	p, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "sweeperC@example.com", DisplayName: "X",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	id := "sw-verified-" + t.Name()
	if err := s.Meta().InsertJMAPIdentity(ctx, store.JMAPIdentity{
		ID: id, PrincipalID: p.ID, Email: "c@external.test", MayDelete: true,
		CreatedAtUs: clk.Now().Add(-30 * 24 * time.Hour).UnixMicro(),
	}); err != nil {
		t.Fatalf("InsertJMAPIdentity: %v", err)
	}
	tok := sha256.Sum256([]byte("v-tok"))
	code := sha256.Sum256([]byte("v-code"))
	if err := s.Meta().IssueIdentityVerificationToken(ctx, id, tok[:], code[:],
		clk.Now().Add(24*time.Hour).UnixMicro()); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if err := s.Meta().MarkIdentityVerified(ctx, id); err != nil {
		t.Fatalf("Mark: %v", err)
	}
	sw := newComplianceSweeper(s, clk, nil, 7*24*time.Hour)
	if err := sw.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	got, err := s.Meta().GetJMAPIdentity(ctx, id)
	if err != nil {
		t.Fatalf("verified row destroyed: %v", err)
	}
	if got.VerifiedAtUs == 0 {
		t.Fatalf("verified row lost VerifiedAtUs")
	}
}
