package storetest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// Issue #553: a reminder lives on exactly one membership of a
// multi-mailbox message. Every path that removes that membership hands
// the pair to a remaining one in the same transaction, except the
// paths that end the reminder on purpose (move into Trash) or carry it
// to the target membership (a move).

type handoverFixture struct {
	t     *testing.T
	s     store.Store
	pid   store.PrincipalID
	lo    store.Mailbox // lower mailbox id: the canonical membership
	hi    store.Mailbox
	wake  store.Mailbox // wake destination, not a membership
	msg   store.MessageID
	until time.Time
}

// newHandoverFixture inserts a message into lo and hi with the reminder
// on holder (lo or hi) and wake destination wake.
func newHandoverFixture(t *testing.T, s store.Store, tag string, holderIsLo bool) *handoverFixture {
	t.Helper()
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, fmt.Sprintf("handover-%s@example.com", tag))
	f := &handoverFixture{t: t, s: s, pid: p.ID, until: time.Date(2030, 6, 1, 12, 0, 0, 0, time.UTC)}
	f.lo = mustInsertMailbox(t, s, p.ID, "Lo")
	f.hi = mustInsertMailbox(t, s, p.ID, "Hi")
	f.wake = mustInsertMailbox(t, s, p.ID, "Wake")
	ref := putBlob(t, s, "handover body "+tag)
	if _, _, err := s.Meta().InsertMessage(ctx,
		store.Message{PrincipalID: p.ID, Blob: ref, Size: ref.Size},
		[]store.MessageMailbox{{MailboxID: f.lo.ID}, {MailboxID: f.hi.ID}}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	f.msg = firstMessageIDFromFeed(t, s, p.ID)
	holder := f.hi
	if holderIsLo {
		holder = f.lo
	}
	w := f.wake.ID
	if _, err := s.Meta().SetSnooze(ctx, f.msg, holder.ID, &f.until, &w); err != nil {
		t.Fatalf("SetSnooze: %v", err)
	}
	return f
}

type holderInfo struct {
	mailbox store.MailboxID
	wake    *store.MailboxID
	kw      bool
}

// holders returns the memberships carrying a deadline, checking the
// keyword/deadline pair never splits.
func (f *handoverFixture) holders() (hs []holderInfo, memberships int) {
	f.t.Helper()
	m, err := f.s.Meta().GetMessage(ctxT(f.t), f.msg)
	if err != nil {
		f.t.Fatalf("GetMessage: %v", err)
	}
	for _, mm := range m.Mailboxes {
		kw := false
		for _, k := range mm.Keywords {
			if k == "$snoozed" {
				kw = true
			}
		}
		if kw != (mm.SnoozedUntil != nil) {
			f.t.Errorf("membership %d: keyword/deadline pair split (kw=%v until=%v)", mm.MailboxID, kw, mm.SnoozedUntil)
		}
		if mm.SnoozedUntil != nil {
			hs = append(hs, holderInfo{mailbox: mm.MailboxID, wake: mm.WakeMailboxID, kw: kw})
		}
	}
	return hs, len(m.Mailboxes)
}

// wantHolder asserts exactly one holder, on mailbox, with the wake
// destination intact and never pointing at removedMailbox.
func (f *handoverFixture) wantHolder(stage string, mailbox store.MailboxID, removed store.MailboxID) {
	f.t.Helper()
	hs, _ := f.holders()
	if len(hs) != 1 {
		f.t.Fatalf("%s: %d memberships hold the reminder, want 1", stage, len(hs))
	}
	if hs[0].mailbox != mailbox {
		f.t.Errorf("%s: reminder on mailbox %d, want %d", stage, hs[0].mailbox, mailbox)
	}
	if hs[0].wake == nil || *hs[0].wake != f.wake.ID {
		f.t.Errorf("%s: wake_mailbox_id = %v, want %d", stage, hs[0].wake, f.wake.ID)
	}
	if hs[0].wake != nil && removed != 0 && *hs[0].wake == removed {
		f.t.Errorf("%s: wake_mailbox_id points at the removed mailbox %d", stage, removed)
	}
}

func testSnoozeHandoverRemoveMessageFromMailbox(t *testing.T, s store.Store) {
	f := newHandoverFixture(t, s, "remove", true)
	if err := s.Meta().RemoveMessageFromMailbox(ctxT(t), f.msg, f.lo.ID); err != nil {
		t.Fatalf("RemoveMessageFromMailbox: %v", err)
	}
	f.wantHolder("after removing the holder", f.hi.ID, f.lo.ID)
}

func testSnoozeHandoverExpunge(t *testing.T, s store.Store) {
	f := newHandoverFixture(t, s, "expunge", true)
	if err := s.Meta().ExpungeMessages(ctxT(t), f.lo.ID, []store.MessageID{f.msg}); err != nil {
		t.Fatalf("ExpungeMessages: %v", err)
	}
	f.wantHolder("after expunging the holder", f.hi.ID, f.lo.ID)
}

func testSnoozeHandoverDeleteMailbox(t *testing.T, s store.Store) {
	f := newHandoverFixture(t, s, "delmb", true)
	if err := s.Meta().DeleteMailbox(ctxT(t), f.lo.ID); err != nil {
		t.Fatalf("DeleteMailbox: %v", err)
	}
	f.wantHolder("after deleting the holder's mailbox", f.hi.ID, f.lo.ID)
}

