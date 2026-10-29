package protoimap_test

// snooze_copy_test.go pins the (keyword "$snoozed", snoozed-until)
// atomicity invariant (REQ-PROTO-49) across the IMAP insert paths:
// COPY, MOVE and APPEND must never create a membership that carries
// "$snoozed" without a deadline (re #549).

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

func hasKW(kws []string, want string) bool {
	for _, k := range kws {
		if strings.EqualFold(k, want) {
			return true
		}
	}
	return false
}

// snoozedInbox seeds a message in INBOX and snoozes it with a wake
// destination of Archive. It returns the message id, deadline and
// wake destination.
func snoozedInbox(t *testing.T, f *fixture, subject string) (store.MessageID, time.Time, store.MailboxID) {
	t.Helper()
	ctx := context.Background()
	id := seedMessageID(t, f, subject)
	wakeMB, err := f.ha.Store.Meta().GetMailboxByName(ctx, f.pid, "Archive")
	if err != nil {
		t.Fatalf("get Archive: %v", err)
	}
	until := time.Date(2031, 1, 2, 3, 4, 5, 0, time.UTC)
	wake := wakeMB.ID
	if _, err := f.ha.Store.Meta().SetSnooze(ctx, id, f.inbox.ID, &until, &wake); err != nil {
		t.Fatalf("SetSnooze: %v", err)
	}
	return id, until, wake
}

func assertPairAtomic(t *testing.T, where string, msgs []store.Message) {
	t.Helper()
	for _, m := range msgs {
		if hasKW(m.Keywords, "$snoozed") != (m.SnoozedUntil != nil) {
			t.Errorf("%s: uid %d breaks the pair: $snoozed=%v snoozedUntil=%v",
				where, m.UID, hasKW(m.Keywords, "$snoozed"), m.SnoozedUntil)
		}
	}
}

// TestCOPY_DropsSnoozedKeyword: a COPY is a new message and must not
// carry the reminder keyword without its deadline.
func TestCOPY_DropsSnoozedKeyword(t *testing.T) {
	for _, be := range internalizePendingBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f := newFixture(t, fxOpts{implicitTLS: true, store: be.st, clk: be.clk})
			ctx := context.Background()
			snoozedInbox(t, f, "copy-snz")
			dest, err := f.ha.Store.Meta().GetMailboxByName(ctx, f.pid, "Archive")
			if err != nil {
				t.Fatalf("get Archive: %v", err)
			}
			c := loggedInClient(t, f)
			defer c.close()
			c.send("s1", "SELECT INBOX")
			resp := c.send("cp1", "COPY 1 Archive")
			if !strings.Contains(resp[len(resp)-1], "OK") {
				t.Fatalf("COPY failed: %v", resp)
			}
			dmsgs, err := f.ha.Store.Meta().ListMessages(ctx, dest.ID, store.MessageFilter{})
			if err != nil || len(dmsgs) != 1 {
				t.Fatalf("list Archive: err=%v n=%d", err, len(dmsgs))
			}
			assertPairAtomic(t, "COPY destination", dmsgs)
			if hasKW(dmsgs[0].Keywords, "$snoozed") {
				t.Errorf("copy carries $snoozed: %v", dmsgs[0].Keywords)
			}
			smsgs, _ := f.ha.Store.Meta().ListMessages(ctx, f.inbox.ID, store.MessageFilter{})
			if len(smsgs) != 1 || smsgs[0].SnoozedUntil == nil || !hasKW(smsgs[0].Keywords, "$snoozed") {
				t.Errorf("source snooze must be untouched: %+v", smsgs)
			}
		})
	}
}

