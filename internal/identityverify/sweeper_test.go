package identityverify

// sweeper_test.go drives the GC pass deterministically using a
// FakeClock the test owns. The matrix exercises:
//   - Expired token trios are cleared; the Identity row survives.
//   - Unverified rows older than the purge window are destroyed.
//   - Verified rows are NEVER touched.
//   - Multiple rows in one tick.
//   - Audit-log entries are written with action=identity.purge.
//   - Context cancellation stops Run cleanly.
//
// The same suite is registered as a storetest helper so the postgres
// backend exercises the same behaviour (see sweeper_storetest.go).

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// auditCapture mirrors fakeAuditor but is private to the sweeper test
// suite so the two test files do not collide on names.
type auditCapture struct {
	mu      sync.Mutex
	entries []store.AuditLogEntry
}

func (a *auditCapture) Append(_ context.Context, e store.AuditLogEntry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, e)
	return nil
}

func (a *auditCapture) snap() []store.AuditLogEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]store.AuditLogEntry, len(a.entries))
	copy(out, a.entries)
	return out
}

type sweeperHarness struct {
	store store.Store
	clk   *clock.FakeClock
	aud   *auditCapture
	sw    *Sweeper
}

func newSweeperHarness(t *testing.T, purgeAfter time.Duration) *sweeperHarness {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC))
	st, err := storesqlite.Open(context.Background(),
		filepath.Join(t.TempDir(), "store.db"), nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	aud := &auditCapture{}
	sw := NewSweeper(SweeperOptions{
		Store:                st,
		Logger:               slog.New(slog.NewTextHandler(io.Discard, nil)),
		Clock:                clk,
		Auditor:              aud,
		Interval:             time.Hour,
		UnverifiedPurgeAfter: purgeAfter,
	})
	return &sweeperHarness{store: st, clk: clk, aud: aud, sw: sw}
}

// seedUnverified inserts an unverified Identity owned by pid with the
// supplied id and email, and pins its created_at to created (so tests
// can place rows on either side of the purge cutoff).
func (h *sweeperHarness) seedUnverified(t *testing.T, pid store.PrincipalID, id, email string, created time.Time) store.JMAPIdentity {
	t.Helper()
	row := store.JMAPIdentity{
		ID: id, PrincipalID: pid, Email: email, MayDelete: true,
		CreatedAtUs: created.UnixMicro(),
	}
	if err := h.store.Meta().InsertJMAPIdentity(context.Background(), row); err != nil {
		t.Fatalf("InsertJMAPIdentity(%s): %v", id, err)
	}
	return row
}

// issueToken writes the verification trio with the supplied expiry.
func (h *sweeperHarness) issueToken(t *testing.T, id string, expiresAt time.Time) {
	t.Helper()
	tok := sha256.Sum256([]byte("token-" + id))
	code := sha256.Sum256([]byte("code-" + id))
	if err := h.store.Meta().IssueIdentityVerificationToken(
		context.Background(), id, tok[:], code[:], expiresAt.UnixMicro()); err != nil {
		t.Fatalf("IssueIdentityVerificationToken(%s): %v", id, err)
	}
}

