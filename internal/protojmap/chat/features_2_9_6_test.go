package chat_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// -- REQ-CHAT-32 ------------------------------------------------------

// TestConversation_Set_DM_RejectsReadReceiptsOptOut covers the
// REQ-CHAT-31/32 boundary: DMs always have read receipts on, so a
// Conversation/set { update: { readReceiptsEnabled: false } } against
// a DM must fail with invalidProperties on the readReceiptsEnabled
// field.
func TestConversation_Set_DM_RejectsReadReceiptsOptOut(t *testing.T) {
	f := setupFixture(t)
	cid, _ := createDM(t, f)

	_, raw := f.invoke(t, "Conversation/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"update": map[string]any{
			cid: map[string]any{"readReceiptsEnabled": false},
		},
	})
	var resp struct {
		Updated    map[string]any            `json:"updated"`
		NotUpdated map[string]map[string]any `json:"notUpdated"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if _, ok := resp.Updated[cid]; ok {
		t.Errorf("DM read-receipt opt-out should be rejected: %+v", resp.Updated)
	}
	notUp, ok := resp.NotUpdated[cid]
	if !ok {
		t.Fatalf("notUpdated should contain %q: %+v", cid, resp.NotUpdated)
	}
	if got := notUp["type"]; got != "invalidProperties" {
		t.Errorf("notUpdated type = %v, want invalidProperties", got)
	}
}

// TestMembership_Get_Space_SuppressesOtherReadPointers verifies that
// when a Space sets readReceiptsEnabled=false, Membership/get masks
// other members' lastReadMessageId while leaving the requester's own
// untouched (REQ-CHAT-32).
func TestMembership_Get_Space_SuppressesOtherReadPointers(t *testing.T) {
	f := setupFixture(t)
	cid, _ := createSpace(t, f, "team")
	ctx := context.Background()

	// Disable read receipts on the Space.
	_, raw := f.invoke(t, "Conversation/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"update": map[string]any{
			cid: map[string]any{"readReceiptsEnabled": false},
		},
	})
	var setResp struct {
		Updated    map[string]any `json:"updated"`
		NotUpdated map[string]any `json:"notUpdated"`
	}
	if err := json.Unmarshal(raw, &setResp); err != nil {
		t.Fatalf("unmarshal Conversation/set: %v: %s", err, raw)
	}
	if _, ok := setResp.Updated[cid]; !ok {
		t.Fatalf("Space readReceiptsEnabled toggle failed: %+v / %+v",
			setResp.Updated, setResp.NotUpdated)
	}

	// Send a message and advance bob's read pointer to it via the store.
	msg := sendMessage(t, f, cid, "hello")
	mid := msg["id"].(string)
	rows, err := f.srv.Store.Meta().ListChatMembershipsByConversation(ctx, parseConvID(cid))
	if err != nil {
		t.Fatalf("ListChatMembershipsByConversation: %v", err)
	}
	var aliceMID, bobMID store.MembershipID
	var msgID store.ChatMessageID
	{
		// Convert mid from string back to ChatMessageID.
		var n uint64
		for _, c := range mid {
			if c < '0' || c > '9' {
				break
			}
			n = n*10 + uint64(c-'0')
		}
		msgID = store.ChatMessageID(n)
	}
	for _, m := range rows {
		switch m.PrincipalID {
		case f.pid:
			aliceMID = m.ID
		case f.otherPID:
			bobMID = m.ID
		}
	}
	if aliceMID == 0 || bobMID == 0 {
		t.Fatalf("memberships missing: alice=%d bob=%d", aliceMID, bobMID)
	}
	// Advance bob's last-read pointer directly via the store.
	if err := f.srv.Store.Meta().SetLastRead(ctx, f.otherPID, parseConvID(cid), msgID); err != nil {
		t.Fatalf("SetLastRead bob: %v", err)
	}
	// Advance alice's own pointer via the store too.
	if err := f.srv.Store.Meta().SetLastRead(ctx, f.pid, parseConvID(cid), msgID); err != nil {
		t.Fatalf("SetLastRead alice: %v", err)
	}

	// Membership/get for both ids in a single call.
	_, raw = f.invoke(t, "Membership/get", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"ids":       []string{idStr(aliceMID), idStr(bobMID)},
	})
	var getResp struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(raw, &getResp); err != nil {
		t.Fatalf("unmarshal Membership/get: %v: %s", err, raw)
	}
	if len(getResp.List) != 2 {
		t.Fatalf("Membership/get returned %d rows, want 2: %+v", len(getResp.List), getResp.List)
	}
	for _, row := range getResp.List {
		pid := row["principalId"]
		lr := row["lastReadMessageId"]
		if pid == pidStr(f.pid) {
			// Alice (the caller) sees her own pointer.
			if lr == "" || lr == nil {
				t.Errorf("alice's own lastReadMessageId suppressed: %+v", row)
			}
		} else {
			// Bob (other) must be masked when receipts are off.
			if lr != "" && lr != nil {
				t.Errorf("other member %v lastReadMessageId not suppressed: %+v", pid, row)
			}
		}
	}
}

// TestMembership_Get_DM_AlwaysExposesReceipts verifies REQ-CHAT-31:
// DMs ignore the readReceiptsEnabled flag and always expose
// lastReadMessageId for both members.
func TestMembership_Get_DM_AlwaysExposesReceipts(t *testing.T) {
	f := setupFixture(t)
	cid, _ := createDM(t, f)
	msg := sendMessage(t, f, cid, "hi")
	mid := msg["id"].(string)
	var msgID store.ChatMessageID
	{
		var n uint64
		for _, c := range mid {
			if c < '0' || c > '9' {
				break
			}
			n = n*10 + uint64(c-'0')
		}
		msgID = store.ChatMessageID(n)
	}
	ctx := context.Background()
	rows, err := f.srv.Store.Meta().ListChatMembershipsByConversation(ctx, parseConvID(cid))
	if err != nil {
		t.Fatalf("ListChatMembershipsByConversation: %v", err)
	}
	var aliceMID, bobMID store.MembershipID
	for _, m := range rows {
		switch m.PrincipalID {
		case f.pid:
			aliceMID = m.ID
		case f.otherPID:
			bobMID = m.ID
		}
	}
	if aliceMID == 0 || bobMID == 0 {
		t.Fatalf("memberships missing")
	}
	if err := f.srv.Store.Meta().SetLastRead(ctx, f.otherPID, parseConvID(cid), msgID); err != nil {
		t.Fatalf("SetLastRead bob: %v", err)
	}

	_, raw := f.invoke(t, "Membership/get", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"ids":       []string{idStr(aliceMID), idStr(bobMID)},
	})
	var getResp struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(raw, &getResp); err != nil {
		t.Fatalf("unmarshal Membership/get: %v: %s", err, raw)
	}
	for _, row := range getResp.List {
		pid := row["principalId"]
		if pid != pidStr(f.otherPID) {
			continue
		}
		lr := row["lastReadMessageId"]
		if lr == "" || lr == nil {
			t.Errorf("DM bob's lastReadMessageId suppressed (DMs must always expose): %+v", row)
		}
	}
}

// -- REQ-CHAT-20 ------------------------------------------------------

// TestMessage_Set_Update_EditWindow_DefaultAllowedAt899s confirms that
// the 15-minute default edit window admits a body update at t+899s.
func TestMessage_Set_Update_EditWindow_DefaultAllowedAt899s(t *testing.T) {
	f := setupFixture(t)
	cid, _ := createSpace(t, f, "team")
	msg := sendMessage(t, f, cid, "original")
	mid := msg["id"].(string)
	// Default window is 900 seconds; 899 is in-bounds.
	f.srv.Advance(899 * time.Second)
	if !attemptEdit(t, f, mid, "edited") {
		t.Errorf("edit at t+899s should succeed under 15-min default window")
	}
}

// TestMessage_Set_Update_EditWindow_DefaultRejectedAt901s confirms the
// boundary: at t+901s the default 15-minute window is closed and the
// update returns a forbidden set-error.
func TestMessage_Set_Update_EditWindow_DefaultRejectedAt901s(t *testing.T) {
	f := setupFixture(t)
	cid, _ := createSpace(t, f, "team")
	msg := sendMessage(t, f, cid, "original")
	mid := msg["id"].(string)
	f.srv.Advance(901 * time.Second)
	if attemptEdit(t, f, mid, "too late") {
		t.Errorf("edit at t+901s should be rejected (window closed)")
	}
}

// TestMessage_Set_Update_EditWindow_ZeroAlwaysAllowed verifies that
// EditWindowSeconds = 0 means "no time limit" — a several-day-old
// message can still be edited.
func TestMessage_Set_Update_EditWindow_ZeroAlwaysAllowed(t *testing.T) {
	f := setupFixture(t)
	// Override the account default to 0 (no limit).
	if err := f.srv.Store.Meta().UpsertChatAccountSettings(context.Background(),
		store.ChatAccountSettings{
			PrincipalID:              f.pid,
			DefaultRetentionSeconds:  store.ChatDefaultRetentionSeconds,
			DefaultEditWindowSeconds: 0,
		}); err != nil {
		t.Fatalf("UpsertChatAccountSettings: %v", err)
	}
	cid, _ := createSpace(t, f, "team")
	msg := sendMessage(t, f, cid, "original")
	mid := msg["id"].(string)
	f.srv.Advance(72 * time.Hour) // many hours later
	if !attemptEdit(t, f, mid, "still ok") {
		t.Errorf("edit with window=0 should always succeed")
	}
}

// TestMessage_Set_Update_EditWindow_PerConversationOverride confirms
// the per-conversation override (60s) takes precedence over the
// account default (900s): a 90s-old message is in-bounds for the
// account default but rejected by the conversation override.
func TestMessage_Set_Update_EditWindow_PerConversationOverride(t *testing.T) {
	f := setupFixture(t)
	cid, _ := createSpace(t, f, "team")

	// Set a 60-second override on the conversation.
	_, raw := f.invoke(t, "Conversation/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"update": map[string]any{
			cid: map[string]any{"editWindowSeconds": 60},
		},
	})
	var setResp struct {
		Updated    map[string]any            `json:"updated"`
		NotUpdated map[string]map[string]any `json:"notUpdated"`
	}
	if err := json.Unmarshal(raw, &setResp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if _, ok := setResp.Updated[cid]; !ok {
		t.Fatalf("editWindowSeconds override failed: %+v / %+v",
			setResp.Updated, setResp.NotUpdated)
	}
	msg := sendMessage(t, f, cid, "original")
	mid := msg["id"].(string)
	// 90s elapsed: still inside the 900s account default but past the
	// 60s conversation override.
	f.srv.Advance(90 * time.Second)
	if attemptEdit(t, f, mid, "too late") {
		t.Errorf("edit at t+90s should be rejected by 60s override (account default 900s would allow)")
	}
}

// attemptEdit posts a Message/set { update: { id: { body } } } and
// returns true iff the update succeeded.
func attemptEdit(t *testing.T, f *fixture, mid, newText string) bool {
	t.Helper()
	_, raw := f.invoke(t, "Message/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"update": map[string]any{
			mid: map[string]any{
				"body": map[string]any{"text": newText, "format": "text"},
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
	_, ok := resp.Updated[mid]
	return ok
}

// -- Capability descriptor --------------------------------------------

// TestCapabilityDescriptor_AdvertisesChatPolicyDefaults confirms the
// Wave 2.9.6 additions to the per-account capability descriptor:
// defaultRetentionSeconds and defaultEditWindowSeconds.
func TestCapabilityDescriptor_AdvertisesChatPolicyDefaults(t *testing.T) {
	f := setupFixture(t)
	desc := fetchChatCapability(t, f)
	for _, key := range []string{
		"defaultRetentionSeconds",
		"defaultEditWindowSeconds",
	} {
		if _, ok := desc[key]; !ok {
			t.Errorf("descriptor missing %q: %+v", key, desc)
		}
	}
}

// fetchChatCapability returns the chat per-account capability map by
// reading the .well-known JMAP session document.
func fetchChatCapability(t *testing.T, f *fixture) map[string]any {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, f.baseURL+"/.well-known/jmap", nil)
	if err != nil {
		t.Fatalf("new session request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+f.apiKey)
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	defer resp.Body.Close()
	var session struct {
		Accounts map[string]struct {
			AccountCapabilities map[string]any `json:"accountCapabilities"`
		} `json:"accounts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	for _, acct := range session.Accounts {
		if desc, ok := acct.AccountCapabilities["https://herold.dev/jmap/chat"].(map[string]any); ok {
			return desc
		}
	}
	t.Fatalf("chat capability not found")
	return nil
}
