package imapimport

// downsync_test.go covers D2+D3: upstream-only \Seen / \Flagged changes are
// applied down to herold on a sync round. The in-process imapmemserver does
// not advertise CONDSTORE, so these tests exercise the non-CONDSTORE fallback
// (bounded flag re-fetch of the known UID set). The CONDSTORE CHANGEDSINCE
// branch is implemented in downsync.go / dialer.go but is only reachable
// against a real CONDSTORE-advertising upstream. REQ-IMAP-IMP-40/24/42.

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
)

// TestDownSyncUpstreamSeenAppliedToHerold verifies that a pure-upstream \Seen
// set is reflected in herold after the next sync round. REQ-IMAP-IMP-40/42.
func TestDownSyncUpstreamSeenAppliedToHerold(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("ds1", "pw")

	ha, _ := testharness.Start(t, testharness.Options{})
	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "ds1@example.test",
		username:            "ds1",
		credentialPlaintext: "pw",
	}, nil)

	// Mirror a message that starts unseen on both sides.
	uid, ms := setupSyncedMessage(t, ha, ts, acc, "INBOX", "ds-seen@test", nil)
	ctx := context.Background()

	before := ms2flags(t, ha, ms.HeroldMessageID)
	if before&store.MessageFlagSeen != 0 {
		t.Fatal("precondition: herold message should start unseen")
	}

	propBefore := testutil.ToFloat64(observe.IMAPImportFlagsPropagatedTotal.WithLabelValues(acc.ID, "down"))

	// Upstream marks it \Seen (e.g. the user read it in the upstream webmail).
	setUpstreamSeen(t, ts, "ds1", "pw", "INBOX", uid)

	// A sync round must pull the upstream-only change down into herold.
	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("sync round: %v", err)
	}

	if ms2flags(t, ha, ms.HeroldMessageID)&store.MessageFlagSeen == 0 {
		t.Error("herold message should be \\Seen after down-sync of the upstream change")
	}

	// last_synced must now reflect the upstream value.
	ms2, found, _ := ha.Store.Meta().GetIMAPImportMessageState(ctx, acc.ID, "INBOX", uint32(uid))
	if !found {
		t.Fatal("message state missing after down-sync")
	}
	if !ms2.LastSyncedFlags.HasSeen() {
		t.Error("last_synced should have \\Seen after down-sync")
	}

	if got := testutil.ToFloat64(observe.IMAPImportFlagsPropagatedTotal.WithLabelValues(acc.ID, "down")); got-propBefore != 1 {
		t.Errorf("flags_propagated_total{direction=down} delta = %v; want 1", got-propBefore)
	}
}

// TestDownSyncConflictUpstreamWins verifies that when both sides changed since
// the last sync, the down-sync applies the upstream value and counts a flag
// conflict (upstream-authoritative). REQ-IMAP-IMP-42.
func TestDownSyncConflictUpstreamWins(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("ds2", "pw")

	ha, _ := testharness.Start(t, testharness.Options{})
	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "ds2@example.test",
		username:            "ds2",
		credentialPlaintext: "pw",
	}, nil)

	uid, ms := setupSyncedMessage(t, ha, ts, acc, "INBOX", "ds-conflict@test", nil)
	ctx := context.Background()

	// herold changes: set \Flagged (not pushed upstream — write-back loop is
	// not running in this test).
	heroldMsg, _ := ha.Store.Meta().GetMessage(ctx, ms.HeroldMessageID)
	if _, err := ha.Store.Meta().UpdateMessageFlags(ctx, heroldMsg.ID, heroldMsg.MailboxID,
		store.MessageFlagFlagged, 0, nil, nil, 0); err != nil {
		t.Fatalf("UpdateMessageFlags (herold): %v", err)
	}
	// upstream changes independently: set \Seen.
	setUpstreamSeen(t, ts, "ds2", "pw", "INBOX", uid)

	conflictsBefore := testutil.ToFloat64(observe.IMAPImportConflictsTotal.WithLabelValues(acc.ID, "flag"))

	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("sync round: %v", err)
	}

	// Upstream wins: herold ends with \Seen and NOT \Flagged.
	flags := ms2flags(t, ha, ms.HeroldMessageID)
	if flags&store.MessageFlagSeen == 0 {
		t.Error("herold should have \\Seen after upstream-wins down-sync")
	}
	if flags&store.MessageFlagFlagged != 0 {
		t.Error("herold should NOT have \\Flagged (upstream didn't have it; upstream won)")
	}
	if got := testutil.ToFloat64(observe.IMAPImportConflictsTotal.WithLabelValues(acc.ID, "flag")); got-conflictsBefore != 1 {
		t.Errorf("conflicts_total{kind=flag} delta = %v; want 1", got-conflictsBefore)
	}
}

func ms2flags(t *testing.T, ha *testharness.Server, id store.MessageID) store.MessageFlags {
	t.Helper()
	m, err := ha.Store.Meta().GetMessage(context.Background(), id)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	return m.Flags
}
