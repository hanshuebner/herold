package imapimport

// writeback_move_survives_test.go pins #558: a mirrored message the user
// drags to another folder in herold must survive the write-back and the next
// down-sync, and the write-back must never delete an unrelated upstream
// message.

import (
	"context"
	"testing"

	imap "github.com/emersion/go-imap/v2"

	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
)

// moveFixture is a mirrored message in the upstream Archive folder together
// with the herold-side pieces a Suite drag-to-Inbox acts on.
type moveFixture struct {
	ha      *testharness.Server
	ts      *testIMAPServer
	acc     store.IMAPImportAccount
	msgID   store.MessageID
	archive store.MailboxID
	inbox   store.MailboxID
	user    string
}

// dragToInbox performs the herold-side change of the Suite's
// bulkMoveToMailbox on a message that holds an Archive and a label
// membership: the Inbox membership is added (inheriting the flags), then
// every other membership is removed (the Email/set general path).
func (f *moveFixture) dragToInbox(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	pid := store.PrincipalID(f.acc.PrincipalID)
	meta := f.ha.Store.Meta()

	label, err := meta.InsertMailbox(ctx, store.Mailbox{PrincipalID: pid, Name: "label558"})
	if err != nil {
		t.Fatalf("InsertMailbox label: %v", err)
	}
	if _, _, err := meta.AddMessageToMailbox(ctx, f.msgID, label.ID); err != nil {
		t.Fatalf("Add label: %v", err)
	}
	if _, err := meta.UpdateMessageFlags(ctx, f.msgID, f.archive, store.MessageFlagSeen, 0, nil, nil, 0); err != nil {
		t.Fatalf("seen: %v", err)
	}
	pre, err := meta.GetMessage(ctx, f.msgID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if _, _, err := meta.AddMessageToMailbox(ctx, f.msgID, f.inbox); err != nil {
		t.Fatalf("Add inbox: %v", err)
	}
	if _, err := meta.UpdateMessageFlags(ctx, f.msgID, f.inbox, store.MessageFlagSeen, 0, nil, nil, 0); err != nil {
		t.Fatalf("seed flags: %v", err)
	}
	for _, mm := range pre.Mailboxes {
		if err := meta.RemoveMessageFromMailbox(ctx, f.msgID, mm.MailboxID); err != nil {
			t.Fatalf("Remove: %v", err)
		}
	}
}

// newMoveFixture mirrors one message into upstream Archive. The Archive
// placement's state row carries MappedMailboxID = Inbox, the shape an
// INBOX-mapped copy redirected into Archive by archive precedence leaves
// behind. Archive is padded so the message's Archive UID is larger than any
// UID the Inbox hands out; stranger puts an unrelated message at that same
// UID in upstream INBOX.
func newMoveFixture(t *testing.T, be ownSentDedupBackend, user string, stranger bool) *moveFixture {
	t.Helper()
	ts := startTestIMAPServer(t)
	u := ts.addUser(user, "pw")
	if err := u.Create("Archive", nil); err != nil {
		t.Fatalf("Create Archive: %v", err)
	}
	ha, _ := testharness.Start(t, testharness.Options{Store: be.st, Clock: be.clk})
	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               user + "@example.test",
		username:            user,
		credentialPlaintext: "pw",
	}, nil)

	var ms store.IMAPImportMessageState
	if stranger {
		setupSyncedMessage(t, ha, ts, acc, "INBOX", user+"-stranger@test", nil)
		_, ms = setupSyncedMessage(t, ha, ts, acc, "Archive", user+"-move@test", nil)
	} else {
		setupSyncedMessage(t, ha, ts, acc, "INBOX", user+"-pad1@test", nil)
		setupSyncedMessage(t, ha, ts, acc, "Archive", user+"-pad2@test", nil)
		setupSyncedMessage(t, ha, ts, acc, "Archive", user+"-pad3@test", nil)
		_, ms = setupSyncedMessage(t, ha, ts, acc, "Archive", user+"-move@test", nil)
	}

	ctx := context.Background()
	mbs, err := ha.Store.Meta().ListMailboxes(ctx, store.PrincipalID(acc.PrincipalID))
	if err != nil {
		t.Fatalf("ListMailboxes: %v", err)
	}
	var inbox store.MailboxID
	for _, mb := range mbs {
		if mb.Attributes&store.MailboxAttrInbox != 0 {
			inbox = mb.ID
		}
	}
	if inbox == 0 {
		t.Fatal("no inbox mailbox")
	}
	ms.MappedMailboxID = inbox
	if err := ha.Store.Meta().UpsertIMAPImportMessageState(ctx, ms); err != nil {
		t.Fatalf("Upsert state: %v", err)
	}
	return &moveFixture{ha: ha, ts: ts, acc: acc, msgID: ms.HeroldMessageID, archive: ms.HeroldMailboxID, inbox: inbox, user: user}
}

// TestWriteBackMoveThenSync_MessageSurvives: after the drag, the write-back
// and the next down-sync, the message still exists in herold and sits in
// Inbox; the write-back records no tracking row under a UID the Inbox never
// assigned.
func TestWriteBackMoveThenSync_MessageSurvives(t *testing.T) {
	for _, be := range ownSentDedupBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f := newMoveFixture(t, be, "wb558a", false)
			ctx := context.Background()

			f.dragToInbox(t)
			runOneWriteBackPass(t, f.ha, f.ts, f.acc)

			states, err := f.ha.Store.Meta().ListIMAPImportMessageStatesByMessage(ctx, f.msgID)
			if err != nil {
				t.Fatalf("ListIMAPImportMessageStatesByMessage: %v", err)
			}
			for _, s := range states {
				if s.UpstreamFolder == "INBOX" {
					t.Errorf("write-back left tracking row %+v under INBOX with the source folder's UID", s)
				}
			}

			if err := runSyncOnce(t, f.ha, f.ts, f.acc, nil); err != nil {
				t.Fatalf("sync: %v", err)
			}

			got, err := f.ha.Store.Meta().GetMessage(ctx, f.msgID)
			if err != nil {
				t.Fatalf("message %d lost after write-back + sync: %v", f.msgID, err)
			}
			inInbox := false
			for _, mm := range got.Mailboxes {
				if mm.MailboxID == f.inbox {
					inInbox = true
				}
			}
			if !inInbox {
				t.Fatalf("memberships = %+v, want Inbox (%d)", got.Mailboxes, f.inbox)
			}
		})
	}
}

// TestWriteBackMove_DoesNotExpungeUnrelatedUpstreamMessage: removing the
// source membership during a move must not expunge whatever message the
// state row's UID names in the destination folder.
func TestWriteBackMove_DoesNotExpungeUnrelatedUpstreamMessage(t *testing.T) {
	for _, be := range ownSentDedupBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f := newMoveFixture(t, be, "wb558b", true)
			ctx := context.Background()

			if getUpstreamFlags(t, f.ts, f.user, "pw", "INBOX", imap.UID(1)) == nil {
				t.Fatal("test setup: unrelated message missing from upstream INBOX")
			}

			f.dragToInbox(t)
			runOneWriteBackPass(t, f.ha, f.ts, f.acc)

			after := getUpstreamFlags(t, f.ts, f.user, "pw", "INBOX", imap.UID(1))
			if after == nil {
				t.Fatal("unrelated upstream INBOX message uid 1 was expunged by the write-back of a membership removal")
			}
			for _, fl := range after {
				if fl == imap.FlagDeleted {
					t.Fatal("unrelated upstream INBOX message uid 1 was flagged \\Deleted by the write-back")
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
