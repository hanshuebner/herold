package chat_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

func TestMembership_Set_Create_AdminOnlySpace(t *testing.T) {
	f := setupFixture(t)
	// Bob creates a Space without alice; we use the store directly
	// since the fixture authenticates alice.
	ctx := context.Background()
	cid, err := f.srv.Store.Meta().InsertChatConversation(ctx, store.ChatConversation{
		Kind:                 store.ChatConversationKindSpace,
		Name:                 "bob-space",
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

	// alice — not a member, much less an admin — tries to add carol.
	_, raw := f.invoke(t, "Membership/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create": map[string]any{
			"m1": map[string]any{
				"conversationId": idStr(cid),
				"principalId":    pidStr(f.thirdPID),
				"role":           "member",
			},
		},
	})
	var setResp struct {
		Created    map[string]any            `json:"created"`
		NotCreated map[string]map[string]any `json:"notCreated"`
	}
	if err := json.Unmarshal(raw, &setResp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(setResp.Created) != 0 {
		t.Fatalf("non-admin add should fail: %+v", setResp.Created)
	}
	if got := setResp.NotCreated["m1"]["type"]; got != "forbidden" {
		t.Errorf("notCreated type = %v, want forbidden: %+v", got, setResp.NotCreated)
	}
}

func TestMembership_Set_Update_SelfMute_OK(t *testing.T) {
	f := setupFixture(t)
	cid, _ := createSpace(t, f, "team")
	// Find alice's own membership id.
	rows, err := f.srv.Store.Meta().ListChatMembershipsByConversation(context.Background(), parseConvID(cid))
	if err != nil {
		t.Fatalf("ListChatMembershipsByConversation: %v", err)
	}
	var aliceMID store.MembershipID
	for _, m := range rows {
		if m.PrincipalID == f.pid {
			aliceMID = m.ID
		}
	}
	if aliceMID == 0 {
		t.Fatalf("alice membership not found")
	}

	_, raw := f.invoke(t, "Membership/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"update": map[string]any{
			idStr(aliceMID): map[string]any{
				"isMuted": true,
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
	if _, ok := upResp.Updated[idStr(aliceMID)]; !ok {
		t.Fatalf("self-mute failed: %+v", upResp.NotUpdated)
	}
}

func TestMembership_Set_Update_OtherRole_AdminOnly(t *testing.T) {
	f := setupFixture(t)
	cid, _ := createSpace(t, f, "team")
	rows, err := f.srv.Store.Meta().ListChatMembershipsByConversation(context.Background(), parseConvID(cid))
	if err != nil {
		t.Fatalf("ListChatMembershipsByConversation: %v", err)
	}
	var bobMID store.MembershipID
	for _, m := range rows {
		if m.PrincipalID == f.otherPID {
			bobMID = m.ID
		}
	}
	if bobMID == 0 {
		t.Fatalf("bob membership not found")
	}

	// alice (owner) promotes bob to admin: should succeed.
	_, raw := f.invoke(t, "Membership/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"update": map[string]any{
			idStr(bobMID): map[string]any{"role": "admin"},
		},
	})
	var upResp struct {
		Updated    map[string]any `json:"updated"`
		NotUpdated map[string]any `json:"notUpdated"`
	}
	if err := json.Unmarshal(raw, &upResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := upResp.Updated[idStr(bobMID)]; !ok {
		t.Fatalf("admin promotion failed: %+v", upResp.NotUpdated)
	}

	// Now demote alice to a regular member; further role changes
	// must be rejected.
	for _, m := range rows {
		if m.PrincipalID == f.pid {
			m.Role = store.ChatRoleMember
			if err := f.srv.Store.Meta().UpdateChatMembership(context.Background(), m); err != nil {
				t.Fatalf("UpdateChatMembership: %v", err)
			}
		}
	}

	// alice attempts to demote bob back to member: should be
	// rejected ("forbidden").
	_, raw = f.invoke(t, "Membership/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"update": map[string]any{
			idStr(bobMID): map[string]any{"role": "member"},
		},
	})
	upResp.Updated = nil
	upResp.NotUpdated = nil
	if err := json.Unmarshal(raw, &upResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := upResp.Updated[idStr(bobMID)]; ok {
		t.Errorf("demoted alice's role-change should fail: %+v", upResp.Updated)
	}
	if got, _ := upResp.NotUpdated[idStr(bobMID)].(map[string]any); got["type"] != "forbidden" {
		t.Errorf("notUpdated type = %+v, want forbidden", got)
	}
}

func TestMembership_SetLastRead_EmitsStateChange(t *testing.T) {
	f := setupFixture(t)
	cid, _ := createSpace(t, f, "team")
	created := sendMessage(t, f, cid, "first")
	mid := created["id"].(string)

	// Capture the change-feed cursor before the call. SetChatLastRead
	// must append a (Membership, Updated) entry; we observe that
	// directly rather than via JMAPStates because (per the current
	// fakestore) the chat-table mutations only append to the change
	// feed and the JMAPStates counters for the chat datatypes are
	// bumped lazily by the dispatcher in production. The change feed
	// is the canonical signal for read-receipt fanout.
	before, err := f.srv.Store.Meta().ReadChangeFeed(context.Background(), f.pid, 0, 1000)
	if err != nil {
		t.Fatalf("ReadChangeFeed before: %v", err)
	}
	beforeLen := 0
	for _, e := range before {
		if e.Kind == store.EntityKindMembership {
			beforeLen++
		}
	}

	_, raw := f.invoke(t, "Membership/setLastRead", map[string]any{
		"accountId":      string(protojmap.AccountIDForPrincipal(f.pid)),
		"conversationId": cid,
		"messageId":      mid,
	})
	var slrResp struct {
		ConversationID string `json:"conversationId"`
		State          string `json:"state"`
	}
	if err := json.Unmarshal(raw, &slrResp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if slrResp.ConversationID != cid {
		t.Errorf("returned conversationId = %q, want %q", slrResp.ConversationID, cid)
	}

	after, err := f.srv.Store.Meta().ReadChangeFeed(context.Background(), f.pid, 0, 1000)
	if err != nil {
		t.Fatalf("ReadChangeFeed after: %v", err)
	}
	afterLen := 0
	for _, e := range after {
		if e.Kind == store.EntityKindMembership {
			afterLen++
		}
	}
	if afterLen <= beforeLen {
		t.Errorf("Membership state-change row not appended after setLastRead: before=%d after=%d", beforeLen, afterLen)
	}

	// Verify that the underlying membership row now carries a non-nil
	// LastReadMessageID matching the supplied id.
	mb, err := f.srv.Store.Meta().GetChatMembership(context.Background(), parseConvID(cid), f.pid)
	if err != nil {
		t.Fatalf("GetChatMembership: %v", err)
	}
	if mb.LastReadMessageID == nil {
		t.Errorf("LastReadMessageID nil after setLastRead: %+v", mb)
	} else if uint64(*mb.LastReadMessageID) != parseUint(mid) {
		t.Errorf("LastReadMessageID = %d, want %s", *mb.LastReadMessageID, mid)
	}
}

func parseUint(s string) uint64 {
	var n uint64
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + uint64(c-'0')
	}
	return n
}
