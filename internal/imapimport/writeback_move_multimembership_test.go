package imapimport

// writeback_move_multimembership_test.go covers #472: writeBackMove must
// decide whether the membership a write-back state row tracks
// (ms.HeroldMailboxID) has moved by checking the message's full current
// membership set, not GetMessage's convenience MailboxID field (whichever
// membership has the lowest mailbox id). Gmail-label imports give one
// message several simultaneous memberships, each potentially tracked by
// its own state row; a stray, untouched membership with a lower mailbox
// id than the tracked one must never be read as "the mailbox changed".

import (
	"context"
	"strings"
	"testing"

	imap "github.com/emersion/go-imap/v2"

	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/sysconfig"
	"github.com/hanshuebner/herold/internal/testharness"
)

// recordingMoveConn wraps a real Conn and records every SelectReadWrite /
// UIDMove invocation without needing the target folder to genuinely exist
// upstream -- the test only cares whether writeBackMove attempted a move,
// not whether the fake server would have accepted it.
type recordingMoveConn struct {
	Conn
	selected []string
	moved    []string
}

func (c *recordingMoveConn) SelectReadWrite(ctx context.Context, mailbox string) (selectInfo, error) {
	c.selected = append(c.selected, mailbox)
	return c.Conn.SelectReadWrite(ctx, mailbox)
}

func (c *recordingMoveConn) UIDMove(ctx context.Context, uid imap.UID, dest string) error {
	c.moved = append(c.moved, dest)
	return c.Conn.UIDMove(ctx, uid, dest)
}

// TestWriteBackMove_UntouchedLowerIDMembership_NoSpuriousMove covers #472:
// a message can carry an additional membership in a mailbox with a LOWER
// id than the one write-back tracks (ms.HeroldMailboxID), without the
// tracked membership itself having moved. writeBackMove must not read
// GetMessage's convenience MailboxID field (which names the stray,
// lower-id membership) as "the tracked mailbox changed" and must not
// attempt an upstream MOVE.
func TestWriteBackMove_UntouchedLowerIDMembership_NoSpuriousMove(t *testing.T) {
	ts := startTestIMAPServer(t)
	u := ts.addUser("wb-multi", "pw")
	if err := u.Create("Travel", nil); err != nil {
		t.Fatalf("Create Travel: %v", err)
	}

	ha, _ := testharness.Start(t, testharness.Options{})
	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "wb-multi@example.test",
		username:            "wb-multi",
		credentialPlaintext: "pw",
	}, nil)

	ctx := context.Background()
	pid := store.PrincipalID(acc.PrincipalID)

	// Stray is created first (lower id); Travel second (higher id) --
	// Travel is the membership write-back tracks via ms below.
	stray, err := ha.Store.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: pid, Name: "INBOX"})
	if err != nil {
		t.Fatalf("InsertMailbox INBOX: %v", err)
	}
	travel, err := ha.Store.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: pid, Name: "Travel"})
	if err != nil {
		t.Fatalf("InsertMailbox Travel: %v", err)
	}
	if travel.ID <= stray.ID {
		t.Fatalf("test precondition broken: want travel.ID (%d) > stray.ID (%d)", travel.ID, stray.ID)
	}

	ref, err := ha.Store.Blobs().Put(ctx, strings.NewReader("wb-multi-body"))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	if _, _, err := ha.Store.Meta().InsertMessage(ctx, store.Message{
		PrincipalID: pid, Blob: ref, Size: ref.Size,
	}, []store.MessageMailbox{{MailboxID: travel.ID}}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	msgID := mostRecentEmailIDFor(t, ha.Store, pid)

	// A stray, untouched membership at the lower id -- e.g. a Gmail
	// label placement that was never part of this write-back view.
	if _, _, err := ha.Store.Meta().AddMessageToMailbox(ctx, msgID, stray.ID); err != nil {
		t.Fatalf("AddMessageToMailbox stray: %v", err)
	}

	got, err := ha.Store.Meta().GetMessage(ctx, msgID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if got.MailboxID != stray.ID {
		t.Fatalf("test setup: convenience MailboxID = %d, want %d (stray, the lowest id) -- the case this test pins", got.MailboxID, stray.ID)
	}

	ms := store.IMAPImportMessageState{
		AccountID:       acc.ID,
		UpstreamFolder:  "Travel",
		UpstreamUID:     1,
		HeroldMessageID: msgID,
		HeroldMailboxID: travel.ID,
	}
	if err := ha.Store.Meta().UpsertIMAPImportMessageState(ctx, ms); err != nil {
		t.Fatalf("UpsertIMAPImportMessageState: %v", err)
	}

	w := newAccountWorker(accountWorkerOpts{
		account:     acc,
		store:       ha.Store,
		dataKey:     testDataKey(t),
		cfg:         sysconfig.IMAPImportConfig{},
		log:         newTestLogger(t),
		clk:         ha.Clock,
		categoriser: noopCategoriser{},
	})

	conn := &recordingMoveConn{Conn: dialFakeConn(t, ts, "wb-multi", "pw")}
	w.writeBackMove(ctx, conn, ms, msgID)

	if len(conn.selected) != 0 || len(conn.moved) != 0 {
		t.Fatalf("writeBackMove attempted a move (selected=%v moved=%v); want none -- the tracked Travel membership never changed", conn.selected, conn.moved)
	}

	after, found, err := ha.Store.Meta().GetIMAPImportMessageState(ctx, acc.ID, "Travel", 1)
	if err != nil || !found || after.HeroldMailboxID != travel.ID {
		t.Fatalf("GetIMAPImportMessageState after = (%+v, %v, %v), want HeroldMailboxID unchanged at %d", after, found, err, travel.ID)
	}
}

// mostRecentEmailIDFor mirrors mostRecentEmailID (email package test
// helper, unavailable here) for the imapimport package's own tests.
func mostRecentEmailIDFor(t *testing.T, st store.Store, pid store.PrincipalID) store.MessageID {
	t.Helper()
	feed, err := st.Meta().ReadChangeFeed(context.Background(), pid, 0, 1000)
	if err != nil {
		t.Fatalf("ReadChangeFeed: %v", err)
	}
	var last store.MessageID
	for _, e := range feed {
		if e.Kind == store.EntityKindEmail && e.Op == store.ChangeOpCreated {
			last = store.MessageID(e.EntityID)
		}
	}
	if last == 0 {
		t.Fatalf("no email created in feed")
	}
	return last
}
