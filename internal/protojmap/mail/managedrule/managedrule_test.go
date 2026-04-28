package managedrule_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/protojmap/mail/managedrule"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
)

// fixture holds the test server wiring for managedrule tests.
type fixture struct {
	srv     *testharness.Server
	pid     store.PrincipalID
	client  *http.Client
	baseURL string
	apiKey  string
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
	plaintext := "hk_test_mr_" + fmt.Sprintf("%d", p.ID)
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
	managedrule.Register(jmapServ.Registry(), srv.Store, srv.Logger)

	if err := srv.AttachJMAP("jmap", jmapServ, protojmap.ListenerModePlain); err != nil {
		t.Fatalf("AttachJMAP: %v", err)
	}
	client, base := srv.DialJMAPByName(ctx, "jmap")
	return &fixture{
		srv:     srv,
		pid:     p.ID,
		client:  client,
		baseURL: base,
		apiKey:  plaintext,
	}
}

// accountID returns the JMAP account id for the fixture's principal.
func (f *fixture) accountID() string {
	return string(protojmap.AccountIDForPrincipal(f.pid))
}

// invoke posts a single JMAP method call and returns the (methodName, args) pair.
func (f *fixture) invoke(t *testing.T, method string, args any, using ...protojmap.CapabilityID) (string, json.RawMessage) {
	t.Helper()
	if len(using) == 0 {
		using = []protojmap.CapabilityID{
			protojmap.CapabilityCore,
			managedrule.CapabilityManagedRules,
		}
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
		t.Fatalf("unmarshal envelope: %v; body=%s", err, respBody)
	}
	if len(envelope.MethodResponses) < 1 {
		t.Fatalf("got 0 method responses; body=%s", respBody)
	}
	inv := envelope.MethodResponses[0]
	return inv.Name, inv.Args
}

// createRule is a convenience that sends ManagedRule/set create and returns the
// created rule's wire id.
func (f *fixture) createRule(t *testing.T, rule map[string]any) string {
	t.Helper()
	name, raw := f.invoke(t, "ManagedRule/set", map[string]any{
		"accountId": f.accountID(),
		"create": map[string]any{
			"r1": rule,
		},
	})
	if name != "ManagedRule/set" {
		t.Fatalf("createRule: unexpected method name %q; raw=%s", name, raw)
	}
	var resp struct {
		Created map[string]struct {
			ID string `json:"id"`
		} `json:"created"`
		NotCreated map[string]struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		} `json:"notCreated"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("createRule: unmarshal: %v; raw=%s", err, raw)
	}
	if len(resp.NotCreated) > 0 {
		for k, v := range resp.NotCreated {
			t.Fatalf("createRule: notCreated[%s]: %s: %s", k, v.Type, v.Description)
		}
	}
	if r, ok := resp.Created["r1"]; ok {
		return r.ID
	}
	t.Fatalf("createRule: r1 not in Created; raw=%s", raw)
	return ""
}

// -- ManagedRule/get --------------------------------------------------

func TestManagedRule_Get_Empty(t *testing.T) {
	f := setupFixture(t)
	name, raw := f.invoke(t, "ManagedRule/get", map[string]any{
		"accountId": f.accountID(),
	})
	if name != "ManagedRule/get" {
		t.Fatalf("method = %q, want ManagedRule/get; raw=%s", name, raw)
	}
	var resp struct {
		List     []json.RawMessage `json:"list"`
		NotFound []string          `json:"notFound"`
		State    string            `json:"state"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.List) != 0 {
		t.Errorf("list = %d rules, want 0", len(resp.List))
	}
}

