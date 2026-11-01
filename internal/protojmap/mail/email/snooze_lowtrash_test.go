package email_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/hanshuebner/herold/internal/store"
)

// Issue #553: Trash is created at provisioning, so it has a lower id than
// a label the user creates later. Adding Trash to a snoozed message that
// lives in a label must leave the reminder on the label, where the message
// still wakes.

func TestEmailSet_AddLowIDTrash_ReminderStaysOnLabel_SQLite(t *testing.T) {
	testAddLowIDTrashKeepsReminder(t, setupFixture(t))
}

func TestEmailSet_AddLowIDTrash_ReminderStaysOnLabel_Postgres(t *testing.T) {
	testAddLowIDTrashKeepsReminder(t, setupFixturePostgres(t))
}

func testAddLowIDTrashKeepsReminder(t *testing.T, f *fixture) {
	// Trash first: its id is below the label's.
	trash, err := f.srv.Store.Meta().InsertMailbox(context.Background(), store.Mailbox{
		PrincipalID: f.pid, Name: "Trash", Attributes: store.MailboxAttrTrash,
	})
	if err != nil {
		t.Fatalf("InsertMailbox(Trash): %v", err)
	}
	s := newSnoozeScenario(t, f)
	label := s.mailbox("Label")
	if trash.ID >= label.ID {
		t.Fatalf("fixture: Trash id %d must be below the label id %d", trash.ID, label.ID)
	}
	w := s.mailbox("W")
	s.set(map[string]any{"mailboxIds": map[string]bool{fmt.Sprintf("%d", label.ID): true}})
	snoozeWithWake(t, s, w.ID)
	s.check("snoozed in the label")

	s.set(map[string]any{fmt.Sprintf("mailboxIds/%d", trash.ID): true})
	gm, err := f.srv.Store.Meta().GetMessage(context.Background(), s.mid)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if len(gm.Mailboxes) != 2 {
		t.Fatalf("memberships = %+v, want label and Trash", gm.Mailboxes)
	}
	h := snoozeHolder(t, f, s.mid)
	if h.MailboxID != label.ID {
		t.Errorf("reminder on mailbox %d, want the label %d", h.MailboxID, label.ID)
	}
	if h.WakeMailboxID == nil || *h.WakeMailboxID != w.ID {
		t.Errorf("wake_mailbox_id = %v, want %d", h.WakeMailboxID, w.ID)
	}
	if got := dueCount(t, f, s.mid); got != 1 {
		t.Errorf("wake sweep lists %d due reminders, want 1 (the message must still wake)", got)
	}
}
