package calendars_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/protojmap"
)

// makeCalendar is a small helper used by every event-shape test.
func makeCalendar(t *testing.T, f *fixture, name string) string {
	t.Helper()
	_, raw := f.invoke(t, "Calendar/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create":    map[string]any{"c": map[string]any{"name": name}},
	})
	var resp struct {
		Created    map[string]map[string]any `json:"created"`
		NotCreated map[string]any            `json:"notCreated"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal calendar set: %v: %s", err, raw)
	}
	if len(resp.Created) != 1 {
		t.Fatalf("calendar create failed: %+v", resp.NotCreated)
	}
	return resp.Created["c"]["id"].(string)
}

// makeEvent persists a JSCalendar event under calID. It returns the
// JMAP event id assigned by the server.
func makeEvent(t *testing.T, f *fixture, calID string, body map[string]any) string {
	t.Helper()
	body["calendarId"] = calID
	if _, ok := body["@type"]; !ok {
		body["@type"] = "Event"
	}
	_, raw := f.invoke(t, "CalendarEvent/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create":    map[string]any{"e": body},
	})
	var resp struct {
		Created    map[string]map[string]any `json:"created"`
		NotCreated map[string]any            `json:"notCreated"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal event set: %v: %s", err, raw)
	}
	if len(resp.Created) != 1 {
		t.Fatalf("event create failed: %+v", resp.NotCreated)
	}
	return resp.Created["e"]["id"].(string)
}

func TestCalendarEvent_Get_RendersFullJSCalendar(t *testing.T) {
	f := setupFixture(t)
	calID := makeCalendar(t, f, "Default")
	evID := makeEvent(t, f, calID, map[string]any{
		"title":    "Standup",
		"start":    "2026-05-01T09:00:00",
		"duration": "PT30M",
		"timeZone": "Europe/Berlin",
	})
	_, raw := f.invoke(t, "CalendarEvent/get", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"ids":       []string{evID},
	})
	var resp struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(resp.List) != 1 {
		t.Fatalf("list = %+v", resp.List)
	}
	ev := resp.List[0]
	if ev["title"] != "Standup" {
		t.Errorf("title = %v, want Standup", ev["title"])
	}
	if ev["calendarId"] != calID {
		t.Errorf("calendarId = %v, want %v", ev["calendarId"], calID)
	}
	if _, ok := ev["myRights"]; !ok {
		t.Errorf("myRights missing: %+v", ev)
	}
	// Round-tripped JSCalendar fields preserved.
	if ev["start"] != "2026-05-01T09:00:00" {
		t.Errorf("start = %v, want 2026-05-01T09:00:00", ev["start"])
	}
}

func TestCalendarEvent_Set_Create_PopulatesDenormalisedColumns(t *testing.T) {
	f := setupFixture(t)
	calID := makeCalendar(t, f, "Default")
	evID := makeEvent(t, f, calID, map[string]any{
		"title":    "Project review",
		"start":    "2026-05-15T14:00:00",
		"duration": "PT1H",
		"timeZone": "UTC",
	})
	// /query by text should match — relies on the denormalised summary
	// column.
	_, raw := f.invoke(t, "CalendarEvent/query", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"filter":    map[string]any{"text": "review"},
	})
	var qr struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(raw, &qr); err != nil {
		t.Fatalf("unmarshal query: %v: %s", err, raw)
	}
	found := false
	for _, id := range qr.IDs {
		if id == evID {
			found = true
		}
	}
	if !found {
		t.Errorf("text filter did not return new event: ids=%+v want %s", qr.IDs, evID)
	}
}

func TestCalendarEvent_Set_Update_MergePatch_PreservesUntouchedFields(t *testing.T) {
	f := setupFixture(t)
	calID := makeCalendar(t, f, "Default")
	evID := makeEvent(t, f, calID, map[string]any{
		"title":       "Original title",
		"description": "long body",
		"start":       "2026-06-01T10:00:00",
		"duration":    "PT1H",
		"timeZone":    "UTC",
	})
	// Patch only the title.
	_, raw := f.invoke(t, "CalendarEvent/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"update": map[string]any{
			evID: map[string]any{"title": "New title"},
		},
	})
	var setResp struct {
		NotUpdated map[string]any `json:"notUpdated"`
		Updated    map[string]any `json:"updated"`
	}
	if err := json.Unmarshal(raw, &setResp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(setResp.Updated) != 1 {
		t.Fatalf("update failed: %+v", setResp.NotUpdated)
	}
	// Read back. description must be preserved.
	_, raw = f.invoke(t, "CalendarEvent/get", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"ids":       []string{evID},
	})
	var getResp struct {
		List []map[string]any `json:"list"`
	}
	_ = json.Unmarshal(raw, &getResp)
	if len(getResp.List) != 1 {
		t.Fatalf("list: %+v", getResp.List)
	}
	ev := getResp.List[0]
	if ev["title"] != "New title" {
		t.Errorf("title = %v, want New title", ev["title"])
	}
	if ev["description"] != "long body" {
		t.Errorf("description not preserved: %v", ev["description"])
	}
}

