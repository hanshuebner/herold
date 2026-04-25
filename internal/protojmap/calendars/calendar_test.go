package calendars_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/protojmap/calendars"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
)

// fixture wires a harness with a JMAP listener attached to a Server
// preloaded with the Calendar/* and CalendarEvent/* handlers. Mirrors
// the contacts addressbook_test.go fixture exactly so the patterns are
// recognisable to readers familiar with the sibling package.
type fixture struct {
	srv      *testharness.Server
	pid      store.PrincipalID
	client   *http.Client
	baseURL  string
	apiKey   string
	jmapServ *protojmap.Server
}

func setupFixture(t *testing.T) *fixture {
	t.Helper()
	srv, _ := testharness.Start(t, testharness.Options{
		Listeners: []testharness.ListenerSpec{{Name: "jmap", Protocol: "jmap"}},
	})

	ctx := context.Background()
	p, err := srv.Store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	plaintext := "hk_test_alice_" + fmt.Sprintf("%d", p.ID)
	hash := protoadmin.HashAPIKey(plaintext)
	if _, err := srv.Store.Meta().InsertAPIKey(ctx, store.APIKey{
		PrincipalID: p.ID,
		Hash:        hash,
		Name:        "test",
	}); err != nil {
		t.Fatalf("InsertAPIKey: %v", err)
	}

	dir := directory.New(srv.Store.Meta(), srv.Logger, srv.Clock, nil)
	jmapServ := protojmap.NewServer(srv.Store, dir, nil, srv.Logger, srv.Clock, protojmap.Options{})
	calendars.Register(jmapServ.Registry(), srv.Store, srv.Logger, srv.Clock)

	if err := srv.AttachJMAP("jmap", jmapServ, protojmap.ListenerModePlain); err != nil {
		t.Fatalf("AttachJMAP: %v", err)
	}
	client, base := srv.DialJMAPByName(ctx, "jmap")
	return &fixture{
		srv: srv, pid: p.ID, client: client, baseURL: base,
		apiKey: plaintext, jmapServ: jmapServ,
	}
}

func (f *fixture) invoke(t *testing.T, method string, args any, using ...protojmap.CapabilityID) (string, json.RawMessage) {
	t.Helper()
	if len(using) == 0 {
		using = []protojmap.CapabilityID{protojmap.CapabilityCore, protojmap.CapabilityJMAPCalendars}
	}
	argsBytes, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	body := map[string]any{
		"using":       using,
		"methodCalls": []any{[]any{method, json.RawMessage(argsBytes), "c0"}},
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, f.baseURL+"/jmap", bytes.NewReader(bodyBytes))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+f.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, respBody)
	}
	var envelope struct {
		MethodResponses []protojmap.Invocation `json:"methodResponses"`
	}
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		t.Fatalf("unmarshal envelope: %v: %s", err, respBody)
	}
	if len(envelope.MethodResponses) != 1 {
		t.Fatalf("got %d method responses, want 1", len(envelope.MethodResponses))
	}
	inv := envelope.MethodResponses[0]
	return inv.Name, inv.Args
}

// -- Calendar tests ---------------------------------------------------

func TestCalendar_Get_Set_RoundTrip(t *testing.T) {
	f := setupFixture(t)
	_, raw := f.invoke(t, "Calendar/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create": map[string]any{
			"cal1": map[string]any{
				"name":        "Work",
				"description": "Work calendar",
				"timeZone":    "Europe/Berlin",
			},
		},
	})
	var setResp struct {
		Created    map[string]map[string]any `json:"created"`
		NotCreated map[string]any            `json:"notCreated"`
	}
	if err := json.Unmarshal(raw, &setResp); err != nil {
		t.Fatalf("unmarshal set: %v: %s", err, raw)
	}
	if len(setResp.Created) != 1 {
		t.Fatalf("created = %+v, NotCreated = %+v", setResp.Created, setResp.NotCreated)
	}
	created := setResp.Created["cal1"]
	if created["name"] != "Work" {
		t.Errorf("created.name = %v, want Work", created["name"])
	}
	id, ok := created["id"].(string)
	if !ok || id == "" {
		t.Fatalf("created has no id: %+v", created)
	}

	_, raw = f.invoke(t, "Calendar/get", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"ids":       []string{id},
	})
	var getResp struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(raw, &getResp); err != nil {
		t.Fatalf("unmarshal get: %v: %s", err, raw)
	}
	if len(getResp.List) != 1 {
		t.Fatalf("list = %+v", getResp.List)
	}
	if getResp.List[0]["name"] != "Work" {
		t.Errorf("get.name = %v, want Work", getResp.List[0]["name"])
	}
}

func TestCalendar_Set_Create_AutoDefaultWhenNoneExists(t *testing.T) {
	f := setupFixture(t)
	_, raw := f.invoke(t, "Calendar/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create": map[string]any{
			"cal1": map[string]any{"name": "First"},
		},
	})
	var resp struct {
		Created map[string]map[string]any `json:"created"`
	}
	_ = json.Unmarshal(raw, &resp)
	if v, ok := resp.Created["cal1"]["isDefault"].(bool); !ok || !v {
		t.Fatalf("first calendar isDefault = %v, want true: %+v", resp.Created["cal1"]["isDefault"], resp.Created["cal1"])
	}

	_, raw2 := f.invoke(t, "Calendar/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create": map[string]any{
			"cal2": map[string]any{"name": "Second"},
		},
	})
	var resp2 struct {
		Created map[string]map[string]any `json:"created"`
	}
	_ = json.Unmarshal(raw2, &resp2)
	if v, ok := resp2.Created["cal2"]["isDefault"].(bool); !ok || v {
		t.Fatalf("second calendar isDefault = %v, want false: %+v", resp2.Created["cal2"]["isDefault"], resp2.Created["cal2"])
	}
}

