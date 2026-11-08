package imapimport

// writeback_ghost_test.go pins the ghost-row handling of #558: a message can
// carry several message_state rows, one of which names a UID its folder does
// not hold. Write-back must act on the row that addresses the message, never
// through the ghost, and never expunge an upstream message it cannot tie to
// the deleted herold message.

import (
	"context"
	"testing"

	imap "github.com/emersion/go-imap/v2"

	"github.com/hanshuebner/herold/internal/store"
)

const ghostUID = 29138

// putGhostFirst replaces the message's state rows with the ghost row followed
// by the real one, so the ghost has the lowest row id / heap position on both
// backends.
func (f *moveFixture) putGhostFirst(t *testing.T) (real store.IMAPImportMessageState) {
	t.Helper()
	ctx := context.Background()
	meta := f.ha.Store.Meta()
	rows, err := meta.ListIMAPImportMessageStatesByMessage(ctx, f.msgID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("setup rows = %+v, %v", rows, err)
	}
	real = rows[0]
	if err := meta.DeleteIMAPImportMessageState(ctx, real.AccountID, real.UpstreamFolder, real.UpstreamUID); err != nil {
		t.Fatalf("delete real row: %v", err)
	}
	ghost := real
	ghost.UpstreamUID = ghostUID
	ghost.MappedMailboxID = f.inbox
	if err := meta.UpsertIMAPImportMessageState(ctx, ghost); err != nil {
		t.Fatalf("upsert ghost: %v", err)
	}
	if err := meta.UpsertIMAPImportMessageState(ctx, real); err != nil {
		t.Fatalf("upsert real: %v", err)
	}
	return real
}

// TestWriteBackMove_GhostRowFirst_MovesRealCopy: with a ghost row stored
// ahead of the real one, the drag moves the real upstream copy, drops the
// ghost row, and the message survives the next sync.
func TestWriteBackMove_GhostRowFirst_MovesRealCopy(t *testing.T) {
	for _, be := range ownSentDedupBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f := newMoveFixture(t, be, "wb558c", false)
			ctx := context.Background()
			real := f.putGhostFirst(t)

			f.dragToInbox(t)
			runOneWriteBackPass(t, f.ha, f.ts, f.acc)

			if fl := getUpstreamFlags(t, f.ts, f.user, "pw", "Archive", imap.UID(real.UpstreamUID)); fl != nil {
				t.Errorf("the real upstream Archive copy (uid %d) was not moved", real.UpstreamUID)
			}
			rows, err := f.ha.Store.Meta().ListIMAPImportMessageStatesByMessage(ctx, f.msgID)
			if err != nil {
				t.Fatalf("list rows: %v", err)
			}
			for _, r := range rows {
				if r.UpstreamUID == ghostUID {
					t.Errorf("ghost row %+v survived the move", r)
				}
			}

			if err := runSyncOnce(t, f.ha, f.ts, f.acc, nil); err != nil {
				t.Fatalf("sync: %v", err)
			}
			if _, err := f.ha.Store.Meta().GetMessage(ctx, f.msgID); err != nil {
				t.Fatalf("message %d lost after write-back + sync: %v", f.msgID, err)
			}
		})
	}
}

// TestWriteBackDestroy_RowNamingLiveMessage_NoExpunge: a deleted herold
// message whose only state row names a live, unrelated upstream message must
// not expunge it.
func TestWriteBackDestroy_RowNamingLiveMessage_NoExpunge(t *testing.T) {
	for _, be := range ownSentDedupBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f := newMoveFixture(t, be, "wb558d", true)
			ctx := context.Background()
			meta := f.ha.Store.Meta()

			rows, _ := meta.ListIMAPImportMessageStatesByMessage(ctx, f.msgID)
			for _, r := range rows {
				_ = meta.DeleteIMAPImportMessageState(ctx, r.AccountID, r.UpstreamFolder, r.UpstreamUID)
			}
			// The only row names INBOX uid 1: a live, mirrored, unrelated message.
			if err := meta.UpsertIMAPImportMessageState(ctx, store.IMAPImportMessageState{
				AccountID: f.acc.ID, UpstreamFolder: "INBOX", UpstreamUID: 1,
				HeroldMessageID: f.msgID, HeroldMailboxID: f.inbox, MappedMailboxID: f.inbox,
			}); err != nil {
				t.Fatalf("upsert: %v", err)
			}
			if err := meta.ExpungeMessages(ctx, f.archive, []store.MessageID{f.msgID}); err != nil {
				t.Fatalf("ExpungeMessages: %v", err)
			}

			runOneWriteBackPass(t, f.ha, f.ts, f.acc)

			assertUpstreamIntact(t, f, "INBOX", 1)
		})
	}
}

// TestWriteBackDestroy_DivergentRow_NoExpunge: a divergent placement row
// (herold redirected the message away from the folder's nominal mailbox)
// is never expunged through.
func TestWriteBackDestroy_DivergentRow_NoExpunge(t *testing.T) {
	for _, be := range ownSentDedupBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f := newMoveFixture(t, be, "wb558e", false)
			ctx := context.Background()
			meta := f.ha.Store.Meta()

			rows, _ := meta.ListIMAPImportMessageStatesByMessage(ctx, f.msgID)
			if len(rows) != 1 || !rowDivergent(rows[0]) {
				t.Fatalf("setup: want one divergent row, got %+v", rows)
			}
			uid := rows[0].UpstreamUID
			if err := meta.ExpungeMessages(ctx, f.archive, []store.MessageID{f.msgID}); err != nil {
				t.Fatalf("ExpungeMessages: %v", err)
			}

			runOneWriteBackPass(t, f.ha, f.ts, f.acc)

			assertUpstreamIntact(t, f, "Archive", imap.UID(uid))
		})
	}
}

func assertUpstreamIntact(t *testing.T, f *moveFixture, folder string, uid imap.UID) {
	t.Helper()
	fl := getUpstreamFlags(t, f.ts, f.user, "pw", folder, uid)
	if fl == nil {
		t.Fatalf("upstream %s uid %d was expunged", folder, uid)
	}
	for _, x := range fl {
		if x == imap.FlagDeleted {
			t.Fatalf("upstream %s uid %d was flagged \\Deleted", folder, uid)
		}
	}
}
