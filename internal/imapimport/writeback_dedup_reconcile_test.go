package imapimport

// writeback_dedup_reconcile_test.go covers the message-scoped flag reconcile
// (re #435): a Message-ID dedup hit (e.g. a To copy and a Cc copy of the same
// message landing in the same upstream folder) folds two upstream copies
// onto one herold message, which then carries two IMAPImportMessageState
// rows. The reconcile must resolve across every row for the message rather
// than one row at a time -- see reconcileMessageFlags in writeback.go.
// REQ-IMAP-IMP-42.

import (
	"context"
	"testing"
	"time"

	imap "github.com/emersion/go-imap/v2"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
)

// appendDedupPair appends the same message (same Message-ID) twice to
// mailbox on the upstream, modelling a To-copy/Cc-copy pair the importer
// folds onto one herold message via Message-ID dedup (REQ-IMAP-IMP-30).
func appendDedupPair(t *testing.T, ts *testIMAPServer, user, password, mailbox, msgIDHeader string) (uidA, uidB imap.UID) {
	t.Helper()
	d := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	raw := buildRFC822(msgIDHeader, "Dup test", d)
	uidA = appendToServer(t, ts, user, password, mailbox, raw, nil, d)
	uidB = appendToServer(t, ts, user, password, mailbox, raw, nil, d)
	return uidA, uidB
}

// TestWriteBackReconcilePushesEveryUpstreamCopy is acceptance test 1 for
// #435: mark a dedup-folded herold message read, run write-back once, then
// run a down-sync round. Before the fix, write-back's
// GetIMAPImportMessageStateByMessage picked one arbitrary row per herold
// message, so the read reached only one of the two upstream copies and the
// other row's LastSyncedFlags never advanced past its ingest-time baseline
// -- a later, unrelated drift on that stale row could then revert the read
// (see TestDownSyncCoherentOutcomeSurvivesUnrelatedDriftOnLaggingCopy below).
// The fix pushes the read to every row for the message in one reconcile
// pass, so both survive and both record the pushed value.
func TestWriteBackReconcilePushesEveryUpstreamCopy(t *testing.T) {
	for _, be := range ownSentDedupBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			ts := startTestIMAPServer(t)
			ts.addUser("dd1", "pw")

			ha, _ := testharness.Start(t, testharness.Options{Store: be.st, Clock: be.clk})
			acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
				email:               "dd1@example.test",
				username:            "dd1",
				credentialPlaintext: "pw",
			}, nil)

			uidTo, uidCc := appendDedupPair(t, ts, "dd1", "pw", "INBOX", "dd1-dup@test")

			ctx := context.Background()
			if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
				t.Fatalf("initial sync: %v", err)
			}

			msTo, found, err := ha.Store.Meta().GetIMAPImportMessageState(ctx, acc.ID, "INBOX", uint32(uidTo))
			if err != nil || !found {
				t.Fatalf("state (To copy): found=%v err=%v", found, err)
			}
			msCc, found, err := ha.Store.Meta().GetIMAPImportMessageState(ctx, acc.ID, "INBOX", uint32(uidCc))
			if err != nil || !found {
				t.Fatalf("state (Cc copy): found=%v err=%v", found, err)
			}
			if msTo.HeroldMessageID != msCc.HeroldMessageID {
				t.Fatalf("precondition: dedup should fold both copies onto one herold message, got %v and %v",
					msTo.HeroldMessageID, msCc.HeroldMessageID)
			}
			msgID := msTo.HeroldMessageID

			// Mark the herold message read.
			heroldMsg, err := ha.Store.Meta().GetMessage(ctx, msgID)
			if err != nil {
				t.Fatalf("GetMessage: %v", err)
			}
			if heroldMsg.Flags&store.MessageFlagSeen != 0 {
				t.Fatal("precondition: herold message should start unseen")
			}
			if _, err := ha.Store.Meta().UpdateMessageFlags(ctx, heroldMsg.ID, heroldMsg.MailboxID,
				store.MessageFlagSeen, 0, nil, nil, 0); err != nil {
				t.Fatalf("UpdateMessageFlags: %v", err)
			}

			// Write-back, then a down-sync round.
			runOneWriteBackPass(t, ha, ts, acc)
			if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
				t.Fatalf("down-sync round: %v", err)
			}

			// The message must stay seen.
			heroldMsg2, err := ha.Store.Meta().GetMessage(ctx, msgID)
			if err != nil {
				t.Fatalf("GetMessage after reconcile: %v", err)
			}
			if heroldMsg2.Flags&store.MessageFlagSeen == 0 {
				t.Error("herold message should stay \\Seen after write-back + down-sync")
			}

			// Both state rows must record the pushed value.
			msTo2, found, err := ha.Store.Meta().GetIMAPImportMessageState(ctx, acc.ID, "INBOX", uint32(uidTo))
			if err != nil || !found {
				t.Fatalf("state after reconcile (To copy): found=%v err=%v", found, err)
			}
			msCc2, found, err := ha.Store.Meta().GetIMAPImportMessageState(ctx, acc.ID, "INBOX", uint32(uidCc))
			if err != nil || !found {
				t.Fatalf("state after reconcile (Cc copy): found=%v err=%v", found, err)
			}
			if !msTo2.LastSyncedFlags.HasSeen() {
				t.Error("To copy's last_synced should have \\Seen after the push")
			}
			if !msCc2.LastSyncedFlags.HasSeen() {
				t.Error("Cc copy's last_synced should have \\Seen after the push (this is the row the old single-row lookup could skip)")
			}

			// Both upstream copies must actually carry \Seen.
			for _, uid := range []imap.UID{uidTo, uidCc} {
				flags := getUpstreamFlags(t, ts, "dd1", "pw", "INBOX", uid)
				seen := false
				for _, f := range flags {
					if f == imap.FlagSeen {
						seen = true
					}
				}
				if !seen {
					t.Errorf("upstream uid %d should carry \\Seen after write-back", uid)
				}
			}
		})
	}
}

