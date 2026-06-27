package imapimport

// migrate_test.go covers the complete-migration cutover (REQ-IMAP-IMP-90..96,
// Wave C) end-to-end against the in-process imapmemserver:
//
//   - TestCompleteMigrationCutover: trial with a short horizon, herold-side
//     curation (flag + label) and an unresolved both-sides conflict, then a
//     cutover that mirrors the COMPLETE mailbox, PRESERVES herold-side state
//     (no upstream overwrite), drains backfill_remaining to 0, reaches the
//     terminal migrated state, and closes its upstream connection.
//   - TestCompleteMigrationResumeIdempotent: re-running the complete backfill
//     (a restart mid-cutover) never duplicates messages and never regresses
//     herold-side state.
//   - TestCompleteMigrationReopen: a migrated account re-opened to enabled
//     re-asserts upstream-authoritative conflict handling and does not re-fetch
//     already-mirrored mail.

import (
	"context"
	"sync"
	"testing"
	"time"

	imap "github.com/emersion/go-imap/v2"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/sysconfig"
	"github.com/hanshuebner/herold/internal/testharness"
)

// --------------------------------------------------------------------------
// connection-close-counting dialer
// --------------------------------------------------------------------------

// countingDialer wraps a Dialer and counts opened vs closed connections so a
// test can assert that the cutover closes its upstream connection
// (REQ-IMAP-IMP-93).
type countingDialer struct {
	inner  Dialer
	mu     sync.Mutex
	opened int
	closed int
}

func (d *countingDialer) Dial(ctx context.Context, p dialParams) (Conn, error) {
	c, err := d.inner.Dial(ctx, p)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.opened++
	d.mu.Unlock()
	return &countingConn{Conn: c, d: d}, nil
}

func (d *countingDialer) counts() (int, int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.opened, d.closed
}

// countingConn overrides Close to count exactly one close per connection.
type countingConn struct {
	Conn
	d    *countingDialer
	once sync.Once
}

func (c *countingConn) Close() error {
	c.once.Do(func() {
		c.d.mu.Lock()
		c.d.closed++
		c.d.mu.Unlock()
	})
	return c.Conn.Close()
}

// setUpstreamFlag sets one flag on the given upstream message via IMAP STORE.
func setUpstreamFlag(t *testing.T, ts *testIMAPServer, user, password, mailbox string, uid imap.UID, flag imap.Flag) {
	t.Helper()
	ctx := context.Background()
	conn := dialFakeConn(t, ts, user, password)
	defer conn.Logout()
	defer conn.Close()
	if _, err := conn.SelectReadWrite(ctx, mailbox); err != nil {
		t.Fatalf("setUpstreamFlag: SelectReadWrite: %v", err)
	}
	if err := conn.UIDStoreFlags(ctx, uid, imap.StoreFlagsAdd, []imap.Flag{flag}); err != nil {
		t.Fatalf("setUpstreamFlag: UIDStoreFlags: %v", err)
	}
}

// msgIDForUID returns the herold message id mirrored from the given upstream
// (folder, uid) via the import message-state row.
func msgIDForUID(t *testing.T, s store.Store, accID, folder string, uid imap.UID) store.MessageID {
	t.Helper()
	ms, found, err := s.Meta().GetIMAPImportMessageState(context.Background(), accID, folder, uint32(uid))
	if err != nil || !found {
		t.Fatalf("GetIMAPImportMessageState(uid=%d): found=%v err=%v", uid, found, err)
	}
	return ms.HeroldMessageID
}

// isMemberOf reports whether the herold message is a member of mailbox mbID.
func isMemberOf(t *testing.T, s store.Store, msgID store.MessageID, mbID store.MailboxID) bool {
	t.Helper()
	m, err := s.Meta().GetMessage(context.Background(), msgID)
	if err != nil {
		t.Fatalf("GetMessage(%d): %v", msgID, err)
	}
	for _, mm := range m.Mailboxes {
		if mm.MailboxID == mbID {
			return true
		}
	}
	return false
}

// appendDatedINBOX appends a message to the upstream INBOX with the given
// Message-ID and INTERNALDATE and returns the assigned UID. Messages are
// appended oldest-first by the caller so UID order tracks arrival/date order
// (as on a real IMAP server), which the horizon backfill relies on.
func appendDatedINBOX(t *testing.T, ts *testIMAPServer, user, msgID string, date time.Time) imap.UID {
	t.Helper()
	raw := buildRFC822(msgID, msgID, date)
	return appendToServer(t, ts, user, "pw", "INBOX", raw, nil, date)
}

// --------------------------------------------------------------------------
// TestCompleteMigrationCutover
// --------------------------------------------------------------------------