func TestCalendar_Set_Destroy_CascadesEvents(t *testing.T) {
	f := setupFixture(t)
	// Create a calendar.
	_, raw := f.invoke(t, "Calendar/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create":    map[string]any{"c": map[string]any{"name": "C"}},
	})
	var setCalResp struct {
		Created map[string]map[string]any `json:"created"`
	}
	_ = json.Unmarshal(raw, &setCalResp)
	calID := setCalResp.Created["c"]["id"].(string)

	// Add an event in the calendar.
	_, raw = f.invoke(t, "CalendarEvent/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create": map[string]any{
			"e1": map[string]any{
				"@type":      "Event",
				"calendarId": calID,
				"title":      "Standup",
				"start":      "2026-05-01T09:00:00",
				"duration":   "PT30M",
				"timeZone":   "Europe/Berlin",
			},
		},
	})
	var setEvResp struct {
		Created    map[string]map[string]any `json:"created"`
		NotCreated map[string]any            `json:"notCreated"`
	}
	if err := json.Unmarshal(raw, &setEvResp); err != nil {
		t.Fatalf("unmarshal event set: %v", err)
	}
	if len(setEvResp.Created) != 1 {
		t.Fatalf("event create failed: %+v", setEvResp.NotCreated)
	}
	evID := setEvResp.Created["e1"]["id"].(string)

	// Destroy the calendar.
	_, raw = f.invoke(t, "Calendar/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"destroy":   []string{calID},
	})
	var destroyResp struct {
		Destroyed []string `json:"destroyed"`
	}
	_ = json.Unmarshal(raw, &destroyResp)
	if len(destroyResp.Destroyed) != 1 {
		t.Fatalf("destroyed = %+v", destroyResp.Destroyed)
	}

	// Both should be gone.
	_, raw = f.invoke(t, "CalendarEvent/get", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"ids":       []string{evID},
	})
	var evGet struct {
		List     []map[string]any `json:"list"`
		NotFound []string         `json:"notFound"`
	}
	_ = json.Unmarshal(raw, &evGet)
	if len(evGet.List) != 0 {
		t.Errorf("event still present after calendar destroy: %+v", evGet.List)
	}
}

func TestCalendar_Changes_FromState(t *testing.T) {
	f := setupFixture(t)
	_, raw := f.invoke(t, "Calendar/changes", map[string]any{
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
	if err := json.Unmarshal(raw, &ch); err != nil {
		t.Fatalf("unmarshal changes: %v: %s", err, raw)
	}
	if ch.OldState != "0" || ch.NewState != "0" {
		t.Errorf("initial states = %q/%q, want 0/0", ch.OldState, ch.NewState)
	}
	if total := len(ch.Created) + len(ch.Updated) + len(ch.Destroyed); total != 0 {
		t.Errorf("initial changes non-empty: %+v", ch)
	}

	// Create a calendar, then ask for changes from "0".
	_, _ = f.invoke(t, "Calendar/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create":    map[string]any{"c": map[string]any{"name": "X"}},
	})
	_, raw = f.invoke(t, "Calendar/changes", map[string]any{
		"accountId":  string(protojmap.AccountIDForPrincipal(f.pid)),
		"sinceState": "0",
	})
	if err := json.Unmarshal(raw, &ch); err != nil {
		t.Fatalf("unmarshal changes2: %v: %s", err, raw)
	}
	if ch.NewState == "0" {
		t.Errorf("newState did not advance after create: %+v", ch)
	}
	if len(ch.Created) != 1 {
		t.Errorf("expected one created entry, got %+v", ch.Created)
	}
}

func TestCapabilityDescriptor_AdvertisesCalendarLimits(t *testing.T) {
	f := setupFixture(t)
	// Hit the JMAP session endpoint to verify the per-account
	// descriptor for the calendars capability advertises the limits
	// struct (max-calendars-per-account et al).
	req, err := http.NewRequest(http.MethodGet, f.baseURL+"/.well-known/jmap", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+f.apiKey)
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var sess struct {
		Capabilities map[string]any `json:"capabilities"`
		Accounts     map[string]struct {
			AccountCapabilities map[string]any `json:"accountCapabilities"`
		} `json:"accounts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sess); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if _, ok := sess.Capabilities["urn:ietf:params:jmap:calendars"]; !ok {
		t.Fatalf("calendars capability not advertised: %+v", sess.Capabilities)
	}
	for _, acc := range sess.Accounts {
		body, ok := acc.AccountCapabilities["urn:ietf:params:jmap:calendars"]
		if !ok {
			t.Fatalf("calendars per-account descriptor missing: %+v", acc.AccountCapabilities)
		}
		m, ok := body.(map[string]any)
		if !ok {
			t.Fatalf("calendars per-account descriptor is not an object: %T", body)
		}
		if _, ok := m["maxCalendarsPerAccount"]; !ok {
			t.Errorf("descriptor missing maxCalendarsPerAccount: %+v", m)
		}
		if _, ok := m["maxEventsPerCalendar"]; !ok {
			t.Errorf("descriptor missing maxEventsPerCalendar: %+v", m)
		}
		if _, ok := m["maxSizePerEventBlob"]; !ok {
			t.Errorf("descriptor missing maxSizePerEventBlob: %+v", m)
		}
	}
}