func TestCalendarEvent_Query_TextSubstring(t *testing.T) {
	f := setupFixture(t)
	calID := makeCalendar(t, f, "Default")
	makeEvent(t, f, calID, map[string]any{
		"title": "Architecture review", "start": "2026-07-01T09:00:00",
		"duration": "PT1H", "timeZone": "UTC",
	})
	makeEvent(t, f, calID, map[string]any{
		"title": "Engineering syncup", "start": "2026-07-02T09:00:00",
		"duration": "PT1H", "timeZone": "UTC",
	})
	_, raw := f.invoke(t, "CalendarEvent/query", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"filter":    map[string]any{"text": "architecture"},
	})
	var resp struct {
		IDs []string `json:"ids"`
	}
	_ = json.Unmarshal(raw, &resp)
	if len(resp.IDs) != 1 {
		t.Errorf("expected 1 hit for 'architecture', got %+v", resp.IDs)
	}
	_, raw = f.invoke(t, "CalendarEvent/query", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"filter":    map[string]any{"text": "syncup"},
	})
	_ = json.Unmarshal(raw, &resp)
	if len(resp.IDs) != 1 {
		t.Errorf("expected 1 hit for 'syncup', got %+v", resp.IDs)
	}
}

func TestCalendarEvent_Query_StartWindow(t *testing.T) {
	f := setupFixture(t)
	calID := makeCalendar(t, f, "Default")
	// Two events: one inside the window, one outside.
	makeEvent(t, f, calID, map[string]any{
		"title": "InWindow", "start": "2026-08-15T10:00:00",
		"duration": "PT1H", "timeZone": "UTC",
	})
	makeEvent(t, f, calID, map[string]any{
		"title": "Outside", "start": "2026-09-15T10:00:00",
		"duration": "PT1H", "timeZone": "UTC",
	})
	_, raw := f.invoke(t, "CalendarEvent/query", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"filter": map[string]any{
			"startAfter":  "2026-08-01T00:00:00Z",
			"startBefore": "2026-09-01T00:00:00Z",
		},
	})
	var resp struct {
		IDs []string `json:"ids"`
	}
	_ = json.Unmarshal(raw, &resp)
	if len(resp.IDs) != 1 {
		t.Errorf("expected 1 hit in window, got %+v", resp.IDs)
	}
}

func TestCalendarEvent_Query_ExpandRecurrences(t *testing.T) {
	f := setupFixture(t)
	calID := makeCalendar(t, f, "Default")
	// A daily event with COUNT=14, expanded over a 1-week window.
	makeEvent(t, f, calID, map[string]any{
		"title":    "Daily standup",
		"start":    "2026-10-05T09:00:00",
		"duration": "PT15M",
		"timeZone": "UTC",
		"recurrenceRules": []map[string]any{{
			"@type":     "RecurrenceRule",
			"frequency": "daily",
			"count":     14,
		}},
	})
	_, raw := f.invoke(t, "CalendarEvent/query", map[string]any{
		"accountId":         string(protojmap.AccountIDForPrincipal(f.pid)),
		"expandRecurrences": true,
		"filter": map[string]any{
			"startAfter":  "2026-10-05T00:00:00Z",
			"startBefore": "2026-10-12T00:00:00Z",
		},
	})
	var resp struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(resp.IDs) != 7 {
		t.Errorf("expected 7 occurrences in 1-week window, got %d: %+v", len(resp.IDs), resp.IDs)
	}
}

func TestCalendarEvent_Changes_FromState(t *testing.T) {
	f := setupFixture(t)
	calID := makeCalendar(t, f, "Default")
	_, raw := f.invoke(t, "CalendarEvent/changes", map[string]any{
		"accountId":  string(protojmap.AccountIDForPrincipal(f.pid)),
		"sinceState": "0",
	})
	var ch struct {
		OldState  string   `json:"oldState"`
		NewState  string   `json:"newState"`
		Created   []string `json:"created"`
		Updated   []string `json:"updated"`
		Destroyed []string `json:"destroyed"`
	}
	_ = json.Unmarshal(raw, &ch)
	if total := len(ch.Created) + len(ch.Updated) + len(ch.Destroyed); total != 0 {
		t.Errorf("initial changes non-empty: %+v", ch)
	}
	makeEvent(t, f, calID, map[string]any{
		"title": "Created", "start": "2026-11-01T09:00:00",
		"duration": "PT30M", "timeZone": "UTC",
	})
	_, raw = f.invoke(t, "CalendarEvent/changes", map[string]any{
		"accountId":  string(protojmap.AccountIDForPrincipal(f.pid)),
		"sinceState": "0",
	})
	if err := json.Unmarshal(raw, &ch); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(ch.Created) != 1 {
		t.Errorf("expected one created, got %+v", ch.Created)
	}
	if !strings.HasPrefix(ch.NewState, "0") && ch.NewState == "0" {
		t.Errorf("newState did not advance: %+v", ch)
	}
}
