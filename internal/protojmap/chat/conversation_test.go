package chat_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// createDM creates a DM between alice (the fixture's authenticated
// principal) and bob, returning the conversation id and the rendered
// "created" body.
func createDM(t *testing.T, f *fixture) (string, map[string]any) {
	t.Helper()
	_, raw := f.invoke(t, "Conversation/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create": map[string]any{
			"c1": map[string]any{
				"kind":    "dm",
				"members": []string{pidStr(f.otherPID)},
			},
		},
	})
	var setResp struct {
		Created    map[string]map[string]any `json:"created"`
		NotCreated map[string]any            `json:"notCreated"`
	}
	if err := json.Unmarshal(raw, &setResp); err != nil {
		t.Fatalf("unmarshal create: %v: %s", err, raw)
	}
	if len(setResp.Created) != 1 {
		t.Fatalf("DM create failed: %+v / %+v", setResp.Created, setResp.NotCreated)
	}
	created := setResp.Created["c1"]
	id, ok := created["id"].(string)
	if !ok || id == "" {
		t.Fatalf("created has no id: %+v", created)
	}
	return id, created
}

// createSpace creates a Space with alice + bob + carol, returning the
// conversation id and the rendered "created" body.
func createSpace(t *testing.T, f *fixture, name string) (string, map[string]any) {
	t.Helper()
	_, raw := f.invoke(t, "Conversation/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create": map[string]any{
			"s1": map[string]any{
				"kind":    "space",
				"name":    name,
				"members": []string{pidStr(f.otherPID), pidStr(f.thirdPID)},
			},
		},
	})
	var setResp struct {
		Created    map[string]map[string]any `json:"created"`
		NotCreated map[string]any            `json:"notCreated"`
	}
	if err := json.Unmarshal(raw, &setResp); err != nil {
		t.Fatalf("unmarshal create: %v: %s", err, raw)
	}
	if len(setResp.Created) != 1 {
		t.Fatalf("Space create failed: %+v / %+v", setResp.Created, setResp.NotCreated)
	}
	created := setResp.Created["s1"]
	id, ok := created["id"].(string)
	if !ok || id == "" {
		t.Fatalf("created has no id: %+v", created)
	}
	return id, created
}

func TestConversation_DM_Create_AutoNameFromOtherMember(t *testing.T) {
	f := setupFixture(t)
	_, created := createDM(t, f)
	name, _ := created["name"].(string)
	if name == "" {
		t.Errorf("DM auto-name is empty: %+v", created)
	}
	// Bob's display name is "Bob"; the auto-name picks up the other
	// member's display name.
	if name != "Bob" {
		t.Errorf("DM auto-name = %q, want %q (Bob's display name)", name, "Bob")
	}
	if got := created["kind"]; got != "dm" {
		t.Errorf("kind = %v, want dm", got)
	}
	members, _ := created["members"].([]any)
	if len(members) != 2 {
		t.Errorf("DM members = %+v, want 2", members)
	}
}

func TestConversation_Space_Create_OwnerAdded(t *testing.T) {
	f := setupFixture(t)
	_, created := createSpace(t, f, "team")
	members, _ := created["members"].([]any)
	if len(members) != 3 {
		t.Fatalf("Space members = %+v, want 3", members)
	}
	// The creator (alice) is recorded as owner.
	foundOwner := false
	for _, raw := range members {
		m, _ := raw.(map[string]any)
		if m == nil {
			continue
		}
		if m["principalId"] == pidStr(f.pid) && m["role"] == "owner" {
			foundOwner = true
		}
	}
	if !foundOwner {
		t.Errorf("creator not recorded as owner: %+v", members)
	}
}

func TestConversation_Get_RendersUnreadCount(t *testing.T) {
	f := setupFixture(t)
	cid, _ := createSpace(t, f, "team")

	// alice sends a message in the space.
	_, _ = f.invoke(t, "Message/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create": map[string]any{
			"m1": map[string]any{
				"conversationId": cid,
				"body":           map[string]any{"text": "hello", "format": "text"},
			},
		},
	})

	// alice's own message is NOT unread for her — Conversation/get
	// from alice's perspective must report 0.
	_, raw := f.invoke(t, "Conversation/get", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"ids":       []string{cid},
	})
	var resp struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal alice: %v: %s", err, raw)
	}
	if len(resp.List) != 1 {
		t.Fatalf("alice list = %+v", resp.List)
	}
	if u, _ := resp.List[0]["unreadCount"].(float64); u != 0 {
		t.Errorf("alice unreadCount = %v, want 0 (own message)", u)
	}

	// From bob's perspective, alice's message IS unread.
	bobKey := bobAPIKey(t, f)
	_, raw = invokeAs(t, f, bobKey, "Conversation/get", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.otherPID)),
		"ids":       []string{cid},
	})
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal bob: %v: %s", err, raw)
	}
	if len(resp.List) != 1 {
		t.Fatalf("bob list = %+v", resp.List)
	}
	if u, _ := resp.List[0]["unreadCount"].(float64); u != 1 {
		t.Errorf("bob unreadCount = %v, want 1", u)
	}
}

