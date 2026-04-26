package email_test

// JMAP integration tests for Email.reactions (REQ-PROTO-100..103).
// These tests drive the full HTTP path using the testharness, so they
// exercise handler wiring, store, and wire format in one pass.

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// reactionsFromRaw extracts the "reactions" field from a raw Email/get
// list entry.  Returns nil when the field is absent.
func reactionsFromRaw(raw map[string]any) map[string][]string {
	v, ok := raw["reactions"]
	if !ok || v == nil {
		return nil
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string][]string, len(obj))
	for emoji, pidList := range obj {
		arr, ok := pidList.([]any)
		if !ok {
			continue
		}
		pids := make([]string, 0, len(arr))
		for _, p := range arr {
			if s, ok := p.(string); ok {
				pids = append(pids, s)
			}
		}
		out[emoji] = pids
	}
	return out
}

// insertMessageWithMsgID stores a message with a specific Message-ID header
// value so inbound reaction tests can look it up.
func (f *fixture) insertMessageWithMsgID(t *testing.T, body, subject, from, to, msgID string) store.Message {
	t.Helper()
	ref := f.putBlob(t, body)
	now := f.srv.Clock.Now()
	msg := store.Message{
		MailboxID:    f.inbox.ID,
		InternalDate: now,
		ReceivedAt:   now,
		Size:         ref.Size,
		Blob:         ref,
		Envelope: store.Envelope{
			Subject:   subject,
			From:      from,
			To:        to,
			Date:      now,
			MessageID: msgID,
		},
	}
	uid, modseq, err := f.srv.Store.Meta().InsertMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	msg.UID = uid
	msg.ModSeq = modseq
	msg.ID = mostRecentMessageID(t, f)
	return msg
}

// TestEmail_Reactions_GetEmpty verifies that Email/get returns an absent
// "reactions" field when no reactions exist (sparse; omitempty in the DTO).
func TestEmail_Reactions_GetEmpty(t *testing.T) {
	f := setupFixture(t)
	m := f.insertMessage(t, "From: a@example.test\r\nTo: b@example.test\r\nSubject: no reactions\r\n\r\nbody",
		"no reactions", "a@example.test", "b@example.test", nil, "")

	_, raw := f.invoke(t, "Email/get", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"ids":       []string{fmt.Sprintf("%d", m.ID)},
		"properties": []string{"id", "reactions"},
	})
	var resp struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(resp.List) != 1 {
		t.Fatalf("want 1 message, got %d: %s", len(resp.List), raw)
	}
	// reactions should be absent when empty.
	if _, present := resp.List[0]["reactions"]; present {
		t.Errorf("reactions should be absent (omitempty) when no reactions, got: %v", resp.List[0]["reactions"])
	}
}

// TestEmail_Reactions_SetAddThenGet verifies that Email/set with a
// reactions/<emoji>/<pid>: true patch stores the reaction and that a
// subsequent Email/get returns it correctly.
func TestEmail_Reactions_SetAddThenGet(t *testing.T) {
	f := setupFixture(t)
	m := f.insertMessage(t, "From: a@example.test\r\nTo: b@example.test\r\nSubject: subj\r\n\r\nbody",
		"subj", "a@example.test", "b@example.test", nil, "")

	pidStr := fmt.Sprintf("%d", f.pid)
	msgIDStr := fmt.Sprintf("%d", m.ID)
	patchKey := "reactions/thumbs-up/" + pidStr

	_, setRaw := f.invoke(t, "Email/set", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"update": map[string]any{
			msgIDStr: map[string]any{
				patchKey: true,
			},
		},
	})
	var setResp struct {
		Updated    map[string]any `json:"updated"`
		NotUpdated map[string]any `json:"notUpdated"`
		NewState   string         `json:"newState"`
		OldState   string         `json:"oldState"`
	}
	if err := json.Unmarshal(setRaw, &setResp); err != nil {
		t.Fatalf("unmarshal set: %v: %s", err, setRaw)
	}
	if _, ok := setResp.Updated[msgIDStr]; !ok {
		t.Fatalf("update not in Updated: notUpdated=%v raw=%s", setResp.NotUpdated, setRaw)
	}
	// State must advance on a reactions patch.
	if setResp.NewState == setResp.OldState {
		t.Errorf("state did not advance: old=%q new=%q", setResp.OldState, setResp.NewState)
	}

	// Now get the email and verify reactions appear.
	_, getRaw := f.invoke(t, "Email/get", map[string]any{
		"accountId":  protojmap.AccountIDForPrincipal(f.pid),
		"ids":        []string{msgIDStr},
		"properties": []string{"id", "reactions"},
	})
	var getResp struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(getRaw, &getResp); err != nil {
		t.Fatalf("unmarshal get: %v: %s", err, getRaw)
	}
	if len(getResp.List) != 1 {
		t.Fatalf("want 1 message, got %d: %s", len(getResp.List), getRaw)
	}
	reactions := reactionsFromRaw(getResp.List[0])
	if reactions == nil {
		t.Fatalf("reactions field missing after add: %s", getRaw)
	}
	pids, ok := reactions["thumbs-up"]
	if !ok {
		t.Fatalf("thumbs-up emoji missing in reactions: %v", reactions)
	}
	if len(pids) != 1 || pids[0] != pidStr {
		t.Errorf("thumbs-up principals = %v, want [%s]", pids, pidStr)
	}
}