func TestCompleteMigrationCutover(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("mig1", "pw")
	ha, _ := testharness.Start(t, testharness.Options{})
	ctx := context.Background()

	// Upstream INBOX, oldest-first so UID order tracks date order.
	old2 := appendDatedINBOX(t, ts, "mig1", "old2@test", time.Date(2025, 1, 15, 9, 0, 0, 0, time.UTC))
	old1 := appendDatedINBOX(t, ts, "mig1", "old1@test", time.Date(2025, 6, 1, 9, 0, 0, 0, time.UTC))
	recent1 := appendDatedINBOX(t, ts, "mig1", "recent1@test", time.Date(2026, 2, 1, 9, 0, 0, 0, time.UTC))
	recent2 := appendDatedINBOX(t, ts, "mig1", "recent2@test", time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC))
	_ = old2
	_ = old1

	// Short horizon: only the 2026 mail is in-horizon for the trial.
	floor := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "mig1@example.test",
		username:            "mig1",
		credentialPlaintext: "pw",
	}, &floor)

	// --- Trial mirror (enabled, short horizon). ---------------------------
	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("trial sync: %v", err)
	}
	if n := countMailboxMessages(t, ha.Store, acc.PrincipalID, "INBOX"); n != 2 {
		t.Fatalf("after trial: INBOX has %d messages; want 2 (only in-horizon mail)", n)
	}

	r1ID := msgIDForUID(t, ha.Store, acc.ID, "INBOX", recent1)
	r2ID := msgIDForUID(t, ha.Store, acc.ID, "INBOX", recent2)

	// --- Herold-side curation built up during the trial. ------------------
	// recent1: \Flagged + a custom user label "Trial".
	r1, _ := ha.Store.Meta().GetMessage(ctx, r1ID)
	if _, err := ha.Store.Meta().UpdateMessageFlags(ctx, r1.ID, r1.MailboxID,
		store.MessageFlagFlagged, 0, nil, nil, 0); err != nil {
		t.Fatalf("flag recent1: %v", err)
	}
	trialMB, err := ha.Store.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: acc.PrincipalID,
		Name:        "Trial",
	})
	if err != nil {
		t.Fatalf("InsertMailbox Trial: %v", err)
	}
	if _, _, err := ha.Store.Meta().AddMessageToMailbox(ctx, r1.ID, trialMB.ID); err != nil {
		t.Fatalf("AddMessageToMailbox recent1->Trial: %v", err)
	}

	// recent2: build an UNRESOLVED both-sides conflict (last_synced=none,
	// herold=\Seen, upstream=\Flagged). Under enabled mirroring the upstream
	// would win; the cutover must NOT overwrite herold.
	r2, _ := ha.Store.Meta().GetMessage(ctx, r2ID)
	if _, err := ha.Store.Meta().UpdateMessageFlags(ctx, r2.ID, r2.MailboxID,
		store.MessageFlagSeen, 0, nil, nil, 0); err != nil {
		t.Fatalf("flag recent2 herold: %v", err)
	}
	setUpstreamFlag(t, ts, "mig1", "pw", "INBOX", recent2, imap.FlagFlagged)

	// --- Request complete migration (enabled -> migrating). ---------------
	if err := ha.Store.Meta().SetIMAPImportAccountState(ctx, acc.ID,
		store.IMAPImportAccountStateMigrating, "", nil); err != nil {
		t.Fatalf("transition to migrating: %v", err)
	}

	// --- Run the cutover worker; it should reach migrated and stop. -------
	cd := &countingDialer{inner: &fakeDialer{ts: ts}}
	w := newAccountWorker(accountWorkerOpts{
		account:     acc, // run() re-reads the now-migrating state from the store
		store:       ha.Store,
		dataKey:     testDataKey(t),
		cfg:         sysconfig.IMAPImportConfig{},
		log:         newTestLogger(t),
		clk:         ha.Clock,
		dialer:      cd,
		categoriser: noopCategoriser{},
	})
	runCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); w.run(runCtx) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		cancel()
		<-done
		t.Fatal("cutover worker did not finish within 20s")
	}

	// --- Assertions. ------------------------------------------------------

	// Complete mailbox present: all four messages, including the below-horizon
	// old mail, are now mirrored (REQ-IMAP-IMP-91).
	if n := countMailboxMessages(t, ha.Store, acc.PrincipalID, "INBOX"); n != 4 {
		t.Errorf("after cutover: INBOX has %d messages; want 4 (complete mailbox)", n)
	}

	// State preserved (REQ-IMAP-IMP-92): recent1 keeps \Flagged + its label.
	r1b, _ := ha.Store.Meta().GetMessage(ctx, r1ID)
	if r1b.Flags&store.MessageFlagFlagged == 0 {
		t.Error("recent1 lost its herold \\Flagged across cutover")
	}
	if !isMemberOf(t, ha.Store, r1ID, trialMB.ID) {
		t.Error("recent1 lost its 'Trial' label membership across cutover")
	}

	// The unresolved conflict is NOT overwritten by the upstream: recent2
	// keeps herold's \Seen and does NOT gain the upstream's \Flagged
	// (REQ-IMAP-IMP-92 — authority transferred to herold).
	r2b, _ := ha.Store.Meta().GetMessage(ctx, r2ID)
	if r2b.Flags&store.MessageFlagSeen == 0 {
		t.Error("recent2 lost herold \\Seen across cutover (upstream wrongly won)")
	}
	if r2b.Flags&store.MessageFlagFlagged != 0 {
		t.Error("recent2 gained upstream \\Flagged across cutover (authority not transferred)")
	}

	// backfill_remaining drained to 0 (REQ-IMAP-IMP-91).
	if g := testutil.ToFloat64(observe.IMAPImportBackfillRemaining.WithLabelValues(acc.ID)); g != 0 {
		t.Errorf("backfill_remaining = %v; want 0 after cutover", g)
	}

	// Terminal migrated state persisted (REQ-IMAP-IMP-93).
	stored, err := ha.Store.Meta().GetIMAPImportAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("GetIMAPImportAccount: %v", err)
	}
	if stored.State != store.IMAPImportAccountStateMigrated {
		t.Errorf("account state = %q; want migrated", stored.State)
	}

	// Upstream connection opened and closed cleanly (REQ-IMAP-IMP-93).
	opened, closed := cd.counts()
	if opened == 0 {
		t.Error("cutover never opened an upstream connection")
	}
	if opened != closed {
		t.Errorf("cutover left connections open: opened=%d closed=%d", opened, closed)
	}
}

