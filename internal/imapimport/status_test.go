package imapimport

// status_test.go tests the live worker observability snapshot surface
// (REQ-IMAP-IMP-65):
//
//   - WorkerStatus fields populated correctly after an initial sync.
//   - Pool.Snapshot returns entries for multiple accounts, sorted by AccountID.
//   - A stopped worker drops out of Snapshot.
//   - A failing dial moves the snapshot to "backoff"/"errored" with
//     ConsecutiveFailures > 0 and a LastError that does not contain any
//     credential substring.
//   - Concurrent Snapshot() calls with a running worker are race-free.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/sysconfig"
	"github.com/hanshuebner/herold/internal/testharness"
)

// --------------------------------------------------------------------------
// Helper: newPoolForTest builds a Pool wired to a test store and IMAP server.
// --------------------------------------------------------------------------

func newPoolForTest(t *testing.T, ha *testharness.Server, ts *testIMAPServer) *Pool {
	t.Helper()
	tr := true
	return NewPool(PoolOptions{
		Store:   ha.Store,
		DataKey: testDataKey(t),
		Config: sysconfig.IMAPImportConfig{
			AllowPassword:      &tr,
			ConcurrentAccounts: 4,
		},
		Logger: newTestLogger(t),
		Clock:  ha.Clock,
		Dialer: &fakeDialer{ts: ts},
	})
}

// --------------------------------------------------------------------------
// Test: after initial sync the snapshot shows connected, idle/polling, LastSyncAt set.
// --------------------------------------------------------------------------

