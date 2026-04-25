package chat_test

import (
	"context"
	"encoding/json"
	"testing"

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

	// Send a message in the space.
	_, _ = f.invoke(t, "Message/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create": map[string]any{
			"m1": map[string]any{
				"conversationId": cid,
				"body":           map[string]any{"text": "hello", "format": "text"},
			},
		},
	})

	// Reload the conversation.
	_, raw := f.invoke(t, "Conversation/get", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"ids":       []string{cid},
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
	got := resp.List[0]
	count, _ := got["messageCount"].(float64)
	if count < 1 {
		t.Errorf("messageCount = %v, want >= 1", count)
	}
	// alice's unread count should be > 0 (she has not read her own
	// message via setLastRead in this scenario; the gap proxy reports
	// the full count).
	unread, _ := got["unreadCount"].(float64)
	if unread < 1 {
		t.Errorf("unreadCount = %v, want >= 1", unread)
	}
}

func TestConversation_Query_HasUnread(t *testing.T) {
	f := setupFixture(t)
	cid, _ := createSpace(t, f, "team")

	// Send a message in the space.
	_, _ = f.invoke(t, "Message/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create": map[string]any{
			"m1": map[string]any{
				"conversationId": cid,
				"body":           map[string]any{"text": "hello", "format": "text"},
			},
		},
	})

	_, raw := f.invoke(t, "Conversation/query", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"filter":    map[string]any{"hasUnread": true},
	})
	var qr struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(raw, &qr); err != nil {
		t.Fatalf("unmarshal query: %v: %s", err, raw)
	}
	if len(qr.IDs) != 1 {
		t.Fatalf("hasUnread=true returned %d, want 1: %+v", len(qr.IDs), qr.IDs)
	}
	if qr.IDs[0] != cid {
		t.Errorf("query result = %q, want %q", qr.IDs[0], cid)
	}
}

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