// TestDownSyncCoherentOutcomeSurvivesUnrelatedDriftOnLaggingCopy demonstrates
// the durability failure #435 reports and confirms the fix closes it: once
// the lagging copy's upstream flags drift for a reason that has nothing to
// do with \Seen (a \Flagged toggle here; the production report's own
// upstream account is equally capable of doing this on its own schedule),
// the drift must not revert a read that was already confirmed via the other
// copy.
func TestDownSyncCoherentOutcomeSurvivesUnrelatedDriftOnLaggingCopy(t *testing.T) {
	for _, be := range ownSentDedupBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			ts := startTestIMAPServer(t)
			ts.addUser("dd2", "pw")

			ha, _ := testharness.Start(t, testharness.Options{Store: be.st, Clock: be.clk})
			acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
				email:               "dd2@example.test",
				username:            "dd2",
				credentialPlaintext: "pw",
			}, nil)

			uidTo, uidCc := appendDedupPair(t, ts, "dd2", "pw", "INBOX", "dd2-dup@test")

			ctx := context.Background()
			if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
				t.Fatalf("initial sync: %v", err)
			}
			msTo, found, err := ha.Store.Meta().GetIMAPImportMessageState(ctx, acc.ID, "INBOX", uint32(uidTo))
			if err != nil || !found {
				t.Fatalf("state (To copy): found=%v err=%v", found, err)
			}
			msgID := msTo.HeroldMessageID

			heroldMsg, err := ha.Store.Meta().GetMessage(ctx, msgID)
			if err != nil {
				t.Fatalf("GetMessage: %v", err)
			}
			if _, err := ha.Store.Meta().UpdateMessageFlags(ctx, heroldMsg.ID, heroldMsg.MailboxID,
				store.MessageFlagSeen, 0, nil, nil, 0); err != nil {
				t.Fatalf("UpdateMessageFlags: %v", err)
			}

			runOneWriteBackPass(t, ha, ts, acc)
			if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
				t.Fatalf("down-sync round 1: %v", err)
			}
			if ms2flags(t, ha, msgID)&store.MessageFlagSeen == 0 {
				t.Fatal("precondition: message should be \\Seen after the first reconcile")
			}

			// An unrelated event on the lagging copy: \Flagged is toggled on
			// the Cc copy directly upstream. Nothing about this touches
			// \Seen on either copy.
			setUpstreamFlagged(t, ts, "dd2", "pw", "INBOX", uidCc)

			runOneWriteBackPass(t, ha, ts, acc)
			if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
				t.Fatalf("down-sync round 2: %v", err)
			}

			if ms2flags(t, ha, msgID)&store.MessageFlagSeen == 0 {
				t.Error("herold message should still be \\Seen: an unrelated \\Flagged drift on the other copy must not revert the read")
			}
		})
	}
}