// TestSnapshotAfterInitialSync drives a worker against the in-process
// memory server, waits for it to complete the initial sync and enter the
// IDLE loop, then asserts the Snapshot fields.
func TestSnapshotAfterInitialSync(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("snap1", "pw")

	ha, _ := testharness.Start(t, testharness.Options{})

	// Seed one message so the fetched counter advances.
	d := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	raw := buildRFC822("snap1-msg@test", "Snap Test", d)
	appendToServer(t, ts, "snap1", "pw", "INBOX", raw, nil, d)

	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "snap1@example.test",
		username:            "snap1",
		credentialPlaintext: "pw",
	}, nil)

	pool := newPoolForTest(t, ha, ts)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = pool.Run(ctx) }()

	// Wait until the worker reaches idle or polling (initial sync done).
	var s WorkerStatus
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		snaps := pool.Snapshot()
		for _, snap := range snaps {
			if snap.AccountID == acc.ID &&
				(snap.Phase == PhaseIdle || snap.Phase == PhasePolling) {
				s = snap
				goto found
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("worker did not reach idle/polling phase within 15s")
found:

	if !s.Connected {
		t.Error("Connected should be true while in idle/polling")
	}
	if s.LastSyncAt == nil {
		t.Error("LastSyncAt should be set after initial sync")
	}
	if s.MessagesFetched < 1 {
		t.Errorf("MessagesFetched = %d; want >= 1", s.MessagesFetched)
	}
	if s.AccountID != acc.ID {
		t.Errorf("AccountID = %q; want %q", s.AccountID, acc.ID)
	}
	if s.PhaseSince.IsZero() {
		t.Error("PhaseSince should not be zero")
	}
	if s.ConnMode != "single" && s.ConnMode != "dual" {
		t.Errorf("ConnMode = %q; want single or dual", s.ConnMode)
	}
}

// --------------------------------------------------------------------------
// Test: bad credential drives snapshot to backoff/errored with ConsecutiveFailures.
// --------------------------------------------------------------------------

// TestSnapshotBackoffAndErrored verifies that a worker using a bad
// credential accumulates ConsecutiveFailures, reports a non-empty redacted
// LastError, and eventually reaches the "errored" phase.
// The credential substring must NOT appear in LastError. REQ-IMAP-IMP-71.
func TestSnapshotBackoffAndErrored(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("snap2", "correctpw")

	ha, _ := testharness.Start(t, testharness.Options{})
	acc := makeAccount(t, ha.Store, accountCfg{
		email:               "snap2@example.test",
		host:                "127.0.0.1",
		port:                993,
		tlsMode:             store.IMAPImportTLSModeImplicit,
		username:            "snap2",
		authMethod:          store.IMAPImportAuthMethodPassword,
		credentialPlaintext: "wrongpw", // will always fail auth
	})

	// Use a tiny M and a FakeClock that advances instantly to skip backoff.
	fc := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	w := newAccountWorker(accountWorkerOpts{
		account:                acc,
		store:                  ha.Store,
		dataKey:                testDataKey(t),
		cfg:                    sysconfig.IMAPImportConfig{},
		log:                    newTestLogger(t),
		clk:                    fc,
		dialer:                 &badCredDialer{},
		categoriser:            noopCategoriser{},
		maxConsecutiveFailures: 3,
	})

	// Build a pool by hand so we can call Snapshot.
	pool := &Pool{
		registry: make(map[string]*accountWorker),
	}
	pool.registryMu.Lock()
	pool.registry[acc.ID] = w
	pool.registryMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		w.run(ctx)
	}()

	// Advance the fake clock in a tight loop to skip all backoff waits.
	clockDone := make(chan struct{})
	go func() {
		defer close(clockDone)
		for {
			select {
			case <-done:
				return
			default:
				fc.Advance(backoffCap + time.Second)
				time.Sleep(time.Millisecond)
			}
		}
	}()

	<-done
	<-clockDone

	// Worker should now be deregistered (drop-out on return) OR in errored
	// phase if we snapshot right as it exits. Check the DB state.
	stored, err := ha.Store.Meta().GetIMAPImportAccount(context.Background(), acc.ID)
	if err != nil {
		t.Fatalf("GetIMAPImportAccount: %v", err)
	}
	if stored.State != store.IMAPImportAccountStateErrored {
		t.Errorf("account state = %q; want errored", stored.State)
	}

	// Snapshot should show empty (worker dropped out) or errored.
	// Either is correct by the drop-out-on-return design.
	// What we verify is that AT SOME POINT while it was running the
	// backoff phase had ConsecutiveFailures > 0 and LastError != "".
	// We can only verify the final DB state at this point, which we did.
	// The phase-progression assertion is done via the worker's own status
	// directly (the worker is still in-scope here).
	snap := w.status.snapshot()
	if snap.Phase != PhaseErrored && snap.Phase != PhaseStopped {
		t.Errorf("final phase = %q; want errored or stopped", snap.Phase)
	}
	if snap.ConsecutiveFailures < 3 {
		t.Errorf("ConsecutiveFailures = %d; want >= 3", snap.ConsecutiveFailures)
	}
	if snap.LastError == "" {
		t.Error("LastError should be set after erroring")
	}
	// The credential must not appear in the error.
	if strings.Contains(snap.LastError, "wrongpw") {
		t.Errorf("LastError may expose credential: %q", snap.LastError)
	}
}

// --------------------------------------------------------------------------
// Test: Pool.Snapshot returns entries for multiple accounts, sorted by AccountID.
// --------------------------------------------------------------------------