// A deleted mailbox that is itself the wake destination leaves no
// dangling reference on the handed-over reminder.
func testSnoozeHandoverDeleteWakeMailbox(t *testing.T, s store.Store) {
	f := newHandoverFixture(t, s, "delwake", true)
	// Re-point the reminder's wake destination at the mailbox about to go.
	w := f.lo.ID
	if _, err := s.Meta().SetSnooze(ctxT(t), f.msg, f.lo.ID, &f.until, &w); err != nil {
		t.Fatalf("SetSnooze: %v", err)
	}
	if err := s.Meta().DeleteMailbox(ctxT(t), f.lo.ID); err != nil {
		t.Fatalf("DeleteMailbox: %v", err)
	}
	hs, _ := f.holders()
	if len(hs) != 1 || hs[0].mailbox != f.hi.ID {
		t.Fatalf("holders = %+v, want one on %d", hs, f.hi.ID)
	}
	if hs[0].wake != nil && *hs[0].wake == f.lo.ID {
		t.Errorf("wake_mailbox_id points at the deleted mailbox %d", f.lo.ID)
	}
}

// Removing a membership that does not hold the reminder leaves it where
// it is, whether that membership is canonical or not.
func testSnoozeHandoverNonHolderRemoval(t *testing.T, s store.Store) {
	f := newHandoverFixture(t, s, "nonholder", true)
	if err := s.Meta().RemoveMessageFromMailbox(ctxT(t), f.msg, f.hi.ID); err != nil {
		t.Fatalf("RemoveMessageFromMailbox(hi): %v", err)
	}
	f.wantHolder("after removing non-holder hi", f.lo.ID, f.hi.ID)

	g := newHandoverFixture(t, s, "nonholder2", false)
	if err := s.Meta().ExpungeMessages(ctxT(t), g.lo.ID, []store.MessageID{g.msg}); err != nil {
		t.Fatalf("ExpungeMessages(lo): %v", err)
	}
	g.wantHolder("after expunging non-holder canonical lo", g.hi.ID, g.lo.ID)
}

// Moving the holder into Trash ends the reminder (#549); it must not
// reappear on a sibling membership.
func testSnoozeHandoverMoveToTrashEnds(t *testing.T, s store.Store) {
	f := newHandoverFixture(t, s, "trash", true)
	trash := mustInsertMailboxWithAttrs(t, s, f.pid, "Trash", store.MailboxAttrTrash)
	if err := s.Meta().MoveMessage(ctxT(t), f.msg, f.lo.ID, trash.ID); err != nil {
		t.Fatalf("MoveMessage: %v", err)
	}
	if hs, _ := f.holders(); len(hs) != 0 {
		t.Fatalf("reminder survived a move into Trash: %+v", hs)
	}
}

// Moving the holder to another mailbox carries the pair to the target.
func testSnoozeHandoverMoveCarriesPair(t *testing.T, s store.Store) {
	f := newHandoverFixture(t, s, "move", true)
	dst := mustInsertMailbox(t, s, f.pid, "Dst")
	if err := s.Meta().MoveMessage(ctxT(t), f.msg, f.lo.ID, dst.ID); err != nil {
		t.Fatalf("MoveMessage: %v", err)
	}
	f.wantHolder("after moving the holder", dst.ID, f.lo.ID)
}

// consolidateHook is the test seam both SQL stores expose: fn runs
// inside ConsolidateSnooze's transaction between its two writes.
type consolidateHook interface {
	SetConsolidateSnoozeHook(fn func(ctx context.Context))
}

// A wake sweep that runs between the two writes of a consolidation
// (from a second connection, seeing only committed state) lists the
// message exactly once, and exactly one holder remains afterwards.
func testSnoozeConsolidateSweepSeesOneHolder(t *testing.T, s store.Store) {
	hk, ok := s.(consolidateHook)
	if !ok {
		t.Fatalf("%T has no SetConsolidateSnoozeHook test seam", s)
	}
	f := newHandoverFixture(t, s, "consolidate", false)
	// Lo already holds nothing and sorts below Hi (the holder), so the
	// consolidation moves the pair from Hi to Lo.
	sweep := func() int {
		due, err := s.Meta().ListDueSnoozedMessages(context.Background(),
			time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC), 1000)
		if err != nil {
			t.Errorf("ListDueSnoozedMessages: %v", err)
			return -1
		}
		n := 0
		for _, m := range due {
			if m.ID == f.msg {
				n++
			}
		}
		return n
	}
	calls, seen := 0, -1
	hk.SetConsolidateSnoozeHook(func(ctx context.Context) {
		calls++
		seen = sweep()
	})
	defer hk.SetConsolidateSnoozeHook(nil)
	if err := s.Meta().ConsolidateSnooze(ctxT(t), f.msg); err != nil {
		t.Fatalf("ConsolidateSnooze: %v", err)
	}
	if calls != 1 {
		t.Fatalf("seam invoked %d times, want 1 (consolidation did not move a pair)", calls)
	}
	if seen != 1 {
		t.Errorf("sweep between the two writes listed the message %d times, want exactly 1", seen)
	}
	if got := sweep(); got != 1 {
		t.Errorf("sweep after consolidation listed the message %d times, want exactly 1", got)
	}
	f.wantHolder("after consolidation", f.lo.ID, 0)
}