// TestConversation_Get_UnreadCountSurvivesAcrossConversations is the
// regression test for the gap-arithmetic bug fixed alongside this
// test. chat_messages.id is a global autoincrement; the previous
// implementation compared lastReadMessageID against the conversation's
// per-row MessageCount, which silently returned 0 once a different
// conversation had pushed message ids beyond the per-conversation
// count.
func TestConversation_Get_UnreadCountSurvivesAcrossConversations(t *testing.T) {
	f := setupFixture(t)
	bobKey := bobAPIKey(t, f)
	dmID, _ := createDM(t, f) // alice + bob

	// alice creates a separate space and posts several messages there
	// to push global chat_messages.id well past the DM's per-row count.
	otherID, _ := createSpace(t, f, "noise")
	for i := 0; i < 5; i++ {
		_, _ = f.invoke(t, "Message/set", map[string]any{
			"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
			"create": map[string]any{
				fmt.Sprintf("n%d", i): map[string]any{
					"conversationId": otherID,
					"body":           map[string]any{"text": "noise", "format": "text"},
				},
			},
		})
	}

	// alice now sends one DM message to bob. The global id of this
	// message is at least 6, but the DM's MessageCount is 1.
	_, _ = f.invoke(t, "Message/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create": map[string]any{
			"d1": map[string]any{
				"conversationId": dmID,
				"body":           map[string]any{"text": "hi bob", "format": "text"},
			},
		},
	})

	// bob has never read anything in the DM (lastReadMessageID == nil),
	// so he should see unreadCount == 1 regardless of message ids.
	_, raw := invokeAs(t, f, bobKey, "Conversation/get", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.otherPID)),
		"ids":       []string{dmID},
	})
	var resp struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal nil-anchor: %v: %s", err, raw)
	}
	if len(resp.List) != 1 {
		t.Fatalf("nil-anchor list = %+v", resp.List)
	}
	if u, _ := resp.List[0]["unreadCount"].(float64); u != 1 {
		t.Errorf("bob unreadCount (nil anchor) = %v, want 1", u)
	}

	// Pull bob's membership row so we can advance his read pointer to
	// the message we just sent (simulates a markRead).
	mems, err := f.srv.Store.Meta().ListChatMembershipsByConversation(context.Background(), parseConvID(dmID))
	if err != nil {
		t.Fatalf("ListChatMembershipsByConversation: %v", err)
	}
	var bobMem store.ChatMembership
	for _, m := range mems {
		if m.PrincipalID == f.otherPID {
			bobMem = m
		}
	}
	if bobMem.ID == 0 {
		t.Fatalf("bob membership not found in %+v", mems)
	}

	// Find the DM message id.
	msgs, err := f.srv.Store.Meta().ListChatMessages(context.Background(), store.ChatMessageFilter{
		ConversationID: ptrConvID(parseConvID(dmID)),
	})
	if err != nil {
		t.Fatalf("ListChatMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("dm messages = %+v", msgs)
	}
	if err := f.srv.Store.Meta().SetLastRead(context.Background(), f.otherPID, parseConvID(dmID), msgs[0].ID); err != nil {
		t.Fatalf("SetLastRead: %v", err)
	}

	// After bob marks the DM read, unreadCount drops to 0.
	_, raw = invokeAs(t, f, bobKey, "Conversation/get", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.otherPID)),
		"ids":       []string{dmID},
	})
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal post-read: %v: %s", err, raw)
	}
	if u, _ := resp.List[0]["unreadCount"].(float64); u != 0 {
		t.Errorf("bob unreadCount post-read = %v, want 0", u)
	}

	// alice now sends a SECOND DM message. With the old gap arithmetic
	// (lastRead is some big global id, MessageCount is 2) the
	// comparison `bigID < 2` would be false and unreadCount would
	// report 0. Verify that the real COUNT query reports 1.
	_, _ = f.invoke(t, "Message/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create": map[string]any{
			"d2": map[string]any{
				"conversationId": dmID,
				"body":           map[string]any{"text": "still there?", "format": "text"},
			},
		},
	})
	_, raw = invokeAs(t, f, bobKey, "Conversation/get", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.otherPID)),
		"ids":       []string{dmID},
	})
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal second-msg: %v: %s", err, raw)
	}
	if u, _ := resp.List[0]["unreadCount"].(float64); u != 1 {
		t.Errorf("bob unreadCount after second message = %v, want 1 (regression: gap arithmetic)", u)
	}
}

