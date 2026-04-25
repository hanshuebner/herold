package mailbox_test

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
	"github.com/hanshuebner/herold/internal/protojmap/mail/mailbox"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
)

// fixture wires a harness with a JMAP listener attached to a Server
// preloaded with the Mailbox/* handlers. Returns the harness, the
// principal id, an authenticated HTTP client, the JMAP base URL, and a
// helper to run a single method call.
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
	mailbox.Register(jmapServ.Registry(), srv.Store, srv.Logger, srv.Clock)

	if err := srv.AttachJMAP("jmap", jmapServ, protojmap.ListenerModePlain); err != nil {
		t.Fatalf("AttachJMAP: %v", err)
	}
	client, base := srv.DialJMAPByName(ctx, "jmap")
	return &fixture{
		srv: srv, pid: p.ID, client: client, baseURL: base,
		apiKey: plaintext, jmapServ: jmapServ,
	}
}

// invoke posts a single method call and returns the parsed response
// invocation triple. It fails the test on transport errors or when the
// response does not carry exactly one method response.
func (f *fixture) invoke(t *testing.T, method string, args any, using ...protojmap.CapabilityID) (string, json.RawMessage) {
	t.Helper()
	if len(using) == 0 {
		using = []protojmap.CapabilityID{protojmap.CapabilityCore, protojmap.CapabilityMail}
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

func mustInsertMailbox(t *testing.T, f *fixture, name string, attrs store.MailboxAttributes) store.Mailbox {
	t.Helper()
	mb, err := f.srv.Store.Meta().InsertMailbox(context.Background(), store.Mailbox{
		PrincipalID: f.pid,
		Name:        name,
		Attributes:  attrs,
	})
	if err != nil {
		t.Fatalf("InsertMailbox %s: %v", name, err)
	}
	return mb
}

// -- tests --------------------------------------------------------

func TestMailbox_Get_All(t *testing.T) {
	f := setupFixture(t)
	mustInsertMailbox(t, f, "INBOX", store.MailboxAttrInbox)
	mustInsertMailbox(t, f, "Drafts", store.MailboxAttrDrafts)
	mustInsertMailbox(t, f, "Archive/2026", 0)

	name, raw := f.invoke(t, "Mailbox/get", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
	})
	if name != "Mailbox/get" {
		t.Fatalf("response name = %q, want Mailbox/get; body=%s", name, raw)
	}
	var resp struct {
		List     []map[string]any `json:"list"`
		NotFound []string         `json:"notFound"`
		State    string           `json:"state"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.List) != 3 {
		t.Fatalf("got %d mailboxes, want 3: %+v", len(resp.List), resp.List)
	}
	names := map[string]bool{}
	for _, m := range resp.List {
		names[m["name"].(string)] = true
	}
	for _, want := range []string{"INBOX", "Drafts", "Archive/2026"} {
		if !names[want] {
			t.Errorf("missing mailbox %q in response", want)
		}
	}
}

func TestMailbox_Get_ByIds(t *testing.T) {
	f := setupFixture(t)
	inbox := mustInsertMailbox(t, f, "INBOX", store.MailboxAttrInbox)
	mustInsertMailbox(t, f, "Drafts", store.MailboxAttrDrafts)

	wantID := fmt.Sprintf("%d", inbox.ID)
	_, raw := f.invoke(t, "Mailbox/get", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"ids":       []string{wantID, "9999"},
	})
	var resp struct {
		List     []map[string]any `json:"list"`
		NotFound []string         `json:"notFound"`
	}
	_ = json.Unmarshal(raw, &resp)
	if len(resp.List) != 1 {
		t.Fatalf("got %d mailboxes, want 1: %s", len(resp.List), raw)
	}
	if resp.List[0]["id"].(string) != wantID {
		t.Fatalf("id = %v, want %s", resp.List[0]["id"], wantID)
	}
	if len(resp.NotFound) != 1 || resp.NotFound[0] != "9999" {
		t.Fatalf("notFound = %v, want [9999]", resp.NotFound)
	}
}

func TestMailbox_Set_Create_NewMailbox(t *testing.T) {
	f := setupFixture(t)
	_, raw := f.invoke(t, "Mailbox/set", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"create": map[string]any{
			"new1": map[string]any{
				"name":         "Projects",
				"isSubscribed": true,
			},
		},
	})
	var resp struct {
		Created    map[string]map[string]any `json:"created"`
		NotCreated map[string]any            `json:"notCreated"`
		NewState   string                    `json:"newState"`
		OldState   string                    `json:"oldState"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(resp.Created) != 1 {
		t.Fatalf("created = %+v, NotCreated=%+v", resp.Created, resp.NotCreated)
	}
	if resp.NewState == resp.OldState {
		t.Fatalf("state did not advance: old=%q new=%q", resp.OldState, resp.NewState)
	}
	mailboxes, _ := f.srv.Store.Meta().ListMailboxes(context.Background(), f.pid)
	found := false
	for _, mb := range mailboxes {
		if mb.Name == "Projects" {
			found = true
			if mb.Attributes&store.MailboxAttrSubscribed == 0 {
				t.Errorf("isSubscribed not honored")
			}
		}
	}
	if !found {
		t.Errorf("Projects mailbox not in store: %v", mailboxes)
	}
}

func TestMailbox_Set_Update_Rename(t *testing.T) {
	f := setupFixture(t)
	mb := mustInsertMailbox(t, f, "OldName", 0)
	_, raw := f.invoke(t, "Mailbox/set", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"update": map[string]any{
			fmt.Sprintf("%d", mb.ID): map[string]any{
				"name": "NewName",
			},
		},
	})
	var resp struct {
		Updated    map[string]any `json:"updated"`
		NotUpdated map[string]any `json:"notUpdated"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(resp.Updated) != 1 {
		t.Fatalf("updated=%v notUpdated=%v", resp.Updated, resp.NotUpdated)
	}
	got, _ := f.srv.Store.Meta().GetMailboxByID(context.Background(), mb.ID)
	if got.Name != "NewName" {
		t.Fatalf("rename not applied: name = %q", got.Name)
	}
}

func TestMailbox_Set_Destroy(t *testing.T) {
	f := setupFixture(t)
	mb := mustInsertMailbox(t, f, "Disposable", 0)
	_, raw := f.invoke(t, "Mailbox/set", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"destroy":   []string{fmt.Sprintf("%d", mb.ID)},
	})
	var resp struct {
		Destroyed    []string       `json:"destroyed"`
		NotDestroyed map[string]any `json:"notDestroyed"`
	}
	_ = json.Unmarshal(raw, &resp)
	if len(resp.Destroyed) != 1 {
		t.Fatalf("destroyed=%v notDestroyed=%v", resp.Destroyed, resp.NotDestroyed)
	}
	if _, err := f.srv.Store.Meta().GetMailboxByID(context.Background(), mb.ID); err == nil {
		t.Fatalf("mailbox still exists after destroy")
	}
}

func TestMailbox_Changes_FromState(t *testing.T) {
	f := setupFixture(t)
	// Read the initial state
	_, getRaw := f.invoke(t, "Mailbox/get", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"ids":       []string{},
	})
	var getResp struct {
		State string `json:"state"`
	}
	_ = json.Unmarshal(getRaw, &getResp)
	startState := getResp.State

	// Mutate via JMAP so the per-principal Email/Mailbox state counter
	// advances in lockstep with the change feed.
	_, setRaw := f.invoke(t, "Mailbox/set", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"create": map[string]any{
			"new1": map[string]any{"name": "AfterStart"},
		},
	})
	var setResp struct {
		Created map[string]map[string]any `json:"created"`
	}
	_ = json.Unmarshal(setRaw, &setResp)
	if len(setResp.Created) != 1 {
		t.Fatalf("create did not produce a mailbox: %s", setRaw)
	}
	createdID := setResp.Created["new1"]["id"].(string)

	// Now /changes from startState should report the new mailbox.
	_, raw := f.invoke(t, "Mailbox/changes", map[string]any{
		"accountId":  protojmap.AccountIDForPrincipal(f.pid),
		"sinceState": startState,
	})
	var resp struct {
		Created   []string `json:"created"`
		Updated   []string `json:"updated"`
		Destroyed []string `json:"destroyed"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	found := false
	for _, c := range resp.Created {
		if c == createdID {
			found = true
		}
	}
	if !found {
		t.Fatalf("created %v does not contain %q (raw=%s)", resp.Created, createdID, raw)
	}
}

func TestMailbox_Query_Filter_Role(t *testing.T) {
	f := setupFixture(t)
	inbox := mustInsertMailbox(t, f, "INBOX", store.MailboxAttrInbox)
	mustInsertMailbox(t, f, "Drafts", store.MailboxAttrDrafts)
	mustInsertMailbox(t, f, "Archive", store.MailboxAttrArchive)

	_, raw := f.invoke(t, "Mailbox/query", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"filter":    map[string]any{"role": "inbox"},
	})
	var resp struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(resp.IDs) != 1 {
		t.Fatalf("got %d ids, want 1: %v", len(resp.IDs), resp.IDs)
	}
	if resp.IDs[0] != fmt.Sprintf("%d", inbox.ID) {
		t.Fatalf("id %s != inbox %d", resp.IDs[0], inbox.ID)
	}
}

// -- REQ-PROTO-56 / REQ-STORE-34 Mailbox.color extension ----------

// TestMailbox_Get_IncludesColor exercises Mailbox/get reflecting the
// color extension field for both unset (null) and set (#RRGGBB) cases.
func TestMailbox_Get_IncludesColor(t *testing.T) {
	f := setupFixture(t)
	plain := mustInsertMailbox(t, f, "Plain", 0)
	colour := "#5B8DEE"
	colored, err := f.srv.Store.Meta().InsertMailbox(context.Background(), store.Mailbox{
		PrincipalID: f.pid, Name: "Coloured", Color: &colour,
	})
	if err != nil {
		t.Fatalf("InsertMailbox(coloured): %v", err)
	}
	_, raw := f.invoke(t, "Mailbox/get", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
	})
	var resp struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	byName := map[string]map[string]any{}
	for _, m := range resp.List {
		byName[m["name"].(string)] = m
	}
	if got := byName["Plain"]; got["color"] != nil {
		t.Fatalf("Plain color = %v, want nil (id=%d)", got["color"], plain.ID)
	}
	if got := byName["Coloured"]; got["color"] != colour {
		t.Fatalf("Coloured color = %v, want %q (id=%d)", got["color"], colour, colored.ID)
	}
}

