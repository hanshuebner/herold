package email_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/hanshuebner/herold/internal/store"
)

// Issue #554: Trash is created at provisioning, so its id is below a
// label's. A message in Trash and a label must take its reminder on the
// label (the lowest-id non-Trash membership), and Email/get plus the
// Snoozed query must report the reminder from the membership that holds it
// rather than from the canonical (lowest-id) row.

func TestEmailSet_SnoozeTrashAndLabel_ReminderOnLabel_SQLite(t *testing.T) {
	testSnoozeTrashAndLabel(t, setupFixture(t))
}

func TestEmailSet_SnoozeTrashAndLabel_ReminderOnLabel_Postgres(t *testing.T) {
	testSnoozeTrashAndLabel(t, setupFixturePostgres(t))
}

func TestEmailGet_SnoozedLabelGainsLowIDTrash_StillSnoozed_SQLite(t *testing.T) {
	testSnoozedLabelGainsLowIDTrash(t, setupFixture(t))
}

func TestEmailGet_SnoozedLabelGainsLowIDTrash_StillSnoozed_Postgres(t *testing.T) {
	testSnoozedLabelGainsLowIDTrash(t, setupFixturePostgres(t))
}

func TestEmailSet_SnoozeTrashOnly_Rejected_SQLite(t *testing.T) {
	testSnoozeTrashOnlyRejected(t, setupFixture(t))
}

func TestEmailSet_SnoozeTrashOnly_Rejected_Postgres(t *testing.T) {
	testSnoozeTrashOnlyRejected(t, setupFixturePostgres(t))
}

func lowIDTrash(t *testing.T, f *fixture) store.Mailbox {
	t.Helper()
	trash, err := f.srv.Store.Meta().InsertMailbox(context.Background(), store.Mailbox{
		PrincipalID: f.pid, Name: "Trash", Attributes: store.MailboxAttrTrash,
	})
	if err != nil {
		t.Fatalf("InsertMailbox(Trash): %v", err)
	}
	return trash
}

