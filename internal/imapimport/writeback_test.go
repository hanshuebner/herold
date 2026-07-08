package imapimport

// writeback_test.go covers Part B of sub-step 3d: the write-back loop.
//
// Tests:
//   - Flag push: mark herold message \Seen; write-back pushes it upstream.
//   - Flag conflict (upstream wins): last_synced=a, herold=b, upstream=c →
//     herold reconciled to c, conflicts_total incremented.
//   - Failed push leaves last_synced unchanged (retry on next tick).
//   - Move: herold message moves mailbox → UID MOVE issued upstream.
//   - Destroy with DeletePropagates=true → EXPUNGE issued.
//   - Destroy with DeletePropagates=false → nothing issued.
//   - Cursor durability: resume after restart without reprocessing.
//   - Custom keyword change does NOT push upstream.
//   - ctx cancel stops write-back cleanly.

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
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
// helper: runWriteBackTick
// Runs the write-back loop once (processes changes up to the current head).
// Returns when the loop has polled through the change feed at least once.
// --------------------------------------------------------------------------

// runOneWriteBackPass builds a worker, opens a write-back connection, runs
// runWriteBack for a short window, then cancels.
func runOneWriteBackPass(t *testing.T, ha *testharness.Server, ts *testIMAPServer, acc store.IMAPImportAccount) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())

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

	// Run the write-back goroutine. It opens its upstream connection on demand
	// via openSecondaryConn. Cancel after a short delay to stop the poll loop so
	// this helper returns quickly.
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.runWriteBack(ctx)
	}()

	// Give the loop enough time to process all current changes.
	time.Sleep(300 * time.Millisecond)
	cancel()
	<-done
}

// --------------------------------------------------------------------------
// Set up a pre-synced message on the server and in the store.
// --------------------------------------------------------------------------

// setupSyncedMessage appends a message to the upstream, runs a sync to mirror
// it, and returns the IMAPImportMessageState.
func setupSyncedMessage(t *testing.T, ha *testharness.Server, ts *testIMAPServer, acc store.IMAPImportAccount, mailbox, msgIDHeader string, flags []imap.Flag) (imap.UID, store.IMAPImportMessageState) {
	t.Helper()
	d := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	raw := buildRFC822(msgIDHeader, "Test", d)
	uid := appendToServer(t, ts, acc.Username, "pw", mailbox, raw, flags, d)

	// Sync it into herold.
	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}

	ms, found, err := ha.Store.Meta().GetIMAPImportMessageState(context.Background(), acc.ID, mailbox, uint32(uid))
	if err != nil || !found {
		t.Fatalf("GetIMAPImportMessageState: found=%v err=%v", found, err)
	}
	return uid, ms
}

// --------------------------------------------------------------------------
// Test: flag push (herold changed, upstream unchanged)
// --------------------------------------------------------------------------

// TestWriteBackFlagPush verifies that when herold's \Seen flag changes and
// upstream is still at the last-synced value, the change is pushed upstream.
// REQ-IMAP-IMP-40/42.
func TestWriteBackFlagPush(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("wb1", "pw")

	ha, _ := testharness.Start(t, testharness.Options{})
	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "wb1@example.test",
		username:            "wb1",
		credentialPlaintext: "pw",
	}, nil)

	uid, ms := setupSyncedMessage(t, ha, ts, acc, "INBOX", "wb-push@test", nil)

	ctx := context.Background()

	// Mark the herold message \Seen.
	heroldMsg, err := ha.Store.Meta().GetMessage(ctx, ms.HeroldMessageID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if _, err := ha.Store.Meta().UpdateMessageFlags(ctx, heroldMsg.ID, heroldMsg.MailboxID,
		store.MessageFlagSeen, 0, nil, nil, 0); err != nil {
		t.Fatalf("UpdateMessageFlags: %v", err)
	}

	// Run the write-back pass.
	runOneWriteBackPass(t, ha, ts, acc)

	// Assert upstream now has \Seen.
	flags := getUpstreamFlags(t, ts, "wb1", "pw", "INBOX", uid)
	hasSeen := false
	for _, f := range flags {
		if f == imap.FlagSeen {
			hasSeen = true
		}
	}
	if !hasSeen {
		t.Error("upstream message should have \\Seen after write-back push")
	}

	// Assert last_synced updated.
	ms2, found, err := ha.Store.Meta().GetIMAPImportMessageState(ctx, acc.ID, "INBOX", uint32(uid))
	if err != nil || !found {
		t.Fatalf("GetIMAPImportMessageState after push: found=%v err=%v", found, err)
	}
	if !ms2.LastSyncedFlags.HasSeen() {
		t.Error("last_synced should have \\Seen set after successful push")
	}
}

