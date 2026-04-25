package chat_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"testing"

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/protojmap/chat"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
)

// fixture wires a harness with a JMAP listener attached to a Server
// preloaded with the chat handlers. Mirrors the contacts/calendars
// fixtures.
type fixture struct {
	srv      *testharness.Server
	pid      store.PrincipalID // principal alice
	otherPID store.PrincipalID // principal bob (used for DM / membership tests)
	thirdPID store.PrincipalID // principal carol (third Space member)
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
	alice, err := srv.Store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@example.test",
		DisplayName:    "Alice",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal alice: %v", err)
	}
	bob, err := srv.Store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "bob@example.test",
		DisplayName:    "Bob",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal bob: %v", err)
	}
	carol, err := srv.Store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "carol@example.test",
		DisplayName:    "Carol",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal carol: %v", err)
	}

	plaintext := "hk_test_alice_" + fmt.Sprintf("%d", alice.ID)
	hash := protoadmin.HashAPIKey(plaintext)
	if _, err := srv.Store.Meta().InsertAPIKey(ctx, store.APIKey{
		PrincipalID: alice.ID,
		Hash:        hash,
		Name:        "test",
	}); err != nil {
		t.Fatalf("InsertAPIKey: %v", err)
	}

	dir := directory.New(srv.Store.Meta(), srv.Logger, srv.Clock, nil)
	jmapServ := protojmap.NewServer(srv.Store, dir, nil, srv.Logger, srv.Clock, protojmap.Options{})
	chat.Register(jmapServ.Registry(), srv.Store, srv.Logger, srv.Clock)

	if err := srv.AttachJMAP("jmap", jmapServ, protojmap.ListenerModePlain); err != nil {
		t.Fatalf("AttachJMAP: %v", err)
	}
	client, base := srv.DialJMAPByName(ctx, "jmap")
	return &fixture{
		srv: srv, pid: alice.ID, otherPID: bob.ID, thirdPID: carol.ID,
		client: client, baseURL: base, apiKey: plaintext, jmapServ: jmapServ,
	}
}

// invoke posts a single method call and returns the parsed response.
func (f *fixture) invoke(t *testing.T, method string, args any, using ...protojmap.CapabilityID) (string, json.RawMessage) {
	t.Helper()
	if len(using) == 0 {
		using = []protojmap.CapabilityID{protojmap.CapabilityCore, protojmap.CapabilityJMAPChat}
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

// pidStr formats a PrincipalID as the wire-form decimal id.
func pidStr(pid store.PrincipalID) string { return strconv.FormatUint(uint64(pid), 10) }

// idStr formats any uint64-shaped id as decimal.
func idStr[T ~uint64](v T) string { return strconv.FormatUint(uint64(v), 10) }

// -- Block tests ------------------------------------------------------

func TestBlock_Set_Create_HidesFromGet(t *testing.T) {
	f := setupFixture(t)

	// alice blocks bob.
	_, raw := f.invoke(t, "Block/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create": map[string]any{
			"b1": map[string]any{
				"blockedPrincipalId": pidStr(f.otherPID),
			},
		},
	})
	var setResp struct {
		Created    map[string]map[string]any `json:"created"`
		NotCreated map[string]any            `json:"notCreated"`
	}
	if err := json.Unmarshal(raw, &setResp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(setResp.Created) != 1 {
		t.Fatalf("expected 1 created, got %+v / %+v", setResp.Created, setResp.NotCreated)
	}
	if got := setResp.Created["b1"]["blockedPrincipalId"]; got != pidStr(f.otherPID) {
		t.Errorf("blockedPrincipalId = %v, want %s", got, pidStr(f.otherPID))
	}

	// Block list now contains bob.
	_, raw = f.invoke(t, "Block/get", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
	})
	var getResp struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(raw, &getResp); err != nil {
		t.Fatalf("unmarshal get: %v", err)
	}
	if len(getResp.List) != 1 {
		t.Fatalf("block list = %+v", getResp.List)
	}

	// Now destroy and verify it's gone.
	_, raw = f.invoke(t, "Block/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"destroy":   []string{pidStr(f.otherPID)},
	})
	var destroyResp struct {
		Destroyed []string `json:"destroyed"`
	}
	if err := json.Unmarshal(raw, &destroyResp); err != nil {
		t.Fatalf("unmarshal destroy: %v", err)
	}
	if len(destroyResp.Destroyed) != 1 {
		t.Fatalf("destroyed = %+v", destroyResp.Destroyed)
	}

	_, raw = f.invoke(t, "Block/get", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
	})
	if err := json.Unmarshal(raw, &getResp); err != nil {
		t.Fatalf("unmarshal get2: %v", err)
	}
	if len(getResp.List) != 0 {
		t.Errorf("block list should be empty after destroy: %+v", getResp.List)
	}
}

func TestCapabilityDescriptor_AdvertisesChatLimits(t *testing.T) {
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
	if _, ok := session.Capabilities["urn:herold:chat"]; !ok {
		t.Fatalf("chat capability not advertised: %+v", session.Capabilities)
	}
	for _, acct := range session.Accounts {
		desc, ok := acct.AccountCapabilities["urn:herold:chat"].(map[string]any)
		if !ok {
			t.Fatalf("chat accountCapability missing: %+v", acct.AccountCapabilities)
		}
		for _, key := range []string{
			"maxConversationsPerAccount",
			"maxMembersPerSpace",
			"maxMessageBodyBytes",
			"maxAttachmentsPerMessage",
			"maxReactionsPerMessage",
		} {
			if _, present := desc[key]; !present {
				t.Errorf("descriptor missing %q: %+v", key, desc)
			}
		}
	}
}
