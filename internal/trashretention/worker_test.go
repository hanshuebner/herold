package trashretention_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
	"github.com/hanshuebner/herold/internal/trashretention"
)

// trashFixture holds a store pre-populated with one principal and one
// Trash mailbox. The FakeClock lets tests control InternalDate values.
type trashFixture struct {
	store   store.Store
	clk     *clock.FakeClock
	pid     store.PrincipalID
	trashID store.MailboxID
}

func newTrashFixture(t *testing.T) *trashFixture {
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
		CanonicalEmail: "alice@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	mb, err := s.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID,
		Name:        "Trash",
		Attributes:  store.MailboxAttrTrash,
	})
	if err != nil {
		t.Fatalf("InsertMailbox(Trash): %v", err)
	}
	return &trashFixture{store: s, clk: clk, pid: p.ID, trashID: mb.ID}
}

// insertMessage inserts a message into the Trash mailbox with the given
// InternalDate, returning its ID.
func (f *trashFixture) insertMessage(t *testing.T, internalDate time.Time) store.MessageID {
	t.Helper()
	ctx := context.Background()
	blob, err := f.store.Blobs().Put(ctx, bytes.NewReader([]byte("From: x@example.test\r\n\r\nBody\r\n")))
	if err != nil {
		t.Fatalf("blob Put: %v", err)
	}
	uid, _, err := f.store.Meta().InsertMessage(ctx, store.Message{
		PrincipalID:  f.pid,
		Blob:         blob,
		Size:         blob.Size,
		InternalDate: internalDate,
		ReceivedAt:   internalDate,
	}, []store.MessageMailbox{{MailboxID: f.trashID}})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	// Retrieve the MessageID via ListMessages (we only got UID back).
	msgs, err := f.store.Meta().ListMessages(ctx, f.trashID, store.MessageFilter{Limit: 1000})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	for _, m := range msgs {
		if m.UID == uid {
			return m.ID
		}
	}
	t.Fatalf("inserted message UID %d not found in ListMessages", uid)
	return 0
}

func TestWorker_Defaults(t *testing.T) {
	f := newTrashFixture(t)
	w := trashretention.NewWorker(trashretention.Options{Store: f.store})
	if w.RetentionDays() != trashretention.DefaultRetentionDays {
		t.Errorf("RetentionDays default = %d, want %d", w.RetentionDays(), trashretention.DefaultRetentionDays)
	}
	if w.SweepInterval() != trashretention.DefaultSweepInterval {
		t.Errorf("SweepInterval default = %v, want %v", w.SweepInterval(), trashretention.DefaultSweepInterval)
	}
	if w.BatchSize() != trashretention.DefaultBatchSize {
		t.Errorf("BatchSize default = %d, want %d", w.BatchSize(), trashretention.DefaultBatchSize)
	}
}

// TestWorker_ExpungesOldMessages inserts three messages: two older than the
// retention window and one recent. After one Tick only the two old ones must
// be gone.
func TestWorker_ExpungesOldMessages(t *testing.T) {
	f := newTrashFixture(t)
	now := f.clk.Now()
	// Insert two messages 31 days old.
	old1 := f.insertMessage(t, now.Add(-31*24*time.Hour))
	old2 := f.insertMessage(t, now.Add(-31*24*time.Hour-time.Minute))
	// Insert one message 1 day old (within the 30-day window).
	recent := f.insertMessage(t, now.Add(-1*24*time.Hour))

	w := trashretention.NewWorker(trashretention.Options{
		Store:         f.store,
		Clock:         f.clk,
		RetentionDays: 30,
		SweepInterval: time.Minute,
	})
	deleted, err := w.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if deleted != 2 {
		t.Errorf("Tick deleted = %d, want 2", deleted)
	}
	if got := w.Deleted(); got != 2 {
		t.Errorf("Deleted() = %d, want 2", got)
	}
	ctx := context.Background()
	// old1 and old2 should be gone.
	if _, err := f.store.Meta().GetMessage(ctx, old1); err == nil {
		t.Errorf("old1 (%d) survived sweep", old1)
	}
	if _, err := f.store.Meta().GetMessage(ctx, old2); err == nil {
		t.Errorf("old2 (%d) survived sweep", old2)
	}
	// recent should still be present.
	if _, err := f.store.Meta().GetMessage(ctx, recent); err != nil {
		t.Errorf("recent (%d) unexpectedly gone after sweep: %v", recent, err)
	}
}