// TestSnapshotMultipleAccountsSorted verifies that when two accounts are
// running, Snapshot returns both entries in sorted AccountID order.
func TestSnapshotMultipleAccountsSorted(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("multi1", "pw")
	ts.addUser("multi2", "pw")

	ha, _ := testharness.Start(t, testharness.Options{})
	acc1 := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "multi1@example.test",
		username:            "multi1",
		credentialPlaintext: "pw",
	}, nil)
	acc2 := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "multi2@example.test",
		username:            "multi2",
		credentialPlaintext: "pw",
	}, nil)

	pool := newPoolForTest(t, ha, ts)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = pool.Run(ctx) }()

	// Wait until both accounts appear in Snapshot.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		snaps := pool.Snapshot()
		if len(snaps) >= 2 {
			// Verify sorted order.
			for i := 1; i < len(snaps); i++ {
				if snaps[i-1].AccountID >= snaps[i].AccountID {
					t.Errorf("Snapshot not sorted: snaps[%d].AccountID=%q >= snaps[%d].AccountID=%q",
						i-1, snaps[i-1].AccountID, i, snaps[i].AccountID)
				}
			}
			// Verify both accounts are present.
			ids := make(map[string]bool)
			for _, s := range snaps {
				ids[s.AccountID] = true
			}
			if !ids[acc1.ID] {
				t.Errorf("acc1 (%s) not in Snapshot", acc1.ID)
			}
			if !ids[acc2.ID] {
				t.Errorf("acc2 (%s) not in Snapshot", acc2.ID)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("Snapshot did not return 2 entries within 15s")
}

// --------------------------------------------------------------------------
// Test: a stopped worker drops out of Snapshot.
// --------------------------------------------------------------------------

// TestSnapshotStoppedWorkerDropsOut verifies that after a worker's context
// is cancelled and the worker exits, it no longer appears in Snapshot.
func TestSnapshotStoppedWorkerDropsOut(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("stop1", "pw")

	ha, _ := testharness.Start(t, testharness.Options{})
	makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "stop1@example.test",
		username:            "stop1",
		credentialPlaintext: "pw",
	}, nil)

	pool := newPoolForTest(t, ha, ts)

	ctx, cancel := context.WithCancel(context.Background())

	poolDone := make(chan error, 1)
	go func() { poolDone <- pool.Run(ctx) }()

	// Wait until the account appears.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if len(pool.Snapshot()) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Cancel and wait for Run to return.
	cancel()
	select {
	case <-poolDone:
	case <-time.After(10 * time.Second):
		t.Fatal("Pool.Run did not return after cancel")
	}

	// After Run returns all workers have exited; Snapshot should be empty.
	snaps := pool.Snapshot()
	if len(snaps) != 0 {
		t.Errorf("Snapshot after shutdown: got %d entries; want 0", len(snaps))
	}
}

// --------------------------------------------------------------------------
// Test: concurrent Snapshot + running worker is race-free.
// --------------------------------------------------------------------------

// TestSnapshotConcurrencyRace runs Snapshot() in a tight loop from one
// goroutine while a worker runs from another. With -race this detects any
// data race between the status write path and the snapshot read path.
// REQ-IMAP-IMP-65: "reads must never block a worker".
func TestSnapshotConcurrencyRace(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("race1", "pw")

	ha, _ := testharness.Start(t, testharness.Options{})
	// Create an account so pool.Run has something to supervise; the
	// returned value is not used directly — pool.Run discovers it via
	// ListEnabledIMAPImportAccounts.
	makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "race1@example.test",
		username:            "race1",
		credentialPlaintext: "pw",
	}, nil)

	pool := newPoolForTest(t, ha, ts)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup

	// Worker goroutine.
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = pool.Run(ctx)
	}()

	// Reader goroutine: hammer Snapshot() for the duration of the test.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			default:
				snaps := pool.Snapshot()
				// Access all fields to ensure they are read (the race
				// detector checks memory accesses).
				for _, s := range snaps {
					_ = s.Phase
					_ = s.Connected
					_ = s.MessagesFetched
					_ = s.ConsecutiveFailures
					_ = s.LastError
					_ = s.ConnMode
					_ = s.CurrentFolder
					if s.LastSyncAt != nil {
						_ = *s.LastSyncAt
					}
					if s.NextPollAt != nil {
						_ = *s.NextPollAt
					}
				}
			}
		}
	}()

	wg.Wait()
}

// --------------------------------------------------------------------------
// Test: workerStatus helpers are individually correct.
// --------------------------------------------------------------------------

