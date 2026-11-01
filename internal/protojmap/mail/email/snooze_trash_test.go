package email_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/hanshuebner/herold/internal/store"
)

// Issue #553: a reminder is never handed to a Trash membership. One
// Email/set patch that adds Trash and removes both holding mailboxes
// leaves the message in Trash with the reminder ended.

func TestEmailSet_AddTrashRemoveTwo_EndsReminder_SQLite(t *testing.T) {
	testAddTrashRemoveTwoEndsReminder(t, setupFixture(t))
}

func TestEmailSet_AddTrashRemoveTwo_EndsReminder_Postgres(t *testing.T) {
	testAddTrashRemoveTwoEndsReminder(t, setupFixturePostgres(t))
}

func testAddTrashRemoveTwoEndsReminder(t *testing.T, f *fixture) {
	s := newSnoozeScenario(t, f)
	a := s.mailbox("A")
	b := s.mailbox("B")
	w := s.mailbox("W")
	trash, err := f.srv.Store.Meta().InsertMailbox(context.Background(), store.Mailbox{
		PrincipalID: f.pid, Name: "Trash", Attributes: store.MailboxAttrTrash,
	})
	if err != nil {
		t.Fatalf("InsertMailbox(Trash): %v", err)
	}
	s.set(map[string]any{"mailboxIds": map[string]bool{
		fmt.Sprintf("%d", a.ID): true, fmt.Sprintf("%d", b.ID): true,
	}})
	snoozeWithWake(t, s, w.ID)
	s.check("snoozed in A+B")

	s.set(map[string]any{
		fmt.Sprintf("mailboxIds/%d", trash.ID): true,
		fmt.Sprintf("mailboxIds/%d", a.ID):     nil,
		fmt.Sprintf("mailboxIds/%d", b.ID):     nil,
	})
	gm, err := f.srv.Store.Meta().GetMessage(context.Background(), s.mid)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if len(gm.Mailboxes) != 1 || gm.Mailboxes[0].MailboxID != trash.ID {
		t.Fatalf("memberships = %+v, want only Trash", gm.Mailboxes)
	}
	mm := gm.Mailboxes[0]
	if mm.SnoozedUntil != nil || mm.WakeMailboxID != nil {
		t.Errorf("Trash membership carries a reminder: until=%v wake=%v", mm.SnoozedUntil, mm.WakeMailboxID)
	}
	for _, k := range mm.Keywords {
		if k == "$snoozed" {
			t.Errorf("Trash membership has $snoozed")
		}
	}
	if got := dueCount(t, f, s.mid); got != 0 {
		t.Errorf("wake sweep lists %d due reminders, want 0", got)
	}
}