// --------------------------------------------------------------------------
// TestCompleteMigrationResumeIdempotent
// --------------------------------------------------------------------------

// TestCompleteMigrationResumeIdempotent re-runs the complete backfill (modelling
// a restart mid-cutover) and asserts it neither duplicates messages nor
// regresses herold-side state (REQ-IMAP-IMP-94/92).
func TestCompleteMigrationResumeIdempotent(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("mig2", "pw")
	ha, _ := testharness.Start(t, testharness.Options{})
	ctx := context.Background()

	appendDatedINBOX(t, ts, "mig2", "r-old@test", time.Date(2025, 5, 1, 9, 0, 0, 0, time.UTC))
	recent := appendDatedINBOX(t, ts, "mig2", "r-new@test", time.Date(2026, 2, 1, 9, 0, 0, 0, time.UTC))

	floor := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "mig2@example.test",
		username:            "mig2",
		credentialPlaintext: "pw",
	}, &floor)

	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("trial sync: %v", err)
	}
	// Curate the in-horizon message so we can assert state is not regressed.
	recID := msgIDForUID(t, ha.Store, acc.ID, "INBOX", recent)
	rec, _ := ha.Store.Meta().GetMessage(ctx, recID)
	if _, err := ha.Store.Meta().UpdateMessageFlags(ctx, rec.ID, rec.MailboxID,
		store.MessageFlagFlagged, 0, nil, nil, 0); err != nil {
		t.Fatalf("flag recent: %v", err)
	}

	w := newAccountWorker(accountWorkerOpts{
		account:     acc,
		store:       ha.Store,
		dataKey:     testDataKey(t),
		cfg:         sysconfig.IMAPImportConfig{},
		log:         newTestLogger(t),
		clk:         ha.Clock,
		dialer:      &fakeDialer{ts: ts},
		categoriser: noopCategoriser{},
	})

	// Run the complete backfill twice; the second run models a restart that
	// resumes the cutover. Each call re-fetches the whole mailbox.
	for i := 0; i < 2; i++ {
		if err := ha.Store.Meta().SetIMAPImportAccountState(ctx, acc.ID,
			store.IMAPImportAccountStateMigrating, "", nil); err != nil {
			t.Fatalf("set migrating (pass %d): %v", i, err)
		}
		// Refresh the in-memory copy so authorityIsHerold()/state are current.
		cur, err := ha.Store.Meta().GetIMAPImportAccount(ctx, acc.ID)
		if err != nil {
			t.Fatalf("GetIMAPImportAccount (pass %d): %v", i, err)
		}
		w.refreshAccount(cur)
		if err := w.attemptMigration(ctx); err != nil {
			t.Fatalf("attemptMigration (pass %d): %v", i, err)
		}
	}

	// No duplication: the mailbox holds exactly the two distinct messages.
	if n := countMailboxMessages(t, ha.Store, acc.PrincipalID, "INBOX"); n != 2 {
		t.Errorf("after resumed cutover: INBOX has %d messages; want 2 (no duplicates)", n)
	}
	// State not regressed: the curated \Flagged survives the re-run.
	recB, _ := ha.Store.Meta().GetMessage(ctx, recID)
	if recB.Flags&store.MessageFlagFlagged == 0 {
		t.Error("resumed cutover regressed herold-side \\Flagged")
	}
	stored, _ := ha.Store.Meta().GetIMAPImportAccount(ctx, acc.ID)
	if stored.State != store.IMAPImportAccountStateMigrated {
		t.Errorf("state = %q; want migrated after resume", stored.State)
	}
}