// TestEmail_Reactions_SetRemove verifies that setting
// reactions/<emoji>/<pid>: null removes the reaction.
func TestEmail_Reactions_SetRemove(t *testing.T) {
	f := setupFixture(t)
	m := f.insertMessage(t, "From: a@example.test\r\nTo: b@example.test\r\nSubject: subj\r\n\r\nbody",
		"subj", "a@example.test", "b@example.test", nil, "")

	pidStr := fmt.Sprintf("%d", f.pid)
	msgIDStr := fmt.Sprintf("%d", m.ID)

	// Add the reaction first via the store directly (faster than a second HTTP round-trip).
	if err := f.srv.Store.Meta().AddEmailReaction(context.Background(), m.ID, "heart", f.pid, f.srv.Clock.Now()); err != nil {
		t.Fatalf("AddEmailReaction: %v", err)
	}

	// Remove via Email/set patch.
	patchKey := "reactions/heart/" + pidStr
	_, setRaw := f.invoke(t, "Email/set", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"update": map[string]any{
			msgIDStr: map[string]any{
				patchKey: nil,
			},
		},
	})
	var setResp struct {
		Updated    map[string]any `json:"updated"`
		NotUpdated map[string]any `json:"notUpdated"`
	}
	if err := json.Unmarshal(setRaw, &setResp); err != nil {
		t.Fatalf("unmarshal set: %v: %s", err, setRaw)
	}
	if _, ok := setResp.Updated[msgIDStr]; !ok {
		t.Fatalf("expected update in Updated: notUpdated=%v raw=%s", setResp.NotUpdated, setRaw)
	}

	// Verify reaction gone.
	rxns, err := f.srv.Store.Meta().ListEmailReactions(context.Background(), m.ID)
	if err != nil {
		t.Fatalf("ListEmailReactions: %v", err)
	}
	if len(rxns) != 0 {
		t.Errorf("reaction not removed: %v", rxns)
	}
}

// TestEmail_Reactions_SetForbiddenOtherPrincipal verifies that patching
// another principal's reaction returns a "forbidden" error.
func TestEmail_Reactions_SetForbiddenOtherPrincipal(t *testing.T) {
	f := setupFixture(t)
	m := f.insertMessage(t, "From: a@example.test\r\nTo: b@example.test\r\nSubject: subj\r\n\r\nbody",
		"subj", "a@example.test", "b@example.test", nil, "")

	msgIDStr := fmt.Sprintf("%d", m.ID)
	// Use a different principal ID than the authenticated one.
	otherPID := fmt.Sprintf("%d", f.pid+9999)
	patchKey := "reactions/fire/" + otherPID

	_, setRaw := f.invoke(t, "Email/set", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"update": map[string]any{
			msgIDStr: map[string]any{
				patchKey: true,
			},
		},
	})
	var setResp struct {
		NotUpdated map[string]map[string]any `json:"notUpdated"`
	}
	if err := json.Unmarshal(setRaw, &setResp); err != nil {
		t.Fatalf("unmarshal set: %v: %s", err, setRaw)
	}
	entry, ok := setResp.NotUpdated[msgIDStr]
	if !ok {
		t.Fatalf("expected notUpdated entry for %s: raw=%s", msgIDStr, setRaw)
	}
	if entry["type"] != "forbidden" {
		t.Errorf("want type=forbidden, got %v", entry["type"])
	}
}

