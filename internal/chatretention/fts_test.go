package chatretention_test

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/chatretention"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storefts"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
)

// stringExtractor is a deterministic TextExtractor for tests; the chat
// FTS path does not currently exercise the email body extractor, but
// the Worker constructor requires one.
type stringExtractor struct{}

func (stringExtractor) Extract(ctx context.Context, _ store.Message, body io.Reader) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	b, err := io.ReadAll(body)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// drainFTS calls FlushForTest in a loop until the feed is empty, then
// asserts that SearchChatMessages returns exactly want hits. It is
// deterministic: no wall-clock reads, no time.Sleep, no time.After.
// The worker must NOT be running concurrently (no Run goroutine).
func drainFTS(
	t *testing.T,
	ctx context.Context,
	w *storefts.Worker,
	idx *storefts.Index,
	term string,
	convIDs []store.ConversationID,
	want int,
) {
	t.Helper()
	for {
		changed, err := w.FlushForTest(ctx)
		if err != nil {
			t.Fatalf("FlushForTest: %v", err)
		}
		if !changed {
			break
		}
	}
	ids, err := idx.SearchChatMessages(ctx, term, convIDs, 0)
	if err != nil {
		t.Fatalf("SearchChatMessages: %v", err)
	}
	if len(ids) != want {
		t.Fatalf("FTS hit count = %d, want %d (ids: %v)", len(ids), want, ids)
	}
}

// TestWorker_HardDelete_RemovesFromFTSIndex pins the end-to-end chain:
// the chat retention sweep hard-deletes a message via
// Metadata.HardDeleteChatMessage, which appends an
// (EntityKindChatMessage, ChangeOpDestroyed) row to the FTS change
// feed; the storefts.Worker reads that row on its next tick and calls
// Index.RemoveChatMessage. After the worker flushes, the message is
// gone from the Bleve index and SearchChatMessages returns zero hits
// (Wave 2.9.6 Track D + Wave 2.9.9 Track C verification).
//
// This is an integration test, not a unit test: it wires a real
// storefts.Index, runs FlushForTest end-to-end, and drives retention +
// FTS through their normal cadences. The fakestore faithfully
// reproduces the per-backend hard-delete path's FTS-change emit (see
// fakestore_chat.go HardDeleteChatMessage); SQLite and Postgres
// reproduce it in production storage layers (Wave 2.9.6 Track D's
// per-backend implementation).
//
// Determinism note: the worker is NOT started as a goroutine here.
// FlushForTest drives indexing synchronously in the test goroutine,
// which eliminates all timing races without wall-clock polling.
func TestWorker_HardDelete_RemovesFromFTSIndex(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := fakestore.New(fakestore.Options{
		Clock:   clk,
		BlobDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("fakestore.New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	retention := int64(60) // 60 s window so the sweep fires below.
	cid, err := st.Meta().InsertChatConversation(ctx, store.ChatConversation{
		Kind:                 store.ChatConversationKindSpace,
		Name:                 "fts-retention",
		CreatedByPrincipalID: p.ID,
		RetentionSeconds:     &retention,
	})
	if err != nil {
		t.Fatalf("InsertChatConversation: %v", err)
	}
	if _, err := st.Meta().InsertChatMembership(ctx, store.ChatMembership{
		ConversationID: cid,
		PrincipalID:    p.ID,
		Role:           store.ChatRoleOwner,
	}); err != nil {
		t.Fatalf("InsertChatMembership: %v", err)
	}

	// Bleve index + worker. The worker is NOT started as a goroutine;
	// FlushForTest drives it synchronously so there are no scheduling
	// races.
	idx, err := storefts.New(t.TempDir(), nil, clk)
	if err != nil {
		t.Fatalf("storefts.New: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })

	wopts := storefts.WorkerOptions{}
	w := storefts.NewWorker(idx, st, stringExtractor{}, nil, clk, wopts)

	// Insert 3 chat messages with searchable bodies. Advance the clock
	// by 1 ms between inserts to give distinct timestamps without crossing
	// the flush interval (500 ms), which would otherwise wake a running
	// worker goroutine mid-loop and fragment the change feed.
	pid := p.ID
	body := "indelible-token-zebra"
	ids := make([]store.ChatMessageID, 0, 3)
	for i := 0; i < 3; i++ {
		clk.Advance(time.Millisecond)
		id, err := st.Meta().InsertChatMessage(ctx, store.ChatMessage{
			ConversationID:    cid,
			SenderPrincipalID: &pid,
			BodyText:          body,
			BodyFormat:        store.ChatBodyFormatText,
		})
		if err != nil {
			t.Fatalf("InsertChatMessage[%d]: %v", i, err)
		}
		ids = append(ids, id)
	}

	// Drain the worker synchronously and assert all 3 messages are
	// indexed before the retention sweep runs.
	drainFTS(t, ctx, w, idx, body, []store.ConversationID{cid}, 3)

	// Advance well past the retention window so the sweep can
	// hard-delete every row. The retention-sweep predicate is
	// at-or-equal (see TestWorker_Retention_BoundaryAtExactly), so a
	// single Advance comfortably past 60 s drops every row.
	clk.Advance(2 * time.Minute)

	// Drive the chat retention sweeper through one Tick. Each delete
	// appends an (EntityKindChatMessage, ChangeOpDestroyed) FTS row.
	rw := chatretention.NewWorker(chatretention.Options{
		Store:         st,
		Clock:         clk,
		SweepInterval: 30 * time.Second,
		BatchSize:     1000,
	})
	deleted, err := rw.Tick(ctx)
	if err != nil {
		t.Fatalf("retention Tick: %v", err)
	}
	if deleted != 3 {
		t.Fatalf("retention deleted = %d, want 3", deleted)
	}

	// Drain the worker synchronously and assert the destroy rows have
	// been applied: the index must now return zero hits.
	drainFTS(t, ctx, w, idx, body, []store.ConversationID{cid}, 0)

	// Sanity: every metadata row is gone too. Guards against a future
	// refactor where the FTS-index removal lands but the metadata
	// row leaks (or vice versa) since the test runs against a faithful
	// fakestore that mirrors both.
	for _, id := range ids {
		if _, err := st.Meta().GetChatMessage(ctx, id); err == nil {
			t.Fatalf("chat message %d unexpectedly survived hard-delete", id)
		}
	}
}