// TestDownSyncCoherentOutcomeMixedUpstreamSeen is acceptance test 2 for
// #435: two upstream copies dedup onto one herold message; one is marked
// \Seen upstream (independently, no herold-side action), the other stays
// unseen. The reconcile must produce one coherent outcome for herold rather
// than depending on which copy a poll happens to look at first.
//
// Chosen outcome: \Seen wins if set on ANY copy. A message read via any one
// of its upstream placements has genuinely been read, and this also closes
// the durability gap #435 reports -- a copy that is never advanced can no
// longer drag an already-confirmed read back to unseen (see
// TestDownSyncCoherentOutcomeSurvivesUnrelatedDriftOnLaggingCopy above).
func TestDownSyncCoherentOutcomeMixedUpstreamSeen(t *testing.T) {
	for _, be := range ownSentDedupBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			ts := startTestIMAPServer(t)
			ts.addUser("dd3", "pw")

			ha, _ := testharness.Start(t, testharness.Options{Store: be.st, Clock: be.clk})
			acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
				email:               "dd3@example.test",
				username:            "dd3",
				credentialPlaintext: "pw",
			}, nil)

			uidTo, uidCc := appendDedupPair(t, ts, "dd3", "pw", "INBOX", "dd3-dup@test")

			ctx := context.Background()
			if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
				t.Fatalf("initial sync: %v", err)
			}
			msTo, found, err := ha.Store.Meta().GetIMAPImportMessageState(ctx, acc.ID, "INBOX", uint32(uidTo))
			if err != nil || !found {
				t.Fatalf("state (To copy): found=%v err=%v", found, err)
			}
			msgID := msTo.HeroldMessageID

			if ms2flags(t, ha, msgID)&store.MessageFlagSeen != 0 {
				t.Fatal("precondition: herold message should start unseen")
			}

			conflictsBefore := testutil.ToFloat64(observe.IMAPImportConflictsTotal.WithLabelValues(acc.ID, "flag"))

			// Only the To copy is marked \Seen upstream; the Cc copy is left
			// unseen. No herold-side action at all.
			setUpstreamSeen(t, ts, "dd3", "pw", "INBOX", uidTo)

			if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
				t.Fatalf("down-sync round: %v", err)
			}

			if ms2flags(t, ha, msgID)&store.MessageFlagSeen == 0 {
				t.Error("herold message should be \\Seen: one of its two upstream copies is \\Seen")
			}

			// The Cc copy itself stays unseen upstream -- the coherent
			// outcome is about herold's single flag, not about forcing every
			// upstream copy to match.
			flags := getUpstreamFlags(t, ts, "dd3", "pw", "INBOX", uidCc)
			for _, f := range flags {
				if f == imap.FlagSeen {
					t.Error("Cc copy should remain unseen upstream; only the To copy was marked seen")
				}
			}

			// This is a genuine only-upstream-changed case, not a conflict:
			// herold was never locally touched.
			if got := testutil.ToFloat64(observe.IMAPImportConflictsTotal.WithLabelValues(acc.ID, "flag")); got != conflictsBefore {
				t.Errorf("conflicts_total{kind=flag} should not increment for a pure upstream-side change, delta=%v", got-conflictsBefore)
			}
		})
	}
}

// setUpstreamFlagged sets \Flagged on the given upstream message via IMAP
// STORE, without touching \Seen.
func setUpstreamFlagged(t *testing.T, ts *testIMAPServer, user, password, mailbox string, uid imap.UID) {
	t.Helper()
	ctx := context.Background()
	conn := dialFakeConn(t, ts, user, password)
	defer conn.Logout()
	defer conn.Close()
	if _, err := conn.SelectReadWrite(ctx, mailbox); err != nil {
		t.Fatalf("SelectReadWrite: %v", err)
	}
	if err := conn.UIDStoreFlags(ctx, uid, imap.StoreFlagsAdd, []imap.Flag{imap.FlagFlagged}); err != nil {
		t.Fatalf("UIDStoreFlags: %v", err)
	}
}
