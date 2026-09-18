package imapimport

// restore_archive_downsync_test.go is the acceptance test for the
// `imapimport restore-archive` repair CLI's message_state repair (re #376,
// second round follow-up): a message shaped like the production report's
// messages 3737/3746 -- mirrored into INBOX by a pre-#376-fix sync pass,
// unseen, and carrying a Sent-role membership (the principal-sent shape) --
// is repaired with RestoreArchivePlacement, then a down-sync round runs
// against the in-process IMAP server with the upstream copy still unseen.
// Before the message_state repair landed, the repair moved the mailbox
// membership but left the state row's HeroldMailboxID/LastSyncedFlags
// pointing at the stale INBOX placement with an unseen baseline; this test
// proves empirically -- not by code reading -- that the repaired row
// survives a down-sync round: the INBOX membership does not come back and
// $seen holds. Runs on both backends per STANDARDS.md.

import (
	"context"
	"testing"

	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
)

func TestRestoreArchivePlacement_SurvivesDownSync(t *testing.T) {
	for _, be := range ownSentDedupBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			ts := startTestIMAPServer(t)
			u := ts.addUser("ra1", "pw")
			if err := u.Create("Sent", nil); err != nil {
				t.Fatalf("create Sent: %v", err)
			}

			ha, _ := testharness.Start(t, testharness.Options{Store: be.st, Clock: be.clk})
			acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
				email:               "ra1@example.test",
				username:            "ra1",
				credentialPlaintext: "pw",
			}, nil)
			ctx := context.Background()
			pid := acc.PrincipalID

			// Mirror a message into INBOX via the normal sync path: the
			// pre-#376-fix shape, unseen on both sides, LastSyncedFlags
			// recorded 0.
			uid, ms := setupSyncedMessage(t, ha, ts, acc, "INBOX", "restore-archive-downsync@test", nil)
			if ms.LastSyncedFlags != 0 {
				t.Fatalf("precondition: want LastSyncedFlags 0 (unseen), got %v", ms.LastSyncedFlags)
			}

			// Give the herold message a Sent-role membership too -- the
			// production shape: a Cc-loop copy of a message the principal
			// sent, dedup-hit into INBOX by the account's own IMAP folder.
			sentMB, err := ha.Store.Meta().InsertMailbox(ctx, store.Mailbox{
				PrincipalID: pid, Name: "Sent", Attributes: store.MailboxAttrSent,
			})
			if err != nil {
				t.Fatalf("InsertMailbox (Sent): %v", err)
			}
			if _, _, err := ha.Store.Meta().AddMessageToMailbox(ctx, ms.HeroldMessageID, sentMB.ID); err != nil {
				t.Fatalf("AddMessageToMailbox (Sent): %v", err)
			}

			// Run the repair.
			res := RestoreArchivePlacement(ctx, ha.Store, pid, ms.HeroldMessageID, false, false)
			if res.Action != RestoreArchiveActionMoved {
				t.Fatalf("RestoreArchivePlacement result = %+v; want moved-to-archive", res)
			}

			archiveMBID := getMailboxID(t, ha.Store, pid, "Archive")
			if archiveMBID == 0 {
				t.Fatal("Archive mailbox was not created by the repair")
			}

			// The message_state row must now point at the Archive placement
			// and record the forced $seen, not the stale INBOX/unseen state
			// the pre-fix sync left behind.
			msAfterRepair, found, err := ha.Store.Meta().GetIMAPImportMessageState(ctx, acc.ID, "INBOX", uint32(uid))
			if err != nil || !found {
				t.Fatalf("GetIMAPImportMessageState (after repair): found=%v err=%v", found, err)
			}
			if msAfterRepair.HeroldMailboxID != archiveMBID {
				t.Errorf("message_state.HeroldMailboxID = %d; want Archive (%d)", msAfterRepair.HeroldMailboxID, archiveMBID)
			}
			if !msAfterRepair.LastSyncedFlags.HasSeen() {
				t.Error("message_state.LastSyncedFlags should record \\Seen after the repair")
			}

			// The upstream copy is still unseen. A down-sync round must not
			// resurface the INBOX membership or clear $seen.
			if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
				t.Fatalf("sync round after repair: %v", err)
			}

			msg, err := ha.Store.Meta().GetMessage(ctx, ms.HeroldMessageID)
			if err != nil {
				t.Fatalf("GetMessage (after down-sync): %v", err)
			}
			var archiveMembership *store.MessageMailbox
			for i := range msg.Mailboxes {
				if msg.Mailboxes[i].MailboxID == getMailboxID(t, ha.Store, pid, "INBOX") {
					t.Fatalf("INBOX membership resurfaced after down-sync: mailboxes = %+v", msg.Mailboxes)
				}
				if msg.Mailboxes[i].MailboxID == archiveMBID {
					archiveMembership = &msg.Mailboxes[i]
				}
			}
			if archiveMembership == nil {
				t.Fatalf("Archive membership missing after down-sync: mailboxes = %+v", msg.Mailboxes)
			}
			if archiveMembership.Flags&store.MessageFlagSeen == 0 {
				t.Error("Archive membership lost $seen after down-sync of the still-unseen upstream copy")
			}
		})
	}
}
