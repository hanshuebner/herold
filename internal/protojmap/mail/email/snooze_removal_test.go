package email_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// Issue #553. A reminder lives on exactly one membership of a
// multi-mailbox message. Removing that membership through Email/set must
// hand the pair to a remaining one; removing any other membership leaves
// it where it is. (The store-level paths and the consolidation
// interleaving are covered in storetest.)

func TestEmailSet_RemoveSnoozeHoldingMembership_KeepsReminder_SQLite(t *testing.T) {
	testRemoveSnoozeHoldingMembership(t, setupFixture(t))
}

func TestEmailSet_RemoveSnoozeHoldingMembership_KeepsReminder_Postgres(t *testing.T) {
	testRemoveSnoozeHoldingMembership(t, setupFixturePostgres(t))
}

func TestEmailSet_RemoveNonHoldingMembership_KeepsReminderInPlace_SQLite(t *testing.T) {
	testRemoveNonHoldingMembership(t, setupFixture(t))
}

func TestEmailSet_RemoveNonHoldingMembership_KeepsReminderInPlace_Postgres(t *testing.T) {
	testRemoveNonHoldingMembership(t, setupFixturePostgres(t))
}

// dueCount reports how many due reminders a wake sweep at 2031 lists for
// message id (a shared Postgres database may hold other tests'
// reminders).
func dueCount(t *testing.T, f *fixture, id store.MessageID) int {
	t.Helper()
	due, err := f.srv.Store.Meta().ListDueSnoozedMessages(context.Background(),
		time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC), 100)
	if err != nil {
		t.Fatalf("ListDueSnoozedMessages: %v", err)
	}
	n := 0
	for _, m := range due {
		if m.ID == id {
			n++
		}
	}
	return n
}

// snoozeHolder returns the single membership holding the reminder.
func snoozeHolder(t *testing.T, f *fixture, id store.MessageID) store.MessageMailbox {
	t.Helper()
	gm, err := f.srv.Store.Meta().GetMessage(context.Background(), id)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	var found []store.MessageMailbox
	for _, mm := range gm.Mailboxes {
		if mm.SnoozedUntil != nil {
			found = append(found, mm)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d memberships hold the reminder, want 1: %+v", len(found), gm.Mailboxes)
	}
	return found[0]
}

func snoozeWithWake(t *testing.T, s *snoozeScenario, wake store.MailboxID) {
	t.Helper()
	s.set(map[string]any{
		"snoozedUntil":        "2030-06-01T12:00:00Z",
		"snoozeWakeMailboxId": fmt.Sprintf("%d", wake),
	})
}

func testRemoveSnoozeHoldingMembership(t *testing.T, f *fixture) {
	s := newSnoozeScenario(t, f)
	a := s.mailbox("A")
	b := s.mailbox("B")
	w := s.mailbox("W")
	s.set(map[string]any{"mailboxIds": map[string]bool{
		fmt.Sprintf("%d", a.ID): true, fmt.Sprintf("%d", b.ID): true,
	}})
	snoozeWithWake(t, s, w.ID)
	s.check("snoozed in A+B")
	if h := snoozeHolder(t, f, s.mid); h.MailboxID != a.ID || h.WakeMailboxID == nil || *h.WakeMailboxID != w.ID {
		t.Fatalf("precondition: holder = %+v, want A with wake W", h)
	}

	s.set(map[string]any{fmt.Sprintf("mailboxIds/%d", a.ID): nil})
	gm, err := f.srv.Store.Meta().GetMessage(context.Background(), s.mid)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if len(gm.Mailboxes) != 1 || gm.Mailboxes[0].MailboxID != b.ID {
		t.Fatalf("memberships = %+v, want only B", gm.Mailboxes)
	}
	s.check("after removing A")
	h := snoozeHolder(t, f, s.mid)
	if h.WakeMailboxID == nil || *h.WakeMailboxID != w.ID {
		t.Errorf("wake_mailbox_id after hand-over = %v, want %d", h.WakeMailboxID, w.ID)
	}
	if h.WakeMailboxID != nil && *h.WakeMailboxID == a.ID {
		t.Errorf("wake_mailbox_id points at the removed mailbox %d", a.ID)
	}
	if got := dueCount(t, f, s.mid); got != 1 {
		t.Errorf("wake sweep lists %d due reminders, want 1", got)
	}
}

func testRemoveNonHoldingMembership(t *testing.T, f *fixture) {
	s := newSnoozeScenario(t, f)
	a := s.mailbox("A")
	b := s.mailbox("B")
	c := s.mailbox("C")
	w := s.mailbox("W")
	s.set(map[string]any{"mailboxIds": map[string]bool{
		fmt.Sprintf("%d", a.ID): true, fmt.Sprintf("%d", b.ID): true, fmt.Sprintf("%d", c.ID): true,
	}})
	snoozeWithWake(t, s, w.ID)
	s.check("snoozed in A+B+C")

	// B is neither canonical nor the holder.
	s.set(map[string]any{fmt.Sprintf("mailboxIds/%d", b.ID): nil})
	s.check("after removing B")
	h := snoozeHolder(t, f, s.mid)
	if h.MailboxID != a.ID {
		t.Errorf("reminder moved to mailbox %d, want it to stay on A (%d)", h.MailboxID, a.ID)
	}
	if h.WakeMailboxID == nil || *h.WakeMailboxID != w.ID {
		t.Errorf("wake_mailbox_id = %v, want %d", h.WakeMailboxID, w.ID)
	}
}
