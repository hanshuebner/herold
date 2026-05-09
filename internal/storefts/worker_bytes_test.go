package storefts_test

import (
	"context"
	"crypto/rand"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storefts"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// TestIndex_PendingBytes_TracksAndResets pins the byte-counter contract
// the byte-budget flush trigger depends on: every IndexMessageFull bumps
// PendingBytes by len(text); Commit zeroes it. A regression here would
// silently break the worker's memory ceiling.
func TestIndex_PendingBytes_TracksAndResets(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	idx, err := storefts.New(t.TempDir(), nil, clk)
	if err != nil {
		t.Fatalf("storefts.New: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })

	if got := idx.PendingBytes(); got != 0 {
		t.Fatalf("fresh index PendingBytes = %d; want 0", got)
	}

	const body = "abcdefghij" // 10 bytes
	for i := 0; i < 7; i++ {
		err := idx.IndexMessageFull(context.Background(), store.PrincipalID(1), store.Message{ID: store.MessageID(i + 1)}, body)
		if err != nil {
			t.Fatalf("IndexMessageFull #%d: %v", i, err)
		}
	}
	if got, want := idx.PendingBytes(), 70; got != want {
		t.Fatalf("PendingBytes after 7 docs = %d; want %d", got, want)
	}

	if err := idx.Commit(context.Background()); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got := idx.PendingBytes(); got != 0 {
		t.Fatalf("PendingBytes after Commit = %d; want 0", got)
	}
}

// TestWorker_ByteBudgetFlushesEarly verifies the worker stops accumulating
// once cumulative body-text bytes pass MaxBatchBytes, even when the
// doc-count ceiling is far away. Without the byte-budget trigger the
// worker would hold every pending body text in the Bleve batch until the
// doc-count or wall-clock ceiling fired — the bug that pinned 144 GiB
// resident on a real corpus.
func TestWorker_ByteBudgetFlushesEarly(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := storesqlite.OpenWithRand(context.Background(), dbPath, nil, clk, rand.Reader)
	if err != nil {
		t.Fatalf("storesqlite.OpenWithRand: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	idx, err := storefts.New(t.TempDir(), nil, clk)
	if err != nil {
		t.Fatalf("storefts.New: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })

	// Doc count is generously above the message count we will insert; the
	// byte budget is the only ceiling that can fire mid-batch.
	const (
		docBudget   = 1000
		byteBudget  = 4096
		bodyBytes   = 1024
		messages    = 8
	)
	w := storefts.NewWorker(idx, st, stringExtractor{}, nil, clk, storefts.WorkerOptions{
		BatchSize:     docBudget,
		MaxBatchBytes: byteBudget,
		FlushInterval: 10 * time.Second, // far enough that the wall clock cannot trip during the test
	})

	p, err := st.Meta().InsertPrincipal(context.Background(), store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	mb, err := st.Meta().InsertMailbox(context.Background(), store.Mailbox{
		PrincipalID: p.ID,
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}

	body := strings.Repeat("x", bodyBytes)
	for i := 0; i < messages; i++ {
		ref, err := st.Blobs().Put(context.Background(), strings.NewReader(body))
		if err != nil {
			t.Fatalf("Put blob: %v", err)
		}
		_, _, err = st.Meta().InsertMessage(context.Background(),
			store.Message{Size: ref.Size, Blob: ref},
			[]store.MessageMailbox{{MailboxID: mb.ID}},
		)
		if err != nil {
			t.Fatalf("InsertMessage: %v", err)
		}
	}

	// Drive a single processBatch invocation. The byte ceiling is at 4 KiB
	// of body text; messages are 1 KiB each, so the loop must break after
	// 4 messages rather than draining all 8 in one batch.
	changed, err := w.FlushForTest(context.Background())
	if err != nil {
		t.Fatalf("FlushForTest: %v", err)
	}
	if !changed {
		t.Fatalf("FlushForTest reported no changes; expected 8 to be available")
	}
	// The cursor advanced past every change the worker handled in this
	// batch (the per-change maxSeq tracking advances even when we break
	// early). The change IDs are sequential per insert, so the cursor
	// after one batch must be strictly less than the cursor after a full
	// drain — i.e. the byte ceiling really did break the loop early.
	cursorAfterFirst := w.Cursor()

	// Drain the rest.
	for {
		changed, err := w.FlushForTest(context.Background())
		if err != nil {
			t.Fatalf("FlushForTest drain: %v", err)
		}
		if !changed {
			break
		}
	}
	cursorAfterDrain := w.Cursor()

	if cursorAfterFirst >= cursorAfterDrain {
		t.Fatalf("byte budget did not break processBatch early: cursorAfterFirst=%d cursorAfterDrain=%d (want strict <)", cursorAfterFirst, cursorAfterDrain)
	}
}
