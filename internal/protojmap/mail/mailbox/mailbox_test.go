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
	_, _ = f.invoke(t, "Mailbox/set", map[string]any{
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

// TestMailbox_Get_PropertiesFilter asserts that the properties field
// in Mailbox/get limits the returned properties (RFC 8620 §5.1).
// "id" must always be returned; fields absent from the list must not
// appear.
func TestMailbox_Get_PropertiesFilter(t *testing.T) {
	f := setupFixture(t)
	mustInsertMailbox(t, f, "INBOX", store.MailboxAttrInbox)

	_, raw := f.invoke(t, "Mailbox/get", map[string]any{
		"accountId":  protojmap.AccountIDForPrincipal(f.pid),
		"properties": []string{"id", "name"},
	})
	var resp struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(resp.List) != 1 {
		t.Fatalf("got %d mailboxes, want 1", len(resp.List))
	}
	m := resp.List[0]
	if _, ok := m["id"]; !ok {
		t.Errorf("id must always be returned")
	}
	if _, ok := m["name"]; !ok {
		t.Errorf("name should be returned when requested")
	}
	if _, ok := m["totalEmails"]; ok {
		t.Errorf("totalEmails must not be returned when not requested")
	}
	if _, ok := m["myRights"]; ok {
		t.Errorf("myRights must not be returned when not requested")
	}
}

// TestMailbox_Query_FilterParentIdNull asserts that { "parentId": null }
// in a Mailbox/query filter returns only top-level mailboxes (RFC 8621
// §2.3).
func TestMailbox_Query_FilterParentIdNull(t *testing.T) {
	f := setupFixture(t)
	// Insert a top-level mailbox and a child.
	parent := mustInsertMailbox(t, f, "Parent", 0)
	_, err := f.srv.Store.Meta().InsertMailbox(context.Background(), store.Mailbox{
		PrincipalID: f.pid,
		ParentID:    parent.ID,
		Name:        "Child",
	})
	if err != nil {
		t.Fatalf("InsertMailbox child: %v", err)
	}

	_, raw := f.invoke(t, "Mailbox/query", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"filter":    map[string]any{"parentId": nil},
	})
	var resp struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	parentIDStr := fmt.Sprintf("%d", parent.ID)
	if len(resp.IDs) != 1 || resp.IDs[0] != parentIDStr {
		t.Errorf("expected only parent %s; got %v", parentIDStr, resp.IDs)
	}
}

// TestMailbox_Set_DuplicateNameAlreadyExists asserts that creating two
// mailboxes with the same name under the same parent returns alreadyExists
// (RFC 8621 §2.5).
func TestMailbox_Set_DuplicateNameAlreadyExists(t *testing.T) {
	f := setupFixture(t)
	// First creation must succeed.
	_, raw := f.invoke(t, "Mailbox/set", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"create":    map[string]any{"a": map[string]any{"name": "Duplicate"}},
	})
	var r1 struct {
		Created map[string]any `json:"created"`
	}
	_ = json.Unmarshal(raw, &r1)
	if len(r1.Created) == 0 {
		t.Fatalf("first create failed: %s", raw)
	}

	// Second creation with the same name must fail with alreadyExists.
	_, raw = f.invoke(t, "Mailbox/set", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"create":    map[string]any{"b": map[string]any{"name": "Duplicate"}},
	})
	var r2 struct {
		NotCreated map[string]map[string]any `json:"notCreated"`
	}
	if err := json.Unmarshal(raw, &r2); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if r2.NotCreated["b"]["type"] != "alreadyExists" {
		t.Errorf("expected alreadyExists, got %v", r2.NotCreated["b"]["type"])
	}
}

// TestMailbox_Set_ChangeSortOrder asserts that sortOrder is persisted
// and read back via Mailbox/get (RFC 8621 §2.1).
func TestMailbox_Set_ChangeSortOrder(t *testing.T) {
	f := setupFixture(t)
	mb := mustInsertMailbox(t, f, "Sorted", 0)
	id := fmt.Sprintf("%d", mb.ID)

	_, raw := f.invoke(t, "Mailbox/set", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"update":    map[string]any{id: map[string]any{"sortOrder": 99}},
	})
	var uresp struct {
		Updated    map[string]any `json:"updated"`
		NotUpdated map[string]any `json:"notUpdated"`
	}
	_ = json.Unmarshal(raw, &uresp)
	if len(uresp.Updated) != 1 {
		t.Fatalf("update failed: %s", raw)
	}

	_, getraw := f.invoke(t, "Mailbox/get", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"ids":       []string{id},
	})
	var gresp struct {
		List []map[string]any `json:"list"`
	}
	_ = json.Unmarshal(getraw, &gresp)
	if len(gresp.List) == 0 {
		t.Fatalf("mailbox not found after sortOrder update")
	}
	so := gresp.List[0]["sortOrder"]
	// JSON numbers unmarshal to float64.
	if so != float64(99) {
		t.Errorf("sortOrder = %v, want 99", so)
	}
}

