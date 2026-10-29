package email_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/protojmap"
)

// Issue #548: a snoozedUntil that is not in the future turns a reminder
// into an immediate wake (the worker releases it within a minute), so
// Email/set refuses it with invalidProperties. The fixture clock reads
// 2026-01-01T00:00:00Z; a few seconds of clock skew are tolerated.

func TestEmailSet_SnoozedUntilInPast_Rejected_SQLite(t *testing.T) {
	testSnoozedUntilPast(t, setupFixture(t))
}

func TestEmailSet_SnoozedUntilInPast_Rejected_Postgres(t *testing.T) {
	testSnoozedUntilPast(t, setupFixturePostgres(t))
}

func testSnoozedUntilPast(t *testing.T, f *fixture) {
	body := "From: a@example.test\r\nTo: b@example.test\r\nSubject: hi\r\n\r\nhi"
	m := f.insertMessage(t, body, "hi", "a@example.test", "b@example.test", nil, "")
	id := fmt.Sprintf("%d", m.ID)

	update := func(until string) (map[string]any, map[string]map[string]any) {
		_, raw := f.invoke(t, "Email/set", map[string]any{
			"accountId": protojmap.AccountIDForPrincipal(f.pid),
			"update":    map[string]any{id: map[string]any{"snoozedUntil": until}},
		})
		var resp struct {
			Updated    map[string]any            `json:"updated"`
			NotUpdated map[string]map[string]any `json:"notUpdated"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			t.Fatalf("unmarshal: %v: %s", err, raw)
		}
		return resp.Updated, resp.NotUpdated
	}

	// Past by hours (the stale-picker case): rejected, nothing stored.
	_, notUpdated := update("2025-12-31T07:00:00Z")
	e := notUpdated[id]
	if e == nil || e["type"] != "invalidProperties" {
		t.Fatalf("past update: notUpdated=%v, want invalidProperties", notUpdated)
	}
	if props, _ := e["properties"].([]any); len(props) != 1 || props[0] != "snoozedUntil" {
		t.Errorf("properties = %v, want [snoozedUntil]", e["properties"])
	}
	if messageHasSnoozeKeyword(t, f, m.ID) || messageSnoozedUntil(t, f, m.ID) != nil {
		t.Errorf("rejected snooze left state behind")
	}

	// Within the skew tolerance: accepted.
	if updated, notUpdated := update("2025-12-31T23:59:55Z"); len(notUpdated) != 0 || len(updated) != 1 {
		t.Errorf("just-now value: updated=%v notUpdated=%v, want accepted", updated, notUpdated)
	}

	// The create path applies the same rule.
	ref := f.putBlob(t, "From: a@example.test\r\nTo: b@example.test\r\nSubject: c\r\n\r\nc")
	_, raw := f.invoke(t, "Email/set", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"create": map[string]any{"n1": map[string]any{
			"blobId":       ref.Hash,
			"mailboxIds":   map[string]bool{fmt.Sprintf("%d", f.inbox.ID): true},
			"snoozedUntil": "2025-12-31T07:00:00Z",
		}},
	})
	if !strings.Contains(string(raw), `"notCreated"`) || !strings.Contains(string(raw), "invalidProperties") {
		t.Errorf("create with past snoozedUntil not rejected: %s", raw)
	}
}