// TestWorker_EmptyTrash asserts that Tick on an empty Trash mailbox deletes
// nothing and returns no error.
func TestWorker_EmptyTrash(t *testing.T) {
	f := newTrashFixture(t)
	w := trashretention.NewWorker(trashretention.Options{
		Store:         f.store,
		Clock:         f.clk,
		RetentionDays: 30,
		SweepInterval: time.Minute,
	})
	deleted, err := w.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if deleted != 0 {
		t.Errorf("Tick deleted = %d on empty Trash, want 0", deleted)
	}
}

// TestWorker_NoPrincipalNoTrash verifies the worker handles a store with no
// principals gracefully.
func TestWorker_NoPrincipalNoTrash(t *testing.T) {
	clk := clock.NewFake(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := storesqlite.OpenWithRand(context.Background(), dbPath, nil, clk, rand.Reader)
	if err != nil {
		t.Fatalf("storesqlite.OpenWithRand: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	w := trashretention.NewWorker(trashretention.Options{
		Store: s, Clock: clk, RetentionDays: 30, SweepInterval: time.Minute,
	})
	deleted, err := w.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if deleted != 0 {
		t.Errorf("Tick deleted = %d, want 0 with no principals", deleted)
	}
}

// TestWorker_NonTrashMailboxNotTouched inserts an old message into a non-Trash
// mailbox and asserts the sweeper does not touch it.
func TestWorker_NonTrashMailboxNotTouched(t *testing.T) {
	f := newTrashFixture(t)
	ctx := context.Background()
	// Create a regular Inbox mailbox.
	inbox, err := f.store.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: f.pid,
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox(INBOX): %v", err)
	}
	now := f.clk.Now()
	blob, err := f.store.Blobs().Put(ctx, bytes.NewReader([]byte("From: x@example.test\r\n\r\nBody\r\n")))
	if err != nil {
		t.Fatalf("blob Put: %v", err)
	}
	_, _, err = f.store.Meta().InsertMessage(ctx, store.Message{
		PrincipalID:  f.pid,
		Blob:         blob,
		Size:         blob.Size,
		InternalDate: now.Add(-60 * 24 * time.Hour),
		ReceivedAt:   now.Add(-60 * 24 * time.Hour),
	}, []store.MessageMailbox{{MailboxID: inbox.ID}})
	if err != nil {
		t.Fatalf("InsertMessage into INBOX: %v", err)
	}

	w := trashretention.NewWorker(trashretention.Options{
		Store:         f.store,
		Clock:         f.clk,
		RetentionDays: 30,
		SweepInterval: time.Minute,
	})
	deleted, err := w.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if deleted != 0 {
		t.Errorf("Tick deleted %d from INBOX, want 0 (non-Trash mailbox)", deleted)
	}
}

// TestWorker_DoubleRunRejected verifies that Run refuses a second concurrent
// invocation.
func TestWorker_DoubleRunRejected(t *testing.T) {
	f := newTrashFixture(t)
	w := trashretention.NewWorker(trashretention.Options{
		Store:         f.store,
		Clock:         f.clk,
		RetentionDays: 30,
		SweepInterval: time.Minute,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	// Brief wait so the goroutine enters Run before we try a second one.
	time.Sleep(10 * time.Millisecond)
	err := w.Run(ctx)
	cancel()
	<-done
	if err == nil {
		t.Error("second Run call should have returned an error")
	}
}