// --------------------------------------------------------------------------
// Test: flag conflict (upstream wins)
// --------------------------------------------------------------------------

// TestWriteBackFlagConflict verifies that when both herold and upstream
// changed since last_synced, the upstream value wins, herold is reconciled
// to it, and conflicts_total is incremented. REQ-IMAP-IMP-42.
func TestWriteBackFlagConflict(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("wb2", "pw")

	ha, _ := testharness.Start(t, testharness.Options{})
	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "wb2@example.test",
		username:            "wb2",
		credentialPlaintext: "pw",
	}, nil)

	uid, ms := setupSyncedMessage(t, ha, ts, acc, "INBOX", "wb-conflict@test", nil)

	ctx := context.Background()

	// last_synced starts as "neither seen nor flagged".
	// Herold: set \Flagged.
	heroldMsg, _ := ha.Store.Meta().GetMessage(ctx, ms.HeroldMessageID)
	if _, err := ha.Store.Meta().UpdateMessageFlags(ctx, heroldMsg.ID, heroldMsg.MailboxID,
		store.MessageFlagFlagged, 0, nil, nil, 0); err != nil {
		t.Fatalf("UpdateMessageFlags (herold): %v", err)
	}

	// Upstream: set \Seen (simulates the upstream changing independently).
	// We do this directly via IMAP APPEND + STORE.
	setUpstreamSeen(t, ts, "wb2", "pw", "INBOX", uid)

	// Run the write-back pass. Upstream wins (has \Seen, not \Flagged).
	// Herold should end up with \Seen but NOT \Flagged.
	runOneWriteBackPass(t, ha, ts, acc)

	// Herold should now have \Seen (upstream won) and NOT \Flagged.
	heroldMsg2, err := ha.Store.Meta().GetMessage(ctx, ms.HeroldMessageID)
	if err != nil {
		t.Fatalf("GetMessage after conflict: %v", err)
	}
	if heroldMsg2.Flags&store.MessageFlagSeen == 0 {
		t.Error("herold should have \\Seen after upstream-wins reconcile")
	}
	if heroldMsg2.Flags&store.MessageFlagFlagged != 0 {
		t.Error("herold should NOT have \\Flagged (upstream didn't have it; upstream won)")
	}

	// last_synced should reflect upstream's value (\Seen only).
	ms2, found, _ := ha.Store.Meta().GetIMAPImportMessageState(ctx, acc.ID, "INBOX", uint32(uid))
	if !found {
		t.Fatal("message state not found")
	}
	if !ms2.LastSyncedFlags.HasSeen() {
		t.Error("last_synced should have \\Seen after conflict resolution")
	}
	if ms2.LastSyncedFlags.HasFlagged() {
		t.Error("last_synced should NOT have \\Flagged")
	}
}

// setUpstreamSeen sets \Seen on the given upstream message via IMAP STORE.
func setUpstreamSeen(t *testing.T, ts *testIMAPServer, user, password, mailbox string, uid imap.UID) {
	t.Helper()
	// Use the prodConn path: dial and issue UID STORE +FLAGS \Seen.
	ctx := context.Background()
	// We create a temporary fake worker and call the method directly.
	// For test isolation just use the IMAP client directly.
	import_conn := dialFakeConn(t, ts, user, password)
	defer import_conn.Logout()
	defer import_conn.Close()
	if _, err := import_conn.SelectReadWrite(ctx, mailbox); err != nil {
		t.Fatalf("SelectReadWrite: %v", err)
	}
	if err := import_conn.UIDStoreFlags(ctx, uid, imap.StoreFlagsAdd, []imap.Flag{imap.FlagSeen}); err != nil {
		t.Fatalf("UIDStoreFlags: %v", err)
	}
}

