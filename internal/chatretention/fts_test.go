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
// storefts.Index, runs the worker goroutine end-to-end, and drives
// retention + FTS through their normal cadences. The fakestore
// faithfully reproduces the per-backend hard-delete path's FTS-change
// emit (see fakestore_chat.go HardDeleteChatMessage); SQLite and
// Postgres reproduce it in production storage layers (Wave 2.9.6
// Track D's per-backend implementation).
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

	// Bleve index + worker.
	idx, err := storefts.New(t.TempDir(), nil, clk)
	if err != nil {
		t.Fatalf("storefts.New: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })

	wopts := storefts.WorkerOptions{}
	w := storefts.NewWorker(idx, st, stringExtractor{}, nil, clk, wopts)
	wctx, cancelWorker := context.WithCancel(ctx)
	wdone := make(chan error, 1)
	go func() { wdone <- w.Run(wctx) }()
	t.Cleanup(func() {
		cancelWorker()
		<-wdone
	})

	// Insert 3 chat messages with searchable bodies. Each insert
	// appends an (EntityKindChatMessage, ChangeOpCreated) FTS change.
	pid := p.ID
	body := "indelible-token-zebra"
	ids := make([]store.ChatMessageID, 0, 3)
	for i := 0; i < 3; i++ {
		clk.Advance(time.Second)
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

	// Drive the worker until the FTS index reaches the expected hit
	// count. The previous single-shot flushOnce was racy: under heavy
	// parallel CPU load the inter-insert clk.Advance(1 s) calls can
	// wake the worker mid-loop, fragmenting the inserts across multiple
	// flushes; by the time the test captures CurrentFlushSignal() the
	// worker has already drained the feed and the next nudge produces
	// no flush, leaving the test waiting on a broadcast that never
	// closes (or, if the worker happened to coalesce one batch with
	// only some of the inserts, asserting against an undercount).
	//
	// expectHits polls: nudge the worker, check the index, repeat until
	// the expected count is observed or a 2 s wall-clock deadline fires.
	// Robust against any flush-fragmentation timing.
	expectHits := func(t *testing.T, want int) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		var got []store.ChatMessageID
		for time.Now().Before(deadline) {
			flushSig := w.CurrentFlushSignal()
			clk.Advance(storefts.DefaultFlushInterval + 10*time.Millisecond)
			select {
			case <-flushSig:
			case <-time.After(50 * time.Millisecond):
			}
			ids, err := idx.SearchChatMessages(ctx, body, []store.ConversationID{cid}, 0)
			if err != nil {
				t.Fatalf("SearchChatMessages: %v", err)
			}
			got = ids
			if len(got) == want {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("FTS hits never reached %d within 2 s (final: %d, %v)", want, len(got), got)
	}
	expectHits(t, 3)

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

	// Poll until the destroy rows have been applied to the FTS index.
	expectHits(t, 0)

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