// TestEmail_Reactions_MultipleEmoji verifies that multiple emoji reactions
// from the same principal are stored and returned correctly.
func TestEmail_Reactions_MultipleEmoji(t *testing.T) {
	f := setupFixture(t)
	m := f.insertMessage(t, "From: a@example.test\r\nTo: b@example.test\r\nSubject: multi\r\n\r\nbody",
		"multi", "a@example.test", "b@example.test", nil, "")

	pidStr := fmt.Sprintf("%d", f.pid)
	msgIDStr := fmt.Sprintf("%d", m.ID)

	_, setRaw := f.invoke(t, "Email/set", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"update": map[string]any{
			msgIDStr: map[string]any{
				"reactions/thumbsup/" + pidStr: true,
				"reactions/fire/" + pidStr:     true,
			},
		},
	})
	var setResp struct {
		Updated    map[string]any `json:"updated"`
		NotUpdated map[string]any `json:"notUpdated"`
	}
	if err := json.Unmarshal(setRaw, &setResp); err != nil {
		t.Fatalf("unmarshal set: %v: %s", err, setRaw)
	}
	if _, ok := setResp.Updated[msgIDStr]; !ok {
		t.Fatalf("update not in Updated: notUpdated=%v raw=%s", setResp.NotUpdated, setRaw)
	}

	// Verify via store directly.
	rxns, err := f.srv.Store.Meta().ListEmailReactions(context.Background(), m.ID)
	if err != nil {
		t.Fatalf("ListEmailReactions: %v", err)
	}
	if _, ok := rxns["thumbsup"][f.pid]; !ok {
		t.Errorf("thumbsup missing: %v", rxns)
	}
	if _, ok := rxns["fire"][f.pid]; !ok {
		t.Errorf("fire missing: %v", rxns)
	}
}

// TestEmail_Reactions_StateAdvancesOnReactionOnly verifies that a
// purely-reaction patch advances state (no other property changes).
func TestEmail_Reactions_StateAdvancesOnReactionOnly(t *testing.T) {
	f := setupFixture(t)
	m := f.insertMessage(t, "From: a@example.test\r\nTo: b@example.test\r\nSubject: s\r\n\r\nb",
		"s", "a@example.test", "b@example.test", nil, "")

	pidStr := fmt.Sprintf("%d", f.pid)
	msgIDStr := fmt.Sprintf("%d", m.ID)

	// Capture state before.
	_, getRaw1 := f.invoke(t, "Email/get", map[string]any{
		"accountId":  protojmap.AccountIDForPrincipal(f.pid),
		"ids":        []string{msgIDStr},
		"properties": []string{"id"},
	})
	var getResp1 struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(getRaw1, &getResp1); err != nil {
		t.Fatalf("unmarshal get1: %v", err)
	}
	state1 := getResp1.State

	// Apply purely-reaction patch.
	_, setRaw := f.invoke(t, "Email/set", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"update": map[string]any{
			msgIDStr: map[string]any{
				"reactions/wave/" + pidStr: true,
			},
		},
	})
	var setResp struct {
		NewState string `json:"newState"`
		OldState string `json:"oldState"`
	}
	if err := json.Unmarshal(setRaw, &setResp); err != nil {
		t.Fatalf("unmarshal set: %v", err)
	}
	if setResp.NewState == setResp.OldState {
		t.Errorf("state did not advance on pure-reaction patch: old=%q new=%q", setResp.OldState, setResp.NewState)
	}
	// New state must differ from the state we captured before.
	if setResp.NewState == state1 {
		t.Errorf("JMAP state post-reaction equals pre-reaction state: %q", state1)
	}
}

