package snooze_test

import (
	"context"
	"crypto/rand"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/snooze"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// fixture holds a pre-baked principal + mailbox so each test can focus
// on the snooze invariants without re-building the boilerplate.
type fixture struct {
	store store.Store
	clk   *clock.FakeClock
	pid   store.PrincipalID
	mbID  store.MailboxID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	clk := clock.NewFake(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := storesqlite.OpenWithRand(context.Background(), dbPath, nil, clk, rand.Reader)
	if err != nil {
		t.Fatalf("storesqlite.OpenWithRand: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	p, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "snooze@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	mb, err := s.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID,
		Name:        "INBOX",
	})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}
	return &fixture{store: s, clk: clk, pid: p.ID, mbID: mb.ID}
}

// snoozeMessage inserts a body and immediately calls SetSnooze with
// the supplied deadline. Returns the message id.
func (f *fixture) snoozeMessage(t *testing.T, body string, when time.Time) store.MessageID {
	t.Helper()
	ctx := context.Background()
	ref, err := f.store.Blobs().Put(ctx, strings.NewReader(body))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	if _, _, err := f.store.Meta().InsertMessage(ctx, store.Message{
		PrincipalID: f.pid,
		Blob:        ref,
		Size:        ref.Size,
	}, []store.MessageMailbox{{MailboxID: f.mbID}}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	// Walk the feed in pages so we don't truncate at the default 1000
	// when the test inserts large batches.
	var cursor store.ChangeSeq
	var id store.MessageID
	for {
		batch, err := f.store.Meta().ReadChangeFeed(ctx, f.pid, cursor, 1000)
		if err != nil {
			t.Fatalf("ReadChangeFeed: %v", err)
		}
		for _, e := range batch {
			cursor = e.Seq
			if e.Kind == store.EntityKindEmail && e.Op == store.ChangeOpCreated {
				id = store.MessageID(e.EntityID)
			}
		}
		if len(batch) < 1000 {
			break
		}
	}
	if id == 0 {
		t.Fatalf("no created entry in feed")
	}
	if _, err := f.store.Meta().SetSnooze(ctx, id, f.mbID, &when); err != nil {
		t.Fatalf("SetSnooze: %v", err)
	}
	return id
}

func TestWorker_WakesDueMessages(t *testing.T) {
	f := newFixture(t)
	t1 := time.Date(2030, 1, 1, 1, 0, 0, 0, time.UTC)
	id1 := f.snoozeMessage(t, "msg1", t1)
	id2 := f.snoozeMessage(t, "msg2", t1)

	w := snooze.NewWorker(snooze.Options{
		Store:        f.store,
		Clock:        f.clk,
		PollInterval: 30 * time.Second,
		BatchSize:    100,
	})
	// Advance past the deadline so the first tick processes both.
	f.clk.Advance(2 * time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if w.Released() >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if w.Released() < 2 {
		t.Fatalf("Released = %d, want >= 2", w.Released())
	}

	// Both messages should have SnoozedUntil cleared and "$snoozed"
	// keyword removed.
	for _, id := range []store.MessageID{id1, id2} {
		m, err := f.store.Meta().GetMessage(context.Background(), id)
		if err != nil {
			t.Fatalf("GetMessage(%d): %v", id, err)
		}
		if m.SnoozedUntil != nil {
			t.Errorf("msg %d: SnoozedUntil = %v, want nil", id, m.SnoozedUntil)
		}
		for _, k := range m.Keywords {
			if k == "$snoozed" {
				t.Errorf("msg %d: $snoozed keyword still set", id)
			}
		}
	}
}

func TestWorker_BoundedBatch(t *testing.T) {
	f := newFixture(t)
	due := time.Date(2030, 1, 1, 1, 0, 0, 0, time.UTC)
	const total = 1000
	for i := 0; i < total; i++ {
		f.snoozeMessage(t, "body", due)
	}
	w := snooze.NewWorker(snooze.Options{
		Store:        f.store,
		Clock:        f.clk,
		PollInterval: 30 * time.Second,
		BatchSize:    100,
	})
	f.clk.Advance(2 * time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if w.Released() >= total {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if w.Released() < total {
		t.Fatalf("Released = %d, want %d", w.Released(), total)
	}
}

func TestWorker_ContextCancel_Stops(t *testing.T) {
	f := newFixture(t)
	w := snooze.NewWorker(snooze.Options{
		Store:        f.store,
		Clock:        f.clk,
		PollInterval: 30 * time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("worker did not stop after ctx cancel")
	}
}

func TestWorker_NoDueMessages_NoOp(t *testing.T) {
	f := newFixture(t)
	w := snooze.NewWorker(snooze.Options{
		Store:        f.store,
		Clock:        f.clk,
		PollInterval: 30 * time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	// Give the worker a moment to run its first tick on an empty
	// store. Released should remain 0.
	time.Sleep(50 * time.Millisecond)
	if got := w.Released(); got != 0 {
		t.Fatalf("Released = %d on empty store, want 0", got)
	}
}
