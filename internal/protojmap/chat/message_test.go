package chat_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// sendMessage posts one Message/set { create: {...} } and returns the
// created message body.
func sendMessage(t *testing.T, f *fixture, cid, text string) map[string]any {
	t.Helper()
	_, raw := f.invoke(t, "Message/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create": map[string]any{
			"m1": map[string]any{
				"conversationId": cid,
				"body":           map[string]any{"text": text, "format": "text"},
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
		t.Fatalf("Message/set create failed: %+v / %+v", setResp.Created, setResp.NotCreated)
	}
	return setResp.Created["m1"]
}

func TestMessage_Set_Create_RejectsNonMemberSender(t *testing.T) {
	f := setupFixture(t)
	// Bob and carol create a Space without alice. We need to do this
	// via the store directly because the fixture authenticates as
	// alice.
	ctx := context.Background()
	cid, err := f.srv.Store.Meta().InsertChatConversation(ctx, store.ChatConversation{
		Kind:                 store.ChatConversationKindSpace,
		Name:                 "private",
		CreatedByPrincipalID: f.otherPID,
	})
	if err != nil {
		t.Fatalf("InsertChatConversation: %v", err)
	}
	if _, err := f.srv.Store.Meta().InsertChatMembership(ctx, store.ChatMembership{
		ConversationID: cid,
		PrincipalID:    f.otherPID,
		Role:           store.ChatRoleOwner,
	}); err != nil {
		t.Fatalf("InsertChatMembership bob: %v", err)
	}
	if _, err := f.srv.Store.Meta().InsertChatMembership(ctx, store.ChatMembership{
		ConversationID: cid,
		PrincipalID:    f.thirdPID,
		Role:           store.ChatRoleMember,
	}); err != nil {
		t.Fatalf("InsertChatMembership carol: %v", err)
	}

	// alice tries to send into a conversation she's not a member of.
	_, raw := f.invoke(t, "Message/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create": map[string]any{
			"m1": map[string]any{
				"conversationId": idStr(cid),
				"body":           map[string]any{"text": "intrusion", "format": "text"},
			},
		},
	})
	var setResp struct {
		Created    map[string]map[string]any `json:"created"`
		NotCreated map[string]map[string]any `json:"notCreated"`
	}
	if err := json.Unmarshal(raw, &setResp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(setResp.Created) != 0 {
		t.Fatalf("non-member send should fail: %+v", setResp.Created)
	}
	if got := setResp.NotCreated["m1"]["type"]; got != "forbidden" {
		t.Errorf("notCreated type = %v, want forbidden: %+v", got, setResp.NotCreated)
	}
}

func TestMessage_Set_Create_RespectsBlock_DM(t *testing.T) {
	f := setupFixture(t)
	cid, _ := createDM(t, f) // alice <-> bob

	// Bob blocks alice via the store (we cannot authenticate as bob).
	ctx := context.Background()
	if err := f.srv.Store.Meta().InsertChatBlock(ctx, store.ChatBlock{
		BlockerPrincipalID: f.otherPID,
		BlockedPrincipalID: f.pid,
	}); err != nil {
		t.Fatalf("InsertChatBlock: %v", err)
	}

	// alice tries to send a DM; should be rejected.
	_, raw := f.invoke(t, "Message/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create": map[string]any{
			"m1": map[string]any{
				"conversationId": cid,
				"body":           map[string]any{"text": "are you there", "format": "text"},
			},
		},
	})
	var setResp struct {
		Created    map[string]map[string]any `json:"created"`
		NotCreated map[string]map[string]any `json:"notCreated"`
	}
	if err := json.Unmarshal(raw, &setResp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(setResp.Created) != 0 {
		t.Fatalf("blocked send should fail: %+v", setResp.Created)
	}
	if got := setResp.NotCreated["m1"]["type"]; got != "forbidden" {
		t.Errorf("notCreated type = %v, want forbidden: %+v", got, setResp.NotCreated)
	}
}

func TestMessage_Set_Update_OnlySender(t *testing.T) {
	f := setupFixture(t)
	cid, _ := createSpace(t, f, "team")

	// alice sends one message; alice (the sender) can edit it.
	created := sendMessage(t, f, cid, "first")
	mid := created["id"].(string)
	_, raw := f.invoke(t, "Message/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"update": map[string]any{
			mid: map[string]any{
				"body": map[string]any{"text": "edited", "format": "text"},
			},
		},
	})
	var upResp struct {
		Updated    map[string]any `json:"updated"`
		NotUpdated map[string]any `json:"notUpdated"`
	}
	if err := json.Unmarshal(raw, &upResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := upResp.Updated[mid]; !ok {
		t.Fatalf("sender's edit failed: %+v", upResp.NotUpdated)
	}

	// Bob sends a separate message via the store directly (the
	// fixture only authenticates alice). alice tries to edit bob's
	// message; the handler must reject.
	ctx := context.Background()
	bobsMsg, err := f.srv.Store.Meta().InsertChatMessage(ctx, store.ChatMessage{
		ConversationID:    parseConvID(cid),
		SenderPrincipalID: &f.otherPID,
		BodyText:          "from bob",
		BodyFormat:        store.ChatBodyFormatText,
	})
	if err != nil {
		t.Fatalf("InsertChatMessage bob: %v", err)
	}
	bobsMID := idStr(bobsMsg)

	_, raw = f.invoke(t, "Message/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"update": map[string]any{
			bobsMID: map[string]any{
				"body": map[string]any{"text": "spoof", "format": "text"},
			},
		},
	})
	upResp.Updated = nil
	upResp.NotUpdated = nil
	if err := json.Unmarshal(raw, &upResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := upResp.Updated[bobsMID]; ok {
		t.Errorf("non-sender edit should have failed: %+v", upResp.Updated)
	}
	if got, _ := upResp.NotUpdated[bobsMID].(map[string]any); got["type"] != "forbidden" {
		t.Errorf("notUpdated type = %+v, want forbidden", got)
	}
}