// TestEmail_Reactions_SetAddIdempotent verifies that adding the same
// reaction twice does not produce an error (store ON CONFLICT DO NOTHING).
func TestEmail_Reactions_SetAddIdempotent(t *testing.T) {
	f := setupFixture(t)
	m := f.insertMessage(t, "From: a@example.test\r\nTo: b@example.test\r\nSubject: s\r\n\r\nb",
		"s", "a@example.test", "b@example.test", nil, "")

	pidStr := fmt.Sprintf("%d", f.pid)
	msgIDStr := fmt.Sprintf("%d", m.ID)
	patchKey := "reactions/ok/" + pidStr

	for i := range 2 {
		_, setRaw := f.invoke(t, "Email/set", map[string]any{
			"accountId": protojmap.AccountIDForPrincipal(f.pid),
			"update": map[string]any{
				msgIDStr: map[string]any{
					patchKey: true,
				},
			},
		})
		var setResp struct {
			Updated    map[string]any `json:"updated"`
			NotUpdated map[string]any `json:"notUpdated"`
		}
		if err := json.Unmarshal(setRaw, &setResp); err != nil {
			t.Fatalf("iter %d unmarshal: %v: %s", i, err, setRaw)
		}
		if _, ok := setResp.Updated[msgIDStr]; !ok {
			t.Fatalf("iter %d: update not in Updated: %v raw=%s", i, setResp.NotUpdated, setRaw)
		}
	}

	// Exactly one reaction row.
	rxns, err := f.srv.Store.Meta().ListEmailReactions(context.Background(), m.ID)
	if err != nil {
		t.Fatalf("ListEmailReactions: %v", err)
	}
	if len(rxns["ok"]) != 1 {
		t.Errorf("expected 1 reaction for ok, got %d: %v", len(rxns["ok"]), rxns)
	}
}

// TestEmail_Reactions_BatchGetMultipleMessages verifies that Email/get
// on multiple IDs loads reactions for all of them in one batch without
// N+1 queries (observable as correctness: all reactions present in response).
func TestEmail_Reactions_BatchGetMultipleMessages(t *testing.T) {
	f := setupFixture(t)
	m1 := f.insertMessage(t, "From: a@example.test\r\nTo: b@example.test\r\nSubject: m1\r\n\r\nb",
		"m1", "a@example.test", "b@example.test", nil, "")
	m2 := f.insertMessage(t, "From: a@example.test\r\nTo: b@example.test\r\nSubject: m2\r\n\r\nb",
		"m2", "a@example.test", "b@example.test", nil, "")
	m3 := f.insertMessage(t, "From: a@example.test\r\nTo: b@example.test\r\nSubject: m3\r\n\r\nb",
		"m3", "a@example.test", "b@example.test", nil, "")

	ctx := context.Background()
	now := f.srv.Clock.Now()
	if err := f.srv.Store.Meta().AddEmailReaction(ctx, m1.ID, "up", f.pid, now); err != nil {
		t.Fatalf("add m1 reaction: %v", err)
	}
	if err := f.srv.Store.Meta().AddEmailReaction(ctx, m3.ID, "down", f.pid, now); err != nil {
		t.Fatalf("add m3 reaction: %v", err)
	}
	// m2 has no reactions.

	_, getRaw := f.invoke(t, "Email/get", map[string]any{
		"accountId":  protojmap.AccountIDForPrincipal(f.pid),
		"ids":        []string{fmt.Sprintf("%d", m1.ID), fmt.Sprintf("%d", m2.ID), fmt.Sprintf("%d", m3.ID)},
		"properties": []string{"id", "reactions"},
	})
	var getResp struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(getRaw, &getResp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, getRaw)
	}
	if len(getResp.List) != 3 {
		t.Fatalf("want 3 messages, got %d: %s", len(getResp.List), getRaw)
	}
	byID := make(map[string]map[string]any, 3)
	for _, item := range getResp.List {
		id, _ := item["id"].(string)
		byID[id] = item
	}

	m1Reactions := reactionsFromRaw(byID[fmt.Sprintf("%d", m1.ID)])
	if m1Reactions == nil || len(m1Reactions["up"]) != 1 {
		t.Errorf("m1: expected up reaction, got %v", m1Reactions)
	}
	m3Reactions := reactionsFromRaw(byID[fmt.Sprintf("%d", m3.ID)])
	if m3Reactions == nil || len(m3Reactions["down"]) != 1 {
		t.Errorf("m3: expected down reaction, got %v", m3Reactions)
	}
	// m2 should have no reactions field.
	if _, ok := byID[fmt.Sprintf("%d", m2.ID)]["reactions"]; ok {
		t.Errorf("m2: reactions should be absent, got: %v", byID[fmt.Sprintf("%d", m2.ID)]["reactions"])
	}
}