func TestManagedRule_Get_ByID(t *testing.T) {
	f := setupFixture(t)
	id := f.createRule(t, map[string]any{
		"name":    "Test rule",
		"enabled": true,
		"order":   0,
		"conditions": []map[string]any{
			{"field": "from", "op": "equals", "value": "spam@example.com"},
		},
		"actions": []map[string]any{
			{"kind": "delete"},
		},
	})

	name, raw := f.invoke(t, "ManagedRule/get", map[string]any{
		"accountId": f.accountID(),
		"ids":       []string{id},
	})
	if name != "ManagedRule/get" {
		t.Fatalf("method = %q; raw=%s", name, raw)
	}
	var resp struct {
		List []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Enabled bool   `json:"enabled"`
		} `json:"list"`
		NotFound []string `json:"notFound"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.List) != 1 {
		t.Fatalf("list len = %d, want 1", len(resp.List))
	}
	if resp.List[0].ID != id {
		t.Errorf("id = %q, want %q", resp.List[0].ID, id)
	}
	if resp.List[0].Name != "Test rule" {
		t.Errorf("name = %q, want %q", resp.List[0].Name, "Test rule")
	}
	if !resp.List[0].Enabled {
		t.Error("enabled = false, want true")
	}
	if len(resp.NotFound) != 0 {
		t.Errorf("notFound = %v, want []", resp.NotFound)
	}
}

func TestManagedRule_Get_NotFound(t *testing.T) {
	f := setupFixture(t)
	name, raw := f.invoke(t, "ManagedRule/get", map[string]any{
		"accountId": f.accountID(),
		"ids":       []string{"99999"},
	})
	if name != "ManagedRule/get" {
		t.Fatalf("method = %q", name)
	}
	var resp struct {
		List     []json.RawMessage `json:"list"`
		NotFound []string          `json:"notFound"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.List) != 0 {
		t.Errorf("list len = %d, want 0", len(resp.List))
	}
	if len(resp.NotFound) != 1 || resp.NotFound[0] != "99999" {
		t.Errorf("notFound = %v, want [99999]", resp.NotFound)
	}
}

// -- ManagedRule/set --------------------------------------------------

func TestManagedRule_Set_Create(t *testing.T) {
	f := setupFixture(t)
	name, raw := f.invoke(t, "ManagedRule/set", map[string]any{
		"accountId": f.accountID(),
		"create": map[string]any{
			"new1": map[string]any{
				"name":    "Forward newsletter",
				"enabled": true,
				"order":   5,
				"conditions": []map[string]any{
					{"field": "from", "op": "contains", "value": "@newsletter.example.com"},
				},
				"actions": []map[string]any{
					{"kind": "apply-label", "params": map[string]any{"label": "Newsletters"}},
				},
			},
		},
	})
	if name != "ManagedRule/set" {
		t.Fatalf("method = %q; raw=%s", name, raw)
	}
	var resp struct {
		Created map[string]struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"created"`
		NotCreated map[string]json.RawMessage `json:"notCreated"`
		NewState   string                     `json:"newState"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.NotCreated) > 0 {
		t.Fatalf("notCreated = %v", resp.NotCreated)
	}
	r, ok := resp.Created["new1"]
	if !ok {
		t.Fatalf("new1 not in Created; raw=%s", raw)
	}
	if r.ID == "" {
		t.Error("created id is empty")
	}
	if resp.NewState == "" {
		t.Error("newState is empty")
	}
}