func (h *sweeperHarness) insertPrincipal(t *testing.T, email string) store.PrincipalID {
	t.Helper()
	p, err := h.store.Meta().InsertPrincipal(context.Background(), store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: email, DisplayName: "X",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	return p.ID
}

// TestSweeper_Tick_ClearsExpiredTokensKeepsRow asserts component 1 of
// REQ-IDENT-35: an expired token has its trio cleared while the row
// itself survives (the user can resend).
func TestSweeper_Tick_ClearsExpiredTokensKeepsRow(t *testing.T) {
	h := newSweeperHarness(t, 7*24*time.Hour)
	pid := h.insertPrincipal(t, "alice@example.com")
	now := h.clk.Now()
	row := h.seedUnverified(t, pid, "iv-exp-token", "alice@external.test", now)

	// Issue a token that expired 1 second ago.
	h.issueToken(t, row.ID, now.Add(-time.Second))

	if err := h.sw.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	got, err := h.store.Meta().GetJMAPIdentity(context.Background(), row.ID)
	if err != nil {
		t.Fatalf("Get after Tick: %v", err)
	}
	if got.VerificationTokenHash != nil || got.VerificationCodeHash != nil ||
		got.VerificationTokenExpiresAtUs != 0 {
		t.Fatalf("trio not cleared: %+v", got)
	}
	if h.sw.TokensCleared() != 1 {
		t.Fatalf("TokensCleared = %d, want 1", h.sw.TokensCleared())
	}
	if h.sw.IdentitiesPurged() != 0 {
		t.Fatalf("IdentitiesPurged = %d, want 0", h.sw.IdentitiesPurged())
	}
	// Audit entry shape: identity.purge with kind=token_cleared.
	entries := h.aud.snap()
	if len(entries) != 1 {
		t.Fatalf("audit entries: got %d, want 1", len(entries))
	}
	if entries[0].Action != "identity.purge" {
		t.Fatalf("audit action = %q, want identity.purge", entries[0].Action)
	}
	if entries[0].Metadata["kind"] != "token_cleared" {
		t.Fatalf("audit kind = %q, want token_cleared", entries[0].Metadata["kind"])
	}
}

// TestSweeper_Tick_PurgesStaleUnverifiedRows asserts component 2 of
// REQ-IDENT-35: an unverified row older than the purge window is
// destroyed.
func TestSweeper_Tick_PurgesStaleUnverifiedRows(t *testing.T) {
	h := newSweeperHarness(t, 7*24*time.Hour)
	pid := h.insertPrincipal(t, "bob@example.com")
	now := h.clk.Now()

	// Pin created_at to 8 days ago.
	stale := h.seedUnverified(t, pid, "iv-stale", "bob@external.test",
		now.Add(-8*24*time.Hour))
	// Pin a fresh row that must NOT be purged (still inside window).
	fresh := h.seedUnverified(t, pid, "iv-fresh", "bob-fresh@external.test",
		now.Add(-1*24*time.Hour))

	if err := h.sw.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	// Stale row must be gone.
	if _, err := h.store.Meta().GetJMAPIdentity(context.Background(), stale.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("stale row still present: err = %v", err)
	}
	// Fresh row must remain.
	if _, err := h.store.Meta().GetJMAPIdentity(context.Background(), fresh.ID); err != nil {
		t.Fatalf("fresh row destroyed: err = %v", err)
	}
	if h.sw.IdentitiesPurged() != 1 {
		t.Fatalf("IdentitiesPurged = %d, want 1", h.sw.IdentitiesPurged())
	}
	// Audit entry shape: identity.purge with kind=identity_purged.
	var purgeFound bool
	for _, e := range h.aud.snap() {
		if e.Action == "identity.purge" && e.Metadata["kind"] == "identity_purged" {
			purgeFound = true
			break
		}
	}
	if !purgeFound {
		t.Fatalf("identity_purged audit entry missing")
	}
}

// TestSweeper_Tick_LeavesVerifiedRowsAlone asserts that a verified
// Identity is NEVER destroyed by the sweeper -- even if its
// created_at is older than the purge window. The
// ListUnverifiedIdentitiesOlderThan filter handles this at the SQL
// level; the test pins down the invariant.
func TestSweeper_Tick_LeavesVerifiedRowsAlone(t *testing.T) {
	h := newSweeperHarness(t, 7*24*time.Hour)
	pid := h.insertPrincipal(t, "carol@example.com")
	now := h.clk.Now()
	row := h.seedUnverified(t, pid, "iv-verified-old", "carol@external.test",
		now.Add(-30*24*time.Hour))
	// Issue then verify.
	h.issueToken(t, row.ID, now.Add(24*time.Hour))
	if err := h.store.Meta().MarkIdentityVerified(context.Background(), row.ID); err != nil {
		t.Fatalf("MarkIdentityVerified: %v", err)
	}

	if err := h.sw.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	got, err := h.store.Meta().GetJMAPIdentity(context.Background(), row.ID)
	if err != nil {
		t.Fatalf("verified row destroyed: %v", err)
	}
	if got.VerifiedAtUs == 0 {
		t.Fatalf("verified row lost VerifiedAtUs")
	}
}

// TestSweeper_Tick_MultipleRows asserts the sweeper handles a batch of
// rows in one pass.
func TestSweeper_Tick_MultipleRows(t *testing.T) {
	h := newSweeperHarness(t, 7*24*time.Hour)
	pid := h.insertPrincipal(t, "dave@example.com")
	now := h.clk.Now()
	// Three rows with expired tokens, two stale unverified rows.
	for i := 0; i < 3; i++ {
		row := h.seedUnverified(t, pid, "iv-token-"+itoa(i), "x@external.test", now)
		h.issueToken(t, row.ID, now.Add(-time.Duration(i+1)*time.Minute))
	}
	for i := 0; i < 2; i++ {
		h.seedUnverified(t, pid, "iv-stale-"+itoa(i), "y@external.test",
			now.Add(-(8+time.Duration(i))*24*time.Hour))
	}
	if err := h.sw.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if h.sw.TokensCleared() != 3 {
		t.Fatalf("TokensCleared = %d, want 3", h.sw.TokensCleared())
	}
	if h.sw.IdentitiesPurged() != 2 {
		t.Fatalf("IdentitiesPurged = %d, want 2", h.sw.IdentitiesPurged())
	}
}

// TestSweeper_Run_StopsOnContextCancellation confirms that a Run
// goroutine returns nil when its context is cancelled, even mid-sleep.
func TestSweeper_Run_StopsOnContextCancellation(t *testing.T) {
	h := newSweeperHarness(t, 7*24*time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.sw.Run(ctx) }()
	// Let the initial Tick complete then cancel; the second sleep is
	// driven by the FakeClock which never advances on its own, so the
	// goroutine blocks on After(interval) until ctx.Done fires.
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Run did not return after cancellation")
	}
}