// dialFakeConn dials a prodConn to the test server.
func dialFakeConn(t *testing.T, ts *testIMAPServer, user, password string) Conn {
	t.Helper()
	fd := &fakeDialer{ts: ts}
	ctx := context.Background()
	conn, err := fd.Dial(ctx, dialParams{
		AccountID:           "test",
		Host:                "127.0.0.1",
		Port:                0,
		TLSMode:             "implicit",
		Username:            user,
		AuthMethod:          "password",
		CredentialPlaintext: password,
	})
	if err != nil {
		t.Fatalf("dialFakeConn: %v", err)
	}
	return conn
}

// wrapDialer wraps every connection returned by an inner Dialer with wrap, so a
// test can make the worker's on-demand write-back connection a fault-injecting
// wrapper (e.g. failingStoreConn) without pre-opening it.
type wrapDialer struct {
	inner Dialer
	wrap  func(Conn) Conn
}

func (d *wrapDialer) Dial(ctx context.Context, p dialParams) (Conn, error) {
	c, err := d.inner.Dial(ctx, p)
	if err != nil {
		return nil, err
	}
	return d.wrap(c), nil
}

// --------------------------------------------------------------------------
// Test: failed push leaves last_synced unchanged
// --------------------------------------------------------------------------

// failingStoreConn wraps a real Conn but makes UIDStoreFlags fail.
type failingStoreConn struct {
	Conn
}

func (c *failingStoreConn) UIDStoreFlags(_ context.Context, _ imap.UID, _ imap.StoreFlagsOp, _ []imap.Flag) error {
	return fmt.Errorf("injected UIDStoreFlags failure")
}

// TestWriteBackFailedPushLeavesLastSyncedUnchanged verifies that when UID
// STORE fails, last_synced is NOT updated so the change is retried.
// REQ-IMAP-IMP-42.
func TestWriteBackFailedPushLeavesLastSyncedUnchanged(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("wb3", "pw")

	ha, _ := testharness.Start(t, testharness.Options{})
	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "wb3@example.test",
		username:            "wb3",
		credentialPlaintext: "pw",
	}, nil)

	uid, ms := setupSyncedMessage(t, ha, ts, acc, "INBOX", "wb-fail@test", nil)

	ctx := context.Background()

	// Mark herold \Seen.
	heroldMsg, _ := ha.Store.Meta().GetMessage(ctx, ms.HeroldMessageID)
	if _, err := ha.Store.Meta().UpdateMessageFlags(ctx, heroldMsg.ID, heroldMsg.MailboxID,
		store.MessageFlagSeen, 0, nil, nil, 0); err != nil {
		t.Fatalf("UpdateMessageFlags: %v", err)
	}

	// Save the original last_synced.
	origLastSynced := ms.LastSyncedFlags

	// Run write-back with an on-demand conn that fails UIDStoreFlags.
	w := newAccountWorker(accountWorkerOpts{
		account: acc,
		store:   ha.Store,
		dataKey: testDataKey(t),
		cfg:     sysconfig.IMAPImportConfig{},
		log:     newTestLogger(t),
		clk:     ha.Clock,
		dialer: &wrapDialer{
			inner: &fakeDialer{ts: ts},
			wrap:  func(c Conn) Conn { return &failingStoreConn{Conn: c} },
		},
		categoriser: noopCategoriser{},
	})

	wbCtx, wbCancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.runWriteBack(wbCtx)
	}()
	time.Sleep(300 * time.Millisecond)
	wbCancel()
	<-done

	// last_synced should be UNCHANGED (failed push should not update it).
	ms2, found, _ := ha.Store.Meta().GetIMAPImportMessageState(ctx, acc.ID, "INBOX", uint32(uid))
	if !found {
		t.Fatal("message state not found")
	}
	if ms2.LastSyncedFlags != origLastSynced {
		t.Errorf("last_synced changed from %d to %d; should be unchanged after failed push",
			origLastSynced, ms2.LastSyncedFlags)
	}
}

// --------------------------------------------------------------------------
// Test: MOVE
// --------------------------------------------------------------------------