func TestConversation_Query_HasUnread(t *testing.T) {
	f := setupFixture(t)
	cid, _ := createSpace(t, f, "team")
	bobKey := bobAPIKey(t, f)

	// alice sends a message in the space.
	_, _ = f.invoke(t, "Message/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create": map[string]any{
			"m1": map[string]any{
				"conversationId": cid,
				"body":           map[string]any{"text": "hello", "format": "text"},
			},
		},
	})

	// alice's own message is not unread for her, so hasUnread=true
	// returns no conversations from her perspective.
	_, raw := f.invoke(t, "Conversation/query", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"filter":    map[string]any{"hasUnread": true},
	})
	var qr struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(raw, &qr); err != nil {
		t.Fatalf("unmarshal alice query: %v: %s", err, raw)
	}
	if len(qr.IDs) != 0 {
		t.Errorf("alice hasUnread=true = %+v, want []", qr.IDs)
	}

	// From bob's perspective the same query returns the space.
	_, raw = invokeAs(t, f, bobKey, "Conversation/query", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.otherPID)),
		"filter":    map[string]any{"hasUnread": true},
	})
	if err := json.Unmarshal(raw, &qr); err != nil {
		t.Fatalf("unmarshal bob query: %v: %s", err, raw)
	}
	if len(qr.IDs) != 1 || qr.IDs[0] != cid {
		t.Errorf("bob hasUnread=true = %+v, want [%q]", qr.IDs, cid)
	}
}

// ptrConvID returns a pointer to the given ConversationID, for use as
// a ChatMessageFilter.ConversationID value.
func ptrConvID(id store.ConversationID) *store.ConversationID { return &id }

func TestConversation_Set_Update_OwnerOnly(t *testing.T) {
	f := setupFixture(t)
	cid, _ := createSpace(t, f, "team")

	// alice (the creator/owner) can update.
	_, raw := f.invoke(t, "Conversation/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"update": map[string]any{
			cid: map[string]any{"topic": "all things kittens"},
		},
	})
	var upResp struct {
		Updated    map[string]any `json:"updated"`
		NotUpdated map[string]any `json:"notUpdated"`
	}
	if err := json.Unmarshal(raw, &upResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := upResp.Updated[cid]; !ok {
		t.Fatalf("owner update failed: %+v", upResp.NotUpdated)
	}

	// Demote alice to member; the conversation still has carol+bob as
	// regular members and alice as owner. Promote bob to admin via a
	// store mutation, then attempt the update as bob via a direct
	// fixture switch — but our fixture only authenticates alice. Use
	// a store-level demotion of alice and re-attempt: the update
	// should fail with "forbidden" once alice is a plain member.
	rows, err := f.srv.Store.Meta().ListChatMembershipsByConversation(context.Background(), parseConvID(cid))
	if err != nil {
		t.Fatalf("ListChatMembershipsByConversation: %v", err)
	}
	for _, m := range rows {
		if m.PrincipalID == f.pid {
			m.Role = store.ChatRoleMember
			if err := f.srv.Store.Meta().UpdateChatMembership(context.Background(), m); err != nil {
				t.Fatalf("UpdateChatMembership: %v", err)
			}
		}
	}

	_, raw = f.invoke(t, "Conversation/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"update": map[string]any{
			cid: map[string]any{"topic": "rejected"},
		},
	})
	upResp.Updated = nil
	upResp.NotUpdated = nil
	if err := json.Unmarshal(raw, &upResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := upResp.Updated[cid]; ok {
		t.Errorf("non-owner/admin update should have failed: %+v", upResp.Updated)
	}
	if _, ok := upResp.NotUpdated[cid]; !ok {
		t.Errorf("notUpdated should contain %q: %+v", cid, upResp.NotUpdated)
	}
}

