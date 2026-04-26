package chatretention_test

import (
	"context"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/chatretention"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
)

// fixture builds a fakestore with one principal, one Space conversation
// and the worker under test wired to a deterministic FakeClock.
type fixture struct {
	store *fakestore.Store
	clk   *clock.FakeClock
	pid   store.PrincipalID
	cid   store.ConversationID
}

func newFixture(t *testing.T, retentionSeconds *int64) *fixture {
	t.Helper()
	clk := clock.NewFake(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	s, err := fakestore.New(fakestore.Options{
		Clock:   clk,
		BlobDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("fakestore.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	p, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	cid, err := s.Meta().InsertChatConversation(ctx, store.ChatConversation{
		Kind:                 store.ChatConversationKindSpace,
		Name:                 "team",
		CreatedByPrincipalID: p.ID,
		RetentionSeconds:     retentionSeconds,
	})
	if err != nil {
		t.Fatalf("InsertChatConversation: %v", err)
	}
	if _, err := s.Meta().InsertChatMembership(ctx, store.ChatMembership{
		ConversationID: cid,
		PrincipalID:    p.ID,
		Role:           store.ChatRoleOwner,
	}); err != nil {
		t.Fatalf("InsertChatMembership: %v", err)
	}
	return &fixture{store: s, clk: clk, pid: p.ID, cid: cid}
}

// insertMsg writes one chat message and returns its id. The message's
// CreatedAt comes from the FakeClock so callers control the age.
func (f *fixture) insertMsg(t *testing.T, body string) store.ChatMessageID {
	t.Helper()
	pid := f.pid
	id, err := f.store.Meta().InsertChatMessage(context.Background(), store.ChatMessage{
		ConversationID:    f.cid,
		SenderPrincipalID: &pid,
		BodyText:          body,
		BodyFormat:        store.ChatBodyFormatText,
	})
	if err != nil {
		t.Fatalf("InsertChatMessage: %v", err)
	}
	return id
}

func TestWorker_Defaults(t *testing.T) {
	w := chatretention.NewWorker(chatretention.Options{
		Store: newFixture(t, nil).store,
	})
	if w.SweepInterval() != chatretention.DefaultSweepInterval {
		t.Errorf("SweepInterval default = %v, want %v",
			w.SweepInterval(), chatretention.DefaultSweepInterval)
	}
	if w.BatchSize() != chatretention.DefaultBatchSize {
		t.Errorf("BatchSize default = %d, want %d",
			w.BatchSize(), chatretention.DefaultBatchSize)
	}
}

// TestWorker_PerConversationOverride exercises REQ-CHAT-92 with a
// per-Space retention override of 1 hour. Five messages are inserted
// at t0; the clock advances 2 hours; one Tick must hard-delete all
// five, recompute the conversation's MessageCount to 0, and clear
// LastMessageAt.
func TestWorker_PerConversationOverride(t *testing.T) {
	retention := int64(3600) // 1 hour
	f := newFixture(t, &retention)
	ids := make([]store.ChatMessageID, 0, 5)
	for i := 0; i < 5; i++ {
		// Stagger CreatedAt by one second so the LastMessageAt fallback
		// path in HardDeleteChatMessage exercises the "drop the most
		// recent live row, fall back to the next" branch.
		f.clk.Advance(time.Second)
		ids = append(ids, f.insertMsg(t, "hello"))
	}
	// At this point the conversation has 5 messages. Advance the clock
	// 2 hours past the most recent insert; every message is now older
	// than the 1-hour retention window.
	f.clk.Advance(2 * time.Hour)

	w := chatretention.NewWorker(chatretention.Options{
		Store:         f.store,
		Clock:         f.clk,
		SweepInterval: 30 * time.Second,
		BatchSize:     1000,
	})
	deleted, err := w.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if deleted != 5 {
		t.Errorf("Tick deleted = %d, want 5", deleted)
	}
	if got := w.Deleted(); got != 5 {
		t.Errorf("Deleted() = %d, want 5", got)
	}
	// Every message id is now gone.
	for _, id := range ids {
		_, err := f.store.Meta().GetChatMessage(context.Background(), id)
		if err == nil {
			t.Errorf("GetChatMessage(%d): unexpected success after sweep", id)
		}
	}
	// Conversation denormalised counters are recomputed.
	conv, err := f.store.Meta().GetChatConversation(context.Background(), f.cid)
	if err != nil {
		t.Fatalf("GetChatConversation: %v", err)
	}
	if conv.MessageCount != 0 {
		t.Errorf("MessageCount = %d, want 0", conv.MessageCount)
	}
	if conv.LastMessageAt != nil {
		t.Errorf("LastMessageAt = %v, want nil", conv.LastMessageAt)
	}
}

// TestWorker_LastMessageAtRecomputesToSurvivor inserts three messages,
// retains a 1-hour window, then advances time so only the OLDEST two
// fall outside the window. The youngest survives. After Tick the
// conversation's LastMessageAt must point to the survivor's CreatedAt
// and MessageCount must be 1.
func TestWorker_LastMessageAtRecomputesToSurvivor(t *testing.T) {
	retention := int64(3600) // 1 hour
	f := newFixture(t, &retention)
	// Insert two old messages at t0..t0+1s.
	f.clk.Advance(time.Second)
	_ = f.insertMsg(t, "old-1")
	f.clk.Advance(time.Second)
	_ = f.insertMsg(t, "old-2")
	// Advance an hour so old-1 / old-2 fall outside the window when we
	// later sweep.
	f.clk.Advance(time.Hour)
	// Insert the survivor "fresh" 1 second after the cutoff.
	f.clk.Advance(time.Second)
	survivorID := f.insertMsg(t, "fresh")
	survivor, err := f.store.Meta().GetChatMessage(context.Background(), survivorID)
	if err != nil {
		t.Fatalf("GetChatMessage survivor: %v", err)
	}
	// Sweep against current clock.
	w := chatretention.NewWorker(chatretention.Options{
		Store:         f.store,
		Clock:         f.clk,
		SweepInterval: 30 * time.Second,
		BatchSize:     1000,
	})
	deleted, err := w.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if deleted != 2 {
		t.Errorf("Tick deleted = %d, want 2", deleted)
	}
	conv, err := f.store.Meta().GetChatConversation(context.Background(), f.cid)
	if err != nil {
		t.Fatalf("GetChatConversation: %v", err)
	}
	if conv.MessageCount != 1 {
		t.Errorf("MessageCount = %d, want 1", conv.MessageCount)
	}
	if conv.LastMessageAt == nil || !conv.LastMessageAt.Equal(survivor.CreatedAt) {
		t.Errorf("LastMessageAt = %v, want %v", conv.LastMessageAt, survivor.CreatedAt)
	}
}

// TestWorker_AccountDefaultRetention verifies that
// ChatAccountSettings.DefaultRetentionSeconds drives the sweeper for
// conversations whose own RetentionSeconds is nil.
func TestWorker_AccountDefaultRetention(t *testing.T) {
	f := newFixture(t, nil) // nil = use account default
	// Set account default to 30 minutes.
	if err := f.store.Meta().UpsertChatAccountSettings(context.Background(), store.ChatAccountSettings{
		PrincipalID:              f.pid,
		DefaultRetentionSeconds:  1800, // 30 min
		DefaultEditWindowSeconds: store.ChatDefaultEditWindowSeconds,
	}); err != nil {
		t.Fatalf("UpsertChatAccountSettings: %v", err)
	}
	for i := 0; i < 3; i++ {
		f.clk.Advance(time.Second)
		f.insertMsg(t, "msg")
	}
	// Advance past the 30-minute window.
	f.clk.Advance(40 * time.Minute)

	w := chatretention.NewWorker(chatretention.Options{
		Store:         f.store,
		Clock:         f.clk,
		SweepInterval: 30 * time.Second,
		BatchSize:     1000,
	})
	deleted, err := w.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if deleted != 3 {
		t.Errorf("Tick deleted = %d, want 3", deleted)
	}
}

// TestWorker_ZeroRetentionMeansNeverExpire confirms that a per-Space
// override of 0 is treated as "never expire" — the messages survive
// arbitrarily long.
func TestWorker_ZeroRetentionMeansNeverExpire(t *testing.T) {
	zero := int64(0)
	f := newFixture(t, &zero)
	for i := 0; i < 3; i++ {
		f.clk.Advance(time.Second)
		f.insertMsg(t, "msg")
	}
	f.clk.Advance(72 * time.Hour) // arbitrarily long
	w := chatretention.NewWorker(chatretention.Options{
		Store:         f.store,
		Clock:         f.clk,
		SweepInterval: 30 * time.Second,
		BatchSize:     1000,
	})
	deleted, err := w.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if deleted != 0 {
		t.Errorf("Tick deleted = %d, want 0 (zero retention is never-expire)", deleted)
	}
}

// TestWorker_Retention_BoundaryAtExactly pins the sweep predicate's
// behaviour at the exact retention horizon (Wave 2.9.9 Track C
// gap-fill). Insert a message at t0; configure retention=3600s; advance
// the clock to t0+3600s exactly; sweep.
//
// Contract (locked here so a future refactor cannot quietly flip it):
//
// The sweeper computes cutoff = now - retention and hard-deletes every
// non-system row whose CreatedAt is at-or-before cutoff. The predicate
// inside expireConversation is `if m.CreatedAt.After(cutoff) { skip }`,
// so a row whose CreatedAt equals cutoff is NOT After cutoff and is
// therefore swept. The accompanying SQL ListChatMessages filter widens
// CreatedBefore by one microsecond (cutoffPlus = cutoff + 1us) so the
// boundary CreatedAt == cutoff is included by ListChatMessages too;
// the in-memory predicate would otherwise be unreachable on real
// backends. In short: the retention horizon is at-or-equal, not
// strictly older. A message inserted at t0 with retention=3600s is
// gone the moment the clock reaches t0+3600s.
func TestWorker_Retention_BoundaryAtExactly(t *testing.T) {
	retention := int64(3600)
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	f := newBoundaryFixture(t, clk, &retention)
	id := f.insertMsg(t, "boundary")
	// Capture the message's CreatedAt for sanity: under the FakeClock
	// the row is stamped with clk.Now() at insert time.
	msg, err := f.store.Meta().GetChatMessage(context.Background(), id)
	if err != nil {
		t.Fatalf("GetChatMessage: %v", err)
	}
	want := clk.Now()
	if !msg.CreatedAt.Equal(want) {
		t.Fatalf("CreatedAt = %v, want %v (FakeClock-stamped)", msg.CreatedAt, want)
	}
	// Advance to exactly t0+retention (NOT past it).
	clk.Advance(time.Duration(retention) * time.Second)
	if got := clk.Now().Sub(msg.CreatedAt); got != time.Duration(retention)*time.Second {
		t.Fatalf("clock delta = %v, want exactly %v", got, time.Duration(retention)*time.Second)
	}
	w := chatretention.NewWorker(chatretention.Options{
		Store:         f.store,
		Clock:         clk,
		SweepInterval: 30 * time.Second,
		BatchSize:     1000,
	})
	deleted, err := w.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("Tick deleted = %d, want 1 (boundary CreatedAt == cutoff is at-or-equal)", deleted)
	}
	if _, err := f.store.Meta().GetChatMessage(context.Background(), id); err == nil {
		t.Fatalf("message %d unexpectedly survived sweep at exact boundary", id)
	}
}

// TestWorker_Retention_BoundaryAtPlusOne pins the sweep predicate one
// second past the horizon. With retention=3600s and clock at t0+3601s,
// CreatedAt < cutoff strictly, so the row is unambiguously swept. This
// guards against a refactor that flipped the predicate to strictly-
// less-than (`Before` instead of `After`-or-equal): the boundary test
// above would catch the equality flip; this test catches a broader
// inversion.
func TestWorker_Retention_BoundaryAtPlusOne(t *testing.T) {
	retention := int64(3600)
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	f := newBoundaryFixture(t, clk, &retention)
	id := f.insertMsg(t, "past-boundary")
	clk.Advance(time.Duration(retention)*time.Second + time.Second)
	w := chatretention.NewWorker(chatretention.Options{
		Store:         f.store,
		Clock:         clk,
		SweepInterval: 30 * time.Second,
		BatchSize:     1000,
	})
	deleted, err := w.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("Tick deleted = %d, want 1", deleted)
	}
	if _, err := f.store.Meta().GetChatMessage(context.Background(), id); err == nil {
		t.Fatalf("message %d unexpectedly survived sweep at t0+retention+1s", id)
	}
}

