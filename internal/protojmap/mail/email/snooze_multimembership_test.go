package email_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// Issue #548 (lead 7828462f): an Email/query without inMailbox reads
// keywords from a message's lowest-mailbox_id membership. The product
// can leave a reminder on a higher-id membership: by adding a membership
// with a lower id, or by moving the snoozed membership above another one.
// Either way the message must stay in the Snoozed view (hasKeyword
// $snoozed), keep its deadline on the row Email/get reads, and not gain a
// dateless "$snoozed" copy.

func TestEmailQuery_SnoozedAfterAddingLowerMembership_SQLite(t *testing.T) {
	testSnoozedAfterAddingLowerMembership(t, setupFixture(t))
}

func TestEmailQuery_SnoozedAfterAddingLowerMembership_Postgres(t *testing.T) {
	testSnoozedAfterAddingLowerMembership(t, setupFixturePostgres(t))
}

func TestEmailQuery_SnoozedAfterMoveAboveSibling_SQLite(t *testing.T) {
	testSnoozedAfterMoveAboveSibling(t, setupFixture(t))
}

func TestEmailQuery_SnoozedAfterMoveAboveSibling_Postgres(t *testing.T) {
	testSnoozedAfterMoveAboveSibling(t, setupFixturePostgres(t))
}

type snoozeScenario struct {
	t    *testing.T
	f    *fixture
	acct string
	id   string
	mid  store.MessageID
}

func newSnoozeScenario(t *testing.T, f *fixture) *snoozeScenario {
	body := "From: a@example.test\r\nTo: b@example.test\r\nSubject: hi\r\n\r\nhi"
	m := f.insertMessage(t, body, "hi", "a@example.test", "b@example.test", nil, "")
	return &snoozeScenario{
		t: t, f: f, acct: protojmap.AccountIDForPrincipal(f.pid),
		id: fmt.Sprintf("%d", m.ID), mid: m.ID,
	}
}

func (s *snoozeScenario) mailbox(name string) store.Mailbox {
	mb, err := s.f.srv.Store.Meta().InsertMailbox(context.Background(),
		store.Mailbox{PrincipalID: s.f.pid, Name: name})
	if err != nil {
		s.t.Fatalf("InsertMailbox(%s): %v", name, err)
	}
	return mb
}

func (s *snoozeScenario) set(patch map[string]any) {
	s.t.Helper()
	_, raw := s.f.invoke(s.t, "Email/set", map[string]any{
		"accountId": s.acct,
		"update":    map[string]any{s.id: patch},
	})
	var resp struct {
		NotUpdated map[string]any `json:"notUpdated"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil || len(resp.NotUpdated) != 0 {
		s.t.Fatalf("Email/set %v: %v %s", patch, err, raw)
	}
}

func (s *snoozeScenario) querySnoozed() []string {
	s.t.Helper()
	_, raw := s.f.invoke(s.t, "Email/query", map[string]any{
		"accountId": s.acct,
		"filter":    map[string]any{"hasKeyword": "$snoozed"},
	})
	var resp struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		s.t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	return resp.IDs
}

// check asserts the message is listed once as snoozed, Email/get still
// reports its deadline, and exactly one membership carries the pair.
func (s *snoozeScenario) check(stage string) {
	s.t.Helper()
	if got := s.querySnoozed(); len(got) != 1 || got[0] != s.id {
		s.t.Errorf("%s: hasKeyword $snoozed = %v, want [%s]", stage, got, s.id)
	}
	if messageSnoozedUntil(s.t, s.f, s.mid) == nil || !messageHasSnoozeKeyword(s.t, s.f, s.mid) {
		s.t.Errorf("%s: Email/get no longer reports the reminder", stage)
	}
	gm, err := s.f.srv.Store.Meta().GetMessage(context.Background(), s.mid)
	if err != nil {
		s.t.Fatalf("GetMessage: %v", err)
	}
	holders := 0
	for _, mm := range gm.Mailboxes {
		hasKW := false
		for _, k := range mm.Keywords {
			if k == "$snoozed" {
				hasKW = true
			}
		}
		if hasKW != (mm.SnoozedUntil != nil) {
			s.t.Errorf("%s: membership %d keyword/deadline pair split (kw=%v until=%v)",
				stage, mm.MailboxID, hasKW, mm.SnoozedUntil)
		}
		if mm.SnoozedUntil != nil {
			holders++
		}
	}
	if holders != 1 {
		s.t.Errorf("%s: %d memberships hold the reminder, want 1", stage, holders)
	}
}

func testSnoozedAfterAddingLowerMembership(t *testing.T, f *fixture) {
	s := newSnoozeScenario(t, f)
	archive := s.mailbox("Archive")
	// Message lives only in Archive (id above Inbox), snoozed there.
	s.set(map[string]any{"mailboxIds": map[string]bool{fmt.Sprintf("%d", archive.ID): true}})
	s.set(map[string]any{"snoozedUntil": "2030-06-01T12:00:00Z"})
	s.check("snoozed in Archive")
	// Inbox (lower id) is added; the reminder is not touched.
	s.set(map[string]any{fmt.Sprintf("mailboxIds/%d", f.inbox.ID): true})
	s.check("after adding Inbox")
}

func testSnoozedAfterMoveAboveSibling(t *testing.T, f *fixture) {
	s := newSnoozeScenario(t, f)
	a := s.mailbox("A")
	b := s.mailbox("B")
	c := s.mailbox("C")
	// Message in A and B; the reminder is set on the canonical row (A).
	s.set(map[string]any{"mailboxIds": map[string]bool{
		fmt.Sprintf("%d", a.ID): true, fmt.Sprintf("%d", b.ID): true,
	}})
	s.set(map[string]any{"snoozedUntil": "2030-06-01T12:00:00Z"})
	s.check("snoozed in A+B")
	// Moving the snoozed membership A -> C puts it above B.
	s.set(map[string]any{
		fmt.Sprintf("mailboxIds/%d", a.ID): nil,
		fmt.Sprintf("mailboxIds/%d", c.ID): true,
	})
	s.check("after moving A -> C")
}