// invokeAs issues a single JMAP method call authenticated with the
// given API key (not the fixture default). Used by tests that need to
// observe the same resource from a different principal's perspective.
func invokeAs(t *testing.T, f *fixture, apiKey, method string, args any) (string, json.RawMessage) {
	t.Helper()
	argsBytes, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	body := map[string]any{
		"using":       []protojmap.CapabilityID{protojmap.CapabilityCore, protojmap.CapabilityJMAPChat},
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
	req.Header.Set("Authorization", "Bearer "+apiKey)
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

// bobAPIKey provisions a fresh API key for bob and returns the plaintext
// token.
func bobAPIKey(t *testing.T, f *fixture) string {
	t.Helper()
	plaintext := fmt.Sprintf("hk_test_bob_%d", f.otherPID)
	hash := protoadmin.HashAPIKey(plaintext)
	if _, err := f.srv.Store.Meta().InsertAPIKey(context.Background(), store.APIKey{
		PrincipalID: f.otherPID,
		Hash:        hash,
		Name:        "test-bob",
	}); err != nil {
		t.Fatalf("InsertAPIKey bob: %v", err)
	}
	return plaintext
}

// TestConversation_DM_Get_BobSeesAliceName asserts Bug-A fix: when bob
// fetches a DM that alice created, the wire Name must be alice's display
// name (not bob's own name).
func TestConversation_DM_Get_BobSeesAliceName(t *testing.T) {
	f := setupFixture(t)
	// Alice creates the DM.
	cid, _ := createDM(t, f)

	// Provision a key so we can speak JMAP as bob.
	bobKey := bobAPIKey(t, f)
	bobAcct := string(protojmap.AccountIDForPrincipal(f.otherPID))

	_, raw := invokeAs(t, f, bobKey, "Conversation/get", map[string]any{
		"accountId": bobAcct,
		"ids":       []string{cid},
	})
	var resp struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal Conversation/get: %v: %s", err, raw)
	}
	if len(resp.List) != 1 {
		t.Fatalf("list = %+v", resp.List)
	}
	got := resp.List[0]
	name, _ := got["name"].(string)
	// Bob must see Alice's display name, not his own.
	if name == "" {
		t.Errorf("DM name is empty for bob's view: %+v", got)
	}
	if name == "Bob" {
		t.Errorf("DM name = %q: bob sees his own name instead of alice's", name)
	}
	if name != "Alice" {
		t.Errorf("DM name = %q, want %q (alice's display name)", name, "Alice")
	}
}

// -- DM deduplication (re #47) -----------------------------------------

// TestConversation_DM_Dedup_SameAliceCreate asserts that alice creating
// the same DM twice returns the same conversation id and exactly one row
// in the store.
func TestConversation_DM_Dedup_SameAliceCreate(t *testing.T) {
	f := setupFixture(t)

	id1, _ := createDM(t, f)
	id2, _ := createDM(t, f)

	if id1 != id2 {
		t.Errorf("duplicate DM create returned distinct ids %q and %q, want same id", id1, id2)
	}

	// Confirm exactly one row exists.
	rows, err := f.srv.Store.Meta().ListChatConversations(context.Background(), store.ChatConversationFilter{
		Kind:            &[]string{store.ChatConversationKindDM}[0],
		IncludeArchived: true,
	})
	if err != nil {
		t.Fatalf("ListChatConversations: %v", err)
	}
	dmCount := 0
	for _, c := range rows {
		if c.Kind == store.ChatConversationKindDM {
			dmCount++
		}
	}
	if dmCount != 1 {
		t.Errorf("store has %d DM conversation rows, want 1", dmCount)
	}
}

// TestConversation_DM_Dedup_Symmetric asserts that bob creating a DM
// with alice returns the same conversation id that alice's earlier DM
// creation produced.
func TestConversation_DM_Dedup_Symmetric(t *testing.T) {
	f := setupFixture(t)

	// Alice creates first.
	id1, _ := createDM(t, f)

	// Provision a key for bob.
	bobKey := bobAPIKey(t, f)
	bobAcct := string(protojmap.AccountIDForPrincipal(f.otherPID))

	// Bob creates (symmetric pair: bob -> alice).
	_, raw := invokeAs(t, f, bobKey, "Conversation/set", map[string]any{
		"accountId": bobAcct,
		"create": map[string]any{
			"c1": map[string]any{
				"kind":    "dm",
				"members": []string{pidStr(f.pid)},
			},
		},
	})
	var setResp struct {
		Created    map[string]map[string]any `json:"created"`
		NotCreated map[string]any            `json:"notCreated"`
	}
	if err := json.Unmarshal(raw, &setResp); err != nil {
		t.Fatalf("unmarshal bob create: %v: %s", err, raw)
	}
	if len(setResp.Created) != 1 {
		t.Fatalf("bob create failed: notCreated=%+v", setResp.NotCreated)
	}
	id2, _ := setResp.Created["c1"]["id"].(string)

	if id1 != id2 {
		t.Errorf("alice created %q, bob created %q; want same DM id", id1, id2)
	}

	// One DM row in total.
	rows, err := f.srv.Store.Meta().ListChatConversations(context.Background(), store.ChatConversationFilter{
		Kind:            &[]string{store.ChatConversationKindDM}[0],
		IncludeArchived: true,
	})
	if err != nil {
		t.Fatalf("ListChatConversations: %v", err)
	}
	dmCount := 0
	for _, c := range rows {
		if c.Kind == store.ChatConversationKindDM {
			dmCount++
		}
	}
	if dmCount != 1 {
		t.Errorf("store has %d DM rows, want 1", dmCount)
	}
}

// TestConversation_DM_Dedup_ArchivedIsHit asserts that a second create
// call returns the existing DM even if it is archived, rather than
// creating a fresh non-archived row.
func TestConversation_DM_Dedup_ArchivedIsHit(t *testing.T) {
	f := setupFixture(t)

	id1, _ := createDM(t, f)

	// Archive the DM.
	_, archRaw := f.invoke(t, "Conversation/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"update": map[string]any{
			id1: map[string]any{"isArchived": true},
		},
	})
	var archResp struct {
		Updated    map[string]any `json:"updated"`
		NotUpdated map[string]any `json:"notUpdated"`
	}
	if err := json.Unmarshal(archRaw, &archResp); err != nil {
		t.Fatalf("unmarshal archive: %v", err)
	}
	if _, ok := archResp.Updated[id1]; !ok {
		t.Fatalf("archive update failed: %+v", archResp.NotUpdated)
	}

	// Create again — must return the archived row, not a new one.
	id2, created := createDM(t, f)
	if id1 != id2 {
		t.Errorf("archived DM was not a dedup hit: first=%q second=%q", id1, id2)
	}
	isArchived, _ := created["isArchived"].(bool)
	if !isArchived {
		t.Errorf("dedup-returned DM should be archived; got isArchived=%v", isArchived)
	}
}