// TestMailbox_Set_AcceptsColor covers the create + update happy paths
// for the color extension. Both new mailboxes and existing rows accept
// well-formed hex literals.
func TestMailbox_Set_AcceptsColor(t *testing.T) {
	f := setupFixture(t)
	// Create with color.
	_, raw := f.invoke(t, "Mailbox/set", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"create": map[string]any{
			"new": map[string]any{
				"name":  "Custom",
				"color": "#abcdef",
			},
		},
	})
	var cresp struct {
		Created    map[string]map[string]any `json:"created"`
		NotCreated map[string]any            `json:"notCreated"`
	}
	if err := json.Unmarshal(raw, &cresp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(cresp.Created) != 1 || len(cresp.NotCreated) != 0 {
		t.Fatalf("create with color failed: %+v / %+v", cresp.Created, cresp.NotCreated)
	}
	if got := cresp.Created["new"]["color"]; got != "#abcdef" {
		t.Fatalf("created color = %v, want #abcdef", got)
	}
	// Update an existing mailbox's color.
	mb := mustInsertMailbox(t, f, "Existing", 0)
	id := fmt.Sprintf("%d", mb.ID)
	_, raw = f.invoke(t, "Mailbox/set", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"update": map[string]any{
			id: map[string]any{"color": "#123456"},
		},
	})
	var uresp struct {
		Updated    map[string]any `json:"updated"`
		NotUpdated map[string]any `json:"notUpdated"`
	}
	if err := json.Unmarshal(raw, &uresp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(uresp.Updated) != 1 || len(uresp.NotUpdated) != 0 {
		t.Fatalf("update color failed: %+v / %+v", uresp.Updated, uresp.NotUpdated)
	}
	got, _ := f.srv.Store.Meta().GetMailboxByID(context.Background(), mb.ID)
	if got.Color == nil || *got.Color != "#123456" {
		t.Fatalf("Color after update = %v, want #123456", got.Color)
	}
	// Clear via explicit null.
	_, raw = f.invoke(t, "Mailbox/set", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"update": map[string]any{
			id: map[string]any{"color": nil},
		},
	})
	got, _ = f.srv.Store.Meta().GetMailboxByID(context.Background(), mb.ID)
	if got.Color != nil {
		t.Fatalf("Color after null update = %v, want nil", *got.Color)
	}
}

// TestMailbox_Set_RejectsInvalidColorFormat asserts the JMAP layer
// rejects malformed hex literals on both create and update with a
// SetError pointing at the "color" property.
func TestMailbox_Set_RejectsInvalidColorFormat(t *testing.T) {
	f := setupFixture(t)
	_, raw := f.invoke(t, "Mailbox/set", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"create": map[string]any{
			"bad": map[string]any{
				"name":  "Folder",
				"color": "red",
			},
		},
	})
	var resp struct {
		Created    map[string]any            `json:"created"`
		NotCreated map[string]map[string]any `json:"notCreated"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(resp.Created) != 0 {
		t.Fatalf("bad color unexpectedly created: %v", resp.Created)
	}
	if resp.NotCreated["bad"]["type"] != "invalidProperties" {
		t.Fatalf("notCreated[bad].type = %v, want invalidProperties: %v", resp.NotCreated["bad"]["type"], resp.NotCreated)
	}
	props, _ := resp.NotCreated["bad"]["properties"].([]any)
	if len(props) == 0 || props[0] != "color" {
		t.Fatalf("notCreated[bad].properties = %v, want [\"color\"]", props)
	}
}
