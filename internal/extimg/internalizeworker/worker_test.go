package internalizeworker_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/extimg"
	"github.com/hanshuebner/herold/internal/extimg/internalizeworker"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// TestWorker_DrainsBacklog seeds 3 pending messages, starts the
// worker in passthrough mode, and asserts every flag is cleared
// within the first batch. Passthrough is the cheapest test surface
// because extimg.Internalize is a no-op there; the worker still
// exercises the list-process-clear path on every row.
func TestWorker_DrainsBacklog(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"), nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	mb, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID,
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}

	// Seed 3 flagged messages.
	want := 3
	for i := 0; i < want; i++ {
		ref, err := st.Blobs().Put(ctx, stringsReader("From: a@x\r\nTo: b@x\r\n\r\nbody"))
		if err != nil {
			t.Fatalf("Blobs.Put: %v", err)
		}
		if _, _, err := st.Meta().InsertMessage(ctx, store.Message{
			PrincipalID:        p.ID,
			Blob:               ref,
			Size:               ref.Size,
			InternalizePending: true,
		}, []store.MessageMailbox{{MailboxID: mb.ID}}); err != nil {
			t.Fatalf("InsertMessage: %v", err)
		}
	}

	// Worker in passthrough mode: Internalize is a no-op, the
	// worker clears the flag on every row it sees.
	w := internalizeworker.New(st, extimg.Config{Mode: extimg.ModePassthrough}, nil, clk, internalizeworker.Options{
		Concurrency:      2,
		BatchSize:        16,
		IdlePollInterval: 10 * time.Millisecond,
	})
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(runCtx)

	// Wait up to 2 s for every flag to clear; then cancel the
	// worker. The test passes when CountInternalizePending(p.ID) == 0.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		n, err := st.Meta().CountInternalizePending(ctx, p.ID)
		if err != nil {
			t.Fatalf("CountInternalizePending: %v", err)
		}
		if n == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if n, _ := st.Meta().CountInternalizePending(ctx, p.ID); n != 0 {
		t.Fatalf("worker did not drain: pending = %d, want 0", n)
	}
}

// TestWorker_NotifyResetsCursor proves the notify path triggers the
// next batch with the cursor reset. The worker is started with no
// pending rows; we wait for it to park, then add a row and call
// Notify; the row should clear shortly after.
func TestWorker_NotifyResetsCursor(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"), nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	p, _ := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@example.test",
	})
	mb, _ := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID, Name: "INBOX", Attributes: store.MailboxAttrInbox,
	})

	w := internalizeworker.New(st, extimg.Config{Mode: extimg.ModePassthrough}, nil, clk, internalizeworker.Options{
		Concurrency:      2,
		BatchSize:        16,
		IdlePollInterval: 1 * time.Hour, // safety net silenced; only Notify wakes the loop
	})
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(runCtx)
	// Let the worker take its initial pass and park.
	time.Sleep(50 * time.Millisecond)

	// Insert a flagged message; Notify should wake the worker.
	ref, _ := st.Blobs().Put(ctx, stringsReader("From: a@x\r\nTo: b@x\r\n\r\n"))
	if _, _, err := st.Meta().InsertMessage(ctx, store.Message{
		PrincipalID:        p.ID,
		Blob:               ref,
		Size:               ref.Size,
		InternalizePending: true,
	}, []store.MessageMailbox{{MailboxID: mb.ID}}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	w.Notify()

	// Expect the flag to clear within 500 ms.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		n, _ := st.Meta().CountInternalizePending(ctx, p.ID)
		if n == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("worker did not pick up flagged row after Notify")
}

// stringsReader is a tiny shim around strings.NewReader so the test
// reads a literal as an io.Reader without pulling the strings import
// into multiple call sites.
func stringsReader(s string) *strings.Reader { return strings.NewReader(s) }