// TestConversation_DM_Dedup_ThreeWay asserts that alice<->bob and
// alice<->carol are distinct DMs — neither aliases the other.
func TestConversation_DM_Dedup_ThreeWay(t *testing.T) {
	f := setupFixture(t)

	idBob, _ := createDM(t, f) // alice <-> bob

	// alice <-> carol
	_, rawCarol := f.invoke(t, "Conversation/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create": map[string]any{
			"c1": map[string]any{
				"kind":    "dm",
				"members": []string{pidStr(f.thirdPID)},
			},
		},
	})
	var setResp struct {
		Created    map[string]map[string]any `json:"created"`
		NotCreated map[string]any            `json:"notCreated"`
	}
	if err := json.Unmarshal(rawCarol, &setResp); err != nil {
		t.Fatalf("unmarshal alice<->carol: %v: %s", err, rawCarol)
	}
	if len(setResp.Created) != 1 {
		t.Fatalf("alice<->carol failed: %+v", setResp.NotCreated)
	}
	idCarol, _ := setResp.Created["c1"]["id"].(string)

	if idBob == idCarol {
		t.Errorf("alice<->bob and alice<->carol share the same id %q", idBob)
	}
}

// TestConversation_Members_HaveDisplayName asserts Bug-B fix: every
// member entry in Conversation/get carries a non-empty displayName so
// the client can label senders without a separate Principal/get call.
func TestConversation_Members_HaveDisplayName(t *testing.T) {
	f := setupFixture(t)
	cid, _ := createSpace(t, f, "team")

	_, raw := f.invoke(t, "Conversation/get", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"ids":       []string{cid},
	})
	var resp struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal Conversation/get: %v: %s", err, raw)
	}
	if len(resp.List) != 1 {
		t.Fatalf("list = %+v", resp.List)
	}
	members, _ := resp.List[0]["members"].([]any)
	if len(members) == 0 {
		t.Fatalf("members is empty")
	}
	for i, raw := range members {
		m, _ := raw.(map[string]any)
		if m == nil {
			t.Fatalf("member[%d] is not a map: %T", i, raw)
		}
		dn, _ := m["displayName"].(string)
		if dn == "" {
			t.Errorf("member[%d] (principalId=%v) has empty displayName: %+v", i, m["principalId"], m)
		}
	}
}

// parseConvID parses a stringified conversation id.
func parseConvID(s string) store.ConversationID {
	var n uint64
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + uint64(c-'0')
	}
	return store.ConversationID(n)
}
