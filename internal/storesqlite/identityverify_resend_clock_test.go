package storesqlite_test

// identityverify_resend_clock_test.go exercises the 24h window-reset
// arm of the resend rate-limit gate (REQ-IDENT-36 component (b)) using
// a FakeClock the test owns directly. The storetest compliance suite
// runs against a clock pinned at a fixed instant by the backend factory
// and therefore cannot drive this case; we bypass the factory here and
// open a store with our own clock.

import (
	"context"
	"crypto/sha256"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

func vh32Local(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

func openWithFake(t *testing.T) (store.Store, *clock.FakeClock, func()) {
	t.Helper()
	start := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	fc := clock.NewFake(start)
	dir := t.TempDir()
	s, err := storesqlite.Open(context.Background(),
		filepath.Join(dir, "meta.db"), nil, fc)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	cleanup := func() { _ = s.Close() }
	return s, fc, cleanup
}

// TestRotate_WindowResetsAfter24h confirms that once
// verify_window_started_at_us is more than 24h behind the store's
// clock, the next Rotate resets window_count to 1 even if the daily
// cap was previously hit.
func TestRotate_WindowResetsAfter24h(t *testing.T) {
	s, fc, cleanup := openWithFake(t)
	defer cleanup()
	ctx := context.Background()

	p, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "alice@example.com",
		DisplayName: "Alice",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	id := "iv-window"
	if err := s.Meta().InsertJMAPIdentity(ctx, store.JMAPIdentity{
		ID: id, PrincipalID: p.ID, Email: "alice@external.test", MayDelete: true,
	}); err != nil {
		t.Fatalf("InsertJMAPIdentity: %v", err)
	}

	exp := fc.Now().Add(24 * time.Hour).UnixMicro()
	if err := s.Meta().IssueIdentityVerificationToken(ctx, id,
		vh32Local("token-1"), vh32Local("100001"), exp); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	stats0, _ := s.Meta().GetVerificationResendStats(ctx, id)
	if stats0.WindowCount != 1 {
		t.Fatalf("post-Issue window_count = %d, want 1", stats0.WindowCount)
	}

	// Burn the cap inside the same window. Cooldown=0 to isolate the
	// daily-cap arm. cap=2 so one extra Rotate fits, the second
	// triggers ErrRateLimited.
	fc.Advance(10 * time.Minute)
	if err := s.Meta().RotateIdentityVerificationToken(ctx, id,
		vh32Local("token-2"), vh32Local("100002"), exp+1, 0, 2); err != nil {
		t.Fatalf("Rotate #1: %v", err)
	}
	stats1, _ := s.Meta().GetVerificationResendStats(ctx, id)
	if stats1.WindowCount != 2 {
		t.Fatalf("after Rotate #1: window_count = %d, want 2", stats1.WindowCount)
	}

	// Rotate that would push count to 3 must reject (still inside window).
	err = s.Meta().RotateIdentityVerificationToken(ctx, id,
		vh32Local("token-3"), vh32Local("100003"), exp+2, 0, 2)
	if !errors.Is(err, store.ErrRateLimited) {
		t.Fatalf("Rotate over-cap inside window: err = %v, want ErrRateLimited", err)
	}

	// Advance past the 24h window boundary; counter must reset to 1
	// on the next Rotate.
	fc.Advance(25 * time.Hour)
	if err := s.Meta().RotateIdentityVerificationToken(ctx, id,
		vh32Local("token-4"), vh32Local("100004"),
		fc.Now().Add(24*time.Hour).UnixMicro(), 0, 2); err != nil {
		t.Fatalf("Rotate after window reset: %v", err)
	}
	stats2, _ := s.Meta().GetVerificationResendStats(ctx, id)
	if stats2.WindowCount != 1 {
		t.Fatalf("after window reset: window_count = %d, want 1", stats2.WindowCount)
	}
	if stats2.WindowStartedAtUs != fc.Now().UnixMicro() {
		t.Fatalf("after window reset: window_started = %d, want %d",
			stats2.WindowStartedAtUs, fc.Now().UnixMicro())
	}
}

// TestRotate_CooldownElapses confirms that advancing the clock past
// the configured cooldown allows the next Rotate to succeed.
func TestRotate_CooldownElapses(t *testing.T) {
	s, fc, cleanup := openWithFake(t)
	defer cleanup()
	ctx := context.Background()

	p, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "bob@example.com",
		DisplayName: "Bob",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	id := "iv-cooldown-elapse"
	if err := s.Meta().InsertJMAPIdentity(ctx, store.JMAPIdentity{
		ID: id, PrincipalID: p.ID, Email: "bob@external.test", MayDelete: true,
	}); err != nil {
		t.Fatalf("InsertJMAPIdentity: %v", err)
	}

	exp := fc.Now().Add(24 * time.Hour).UnixMicro()
	if err := s.Meta().IssueIdentityVerificationToken(ctx, id,
		vh32Local("ce-1"), vh32Local("110001"), exp); err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// Just before cooldown elapses: still rate-limited.
	fc.Advance(59 * time.Second)
	err = s.Meta().RotateIdentityVerificationToken(ctx, id,
		vh32Local("ce-2"), vh32Local("110002"), exp+1, 60*time.Second, 5)
	if !errors.Is(err, store.ErrRateLimited) {
		t.Fatalf("Rotate at 59s: err = %v, want ErrRateLimited", err)
	}

	// Step past the cooldown -> Rotate succeeds.
	fc.Advance(2 * time.Second) // total 61s
	if err := s.Meta().RotateIdentityVerificationToken(ctx, id,
		vh32Local("ce-3"), vh32Local("110003"), exp+2, 60*time.Second, 5); err != nil {
		t.Fatalf("Rotate at 61s: %v", err)
	}
}