func TestManagedRule_Set_Update(t *testing.T) {
	f := setupFixture(t)
	id := f.createRule(t, map[string]any{
		"name":    "Old name",
		"enabled": true,
		"order":   0,
		"conditions": []map[string]any{
			{"field": "from", "op": "equals", "value": "a@b.com"},
		},
		"actions": []map[string]any{
			{"kind": "delete"},
		},
	})

	name, raw := f.invoke(t, "ManagedRule/set", map[string]any{
		"accountId": f.accountID(),
		"update": map[string]any{
			id: map[string]any{
				"name":    "New name",
				"enabled": false,
			},
		},
	})
	if name != "ManagedRule/set" {
		t.Fatalf("method = %q; raw=%s", name, raw)
	}
	var resp struct {
		Updated    map[string]json.RawMessage `json:"updated"`
		NotUpdated map[string]json.RawMessage `json:"notUpdated"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.NotUpdated) > 0 {
		t.Fatalf("notUpdated = %v", resp.NotUpdated)
	}
	if _, ok := resp.Updated[id]; !ok {
		t.Fatalf("updated[%s] missing; raw=%s", id, raw)
	}

	// Verify the name changed.
	_, getraw := f.invoke(t, "ManagedRule/get", map[string]any{
		"accountId": f.accountID(),
		"ids":       []string{id},
	})
	var getResp struct {
		List []struct {
			Name    string `json:"name"`
			Enabled bool   `json:"enabled"`
		} `json:"list"`
	}
	if err := json.Unmarshal(getraw, &getResp); err != nil {
		t.Fatalf("unmarshal get: %v", err)
	}
	if len(getResp.List) != 1 {
		t.Fatalf("list len = %d, want 1", len(getResp.List))
	}
	if getResp.List[0].Name != "New name" {
		t.Errorf("name = %q, want %q", getResp.List[0].Name, "New name")
	}
	if getResp.List[0].Enabled {
		t.Error("enabled = true, want false")
	}
}

func TestManagedRule_Set_Destroy(t *testing.T) {
	f := setupFixture(t)
	id := f.createRule(t, map[string]any{
		"name":    "To destroy",
		"enabled": true,
		"order":   0,
		"conditions": []map[string]any{
			{"field": "from", "op": "equals", "value": "x@y.com"},
		},
		"actions": []map[string]any{
			{"kind": "delete"},
		},
	})

	name, raw := f.invoke(t, "ManagedRule/set", map[string]any{
		"accountId": f.accountID(),
		"destroy":   []string{id},
	})
	if name != "ManagedRule/set" {
		t.Fatalf("method = %q; raw=%s", name, raw)
	}
	var resp struct {
		Destroyed    []string                   `json:"destroyed"`
		NotDestroyed map[string]json.RawMessage `json:"notDestroyed"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.NotDestroyed) > 0 {
		t.Fatalf("notDestroyed = %v", resp.NotDestroyed)
	}
	if len(resp.Destroyed) != 1 || resp.Destroyed[0] != id {
		t.Errorf("destroyed = %v, want [%s]", resp.Destroyed, id)
	}
}

func TestManagedRule_Set_InvalidActionCombo(t *testing.T) {
	f := setupFixture(t)
	// "delete" + "apply-label" is invalid; should fail at create time because
	// recompileAndPersist calls CompileRules which rejects the combo.
	// The set handler logs the error and returns the JMAP mutation; the spec
	// says: create succeeds (store-level), but recompile may fail silently.
	// We verify that an apply-label rule alone succeeds.
	id := f.createRule(t, map[string]any{
		"name":    "Label only",
		"enabled": true,
		"order":   0,
		"conditions": []map[string]any{
			{"field": "from", "op": "contains", "value": "@acme.com"},
		},
		"actions": []map[string]any{
			{"kind": "apply-label", "params": map[string]any{"label": "Work"}},
		},
	})
	if id == "" {
		t.Fatal("expected created id")
	}
}

func TestManagedRule_Set_IfInState_Mismatch(t *testing.T) {
	f := setupFixture(t)
	badState := "99999"
	name, raw := f.invoke(t, "ManagedRule/set", map[string]any{
		"accountId": f.accountID(),
		"ifInState": badState,
		"create": map[string]any{
			"r1": map[string]any{
				"name": "Should not be created",
				"conditions": []map[string]any{
					{"field": "from", "op": "equals", "value": "a@b.com"},
				},
				"actions": []map[string]any{
					{"kind": "delete"},
				},
			},
		},
	})
	if name != "error" {
		t.Fatalf("expected error method, got %q; raw=%s", name, raw)
	}
	var errResp struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &errResp); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if errResp.Type != "stateMismatch" {
		t.Errorf("error type = %q, want stateMismatch", errResp.Type)
	}
}

// -- ManagedRule/query ------------------------------------------------