// newBoundaryFixture mirrors newFixture but accepts a caller-supplied
// FakeClock so the boundary tests can use the Wave 2.9.9 standard clock
// origin (2026-01-01) without coupling to the package-level helper.
func newBoundaryFixture(t *testing.T, clk *clock.FakeClock, retentionSeconds *int64) *fixture {
	t.Helper()
	s, err := fakestore.New(fakestore.Options{
		Clock:   clk,
		BlobDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("fakestore.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	p, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "boundary@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	cid, err := s.Meta().InsertChatConversation(ctx, store.ChatConversation{
		Kind:                 store.ChatConversationKindSpace,
		Name:                 "boundary",
		CreatedByPrincipalID: p.ID,
		RetentionSeconds:     retentionSeconds,
	})
	if err != nil {
		t.Fatalf("InsertChatConversation: %v", err)
	}
	if _, err := s.Meta().InsertChatMembership(ctx, store.ChatMembership{
		ConversationID: cid,
		PrincipalID:    p.ID,
		Role:           store.ChatRoleOwner,
	}); err != nil {
		t.Fatalf("InsertChatMembership: %v", err)
	}
	return &fixture{store: s, clk: clk, pid: p.ID, cid: cid}
}

// TestWorker_SystemMessagesAreRetained inserts a mixed batch of user
// messages and an in-band system message, advances past the retention
// window, and asserts the system row survives the sweep while the
// non-system rows are gone (REQ-CHAT-92 system-message retention).
func TestWorker_SystemMessagesAreRetained(t *testing.T) {
	retention := int64(60)
	f := newFixture(t, &retention)
	pid := f.pid
	f.clk.Advance(time.Second)
	userID := f.insertMsg(t, "hello")
	// Insert a system message directly via the store.
	sysID, err := f.store.Meta().InsertChatMessage(context.Background(), store.ChatMessage{
		ConversationID:    f.cid,
		IsSystem:          true,
		BodyText:          "alice joined the space",
		BodyFormat:        store.ChatBodyFormatText,
		SenderPrincipalID: &pid,
		MetadataJSON:      []byte(`{"event":"member.joined"}`),
	})
	if err != nil {
		t.Fatalf("InsertChatMessage system: %v", err)
	}
	f.clk.Advance(2 * time.Minute) // > 60s window

	w := chatretention.NewWorker(chatretention.Options{
		Store:         f.store,
		Clock:         f.clk,
		SweepInterval: 30 * time.Second,
		BatchSize:     1000,
	})
	if _, err := w.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	// User row gone.
	if _, err := f.store.Meta().GetChatMessage(context.Background(), userID); err == nil {
		t.Errorf("user message %d survived sweep", userID)
	}
	// System row retained.
	if _, err := f.store.Meta().GetChatMessage(context.Background(), sysID); err != nil {
		t.Errorf("system message %d should survive sweep: %v", sysID, err)
	}
}