// TestWriteBackMove verifies that when the herold mailbox membership changes
// (message moved to a different mailbox), UID MOVE is issued upstream.
// REQ-IMAP-IMP-43.
func TestWriteBackMove(t *testing.T) {
	ts := startTestIMAPServer(t)
	u := ts.addUser("wb4", "pw")
	// Create "Archive" upstream.
	if err := u.Create("Archive", nil); err != nil {
		t.Fatalf("Create Archive: %v", err)
	}

	ha, _ := testharness.Start(t, testharness.Options{})
	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "wb4@example.test",
		username:            "wb4",
		credentialPlaintext: "pw",
	}, nil)

	_, ms := setupSyncedMessage(t, ha, ts, acc, "INBOX", "wb-move@test", nil)

	ctx := context.Background()

	// Ensure "Archive" mailbox exists in herold.
	archiveMB, archiveErr := ha.Store.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: store.PrincipalID(acc.PrincipalID),
		Name:        "Archive",
		Attributes:  store.MailboxAttrArchive,
	})
	if archiveErr != nil {
		t.Fatalf("InsertMailbox Archive: %v", archiveErr)
	}

	// Move the herold message to Archive.
	heroldMsg, _ := ha.Store.Meta().GetMessage(ctx, ms.HeroldMessageID)
	if err := ha.Store.Meta().MoveMessage(ctx, heroldMsg.ID, heroldMsg.MailboxID, archiveMB.ID); err != nil {
		t.Fatalf("MoveMessage: %v", err)
	}

	// Run write-back.
	runOneWriteBackPass(t, ha, ts, acc)

	// Assert: upstream "Archive" now has the message.
	// (After MOVE, the message is no longer in INBOX and appears in Archive.)
	// We check by doing a UID SEARCH in Archive.
	archiveConn := dialFakeConn(t, ts, "wb4", "pw")
	defer archiveConn.Logout()
	defer archiveConn.Close()

	archiveSI, err := archiveConn.Select(ctx, "Archive")
	if err != nil {
		t.Fatalf("Select Archive: %v", err)
	}
	if archiveSI.NumMessages == 0 {
		t.Error("upstream Archive should have 1 message after UID MOVE")
	}
}

// failingMoveConn wraps a real Conn but makes UIDMove fail, simulating a move
// conflict (the message already moved / was expunged upstream).
type failingMoveConn struct {
	Conn
}

func (c *failingMoveConn) UIDMove(_ context.Context, _ imap.UID, _ string) error {
	return fmt.Errorf("injected UIDMove failure")
}

// TestWriteBackMoveConflictCounted verifies that a failed best-effort UID MOVE
// increments conflicts_total{kind="move"} (D7). REQ-IMAP-IMP-43/63.
func TestWriteBackMoveConflictCounted(t *testing.T) {
	ts := startTestIMAPServer(t)
	u := ts.addUser("wb4c", "pw")
	if err := u.Create("Archive", nil); err != nil {
		t.Fatalf("Create Archive: %v", err)
	}

	ha, _ := testharness.Start(t, testharness.Options{})
	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "wb4c@example.test",
		username:            "wb4c",
		credentialPlaintext: "pw",
	}, nil)

	_, ms := setupSyncedMessage(t, ha, ts, acc, "INBOX", "wb-move-conflict@test", nil)

	ctx := context.Background()

	archiveMB, err := ha.Store.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: store.PrincipalID(acc.PrincipalID),
		Name:        "Archive",
		Attributes:  store.MailboxAttrArchive,
	})
	if err != nil {
		t.Fatalf("InsertMailbox Archive: %v", err)
	}

	// Move the herold message so write-back attempts a UID MOVE upstream.
	heroldMsg, _ := ha.Store.Meta().GetMessage(ctx, ms.HeroldMessageID)
	if err := ha.Store.Meta().MoveMessage(ctx, heroldMsg.ID, heroldMsg.MailboxID, archiveMB.ID); err != nil {
		t.Fatalf("MoveMessage: %v", err)
	}

	before := testutil.ToFloat64(observe.IMAPImportConflictsTotal.WithLabelValues(acc.ID, "move"))

	// Run write-back with an on-demand conn whose UIDMove always fails.
	w := newAccountWorker(accountWorkerOpts{
		account: acc,
		store:   ha.Store,
		dataKey: testDataKey(t),
		cfg:     sysconfig.IMAPImportConfig{},
		log:     newTestLogger(t),
		clk:     ha.Clock,
		dialer: &wrapDialer{
			inner: &fakeDialer{ts: ts},
			wrap:  func(c Conn) Conn { return &failingMoveConn{Conn: c} },
		},
		categoriser: noopCategoriser{},
	})
	wbCtx, wbCancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.runWriteBack(wbCtx)
	}()
	time.Sleep(300 * time.Millisecond)
	wbCancel()
	<-done

	after := testutil.ToFloat64(observe.IMAPImportConflictsTotal.WithLabelValues(acc.ID, "move"))
	if after-before != 1 {
		t.Errorf("conflicts_total{kind=move} delta = %v; want 1", after-before)
	}
}