// TestMailbox_Set_CannotDestroyWithChildren asserts that destroying a
// mailbox that has child mailboxes returns a mailboxHasChild SetError
// (RFC 8621 §2.5).
func TestMailbox_Set_CannotDestroyWithChildren(t *testing.T) {
	f := setupFixture(t)
	parent := mustInsertMailbox(t, f, "Parent", 0)
	_, err := f.srv.Store.Meta().InsertMailbox(context.Background(), store.Mailbox{
		PrincipalID: f.pid,
		ParentID:    parent.ID,
		Name:        "Child",
	})
	if err != nil {
		t.Fatalf("InsertMailbox child: %v", err)
	}

	_, raw := f.invoke(t, "Mailbox/set", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"destroy":   []string{fmt.Sprintf("%d", parent.ID)},
	})
	var resp struct {
		Destroyed    []string                  `json:"destroyed"`
		NotDestroyed map[string]map[string]any `json:"notDestroyed"`
	}
	_ = json.Unmarshal(raw, &resp)
	if len(resp.Destroyed) != 0 {
		t.Errorf("parent should not be destroyed when it has children")
	}
	parentIDStr := fmt.Sprintf("%d", parent.ID)
	if resp.NotDestroyed[parentIDStr]["type"] != "mailboxHasChild" {
		t.Errorf("expected mailboxHasChild, got %v", resp.NotDestroyed[parentIDStr]["type"])
	}
}

// TestMailbox_Changes_AfterRename asserts that renaming a mailbox via
// Mailbox/set causes it to appear in the updated list of a subsequent
// Mailbox/changes call. The mailbox is created via JMAP (not directly
// via the store) so that IncrementJMAPState is called and the
// change-feed cursor aligns with the JMAP state counter.
func TestMailbox_Changes_AfterRename(t *testing.T) {
	f := setupFixture(t)

	// Create via JMAP so IncrementJMAPState runs.
	_, createRaw := f.invoke(t, "Mailbox/set", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"create":    map[string]any{"mb": map[string]any{"name": "BeforeRename"}},
	})
	var createResp struct {
		Created  map[string]map[string]any `json:"created"`
		NewState string                    `json:"newState"`
	}
	_ = json.Unmarshal(createRaw, &createResp)
	mbID := createResp.Created["mb"]["id"].(string)

	// startState is after the creation.
	startState := createResp.NewState

	// Rename via Mailbox/set.
	f.invoke(t, "Mailbox/set", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"update":    map[string]any{mbID: map[string]any{"name": "AfterRename"}},
	})

	// Mailbox/changes from startState must include the renamed mailbox.
	_, raw := f.invoke(t, "Mailbox/changes", map[string]any{
		"accountId":  protojmap.AccountIDForPrincipal(f.pid),
		"sinceState": startState,
	})
	var chResp struct {
		Updated []string `json:"updated"`
	}
	_ = json.Unmarshal(raw, &chResp)
	found := false
	for _, id := range chResp.Updated {
		if id == mbID {
			found = true
		}
	}
	if !found {
		t.Errorf("renamed mailbox %s not in updated list: %v (raw=%s)", mbID, chResp.Updated, raw)
	}
}