// TestSweeper_Defaults checks that NewSweeper applies the documented
// defaults when SweeperOptions fields are zero.
func TestSweeper_Defaults(t *testing.T) {
	sw := NewSweeper(SweeperOptions{Store: nullStore{}})
	if sw.Interval() != DefaultGCInterval {
		t.Fatalf("default interval = %s, want %s", sw.Interval(), DefaultGCInterval)
	}
	if sw.UnverifiedPurgeAfter() != DefaultUnverifiedPurgeAfter {
		t.Fatalf("default purge = %s, want %s", sw.UnverifiedPurgeAfter(), DefaultUnverifiedPurgeAfter)
	}
	// Sub-minute interval should clamp to DefaultGCInterval.
	sw2 := NewSweeper(SweeperOptions{
		Store:                nullStore{},
		Interval:             10 * time.Second,
		UnverifiedPurgeAfter: 5 * time.Minute,
	})
	if sw2.Interval() != DefaultGCInterval {
		t.Fatalf("sub-minute interval = %s, want %s", sw2.Interval(), DefaultGCInterval)
	}
	if sw2.UnverifiedPurgeAfter() != 5*time.Minute {
		t.Fatalf("explicit purge ignored: %s", sw2.UnverifiedPurgeAfter())
	}
}

// nullStore is just enough of store.Store to satisfy NewSweeper's
// type-only requirement. Tests that don't drive Run/Tick can use it.
type nullStore struct{}

func (nullStore) Meta() store.Metadata { return nil }
func (nullStore) Blobs() store.Blobs   { return nil }
func (nullStore) FTS() store.FTS       { return nil }
func (nullStore) Close() error         { return nil }