// TestWorkerStatusHelpers unit-tests the mutation helpers on workerStatus
// in isolation, without running a full worker. This validates the mutex
// discipline and field semantics without network overhead.
func TestWorkerStatusHelpers(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	ws := &workerStatus{
		accountID: "test-acct",
		host:      "imap.example.test",
		phase:     PhaseStarting,
	}

	// setPhase clears currentFolder when leaving syncing.
	ws.setPhase(PhaseSyncing, now)
	ws.setSyncingFolder("INBOX")
	ws.setPhase(PhaseIdle, now.Add(time.Second))

	s := ws.snapshot()
	if s.Phase != PhaseIdle {
		t.Errorf("Phase = %q; want idle", s.Phase)
	}
	if s.CurrentFolder != "" {
		t.Errorf("CurrentFolder = %q; want empty after leaving syncing", s.CurrentFolder)
	}

	// setConnMode.
	ws.setConnMode("dual")
	s = ws.snapshot()
	if s.ConnMode != "dual" {
		t.Errorf("ConnMode = %q; want dual", s.ConnMode)
	}

	// setConnected(false) clears ConnMode.
	ws.setConnected(false)
	s = ws.snapshot()
	if s.Connected {
		t.Error("Connected should be false")
	}
	if s.ConnMode != "" {
		t.Errorf("ConnMode = %q; want empty after disconnect", s.ConnMode)
	}

	// recordSyncOK.
	ws.recordSyncOK(now)
	s = ws.snapshot()
	if s.LastSyncAt == nil {
		t.Error("LastSyncAt should be set after recordSyncOK")
	} else if !s.LastSyncAt.Equal(now) {
		t.Errorf("LastSyncAt = %v; want %v", s.LastSyncAt, now)
	}

	// incFetched / incPropagated.
	ws.incFetched(5)
	ws.incFetched(3)
	ws.incPropagated(2)
	s = ws.snapshot()
	if s.MessagesFetched != 8 {
		t.Errorf("MessagesFetched = %d; want 8", s.MessagesFetched)
	}
	if s.FlagsPropagated != 2 {
		t.Errorf("FlagsPropagated = %d; want 2", s.FlagsPropagated)
	}

	// setConsecutiveFailures.
	ws.setConsecutiveFailures(7)
	s = ws.snapshot()
	if s.ConsecutiveFailures != 7 {
		t.Errorf("ConsecutiveFailures = %d; want 7", s.ConsecutiveFailures)
	}

	// setLastError.
	ws.setLastError("connection refused")
	s = ws.snapshot()
	if s.LastError != "connection refused" {
		t.Errorf("LastError = %q; want connection refused", s.LastError)
	}

	// setNextPoll / polling phase.
	poll := now.Add(60 * time.Second)
	ws.setPhase(PhasePolling, now)
	ws.setNextPoll(poll)
	s = ws.snapshot()
	if s.Phase != PhasePolling {
		t.Errorf("Phase = %q; want polling", s.Phase)
	}
	if s.NextPollAt == nil || !s.NextPollAt.Equal(poll) {
		t.Errorf("NextPollAt = %v; want %v", s.NextPollAt, poll)
	}
	// Leaving polling clears NextPollAt.
	ws.setPhase(PhaseSyncing, now)
	s = ws.snapshot()
	if s.NextPollAt != nil {
		t.Errorf("NextPollAt should be nil after leaving polling; got %v", s.NextPollAt)
	}

	// Snapshot returns independent copies (mutating the returned value
	// does not affect the workerStatus).
	ws.setLastError("original")
	s1 := ws.snapshot()
	s1.LastError = "mutated"
	s2 := ws.snapshot()
	if s2.LastError != "original" {
		t.Errorf("snapshot not independent: s2.LastError = %q; want original", s2.LastError)
	}
}

// --------------------------------------------------------------------------
// Test: WorkerStatus.String() summary format.
// --------------------------------------------------------------------------

// TestWorkerStatusString is a smoke test for the String() method so that
// the format remains consistent and does not panic on nil pointer fields.
func TestWorkerStatusString(t *testing.T) {
	s := WorkerStatus{
		AccountID:           "acc-xyz",
		Host:                "imap.example.com",
		Phase:               PhaseIdle,
		Connected:           true,
		MessagesFetched:     42,
		FlagsPropagated:     7,
		ConsecutiveFailures: 0,
	}
	str := s.String()
	if !strings.Contains(str, "acc-xyz") {
		t.Errorf("String() missing account ID: %q", str)
	}
	if !strings.Contains(str, "idle") {
		t.Errorf("String() missing phase: %q", str)
	}

	// With nil optional fields — must not panic.
	s2 := WorkerStatus{Phase: PhaseErrored, ConsecutiveFailures: 5}
	_ = s2.String()

	s3 := WorkerStatus{Phase: PhasePolling, NextPollAt: func() *time.Time {
		t := time.Now()
		return &t
	}()}
	_ = s3.String()
}