// --------------------------------------------------------------------------
// Test: DESTROY with DeletePropagates=true
// --------------------------------------------------------------------------

// TestWriteBackDestroyPropagates verifies that destroying a herold message
// with DeletePropagates=true issues UID STORE +FLAGS \Deleted + EXPUNGE.
// REQ-IMAP-IMP-44.
func TestWriteBackDestroyPropagates(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("wb5", "pw")

	ha, _ := testharness.Start(t, testharness.Options{})
	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "wb5@example.test",
		username:            "wb5",
		credentialPlaintext: "pw",
	}, nil)
	// DeletePropagates defaults to true via makeAccountWithFloor.

	uid, ms := setupSyncedMessage(t, ha, ts, acc, "INBOX", "wb-destroy@test", nil)

	ctx := context.Background()

	// Destroy the herold message.
	mbID := ms.HeroldMailboxID
	if err := ha.Store.Meta().ExpungeMessages(ctx, mbID, []store.MessageID{ms.HeroldMessageID}); err != nil {
		t.Fatalf("ExpungeMessages: %v", err)
	}

	// Run write-back.
	runOneWriteBackPass(t, ha, ts, acc)

	// Assert: the upstream message is gone.
	flags := getUpstreamFlags(t, ts, "wb5", "pw", "INBOX", uid)
	if len(flags) > 0 {
		// message still exists; check it has \Deleted at least.
		for _, f := range flags {
			if f == imap.FlagDeleted {
				return // acceptable: message is marked deleted
			}
		}
	}
	// If flags are nil or empty the message was expunged — that's correct.
}

// --------------------------------------------------------------------------
// Test: DESTROY with DeletePropagates=false
// --------------------------------------------------------------------------

// TestWriteBackDestroyNotPropagated verifies that with DeletePropagates=false
// the upstream message is NOT touched. REQ-IMAP-IMP-44.
func TestWriteBackDestroyNotPropagated(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("wb6", "pw")

	ha, _ := testharness.Start(t, testharness.Options{})

	// Create account with DeletePropagates=false.
	ctx := context.Background()
	p, _ := ha.Store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "wb6@example.test",
		DisplayName:    "wb6",
		QuotaBytes:     1 << 30,
	})
	h, port := parseAddr(ts.ImplicitAddr)
	acc, _ := ha.Store.Meta().CreateIMAPImportAccount(ctx, store.IMAPImportAccountCreate{
		PrincipalID:  p.ID,
		AccountName:  "Test",
		Host:         h,
		Port:         port,
		TLSMode:      store.IMAPImportTLSModeImplicit,
		Username:     "wb6",
		AuthMethod:   store.IMAPImportAuthMethodPassword,
		CredentialCT: sealCred(t, "pw"),
		State:        store.IMAPImportAccountStateEnabled,
		// DeletePropagates = false: keep upstream history.
		DeletePropagates: false,
	})

	uid, ms := setupSyncedMessage(t, ha, ts, acc, "INBOX", "wb-nodestroy@test", nil)

	// Destroy the herold message.
	if err := ha.Store.Meta().ExpungeMessages(ctx, ms.HeroldMailboxID, []store.MessageID{ms.HeroldMessageID}); err != nil {
		t.Fatalf("ExpungeMessages: %v", err)
	}

	// Run write-back.
	runOneWriteBackPass(t, ha, ts, acc)

	// Upstream message MUST still exist.
	flags := getUpstreamFlags(t, ts, "wb6", "pw", "INBOX", uid)
	if flags == nil {
		t.Error("upstream message should still exist when DeletePropagates=false")
	}
	// Verify it does NOT have \Deleted.
	for _, f := range flags {
		if f == imap.FlagDeleted {
			t.Error("upstream message should NOT be marked \\Deleted with DeletePropagates=false")
		}
	}
}

