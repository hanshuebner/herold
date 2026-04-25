package contacts_test

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
	"github.com/hanshuebner/herold/internal/protojmap/contacts"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
)

// fixture wires a harness with a JMAP listener attached to a Server
// preloaded with the AddressBook/* and Contact/* handlers. Mirrors the
// pattern in internal/protojmap/mail/mailbox/mailbox_test.go.
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
	contacts.Register(jmapServ.Registry(), srv.Store, srv.Logger, srv.Clock)

	if err := srv.AttachJMAP("jmap", jmapServ, protojmap.ListenerModePlain); err != nil {
		t.Fatalf("AttachJMAP: %v", err)
	}
	client, base := srv.DialJMAPByName(ctx, "jmap")
	return &fixture{
		srv: srv, pid: p.ID, client: client, baseURL: base,
		apiKey: plaintext, jmapServ: jmapServ,
	}
}

// invoke posts a single method call and returns the parsed response.
func (f *fixture) invoke(t *testing.T, method string, args any, using ...protojmap.CapabilityID) (string, json.RawMessage) {
	t.Helper()
	if len(using) == 0 {
		using = []protojmap.CapabilityID{protojmap.CapabilityCore, protojmap.CapabilityJMAPContacts}
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

// -- AddressBook tests ------------------------------------------------

func TestAddressBook_Get_Set_RoundTrip(t *testing.T) {
	f := setupFixture(t)
	_, raw := f.invoke(t, "AddressBook/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create": map[string]any{
			"book1": map[string]any{
				"name":        "Personal",
				"description": "My personal contacts",
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
	created := setResp.Created["book1"]
	if created["name"] != "Personal" {
		t.Errorf("created.name = %v, want Personal", created["name"])
	}
	id, ok := created["id"].(string)
	if !ok || id == "" {
		t.Fatalf("created has no id: %+v", created)
	}

	_, raw = f.invoke(t, "AddressBook/get", map[string]any{
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
	if getResp.List[0]["name"] != "Personal" {
		t.Errorf("get.name = %v, want Personal", getResp.List[0]["name"])
	}
}

func TestAddressBook_Set_Create_AutoDefaultWhenNoneExists(t *testing.T) {
	f := setupFixture(t)
	_, raw := f.invoke(t, "AddressBook/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create": map[string]any{
			"book1": map[string]any{"name": "First"},
		},
	})
	var resp struct {
		Created map[string]map[string]any `json:"created"`
	}
	_ = json.Unmarshal(raw, &resp)
	if v, ok := resp.Created["book1"]["isDefault"].(bool); !ok || !v {
		t.Fatalf("first book isDefault = %v, want true: %+v", resp.Created["book1"]["isDefault"], resp.Created["book1"])
	}

	// Second create should NOT be default by default.
	_, raw2 := f.invoke(t, "AddressBook/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create": map[string]any{
			"book2": map[string]any{"name": "Second"},
		},
	})
	var resp2 struct {
		Created map[string]map[string]any `json:"created"`
	}
	_ = json.Unmarshal(raw2, &resp2)
	if v, ok := resp2.Created["book2"]["isDefault"].(bool); !ok || v {
		t.Fatalf("second book isDefault = %v, want false: %+v", resp2.Created["book2"]["isDefault"], resp2.Created["book2"])
	}
}

func TestAddressBook_Set_Destroy_CascadesContacts(t *testing.T) {
	f := setupFixture(t)
	// Create a book.
	_, raw := f.invoke(t, "AddressBook/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create":    map[string]any{"b": map[string]any{"name": "Work"}},
	})
	var setBookResp struct {
		Created map[string]map[string]any `json:"created"`
	}
	_ = json.Unmarshal(raw, &setBookResp)
	bookID := setBookResp.Created["b"]["id"].(string)

	// Add a contact in the book.
	_, raw = f.invoke(t, "Contact/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create": map[string]any{
			"c1": map[string]any{
				"version":       "1.0",
				"addressBookId": bookID,
				"name":          map[string]any{"full": "Bob"},
			},
		},
	})
	var setCResp struct {
		Created    map[string]map[string]any `json:"created"`
		NotCreated map[string]any            `json:"notCreated"`
	}
	if err := json.Unmarshal(raw, &setCResp); err != nil {
		t.Fatalf("unmarshal contact set: %v", err)
	}
	if len(setCResp.Created) != 1 {
		t.Fatalf("contact create failed: %+v", setCResp.NotCreated)
	}
	contactID := setCResp.Created["c1"]["id"].(string)

	// Destroy the book.
	_, raw = f.invoke(t, "AddressBook/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"destroy":   []string{bookID},
	})
	var destroyResp struct {
		Destroyed []string `json:"destroyed"`
	}
	_ = json.Unmarshal(raw, &destroyResp)
	if len(destroyResp.Destroyed) != 1 {
		t.Fatalf("destroyed = %+v", destroyResp.Destroyed)
	}

	// Both should now be gone.
	_, raw = f.invoke(t, "AddressBook/get", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"ids":       []string{bookID},
	})
	var bookGet struct {
		List     []map[string]any `json:"list"`
		NotFound []string         `json:"notFound"`
	}
	_ = json.Unmarshal(raw, &bookGet)
	if len(bookGet.List) != 0 {
		t.Errorf("book still present after destroy: %+v", bookGet.List)
	}
	if len(bookGet.NotFound) != 1 {
		t.Errorf("book notFound = %+v", bookGet.NotFound)
	}

	_, raw = f.invoke(t, "Contact/get", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"ids":       []string{contactID},
	})
	var contactGet struct {
		List     []map[string]any `json:"list"`
		NotFound []string         `json:"notFound"`
	}
	_ = json.Unmarshal(raw, &contactGet)
	if len(contactGet.List) != 0 {
		t.Errorf("contact still present after book destroy: %+v", contactGet.List)
	}
	if len(contactGet.NotFound) != 1 {
		t.Errorf("contact notFound = %+v", contactGet.NotFound)
	}
}

