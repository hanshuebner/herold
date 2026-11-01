package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// A reminder is never handed to a Trash membership: with only Trash
// left, removing the last non-Trash holder ends the reminder, as moving
// into Trash does (#549). While a non-Trash membership remains the pair
// goes there.
func testSnoozeHandoverNeverToTrash(t *testing.T, s store.Store) {
	f := newHandoverFixture(t, s, "tonever", false) // reminder on Hi
	trash := mustInsertMailboxWithAttrs(t, s, f.pid, "Trash", store.MailboxAttrTrash)
	if _, _, err := s.Meta().AddMessageToMailbox(ctxT(t), f.msg, trash.ID); err != nil {
		t.Fatalf("AddMessageToMailbox(Trash): %v", err)
	}
	if err := s.Meta().RemoveMessageFromMailbox(ctxT(t), f.msg, f.hi.ID); err != nil {
		t.Fatalf("RemoveMessageFromMailbox(Hi): %v", err)
	}
	// Lo is a non-Trash membership: it takes the pair.
	f.wantHolder("after removing Hi with Lo and Trash left", f.lo.ID, f.hi.ID)
	if err := s.Meta().RemoveMessageFromMailbox(ctxT(t), f.msg, f.lo.ID); err != nil {
		t.Fatalf("RemoveMessageFromMailbox(Lo): %v", err)
	}
	f.wantNoReminder("after removing the last non-Trash membership", trash.ID)
}

// Expunge and DeleteMailbox follow the same rule.
func testSnoozeHandoverNeverToTrashOtherPaths(t *testing.T, s store.Store) {
	f := newHandoverFixture(t, s, "tonever2", true) // reminder on Lo
	trash := mustInsertMailboxWithAttrs(t, s, f.pid, "Trash", store.MailboxAttrTrash)
	if _, _, err := s.Meta().AddMessageToMailbox(ctxT(t), f.msg, trash.ID); err != nil {
		t.Fatalf("AddMessageToMailbox(Trash): %v", err)
	}
	if err := s.Meta().RemoveMessageFromMailbox(ctxT(t), f.msg, f.hi.ID); err != nil {
		t.Fatalf("RemoveMessageFromMailbox(Hi): %v", err)
	}
	if err := s.Meta().ExpungeMessages(ctxT(t), f.lo.ID, []store.MessageID{f.msg}); err != nil {
		t.Fatalf("ExpungeMessages(Lo): %v", err)
	}
	f.wantNoReminder("after expunging the last non-Trash membership", trash.ID)

	g := newHandoverFixture(t, s, "tonever3", true)
	trash2 := mustInsertMailboxWithAttrs(t, s, g.pid, "Trash", store.MailboxAttrTrash)
	if _, _, err := s.Meta().AddMessageToMailbox(ctxT(t), g.msg, trash2.ID); err != nil {
		t.Fatalf("AddMessageToMailbox(Trash): %v", err)
	}
	if err := s.Meta().RemoveMessageFromMailbox(ctxT(t), g.msg, g.hi.ID); err != nil {
		t.Fatalf("RemoveMessageFromMailbox(Hi): %v", err)
	}
	if err := s.Meta().DeleteMailbox(ctxT(t), g.lo.ID); err != nil {
		t.Fatalf("DeleteMailbox(Lo): %v", err)
	}
	g.wantNoReminder("after deleting the last non-Trash mailbox", trash2.ID)
}

// wantNoReminder asserts no membership holds a deadline, keyword or wake
// destination, and that the message is still a member of trashID.
func (f *handoverFixture) wantNoReminder(stage string, trashID store.MailboxID) {
	f.t.Helper()
	m, err := f.s.Meta().GetMessage(ctxT(f.t), f.msg)
	if err != nil {
		f.t.Fatalf("%s: GetMessage: %v", stage, err)
	}
	inTrash := false
	for _, mm := range m.Mailboxes {
		if mm.MailboxID == trashID {
			inTrash = true
		}
		if mm.SnoozedUntil != nil || mm.WakeMailboxID != nil {
			f.t.Errorf("%s: membership %d still carries a reminder (until=%v wake=%v)", stage, mm.MailboxID, mm.SnoozedUntil, mm.WakeMailboxID)
		}
		for _, k := range mm.Keywords {
			if k == "$snoozed" {
				f.t.Errorf("%s: membership %d still has $snoozed", stage, mm.MailboxID)
			}
		}
	}
	if !inTrash {
		f.t.Errorf("%s: message is no longer in Trash", stage)
	}
	due, err := f.s.Meta().ListDueSnoozedMessages(context.Background(), time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC), 1000)
	if err != nil {
		f.t.Fatalf("ListDueSnoozedMessages: %v", err)
	}
	for _, d := range due {
		if d.ID == f.msg {
			f.t.Errorf("%s: wake sweep still lists the message", stage)
		}
	}
}

// ReparentMessage carries the pair to the target membership of a
// multi-membership message: one holder afterwards, keyword and deadline
// together, wake destination remapped.
func testSnoozeHandoverReparentCarriesPair(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	f := newHandoverFixture(t, s, "reparent", false) // reminder on Hi
	newP := mustInsertPrincipal(t, s, "handover-reparent-new@example.com")
	nLo := mustInsertMailbox(t, s, newP.ID, "Lo")
	nHi := mustInsertMailbox(t, s, newP.ID, "Hi")
	nWake := mustInsertMailbox(t, s, newP.ID, "Wake")
	moves := map[store.MailboxID]store.MailboxID{f.lo.ID: nLo.ID, f.hi.ID: nHi.ID, f.wake.ID: nWake.ID}
	if err := s.Meta().ReparentMessage(ctx, f.msg, newP.ID, moves); err != nil {
		t.Fatalf("ReparentMessage: %v", err)
	}
	hs, n := f.holders()
	if n != 2 {
		t.Fatalf("memberships = %d, want 2", n)
	}
	if len(hs) != 1 || hs[0].mailbox != nHi.ID {
		t.Fatalf("holders = %+v, want exactly one on the new Hi (%d)", hs, nHi.ID)
	}
	if hs[0].wake == nil || *hs[0].wake != nWake.ID {
		t.Errorf("wake_mailbox_id = %v, want remapped %d", hs[0].wake, nWake.ID)
	}
	m, err := s.Meta().GetMessage(ctx, f.msg)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if m.PrincipalID != newP.ID {
		t.Errorf("PrincipalID = %d, want %d", m.PrincipalID, newP.ID)
	}
	for _, mm := range m.Mailboxes {
		if mm.MailboxID == nHi.ID && (mm.SnoozedUntil == nil || !mm.SnoozedUntil.Equal(f.until)) {
			t.Errorf("deadline on new Hi = %v, want %v", mm.SnoozedUntil, f.until)
		}
	}
}
