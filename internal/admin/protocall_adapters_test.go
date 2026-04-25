package admin

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
)

// TestCallSysmsgsAdapter_BumpsLastMessageAtAndMessageCount pins
// REQ-CALL-32: a missed-call sysmsg is routed through the same
// InsertChatMessage path as a user message and therefore
// (a) bumps the conversation's LastMessageAt and MessageCount and
// (b) increments last_message_id past Membership.LastReadMessageID
// so the conversation's unread count picks up the row. The fakestore
// uses the same denormalised bump semantics the SQLite + Postgres
// backends do, so a passing test here protects all three.
func TestCallSysmsgsAdapter_BumpsLastMessageAtAndMessageCount(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := fakestore.New(fakestore.Options{Clock: clk, BlobDir: t.TempDir()})
	if err != nil {
		t.Fatalf("fakestore: %v", err)
	}
	// Seed two principals + a DM conversation + memberships.
	caller, err := fs.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "caller@test.local",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal caller: %v", err)
	}
	recipient, err := fs.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "recipient@test.local",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal recipient: %v", err)
	}
	convID, err := fs.Meta().InsertChatConversation(ctx, store.ChatConversation{
		Kind:                 "dm",
		CreatedByPrincipalID: caller.ID,
	})
	if err != nil {
		t.Fatalf("InsertChatConversation: %v", err)
	}
	if _, err := fs.Meta().InsertChatMembership(ctx, store.ChatMembership{
		ConversationID:       convID,
		PrincipalID:          caller.ID,
		Role:                 "member",
		NotificationsSetting: "all",
	}); err != nil {
		t.Fatalf("InsertChatMembership caller: %v", err)
	}
	if _, err := fs.Meta().InsertChatMembership(ctx, store.ChatMembership{
		ConversationID:       convID,
		PrincipalID:          recipient.ID,
		Role:                 "member",
		NotificationsSetting: "all",
	}); err != nil {
		t.Fatalf("InsertChatMembership recipient: %v", err)
	}

	// Snapshot the conversation pre-sysmsg.
	pre, err := fs.Meta().GetChatConversation(ctx, convID)
	if err != nil {
		t.Fatalf("GetChatConversation: %v", err)
	}
	if pre.MessageCount != 0 {
		t.Fatalf("pre MessageCount = %d, want 0", pre.MessageCount)
	}
	if pre.LastMessageAt != nil {
		t.Fatalf("pre LastMessageAt = %v, want nil", pre.LastMessageAt)
	}

	// Drive the adapter directly with a missed-call payload (the
	// same shape protocall.handleOffer's recipient-busy branch
	// produces).
	clk.Advance(15 * time.Second)
	adapter := newCallSysmsgsAdapter(fs)
	convStr := strconv.FormatUint(uint64(convID), 10)
	missedPayload := []byte(`{"kind":"call.ended","call_id":"abc","caller_principal_id":1,"disposition":"missed","hangup_reason":"busy"}`)
	if err := adapter.InsertChatSystemMessage(ctx, convStr, caller.ID, missedPayload); err != nil {
		t.Fatalf("InsertChatSystemMessage: %v", err)
	}

	post, err := fs.Meta().GetChatConversation(ctx, convID)
	if err != nil {
		t.Fatalf("GetChatConversation post: %v", err)
	}
	if post.MessageCount != 1 {
		t.Fatalf("post MessageCount = %d, want 1", post.MessageCount)
	}
	if post.LastMessageAt == nil {
		t.Fatalf("post LastMessageAt = nil, want non-nil bump")
	}
	if !post.LastMessageAt.Equal(clk.Now()) {
		t.Fatalf("post LastMessageAt = %v, want %v", post.LastMessageAt, clk.Now())
	}

	// Unread side: the recipient's membership has LastReadMessageID
	// nil, and the freshly inserted message has a non-zero ID. List
	// messages newer than recipient's read pointer to confirm the
	// missed-call row is in the unread set.
	mb, err := fs.Meta().GetChatMembership(ctx, convID, recipient.ID)
	if err != nil {
		t.Fatalf("GetChatMembership: %v", err)
	}
	if mb.LastReadMessageID != nil {
		t.Fatalf("recipient already had LastReadMessageID set; want nil")
	}
	msgs, err := fs.Meta().ListChatMessages(ctx, store.ChatMessageFilter{
		ConversationID: &convID,
	})
	if err != nil {
		t.Fatalf("ListChatMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want 1", len(msgs))
	}
	if !msgs[0].IsSystem {
		t.Fatalf("inserted message is not flagged IsSystem")
	}
	if msgs[0].SenderPrincipalID == nil || *msgs[0].SenderPrincipalID != caller.ID {
		t.Fatalf("sender = %v, want %d", msgs[0].SenderPrincipalID, caller.ID)
	}
}