func TestAddressBook_Changes_FromState(t *testing.T) {
	f := setupFixture(t)
	// Initial state should be "0".
	_, raw := f.invoke(t, "AddressBook/changes", map[string]any{
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
	if len(ch.Created)+len(ch.Updated)+len(ch.Destroyed) != 0 {
		t.Errorf("initial changes non-empty: %+v", ch)
	}

	// Create a book, then ask for changes from "0".
	_, _ = f.invoke(t, "AddressBook/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create":    map[string]any{"b": map[string]any{"name": "X"}},
	})
	_, raw = f.invoke(t, "AddressBook/changes", map[string]any{
		"accountId":  string(protojmap.AccountIDForPrincipal(f.pid)),
		"sinceState": "0",
	})
	if err := json.Unmarshal(raw, &ch); err != nil {
		t.Fatalf("unmarshal changes2: %v", err)
	}
	if len(ch.Created) != 1 {
		t.Errorf("created after one set = %+v (raw=%s)", ch.Created, raw)
	}
	if ch.NewState == "0" {
		t.Errorf("newState should be > 0 after a create, got %q", ch.NewState)
	}
}

func TestCapabilityDescriptor_AdvertisesContactsLimits(t *testing.T) {
	f := setupFixture(t)
	req, _ := http.NewRequest(http.MethodGet, f.baseURL+"/.well-known/jmap", nil)
	req.Header.Set("Authorization", "Bearer "+f.apiKey)
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("session status = %d: %s", resp.StatusCode, body)
	}
	var session struct {
		Capabilities map[string]any `json:"capabilities"`
		Accounts     map[string]struct {
			AccountCapabilities map[string]any `json:"accountCapabilities"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(body, &session); err != nil {
		t.Fatalf("unmarshal session: %v: %s", err, body)
	}
	if _, ok := session.Capabilities["urn:ietf:params:jmap:contacts"]; !ok {
		t.Fatalf("contacts capability not advertised: %+v", session.Capabilities)
	}
	for _, acct := range session.Accounts {
		desc, ok := acct.AccountCapabilities["urn:ietf:params:jmap:contacts"].(map[string]any)
		if !ok {
			t.Fatalf("contacts accountCapability missing: %+v", acct.AccountCapabilities)
		}
		for _, key := range []string{"maxAddressBooksPerAccount", "maxContactsPerAddressBook", "maxSizePerContactBlob"} {
			if _, present := desc[key]; !present {
				t.Errorf("descriptor missing %q: %+v", key, desc)
			}
		}
	}
}