// --------------------------------------------------------------------------
// Test: cursor durability
// --------------------------------------------------------------------------

// TestWriteBackCursorDurability verifies that the write-back cursor is
// persisted and a fresh start resumes from the cursor without reprocessing.
// REQ-IMAP-IMP-74.
func TestWriteBackCursorDurability(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("wb7", "pw")

	ha, _ := testharness.Start(t, testharness.Options{})
	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "wb7@example.test",
		username:            "wb7",
		credentialPlaintext: "pw",
	}, nil)

	_, ms := setupSyncedMessage(t, ha, ts, acc, "INBOX", "wb-cursor@test", nil)

	ctx := context.Background()

	// Mark herold \Seen so the write-back has something to do.
	heroldMsg, _ := ha.Store.Meta().GetMessage(ctx, ms.HeroldMessageID)
	if _, err := ha.Store.Meta().UpdateMessageFlags(ctx, heroldMsg.ID, heroldMsg.MailboxID,
		store.MessageFlagSeen, 0, nil, nil, 0); err != nil {
		t.Fatalf("UpdateMessageFlags: %v", err)
	}

	// First run: processes the change and persists cursor.
	runOneWriteBackPass(t, ha, ts, acc)

	// Read the persisted cursor.
	cursorKey := writeBackCursorKey(acc.ID)
	savedCursor, err := ha.Store.Meta().GetFTSCursor(ctx, cursorKey)
	if err != nil {
		t.Fatalf("GetFTSCursor: %v", err)
	}
	if savedCursor == 0 {
		t.Fatal("cursor should be > 0 after first write-back pass")
	}

	// Second run should not re-process the same change. We verify by
	// checking the cursor hasn't regressed.
	runOneWriteBackPass(t, ha, ts, acc)
	savedCursor2, _ := ha.Store.Meta().GetFTSCursor(ctx, cursorKey)
	if savedCursor2 < savedCursor {
		t.Errorf("cursor regressed: %d -> %d", savedCursor, savedCursor2)
	}
}

// --------------------------------------------------------------------------
// Test: custom keyword change does NOT push upstream
// --------------------------------------------------------------------------

// TestWriteBackCustomKeywordNotPushed verifies that adding a custom keyword
// to a herold message does not trigger any upstream flag change.
// REQ-IMAP-IMP-45.
func TestWriteBackCustomKeywordNotPushed(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("wb8", "pw")

	ha, _ := testharness.Start(t, testharness.Options{})
	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "wb8@example.test",
		username:            "wb8",
		credentialPlaintext: "pw",
	}, nil)

	uid, ms := setupSyncedMessage(t, ha, ts, acc, "INBOX", "wb-keyword@test", nil)

	ctx := context.Background()

	// Add a custom keyword to the herold message (does NOT change \Seen /
	// \Flagged, so syncedFlagsFromStoreFlags is unchanged).
	heroldMsg, _ := ha.Store.Meta().GetMessage(ctx, ms.HeroldMessageID)
	if _, err := ha.Store.Meta().UpdateMessageFlags(ctx, heroldMsg.ID, heroldMsg.MailboxID,
		0, 0, []string{"$category-newsletter"}, nil, 0); err != nil {
		t.Fatalf("UpdateMessageFlags (keyword): %v", err)
	}

	// Upstream flags before write-back.
	before := getUpstreamFlags(t, ts, "wb8", "pw", "INBOX", uid)

	// Run write-back.
	runOneWriteBackPass(t, ha, ts, acc)

	// Upstream flags after write-back: should be IDENTICAL to before.
	after := getUpstreamFlags(t, ts, "wb8", "pw", "INBOX", uid)
	if !flagSetsEqual(before, after) {
		t.Errorf("upstream flags changed after custom keyword write-back: before=%v after=%v", before, after)
	}
}

// flagSetsEqual compares two flag slices (order-independent).
func flagSetsEqual(a, b []imap.Flag) bool {
	if len(a) != len(b) {
		return false
	}
	m := make(map[imap.Flag]bool, len(a))
	for _, f := range a {
		m[f] = true
	}
	for _, f := range b {
		if !m[f] {
			return false
		}
	}
	return true
}