// TestMailbox_QueryChanges_NoChanges asserts the queryChanges response
// structure when no mailboxes have changed since sinceQueryState.
func TestMailbox_QueryChanges_NoChanges(t *testing.T) {
	f := setupFixture(t)
	mustInsertMailbox(t, f, "INBOX", store.MailboxAttrInbox)

	_, queryRaw := f.invoke(t, "Mailbox/query", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
	})
	var qresp struct{ QueryState string `json:"queryState"` }
	_ = json.Unmarshal(queryRaw, &qresp)

	_, raw := f.invoke(t, "Mailbox/queryChanges", map[string]any{
		"accountId":       protojmap.AccountIDForPrincipal(f.pid),
		"sinceQueryState": qresp.QueryState,
	})
	var resp struct {
		OldQueryState string   `json:"oldQueryState"`
		NewQueryState string   `json:"newQueryState"`
		Removed       []string `json:"removed"`
		Added         []any    `json:"added"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if resp.OldQueryState != qresp.QueryState {
		t.Errorf("oldQueryState = %q, want %q", resp.OldQueryState, qresp.QueryState)
	}
	if len(resp.Removed) != 0 {
		t.Errorf("removed = %v, want empty", resp.Removed)
	}
	if len(resp.Added) != 0 {
		t.Errorf("added = %v, want empty", resp.Added)
	}
}

// TestMailbox_QueryChanges_AfterCreate asserts that a newly created
// mailbox appears in the added list of a queryChanges response.
func TestMailbox_QueryChanges_AfterCreate(t *testing.T) {
	f := setupFixture(t)

	_, queryRaw := f.invoke(t, "Mailbox/query", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
	})
	var qresp struct{ QueryState string `json:"queryState"` }
	_ = json.Unmarshal(queryRaw, &qresp)
	startState := qresp.QueryState

	// Create a mailbox.
	_, setRaw := f.invoke(t, "Mailbox/set", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"create":    map[string]any{"n": map[string]any{"name": "NewBox"}},
	})
	var setResp struct {
		Created map[string]map[string]any `json:"created"`
	}
	_ = json.Unmarshal(setRaw, &setResp)
	newID := setResp.Created["n"]["id"].(string)

	_, raw := f.invoke(t, "Mailbox/queryChanges", map[string]any{
		"accountId":       protojmap.AccountIDForPrincipal(f.pid),
		"sinceQueryState": startState,
	})
	var resp struct {
		Added []map[string]any `json:"added"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	found := false
	for _, item := range resp.Added {
		if item["id"] == newID {
			found = true
		}
	}
	if !found {
		t.Errorf("new mailbox %s not in added list: %v (raw=%s)", newID, resp.Added, raw)
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

// TestMailbox_Set_DestroyParentAndChildTogether asserts that RFC 8621 §2.5
// ordering is respected: when both a parent and its child are in the same
// destroy list, the server must destroy the child first so the parent
// destroy succeeds. The client sends the parent ID first in the list
// (worst-case ordering) to verify the server reorders correctly.
func TestMailbox_Set_DestroyParentAndChildTogether(t *testing.T) {
	f := setupFixture(t)

	// Create parent via JMAP so IncrementJMAPState fires and currentState
	// is consistent.
	accountID := protojmap.AccountIDForPrincipal(f.pid)
	_, rawCreate := f.invoke(t, "Mailbox/set", map[string]any{
		"accountId": accountID,
		"create": map[string]any{
			"parent": map[string]any{"name": "Parent"},
		},
	})
	var createResp struct {
		Created map[string]map[string]any `json:"created"`
	}
	if err := json.Unmarshal(rawCreate, &createResp); err != nil {
		t.Fatalf("unmarshal create: %v: %s", err, rawCreate)
	}
	parentRaw, ok := createResp.Created["parent"]
	if !ok {
		t.Fatalf("parent not created: %s", rawCreate)
	}
	parentID, _ := parentRaw["id"].(string)

	_, rawCreate2 := f.invoke(t, "Mailbox/set", map[string]any{
		"accountId": accountID,
		"create": map[string]any{
			"child": map[string]any{"name": "Child", "parentId": parentID},
		},
	})
	var createResp2 struct {
		Created map[string]map[string]any `json:"created"`
	}
	if err := json.Unmarshal(rawCreate2, &createResp2); err != nil {
		t.Fatalf("unmarshal create2: %v: %s", err, rawCreate2)
	}
	childRaw, ok := createResp2.Created["child"]
	if !ok {
		t.Fatalf("child not created: %s", rawCreate2)
	}
	childID, _ := childRaw["id"].(string)

	// Destroy parent first in the list (worst-case ordering for the server).
	_, rawDestroy := f.invoke(t, "Mailbox/set", map[string]any{
		"accountId":              accountID,
		"destroy":                []string{parentID, childID},
		"onDestroyRemoveEmails": false,
	})
	var destroyResp struct {
		Destroyed    []string          `json:"destroyed"`
		NotDestroyed map[string]any    `json:"notDestroyed"`
	}
	if err := json.Unmarshal(rawDestroy, &destroyResp); err != nil {
		t.Fatalf("unmarshal destroy: %v: %s", err, rawDestroy)
	}
	if len(destroyResp.NotDestroyed) > 0 {
		t.Fatalf("notDestroyed should be empty, got: %v (raw=%s)", destroyResp.NotDestroyed, rawDestroy)
	}
	if len(destroyResp.Destroyed) != 2 {
		t.Fatalf("expected 2 destroyed, got %d: %v", len(destroyResp.Destroyed), destroyResp.Destroyed)
	}
}