// --------------------------------------------------------------------------
// TestCompleteMigrationReopen
// --------------------------------------------------------------------------

// TestCompleteMigrationReopen verifies that a migrated account re-opened to
// enabled re-asserts upstream-authoritative conflict handling from that point
// and does not re-fetch already-mirrored mail (REQ-IMAP-IMP-95).
func TestCompleteMigrationReopen(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("mig3", "pw")
	ha, _ := testharness.Start(t, testharness.Options{})
	ctx := context.Background()

	recent := appendDatedINBOX(t, ts, "mig3", "ro-new@test", time.Date(2026, 2, 1, 9, 0, 0, 0, time.UTC))

	floor := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "mig3@example.test",
		username:            "mig3",
		credentialPlaintext: "pw",
	}, &floor)

	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("trial sync: %v", err)
	}
	recID := msgIDForUID(t, ha.Store, acc.ID, "INBOX", recent)

	// Curate herold-side: \Flagged. last_synced is still "none" from the trial.
	rec, _ := ha.Store.Meta().GetMessage(ctx, recID)
	if _, err := ha.Store.Meta().UpdateMessageFlags(ctx, rec.ID, rec.MailboxID,
		store.MessageFlagFlagged, 0, nil, nil, 0); err != nil {
		t.Fatalf("flag recent: %v", err)
	}

	// Cutover to migrated (no down-sync; herold \Flagged preserved).
	if err := ha.Store.Meta().SetIMAPImportAccountState(ctx, acc.ID,
		store.IMAPImportAccountStateMigrating, "", nil); err != nil {
		t.Fatalf("set migrating: %v", err)
	}
	cur, _ := ha.Store.Meta().GetIMAPImportAccount(ctx, acc.ID)
	w := newAccountWorker(accountWorkerOpts{
		account: cur, store: ha.Store, dataKey: testDataKey(t),
		cfg: sysconfig.IMAPImportConfig{}, log: newTestLogger(t),
		clk: ha.Clock, dialer: &fakeDialer{ts: ts}, categoriser: noopCategoriser{},
	})
	if err := w.attemptMigration(ctx); err != nil {
		t.Fatalf("attemptMigration: %v", err)
	}
	recAfterMig, _ := ha.Store.Meta().GetMessage(ctx, recID)
	if recAfterMig.Flags&store.MessageFlagFlagged == 0 {
		t.Fatal("precondition: cutover should have preserved herold \\Flagged")
	}

	// --- Re-open to enabled. ----------------------------------------------
	if err := ha.Store.Meta().SetIMAPImportAccountState(ctx, acc.ID,
		store.IMAPImportAccountStateEnabled, "", nil); err != nil {
		t.Fatalf("re-open to enabled: %v", err)
	}
	reopened, _ := ha.Store.Meta().GetIMAPImportAccount(ctx, acc.ID)
	if reopened.State != store.IMAPImportAccountStateEnabled {
		t.Fatalf("re-opened state = %q; want enabled", reopened.State)
	}

	// Upstream changes the message independently (sets \Seen). With authority
	// back to the upstream, an enabled sync round must apply this down to
	// herold, overwriting the herold-side curation (upstream-authoritative
	// re-asserted, REQ-IMAP-IMP-95/42).
	setUpstreamFlag(t, ts, "mig3", "pw", "INBOX", recent, imap.FlagSeen)

	if err := runSyncOnce(t, ha, ts, reopened, nil); err != nil {
		t.Fatalf("enabled sync after re-open: %v", err)
	}

	recReopen, _ := ha.Store.Meta().GetMessage(ctx, recID)
	if recReopen.Flags&store.MessageFlagSeen == 0 {
		t.Error("after re-open, upstream \\Seen was not applied down to herold (upstream-authoritative not re-asserted)")
	}
	if recReopen.Flags&store.MessageFlagFlagged != 0 {
		t.Error("after re-open, herold \\Flagged should have been overwritten (upstream did not have it)")
	}

	// Re-open does not re-fetch already-mirrored mail: the single message is
	// not duplicated.
	if n := countMailboxMessages(t, ha.Store, acc.PrincipalID, "INBOX"); n != 1 {
		t.Errorf("after re-open: INBOX has %d messages; want 1 (no re-fetch/duplication)", n)
	}
}