func TestMessage_Set_Destroy_SoftDelete(t *testing.T) {
	f := setupFixture(t)
	cid, _ := createSpace(t, f, "team")
	created := sendMessage(t, f, cid, "regrettable")
	mid := created["id"].(string)

	_, raw := f.invoke(t, "Message/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"destroy":   []string{mid},
	})
	var setResp struct {
		Destroyed []string `json:"destroyed"`
	}
	if err := json.Unmarshal(raw, &setResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(setResp.Destroyed) != 1 {
		t.Fatalf("destroyed = %+v", setResp.Destroyed)
	}

	// Get should still return the row with deletedAt set + body cleared.
	_, raw = f.invoke(t, "Message/get", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"ids":       []string{mid},
	})
	var getResp struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(raw, &getResp); err != nil {
		t.Fatalf("unmarshal get: %v", err)
	}
	if len(getResp.List) != 1 {
		t.Fatalf("list = %+v", getResp.List)
	}
	got := getResp.List[0]
	if got["deletedAt"] == nil {
		t.Errorf("deletedAt should be set on tombstone: %+v", got)
	}
	body, _ := got["body"].(map[string]any)
	if body == nil || body["text"] != "" {
		t.Errorf("body should be cleared on tombstone: %+v", body)
	}
}

func TestMessage_React_AddRemove(t *testing.T) {
	f := setupFixture(t)
	cid, _ := createSpace(t, f, "team")
	created := sendMessage(t, f, cid, "react to me")
	mid := created["id"].(string)

	// Add a reaction.
	_, raw := f.invoke(t, "Message/react", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"messageId": mid,
		"emoji":     "+1",
		"present":   true,
	})
	var rr struct {
		Reactions map[string][]string `json:"reactions"`
	}
	if err := json.Unmarshal(raw, &rr); err != nil {
		t.Fatalf("unmarshal react: %v: %s", err, raw)
	}
	if len(rr.Reactions["+1"]) != 1 || rr.Reactions["+1"][0] != pidStr(f.pid) {
		t.Errorf("after add: reactions = %+v", rr.Reactions)
	}

	// Remove the reaction.
	_, raw = f.invoke(t, "Message/react", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"messageId": mid,
		"emoji":     "+1",
		"present":   false,
	})
	rr.Reactions = nil
	if err := json.Unmarshal(raw, &rr); err != nil {
		t.Fatalf("unmarshal react2: %v: %s", err, raw)
	}
	if len(rr.Reactions["+1"]) != 0 {
		t.Errorf("after remove: reactions = %+v", rr.Reactions)
	}
}

func TestMessage_Get_TombstoneRendersDeletedAt(t *testing.T) {
	f := setupFixture(t)
	cid, _ := createSpace(t, f, "team")
	created := sendMessage(t, f, cid, "fleeting")
	mid := created["id"].(string)

	// Soft-delete via Message/set destroy.
	_, _ = f.invoke(t, "Message/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"destroy":   []string{mid},
	})

	_, raw := f.invoke(t, "Message/get", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"ids":       []string{mid},
	})
	if !strings.Contains(string(raw), `"deletedAt"`) {
		t.Errorf("response missing deletedAt: %s", raw)
	}
	var resp struct {
		List []map[string]any `json:"list"`
	}
	_ = json.Unmarshal(raw, &resp)
	if len(resp.List) != 1 {
		t.Fatalf("list = %+v", resp.List)
	}
	if resp.List[0]["deletedAt"] == nil {
		t.Errorf("deletedAt nil on tombstone: %+v", resp.List[0])
	}
}

// parseMsgID parses a stringified message id.
func parseMsgID(s string) store.ChatMessageID {
	var n uint64
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + uint64(c-'0')
	}
	return store.ChatMessageID(n)
}