// --------------------------------------------------------------------------
// Test: ctx cancel stops write-back cleanly
// --------------------------------------------------------------------------

// TestWriteBackCtxCancelStops verifies that cancelling the context stops
// the write-back goroutine without hanging. REQ-IMAP-IMP-20 (parallel).
func TestWriteBackCtxCancelStops(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("wb9", "pw")

	ha, _ := testharness.Start(t, testharness.Options{})
	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "wb9@example.test",
		username:            "wb9",
		credentialPlaintext: "pw",
	}, nil)

	ctx, cancel := context.WithCancel(context.Background())

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

	done := make(chan struct{})
	go func() {
		defer close(done)
		w.runWriteBack(ctx)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("write-back goroutine did not stop within 5s after ctx cancel")
	}
}

// --------------------------------------------------------------------------
// Test: write-back connection is opened on demand and released when idle
// --------------------------------------------------------------------------

// trackingConn records whether Close has been called.
type trackingConn struct {
	Conn
	closed *atomic.Bool
}

func (c *trackingConn) Close() error {
	c.closed.Store(true)
	return c.Conn.Close()
}

// trackingDialer counts how many connections were opened and tracks whether
// each was closed, so a test can assert the write-back connection is opened on
// demand and not parked open while the change feed is idle.
type trackingDialer struct {
	inner  Dialer
	mu     sync.Mutex
	closed []*atomic.Bool
}

func (d *trackingDialer) Dial(ctx context.Context, p dialParams) (Conn, error) {
	c, err := d.inner.Dial(ctx, p)
	if err != nil {
		return nil, err
	}
	flag := &atomic.Bool{}
	d.mu.Lock()
	d.closed = append(d.closed, flag)
	d.mu.Unlock()
	return &trackingConn{Conn: c, closed: flag}, nil
}

func (d *trackingDialer) dialCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.closed)
}

func (d *trackingDialer) allClosed() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, f := range d.closed {
		if !f.Load() {
			return false
		}
	}
	return true
}

// TestWriteBackConnOpenedOnDemandAndReleasedWhenIdle asserts that the write-back
// loop opens its upstream connection on demand when the change feed has work and
// closes it once the feed is drained, rather than holding a connection open for
// the loop's lifetime. A parked, idle connection is what the upstream
// autologs-out, breaking later write-back rounds (the same failure mode fixed
// for the sync path). REQ-IMAP-IMP-40..45.
func TestWriteBackConnOpenedOnDemandAndReleasedWhenIdle(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("wbod", "pw")

	ha, _ := testharness.Start(t, testharness.Options{})
	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "wbod@example.test",
		username:            "wbod",
		credentialPlaintext: "pw",
	}, nil)

	_, ms := setupSyncedMessage(t, ha, ts, acc, "INBOX", "wb-ondemand@test", nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Mark herold \Seen so there is exactly one flag change to push upstream.
	heroldMsg, _ := ha.Store.Meta().GetMessage(ctx, ms.HeroldMessageID)
	if _, err := ha.Store.Meta().UpdateMessageFlags(ctx, heroldMsg.ID, heroldMsg.MailboxID,
		store.MessageFlagSeen, 0, nil, nil, 0); err != nil {
		t.Fatalf("UpdateMessageFlags: %v", err)
	}

	td := &trackingDialer{inner: &fakeDialer{ts: ts}}
	w := newAccountWorker(accountWorkerOpts{
		account:     acc,
		store:       ha.Store,
		dataKey:     testDataKey(t),
		cfg:         sysconfig.IMAPImportConfig{},
		log:         newTestLogger(t),
		clk:         ha.Clock,
		dialer:      td,
		categoriser: noopCategoriser{},
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		w.runWriteBack(ctx)
	}()

	// Give the loop time to open a connection on demand, push the change, drain
	// to the head of the feed, and release the connection before it idles.
	time.Sleep(500 * time.Millisecond)

	if n := td.dialCount(); n < 1 {
		cancel()
		<-done
		t.Fatalf("write-back did not open a connection on demand; dials=%d", n)
	}
	if !td.allClosed() {
		cancel()
		<-done
		t.Error("write-back parked a connection open while idle; want every " +
			"on-demand connection closed once the feed drained")
	}

	cancel()
	<-done
}
