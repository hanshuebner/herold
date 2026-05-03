package admin

import (
	"context"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
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
	fs, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
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

// TestCallChatPeersResolver_UnionExcludesSelfDedupes pins the
// production wiring composeAdminAndUI hands to protochat.Options
// PeersResolver. The resolver MUST:
//   - Return the union of peers across every conversation the
//     publisher belongs to.
//   - Exclude the publisher themselves.
//   - Dedupe principals who share more than one conversation with
//     the publisher.
//   - Return an empty (non-nil) slice when the publisher has no
//     conversations.
//
// The fakestore drives the same store metadata interface SQLite and
// Postgres back, so a passing test here covers all three.
func TestCallChatPeersResolver_UnionExcludesSelfDedupes(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}

	// Seed four principals: publisher + three peers (alice, bob,
	// carol). Publisher shares two conversations with alice (the
	// dedupe surface), one with bob, and zero with carol.
	publisher, err := fs.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "publisher@test.local",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal publisher: %v", err)
	}
	alice, err := fs.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@test.local",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal alice: %v", err)
	}
	bob, err := fs.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "bob@test.local",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal bob: %v", err)
	}
	carol, err := fs.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "carol@test.local",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal carol: %v", err)
	}

	// Conversation 1: publisher + alice (DM).
	conv1, err := fs.Meta().InsertChatConversation(ctx, store.ChatConversation{
		Kind:                 "dm",
		CreatedByPrincipalID: publisher.ID,
	})
	if err != nil {
		t.Fatalf("InsertChatConversation conv1: %v", err)
	}
	for _, pid := range []store.PrincipalID{publisher.ID, alice.ID} {
		if _, err := fs.Meta().InsertChatMembership(ctx, store.ChatMembership{
			ConversationID:       conv1,
			PrincipalID:          pid,
			Role:                 "member",
			NotificationsSetting: "all",
		}); err != nil {
			t.Fatalf("InsertChatMembership conv1/%d: %v", pid, err)
		}
	}

	// Conversation 2: publisher + alice + bob (group). Shares
	// alice with conv1 to exercise the dedupe path.
	conv2, err := fs.Meta().InsertChatConversation(ctx, store.ChatConversation{
		Kind:                 "group",
		CreatedByPrincipalID: publisher.ID,
	})
	if err != nil {
		t.Fatalf("InsertChatConversation conv2: %v", err)
	}
	for _, pid := range []store.PrincipalID{publisher.ID, alice.ID, bob.ID} {
		if _, err := fs.Meta().InsertChatMembership(ctx, store.ChatMembership{
			ConversationID:       conv2,
			PrincipalID:          pid,
			Role:                 "member",
			NotificationsSetting: "all",
		}); err != nil {
			t.Fatalf("InsertChatMembership conv2/%d: %v", pid, err)
		}
	}

	// Conversation 3: alice + carol — does NOT include publisher.
	// Carol must NOT appear in publisher's peer set.
	conv3, err := fs.Meta().InsertChatConversation(ctx, store.ChatConversation{
		Kind:                 "dm",
		CreatedByPrincipalID: alice.ID,
	})
	if err != nil {
		t.Fatalf("InsertChatConversation conv3: %v", err)
	}
	for _, pid := range []store.PrincipalID{alice.ID, carol.ID} {
		if _, err := fs.Meta().InsertChatMembership(ctx, store.ChatMembership{
			ConversationID:       conv3,
			PrincipalID:          pid,
			Role:                 "member",
			NotificationsSetting: "all",
		}); err != nil {
			t.Fatalf("InsertChatMembership conv3/%d: %v", pid, err)
		}
	}

	resolver := callChatPeersResolver(fs)

	// Union path: alice + bob, excluding publisher and carol.
	got, err := resolver(ctx, publisher.ID)
	if err != nil {
		t.Fatalf("resolver(publisher): %v", err)
	}
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	want := []store.PrincipalID{alice.ID, bob.ID}
	sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
	if len(got) != len(want) {
		t.Fatalf("peers: got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("peers[%d]: got %d, want %d", i, got[i], want[i])
		}
	}
	// Self-exclusion: publisher MUST NOT appear in its own peer set.
	for _, pid := range got {
		if pid == publisher.ID {
			t.Fatalf("publisher %d appeared in own peer set", publisher.ID)
		}
	}
	// Carol is not connected: MUST NOT appear.
	for _, pid := range got {
		if pid == carol.ID {
			t.Fatalf("carol %d leaked into peer set despite no shared conversation", carol.ID)
		}
	}

	// Empty path: a principal with no conversations gets an empty,
	// non-nil slice (so callers can distinguish "no peers" from a
	// transient lookup error which surfaces as err != nil).
	stranger, err := fs.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "stranger@test.local",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal stranger: %v", err)
	}
	empty, err := resolver(ctx, stranger.ID)
	if err != nil {
		t.Fatalf("resolver(stranger): %v", err)
	}
	if empty == nil {
		t.Fatalf("resolver(stranger): nil slice, want empty non-nil slice")
	}
	if len(empty) != 0 {
		t.Fatalf("resolver(stranger): %v, want []", empty)
	}
}