// jmapSnooze reads the message through Email/get and returns what a JMAP
// client sees: the keyword set and snoozedUntil.
func jmapSnooze(t *testing.T, s *snoozeScenario) (map[string]bool, *string) {
	t.Helper()
	_, raw := s.f.invoke(t, "Email/get", map[string]any{
		"accountId":  s.acct,
		"ids":        []string{s.id},
		"properties": []string{"keywords", "snoozedUntil", "snoozeWakeMailboxId"},
	})
	var resp struct {
		List []struct {
			Keywords     map[string]bool `json:"keywords"`
			SnoozedUntil *string         `json:"snoozedUntil"`
		} `json:"list"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil || len(resp.List) != 1 {
		t.Fatalf("Email/get: %v: %s", err, raw)
	}
	return resp.List[0].Keywords, resp.List[0].SnoozedUntil
}

func (s *snoozeScenario) expectSnoozedView(stage string) {
	s.t.Helper()
	kw, until := jmapSnooze(s.t, s)
	if !kw["$snoozed"] || until == nil || *until != "2030-06-01T12:00:00Z" {
		s.t.Errorf("%s: Email/get keywords=%v snoozedUntil=%v, want $snoozed and 2030-06-01T12:00:00Z", stage, kw, until)
	}
	if got := s.querySnoozed(); len(got) != 1 || got[0] != s.id {
		s.t.Errorf("%s: hasKeyword $snoozed = %v, want [%s]", stage, got, s.id)
	}
}

func testSnoozeTrashAndLabel(t *testing.T, f *fixture) {
	trash := lowIDTrash(t, f)
	s := newSnoozeScenario(t, f)
	label := s.mailbox("Label")
	if trash.ID >= label.ID {
		t.Fatalf("fixture: Trash id %d must be below the label id %d", trash.ID, label.ID)
	}
	w := s.mailbox("W")
	// Label first, Trash added afterwards: a single patch naming both would
	// read as a move into Trash.
	s.set(map[string]any{"mailboxIds": map[string]bool{fmt.Sprintf("%d", label.ID): true}})
	s.set(map[string]any{fmt.Sprintf("mailboxIds/%d", trash.ID): true})
	snoozeWithWake(t, s, w.ID)

	h := snoozeHolder(t, f, s.mid)
	if h.MailboxID != label.ID {
		t.Errorf("reminder on mailbox %d, want the label %d (Trash is %d)", h.MailboxID, label.ID, trash.ID)
	}
	if h.WakeMailboxID == nil || *h.WakeMailboxID != w.ID {
		t.Errorf("wake_mailbox_id = %v, want %d", h.WakeMailboxID, w.ID)
	}
	s.expectSnoozedView("after snoozing")
	if got := dueCount(t, f, s.mid); got != 1 {
		t.Errorf("wake sweep lists %d due reminders, want 1", got)
	}

	// Clearing through Email/set removes the reminder from the holder.
	s.set(map[string]any{"snoozedUntil": nil})
	kw, until := jmapSnooze(t, s)
	if kw["$snoozed"] || until != nil {
		t.Errorf("after clear: keywords=%v snoozedUntil=%v, want none", kw, until)
	}
	gm, err := f.srv.Store.Meta().GetMessage(context.Background(), s.mid)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	for _, mm := range gm.Mailboxes {
		if mm.SnoozedUntil != nil {
			t.Errorf("membership %d still holds a reminder after clear", mm.MailboxID)
		}
	}
}

func testSnoozedLabelGainsLowIDTrash(t *testing.T, f *fixture) {
	trash := lowIDTrash(t, f)
	s := newSnoozeScenario(t, f)
	label := s.mailbox("Label")
	s.set(map[string]any{"mailboxIds": map[string]bool{fmt.Sprintf("%d", label.ID): true}})
	s.set(map[string]any{"snoozedUntil": "2030-06-01T12:00:00Z"})
	s.expectSnoozedView("snoozed in the label")

	s.set(map[string]any{fmt.Sprintf("mailboxIds/%d", trash.ID): true})
	if h := snoozeHolder(t, f, s.mid); h.MailboxID != label.ID {
		t.Fatalf("reminder on mailbox %d, want the label %d", h.MailboxID, label.ID)
	}
	s.expectSnoozedView("after adding Trash")

	// The patch that clears the reminder reaches the holder too.
	s.set(map[string]any{"snoozedUntil": nil})
	if kw, until := jmapSnooze(t, s); kw["$snoozed"] || until != nil {
		t.Errorf("after clear: keywords=%v snoozedUntil=%v, want none", kw, until)
	}
	if got := dueCount(t, f, s.mid); got != 0 {
		t.Errorf("wake sweep lists %d due reminders after clear, want 0", got)
	}
}

func testSnoozeTrashOnlyRejected(t *testing.T, f *fixture) {
	trash := lowIDTrash(t, f)
	s := newSnoozeScenario(t, f)
	s.set(map[string]any{"mailboxIds": map[string]bool{fmt.Sprintf("%d", trash.ID): true}})
	_, raw := f.invoke(t, "Email/set", map[string]any{
		"accountId": s.acct,
		"update":    map[string]any{s.id: map[string]any{"snoozedUntil": "2030-06-01T12:00:00Z"}},
	})
	var resp struct {
		NotUpdated map[string]map[string]any `json:"notUpdated"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	e := resp.NotUpdated[s.id]
	if e == nil || e["type"] != "invalidProperties" {
		t.Fatalf("notUpdated=%v, want invalidProperties", resp.NotUpdated)
	}
	if props, _ := e["properties"].([]any); len(props) != 1 || props[0] != "snoozedUntil" {
		t.Errorf("properties = %v, want [snoozedUntil]", e["properties"])
	}
	if kw, until := jmapSnooze(t, s); kw["$snoozed"] || until != nil {
		t.Errorf("rejected snooze left state: keywords=%v snoozedUntil=%v", kw, until)
	}
	if got := dueCount(t, f, s.mid); got != 0 {
		t.Errorf("wake sweep lists %d due reminders, want 0", got)
	}
}
