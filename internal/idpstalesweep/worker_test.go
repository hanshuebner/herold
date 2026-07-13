package idpstalesweep_test

import (
	"context"
	"crypto/rand"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/idpstalesweep"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// fixture holds a store + fake clock for one test, along with the subject
// principal that carries the grants under test.
type fixture struct {
	store   store.Store
	clk     *clock.FakeClock
	subject store.PrincipalID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	clk := clock.NewFake(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := storesqlite.OpenWithRand(context.Background(), dbPath, nil, clk, rand.Reader)
	if err != nil {
		t.Fatalf("storesqlite.OpenWithRand: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	p, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "sweep-subject@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	return &fixture{store: s, clk: clk, subject: p.ID}
}

// insertIdPGrant inserts an idp:<provider> grant with the given
// LastAssertedAt, returning its ID.
func (f *fixture) insertIdPGrant(t *testing.T, provider string, resourceID string, assertedAt time.Time) store.GrantID {
	t.Helper()
	g, err := f.store.Meta().InsertGrant(context.Background(), store.Grant{
		SubjectID: uint64(f.subject), ResourceKind: store.GrantResourceDomain,
		ResourceID: resourceID, Level: store.GrantLevelOperator,
		Provenance: "idp:" + provider, LastAssertedAt: &assertedAt,
	})
	if err != nil {
		t.Fatalf("InsertGrant: %v", err)
	}
	return g.ID
}

// insertLocalGrant inserts a local-provenance grant, returning its ID.
func (f *fixture) insertLocalGrant(t *testing.T, resourceID string) store.GrantID {
	t.Helper()
	g, err := f.store.Meta().InsertGrant(context.Background(), store.Grant{
		SubjectID: uint64(f.subject), ResourceKind: store.GrantResourceDomain,
		ResourceID: resourceID, Level: store.GrantLevelOperator,
		Provenance: store.GrantProvenanceLocal,
	})
	if err != nil {
		t.Fatalf("InsertGrant local: %v", err)
	}
	return g.ID
}

func (f *fixture) hasGrant(t *testing.T, id store.GrantID) bool {
	t.Helper()
	all, err := f.store.Meta().ListGrantsForPrincipal(context.Background(), f.subject)
	if err != nil {
		t.Fatalf("ListGrantsForPrincipal: %v", err)
	}
	for _, g := range all {
		if g.ID == id {
			return true
		}
	}
	return false
}

func TestWorker_Defaults(t *testing.T) {
	f := newFixture(t)
	w := idpstalesweep.NewWorker(idpstalesweep.Options{Meta: f.store.Meta()})
	if w.Staleness() != idpstalesweep.DefaultStaleness {
		t.Errorf("Staleness default = %v, want %v", w.Staleness(), idpstalesweep.DefaultStaleness)
	}
	if w.SweepInterval() != idpstalesweep.DefaultSweepInterval {
		t.Errorf("SweepInterval default = %v, want %v", w.SweepInterval(), idpstalesweep.DefaultSweepInterval)
	}
}

// TestTick_RemovesOnlyStaleIdPGrants is the REQ-AC-63b core scenario: a
// stale idp: grant is swept, a fresh idp: grant and a local grant on the
// same subject both survive untouched.
func TestTick_RemovesOnlyStaleIdPGrants(t *testing.T) {
	f := newFixture(t)
	now := f.clk.Now()

	staleID := f.insertIdPGrant(t, "acme", "stale.example", now.Add(-40*24*time.Hour))
	freshID := f.insertIdPGrant(t, "acme", "fresh.example", now.Add(-5*24*time.Hour))
	localID := f.insertLocalGrant(t, "local.example")

	w := idpstalesweep.NewWorker(idpstalesweep.Options{
		Meta:      f.store.Meta(),
		Clock:     f.clk,
		Staleness: 30 * 24 * time.Hour,
	})
	removed, err := w.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(removed) != 1 || removed[0].ID != staleID {
		t.Fatalf("Tick removed = %+v, want exactly [grant %d]", removed, staleID)
	}
	if f.hasGrant(t, staleID) {
		t.Errorf("stale grant %d survived Tick", staleID)
	}
	if !f.hasGrant(t, freshID) {
		t.Errorf("fresh grant %d was removed by Tick", freshID)
	}
	if !f.hasGrant(t, localID) {
		t.Errorf("local grant %d was removed by Tick", localID)
	}
	if got := w.Removed(); got != 1 {
		t.Errorf("Removed() = %d, want 1", got)
	}
}

// TestTick_NoneStale verifies an all-fresh grant set is a no-op.
func TestTick_NoneStale(t *testing.T) {
	f := newFixture(t)
	now := f.clk.Now()
	freshID := f.insertIdPGrant(t, "acme", "fresh.example", now.Add(-1*time.Hour))

	w := idpstalesweep.NewWorker(idpstalesweep.Options{
		Meta:      f.store.Meta(),
		Clock:     f.clk,
		Staleness: 30 * 24 * time.Hour,
	})
	removed, err := w.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("Tick removed = %+v, want none", removed)
	}
	if !f.hasGrant(t, freshID) {
		t.Errorf("fresh grant %d was removed by Tick", freshID)
	}
}

// waitForWaiters polls clk.NumWaiters until it reaches n, following the
// FakeClock poll-until-stable convention used elsewhere in the codebase
// (internal/imapimport/status_test.go): the underlying clock state is
// fully deterministic (advanced only by explicit Advance calls), and the
// only non-determinism here is OS goroutine scheduling handoff between
// the test goroutine and the worker's Run loop -- there is no scenario
// under test whose PASS/FAIL depends on wall-clock timing.
func waitForWaiters(t *testing.T, clk *clock.FakeClock, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if clk.NumWaiters() == n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("NumWaiters never reached %d (stuck at %d)", n, clk.NumWaiters())
}

// TestRun_TicksOnFakeClockAdvance drives Run in a goroutine and proves it
// (a) sweeps immediately on the first tick and (b) sweeps again only once
// the fake clock has been advanced past the sweep interval -- entirely via
// clk.Advance, never a real sleep for timing (STANDARDS.md / CLAUDE.md
// flaky-test rule: fix the determinism gap, never bump a deadline).
func TestRun_TicksOnFakeClockAdvance(t *testing.T) {
	f := newFixture(t)
	now := f.clk.Now()
	// Asserted 1 minute ago: survives the immediate first tick (well
	// inside the 1h staleness window), but will have aged past it once
	// the clock advances by the 1h sweep interval.
	grantID := f.insertIdPGrant(t, "acme", "ages-out.example", now.Add(-time.Minute))

	w := idpstalesweep.NewWorker(idpstalesweep.Options{
		Meta:          f.store.Meta(),
		Clock:         f.clk,
		Staleness:     time.Hour,
		SweepInterval: time.Hour,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	// Run's first tick fires synchronously before the loop's first
	// select, so once the worker is parked on its first After() wait the
	// first tick has already completed and the grant, not yet stale,
	// must have survived it.
	waitForWaiters(t, f.clk, 1)
	if !f.hasGrant(t, grantID) {
		t.Fatalf("grant %d removed by the immediate first tick; it was not yet stale", grantID)
	}

	// Advancing by exactly the sweep interval both fires the pending
	// waiter and ages the grant's LastAssertedAt past the 1h staleness
	// window, so the second tick removes it.
	f.clk.Advance(time.Hour)
	waitForWaiters(t, f.clk, 1)
	if f.hasGrant(t, grantID) {
		t.Errorf("grant %d survived the second tick after the clock advanced past staleness", grantID)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned error after cancel: %v", err)
	}
}

func TestRun_NilMeta(t *testing.T) {
	w := idpstalesweep.NewWorker(idpstalesweep.Options{})
	if err := w.Run(context.Background()); err == nil {
		t.Fatal("Run with nil Meta = nil error, want error")
	}
}

// TestRun_AlreadyRunning verifies Run refuses a second concurrent
// invocation, mirroring internal/trashretention and internal/archiveretention.
func TestRun_AlreadyRunning(t *testing.T) {
	f := newFixture(t)
	w := idpstalesweep.NewWorker(idpstalesweep.Options{
		Meta:          f.store.Meta(),
		Clock:         f.clk,
		SweepInterval: time.Hour,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	// Once the worker is parked on its first After() wait, the running
	// flag is set and a second Run must be refused immediately.
	waitForWaiters(t, f.clk, 1)
	if err := w.Run(context.Background()); err == nil {
		t.Error("second concurrent Run call should have returned an error")
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned error after cancel: %v", err)
	}
}