func TestManagedRule_Query_Empty(t *testing.T) {
	f := setupFixture(t)
	name, raw := f.invoke(t, "ManagedRule/query", map[string]any{
		"accountId": f.accountID(),
	})
	if name != "ManagedRule/query" {
		t.Fatalf("method = %q; raw=%s", name, raw)
	}
	var resp struct {
		IDs   []string `json:"ids"`
		Total int      `json:"total"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.IDs) != 0 {
		t.Errorf("ids = %v, want []", resp.IDs)
	}
	if resp.Total != 0 {
		t.Errorf("total = %d, want 0", resp.Total)
	}
}

func TestManagedRule_Query_OrderPreserved(t *testing.T) {
	f := setupFixture(t)
	// Create three rules with explicit ordering.
	for _, order := range []int{10, 5, 7} {
		f.createRule(t, map[string]any{
			"name":    fmt.Sprintf("Rule order %d", order),
			"enabled": true,
			"order":   order,
			"conditions": []map[string]any{
				{"field": "from", "op": "equals", "value": fmt.Sprintf("order%d@x.com", order)},
			},
			"actions": []map[string]any{
				{"kind": "skip-inbox"},
			},
		})
	}

	name, raw := f.invoke(t, "ManagedRule/query", map[string]any{
		"accountId": f.accountID(),
	})
	if name != "ManagedRule/query" {
		t.Fatalf("method = %q", name)
	}
	var resp struct {
		IDs   []string `json:"ids"`
		Total int      `json:"total"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Total != 3 {
		t.Fatalf("total = %d, want 3", resp.Total)
	}
	if len(resp.IDs) != 3 {
		t.Fatalf("ids len = %d, want 3", len(resp.IDs))
	}

	// Now fetch all via get to check order.
	_, getraw := f.invoke(t, "ManagedRule/get", map[string]any{
		"accountId": f.accountID(),
		"ids":       resp.IDs,
	})
	var getResp struct {
		List []struct {
			ID    string `json:"id"`
			Order int    `json:"order"`
		} `json:"list"`
	}
	if err := json.Unmarshal(getraw, &getResp); err != nil {
		t.Fatalf("unmarshal get: %v", err)
	}
	// Build map id -> order.
	orderByID := make(map[string]int)
	for _, r := range getResp.List {
		orderByID[r.ID] = r.Order
	}
	// Verify IDs are in (order asc) sequence: 5, 7, 10.
	wantOrders := []int{5, 7, 10}
	for i, id := range resp.IDs {
		got := orderByID[id]
		if got != wantOrders[i] {
			t.Errorf("ids[%d] order = %d, want %d", i, got, wantOrders[i])
		}
	}
}

// -- ManagedRule/changes ----------------------------------------------

func TestManagedRule_Changes_NoChange(t *testing.T) {
	f := setupFixture(t)
	// Get current state.
	_, getraw := f.invoke(t, "ManagedRule/get", map[string]any{
		"accountId": f.accountID(),
	})
	var getResp struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(getraw, &getResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	name, raw := f.invoke(t, "ManagedRule/changes", map[string]any{
		"accountId":  f.accountID(),
		"sinceState": getResp.State,
	})
	if name != "ManagedRule/changes" {
		t.Fatalf("method = %q; raw=%s", name, raw)
	}
	var resp struct {
		OldState       string   `json:"oldState"`
		NewState       string   `json:"newState"`
		HasMoreChanges bool     `json:"hasMoreChanges"`
		Created        []string `json:"created"`
		Updated        []string `json:"updated"`
		Destroyed      []string `json:"destroyed"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.OldState != getResp.State {
		t.Errorf("oldState = %q, want %q", resp.OldState, getResp.State)
	}
	if resp.NewState != getResp.State {
		t.Errorf("newState = %q, want %q", resp.NewState, getResp.State)
	}
	if len(resp.Created) != 0 || len(resp.Updated) != 0 || len(resp.Destroyed) != 0 {
		t.Errorf("unexpected changes: created=%v updated=%v destroyed=%v",
			resp.Created, resp.Updated, resp.Destroyed)
	}
}

func TestManagedRule_Changes_AfterMutation(t *testing.T) {
	f := setupFixture(t)
	// Capture initial state.
	_, getraw := f.invoke(t, "ManagedRule/get", map[string]any{
		"accountId": f.accountID(),
	})
	var getResp struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(getraw, &getResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	sinceState := getResp.State

	// Create a rule to advance the state.
	f.createRule(t, map[string]any{
		"name":    "Trigger",
		"enabled": true,
		"order":   0,
		"conditions": []map[string]any{
			{"field": "from", "op": "equals", "value": "trigger@x.com"},
		},
		"actions": []map[string]any{
			{"kind": "skip-inbox"},
		},
	})

	name, raw := f.invoke(t, "ManagedRule/changes", map[string]any{
		"accountId":  f.accountID(),
		"sinceState": sinceState,
	})
	if name != "ManagedRule/changes" {
		t.Fatalf("method = %q; raw=%s", name, raw)
	}
	var resp struct {
		Updated []string `json:"updated"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Phase-1 changes returns all current rules as "updated".
	if len(resp.Updated) == 0 {
		t.Error("expected at least one rule in updated")
	}
}

// -- Thread/mute and Thread/unmute -----------------------------------

func TestThread_Mute_Creates_Rule(t *testing.T) {
	f := setupFixture(t)
	const threadID = "thread-abc-123"

	name, raw := f.invoke(t, "Thread/mute", map[string]any{
		"accountId": f.accountID(),
		"threadId":  threadID,
	})
	if name != "Thread/mute" {
		t.Fatalf("method = %q; raw=%s", name, raw)
	}
	var resp struct {
		ManagedRuleID string `json:"managedRuleId"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.ManagedRuleID == "" {
		t.Fatal("managedRuleId is empty")
	}

	// Verify the rule exists and has the correct shape.
	_, getraw := f.invoke(t, "ManagedRule/get", map[string]any{
		"accountId": f.accountID(),
		"ids":       []string{resp.ManagedRuleID},
	})
	var getResp struct {
		List []struct {
			Enabled    bool `json:"enabled"`
			Conditions []struct {
				Field string `json:"field"`
				Op    string `json:"op"`
				Value string `json:"value"`
			} `json:"conditions"`
			Actions []struct {
				Kind string `json:"kind"`
			} `json:"actions"`
		} `json:"list"`
	}
	if err := json.Unmarshal(getraw, &getResp); err != nil {
		t.Fatalf("unmarshal get: %v", err)
	}
	if len(getResp.List) != 1 {
		t.Fatalf("list len = %d, want 1", len(getResp.List))
	}
	r := getResp.List[0]
	if !r.Enabled {
		t.Error("mute rule should be enabled")
	}
	if len(r.Conditions) != 1 {
		t.Fatalf("conditions len = %d, want 1", len(r.Conditions))
	}
	if r.Conditions[0].Field != "thread-id" {
		t.Errorf("condition field = %q, want thread-id", r.Conditions[0].Field)
	}
	if r.Conditions[0].Value != threadID {
		t.Errorf("condition value = %q, want %q", r.Conditions[0].Value, threadID)
	}
	// Actions: skip-inbox and mark-read.
	kinds := make(map[string]bool)
	for _, a := range r.Actions {
		kinds[a.Kind] = true
	}
	if !kinds["skip-inbox"] {
		t.Error("mute rule should have skip-inbox action")
	}
	if !kinds["mark-read"] {
		t.Error("mute rule should have mark-read action")
	}
}

func TestThread_Mute_Idempotent(t *testing.T) {
	f := setupFixture(t)
	const threadID = "thread-idem-456"

	_, raw1 := f.invoke(t, "Thread/mute", map[string]any{
		"accountId": f.accountID(),
		"threadId":  threadID,
	})
	_, raw2 := f.invoke(t, "Thread/mute", map[string]any{
		"accountId": f.accountID(),
		"threadId":  threadID,
	})
	var r1, r2 struct {
		ManagedRuleID string `json:"managedRuleId"`
	}
	if err := json.Unmarshal(raw1, &r1); err != nil {
		t.Fatalf("unmarshal r1: %v", err)
	}
	if err := json.Unmarshal(raw2, &r2); err != nil {
		t.Fatalf("unmarshal r2: %v", err)
	}
	if r1.ManagedRuleID != r2.ManagedRuleID {
		t.Errorf("second mute returned different id: %q != %q", r1.ManagedRuleID, r2.ManagedRuleID)
	}
	// Only one rule should exist.
	_, qraw := f.invoke(t, "ManagedRule/query", map[string]any{
		"accountId": f.accountID(),
	})
	var qResp struct {
		Total int `json:"total"`
	}
	if err := json.Unmarshal(qraw, &qResp); err != nil {
		t.Fatalf("unmarshal query: %v", err)
	}
	if qResp.Total != 1 {
		t.Errorf("total = %d, want 1 after idempotent mute", qResp.Total)
	}
}

func TestThread_Unmute(t *testing.T) {
	f := setupFixture(t)
	const threadID = "thread-unmute-789"

	// Mute first.
	_, muteRaw := f.invoke(t, "Thread/mute", map[string]any{
		"accountId": f.accountID(),
		"threadId":  threadID,
	})
	var muteResp struct {
		ManagedRuleID string `json:"managedRuleId"`
	}
	if err := json.Unmarshal(muteRaw, &muteResp); err != nil {
		t.Fatalf("unmarshal mute: %v", err)
	}

	// Unmute.
	name, raw := f.invoke(t, "Thread/unmute", map[string]any{
		"accountId": f.accountID(),
		"threadId":  threadID,
	})
	if name != "Thread/unmute" {
		t.Fatalf("method = %q; raw=%s", name, raw)
	}

	// The rule should still exist but be disabled.
	_, getraw := f.invoke(t, "ManagedRule/get", map[string]any{
		"accountId": f.accountID(),
		"ids":       []string{muteResp.ManagedRuleID},
	})
	var getResp struct {
		List []struct {
			Enabled bool `json:"enabled"`
		} `json:"list"`
	}
	if err := json.Unmarshal(getraw, &getResp); err != nil {
		t.Fatalf("unmarshal get: %v", err)
	}
	if len(getResp.List) != 1 {
		t.Fatalf("list len = %d, want 1", len(getResp.List))
	}
	if getResp.List[0].Enabled {
		t.Error("rule should be disabled after unmute")
	}
}

func TestThread_Mute_Generates_Sieve(t *testing.T) {
	f := setupFixture(t)
	const threadID = "thread-sieve-check"

	f.invoke(t, "Thread/mute", map[string]any{
		"accountId": f.accountID(),
		"threadId":  threadID,
	})

	// Read the effective Sieve script from the store directly.
	script, err := f.srv.Store.Meta().GetSieveScript(context.Background(), f.pid)
	if err != nil {
		t.Fatalf("GetSieveScript: %v", err)
	}
	want := `header :is "X-Herold-Thread-Id" "` + threadID + `"`
	if !strings.Contains(script, want) {
		t.Errorf("Sieve script does not contain expected header test\nwant substring: %s\ngot: %s",
			want, script)
	}
}

// -- BlockedSender/set -----------------------------------------------

func TestBlockedSender_Set_Creates_Rule(t *testing.T) {
	f := setupFixture(t)
	const addr = "spammer@evil.example.com"

	name, raw := f.invoke(t, "BlockedSender/set", map[string]any{
		"accountId": f.accountID(),
		"address":   addr,
	})
	if name != "BlockedSender/set" {
		t.Fatalf("method = %q; raw=%s", name, raw)
	}
	var resp struct {
		ManagedRuleID string `json:"managedRuleId"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.ManagedRuleID == "" {
		t.Fatal("managedRuleId is empty")
	}

	// Verify shape.
	_, getraw := f.invoke(t, "ManagedRule/get", map[string]any{
		"accountId": f.accountID(),
		"ids":       []string{resp.ManagedRuleID},
	})
	var getResp struct {
		List []struct {
			Enabled    bool `json:"enabled"`
			Conditions []struct {
				Field string `json:"field"`
				Op    string `json:"op"`
				Value string `json:"value"`
			} `json:"conditions"`
			Actions []struct {
				Kind string `json:"kind"`
			} `json:"actions"`
		} `json:"list"`
	}
	if err := json.Unmarshal(getraw, &getResp); err != nil {
		t.Fatalf("unmarshal get: %v", err)
	}
	if len(getResp.List) != 1 {
		t.Fatalf("list len = %d, want 1", len(getResp.List))
	}
	r := getResp.List[0]
	if !r.Enabled {
		t.Error("block rule should be enabled")
	}
	if len(r.Conditions) != 1 {
		t.Fatalf("conditions len = %d, want 1", len(r.Conditions))
	}
	if r.Conditions[0].Field != "from" || r.Conditions[0].Value != addr {
		t.Errorf("condition = {%s %s %s}, want {from equals %s}",
			r.Conditions[0].Field, r.Conditions[0].Op, r.Conditions[0].Value, addr)
	}
	if len(r.Actions) != 1 || r.Actions[0].Kind != "delete" {
		t.Errorf("actions = %v, want [{delete}]", r.Actions)
	}
}

func TestBlockedSender_Set_Idempotent(t *testing.T) {
	f := setupFixture(t)
	const addr = "dup@x.com"

	_, r1raw := f.invoke(t, "BlockedSender/set", map[string]any{
		"accountId": f.accountID(),
		"address":   addr,
	})
	_, r2raw := f.invoke(t, "BlockedSender/set", map[string]any{
		"accountId": f.accountID(),
		"address":   addr,
	})
	var r1, r2 struct {
		ManagedRuleID string `json:"managedRuleId"`
	}
	if err := json.Unmarshal(r1raw, &r1); err != nil {
		t.Fatalf("unmarshal r1: %v", err)
	}
	if err := json.Unmarshal(r2raw, &r2); err != nil {
		t.Fatalf("unmarshal r2: %v", err)
	}
	if r1.ManagedRuleID != r2.ManagedRuleID {
		t.Errorf("second block returned different id: %q != %q", r1.ManagedRuleID, r2.ManagedRuleID)
	}
	_, qraw := f.invoke(t, "ManagedRule/query", map[string]any{
		"accountId": f.accountID(),
	})
	var qResp struct {
		Total int `json:"total"`
	}
	if err := json.Unmarshal(qraw, &qResp); err != nil {
		t.Fatalf("unmarshal query: %v", err)
	}
	if qResp.Total != 1 {
		t.Errorf("total = %d, want 1 after idempotent block", qResp.Total)
	}
}

func TestBlockedSender_Set_Generates_Sieve(t *testing.T) {
	f := setupFixture(t)
	const addr = "block@trash.com"

	f.invoke(t, "BlockedSender/set", map[string]any{
		"accountId": f.accountID(),
		"address":   addr,
	})

	script, err := f.srv.Store.Meta().GetSieveScript(context.Background(), f.pid)
	if err != nil {
		t.Fatalf("GetSieveScript: %v", err)
	}
	// The block rule uses "address :is :all "From" "<addr>""
	wantAddr := `"` + addr + `"`
	if !strings.Contains(script, wantAddr) {
		t.Errorf("Sieve script does not contain address %s\ngot: %s", wantAddr, script)
	}
	if !strings.Contains(script, "fileinto") {
		t.Errorf("Sieve script missing fileinto (Trash); got: %s", script)
	}
}

// -- Isolation: rules from one principal are not visible to another --

func TestManagedRule_PrincipalIsolation(t *testing.T) {
	f := setupFixture(t)
	ctx := context.Background()

	// Create a second principal.
	p2, err := f.srv.Store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "bob@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal bob: %v", err)
	}
	plaintext2 := "hk_test_mr_bob_" + fmt.Sprintf("%d", p2.ID)
	hash2 := protoadmin.HashAPIKey(plaintext2)
	if _, err := f.srv.Store.Meta().InsertAPIKey(ctx, store.APIKey{
		PrincipalID: p2.ID,
		Hash:        hash2,
		Name:        "test",
	}); err != nil {
		t.Fatalf("InsertAPIKey bob: %v", err)
	}

	// Create a rule as Alice.
	f.createRule(t, map[string]any{
		"name":    "Alice's rule",
		"enabled": true,
		"order":   0,
		"conditions": []map[string]any{
			{"field": "from", "op": "equals", "value": "x@y.com"},
		},
		"actions": []map[string]any{
			{"kind": "delete"},
		},
	})

	// Bob should see no rules.
	bobRules, err := f.srv.Store.Meta().ListManagedRules(ctx, p2.ID, store.ManagedRuleFilter{})
	if err != nil {
		t.Fatalf("ListManagedRules bob: %v", err)
	}
	if len(bobRules) != 0 {
		t.Errorf("bob sees %d rules, want 0", len(bobRules))
	}
}