// TestMOVE_CarriesSnoozePair: a MOVE relocates the one membership, so
// the reminder (deadline, keyword and wake destination) travels with
// it, as the store's MoveMessage does.
func TestMOVE_CarriesSnoozePair(t *testing.T) {
	for _, be := range internalizePendingBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f := newFixture(t, fxOpts{implicitTLS: true, store: be.st, clk: be.clk})
			ctx := context.Background()
			_, until, wake := snoozedInbox(t, f, "move-snz")
			dest, err := f.ha.Store.Meta().GetMailboxByName(ctx, f.pid, "Archive")
			if err != nil {
				t.Fatalf("get Archive: %v", err)
			}
			c := loggedInClient(t, f)
			defer c.close()
			c.send("s1", "SELECT INBOX")
			resp := c.send("mv1", "MOVE 1 Archive")
			if !strings.Contains(resp[len(resp)-1], "OK") {
				t.Fatalf("MOVE failed: %v", resp)
			}
			dmsgs, err := f.ha.Store.Meta().ListMessages(ctx, dest.ID, store.MessageFilter{})
			if err != nil || len(dmsgs) != 1 {
				t.Fatalf("list Archive: err=%v n=%d", err, len(dmsgs))
			}
			assertPairAtomic(t, "MOVE destination", dmsgs)
			m := dmsgs[0]
			if m.SnoozedUntil == nil || !m.SnoozedUntil.Equal(until) || !hasKW(m.Keywords, "$snoozed") {
				t.Errorf("MOVE must keep the reminder: until=%v kw=%v", m.SnoozedUntil, m.Keywords)
			}
			full, err := f.ha.Store.Meta().GetMessage(ctx, m.ID)
			if err != nil {
				t.Fatalf("GetMessage: %v", err)
			}
			var gotWake *store.MailboxID
			for _, mm := range full.Mailboxes {
				if mm.MailboxID == dest.ID {
					gotWake = mm.WakeMailboxID
				}
			}
			if gotWake == nil || *gotWake != wake {
				t.Errorf("MOVE must keep the wake destination: %v want %d", gotWake, wake)
			}
		})
	}
}

// TestMOVE_ToTrashEndsSnooze: as MoveMessage does (re #460), a MOVE
// into Trash drops the reminder pair together.
func TestMOVE_ToTrashEndsSnooze(t *testing.T) {
	for _, be := range internalizePendingBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f := newFixture(t, fxOpts{implicitTLS: true, store: be.st, clk: be.clk})
			ctx := context.Background()
			snoozedInbox(t, f, "trash-snz")
			dest, err := f.ha.Store.Meta().GetMailboxByName(ctx, f.pid, "Trash")
			if err != nil {
				t.Fatalf("get Trash: %v", err)
			}
			if dest.Attributes&store.MailboxAttrTrash == 0 {
				t.Fatalf("fixture Trash lacks the Trash attribute")
			}
			c := loggedInClient(t, f)
			defer c.close()
			c.send("s1", "SELECT INBOX")
			resp := c.send("mv1", "MOVE 1 Trash")
			if !strings.Contains(resp[len(resp)-1], "OK") {
				t.Fatalf("MOVE failed: %v", resp)
			}
			dmsgs, _ := f.ha.Store.Meta().ListMessages(ctx, dest.ID, store.MessageFilter{})
			if len(dmsgs) != 1 {
				t.Fatalf("list Trash: n=%d", len(dmsgs))
			}
			assertPairAtomic(t, "MOVE to Trash", dmsgs)
			if dmsgs[0].SnoozedUntil != nil || hasKW(dmsgs[0].Keywords, "$snoozed") {
				t.Errorf("reminder survived a move into Trash: %+v", dmsgs[0])
			}
		})
	}
}

// TestAPPEND_DropsSnoozedKeyword: an APPEND cannot supply a deadline,
// so a "$snoozed" among its flags must not be stored.
func TestAPPEND_DropsSnoozedKeyword(t *testing.T) {
	for _, be := range internalizePendingBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f := newFixture(t, fxOpts{implicitTLS: true, store: be.st, clk: be.clk})
			ctx := context.Background()
			c := loggedInClient(t, f)
			defer c.close()
			msg := buildHTMLMessage("append-snz", false)
			c.write(fmt.Sprintf("a1 APPEND INBOX (\\Seen $snoozed $other) {%d}\r\n", len(msg)))
			if line := c.readLine(); !strings.HasPrefix(line, "+") {
				t.Fatalf("expected continuation, got %q", line)
			}
			c.write(msg + "\r\n")
			resp := c.readUntilTag("a1")
			if !strings.Contains(resp[len(resp)-1], "OK") {
				t.Fatalf("APPEND failed: %v", resp)
			}
			msgs, err := f.ha.Store.Meta().ListMessages(ctx, f.inbox.ID, store.MessageFilter{})
			if err != nil || len(msgs) != 1 {
				t.Fatalf("list INBOX: err=%v n=%d", err, len(msgs))
			}
			assertPairAtomic(t, "APPEND", msgs)
			if !hasKW(msgs[0].Keywords, "$other") {
				t.Errorf("other keywords must survive: %v", msgs[0].Keywords)
			}
		})
	}
}
