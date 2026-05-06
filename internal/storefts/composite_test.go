package storefts_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storefts"
)

// fakeFTSDelegate captures ReadChangeFeedForFTS calls so the Composite
// test can prove the change-feed leg routes to the delegate (the
// per-backend SQL stub) and never to the Bleve Index.
type fakeFTSDelegate struct {
	readCalls int
	readArgs  struct {
		cursor uint64
		max    int
	}
	readResult []store.FTSChange
	readErr    error
}

func (f *fakeFTSDelegate) IndexMessage(_ context.Context, _ store.Message, _ string) error {
	return errors.New("delegate.IndexMessage must not be called from Composite")
}

func (f *fakeFTSDelegate) RemoveMessage(_ context.Context, _ store.MessageID) error {
	return errors.New("delegate.RemoveMessage must not be called from Composite")
}

func (f *fakeFTSDelegate) Query(_ context.Context, _ store.PrincipalID, _ store.Query) ([]store.MessageRef, error) {
	return nil, errors.New("delegate.Query must not be called from Composite")
}

func (f *fakeFTSDelegate) ReadChangeFeedForFTS(_ context.Context, cursor uint64, max int) ([]store.FTSChange, error) {
	f.readCalls++
	f.readArgs.cursor = cursor
	f.readArgs.max = max
	return f.readResult, f.readErr
}

func (f *fakeFTSDelegate) Commit(_ context.Context) error {
	return errors.New("delegate.Commit must not be called from Composite")
}

func TestComposite_RoutesQueryToBleve(t *testing.T) {
	idx := newIndex(t)
	pid, mboxID, _ := seedIndex(t, idx)
	delegate := &fakeFTSDelegate{}
	c := storefts.NewComposite(idx, delegate)

	hits, err := c.Query(context.Background(), pid, store.Query{
		MailboxID: mboxID,
		Subject:   []string{"invoice"},
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("Composite.Query: %v", err)
	}
	if len(hits) == 0 {
		t.Fatalf("Composite.Query returned no hits; expected Bleve to find 'invoice'")
	}
	if delegate.readCalls != 0 {
		t.Fatalf("delegate.ReadChangeFeedForFTS called %d times during Query; expected 0", delegate.readCalls)
	}
}

func TestComposite_RoutesChangeFeedToDelegate(t *testing.T) {
	idx := newIndex(t)
	delegate := &fakeFTSDelegate{
		readResult: []store.FTSChange{{
			Seq:         1,
			PrincipalID: 7,
			Kind:        store.EntityKindEmail,
			EntityID:    42,
			Op:          store.ChangeOpCreated,
			ProducedAt:  time.Now(),
		}},
	}
	c := storefts.NewComposite(idx, delegate)

	got, err := c.ReadChangeFeedForFTS(context.Background(), 99, 500)
	if err != nil {
		t.Fatalf("Composite.ReadChangeFeedForFTS: %v", err)
	}
	if delegate.readCalls != 1 {
		t.Fatalf("delegate.ReadChangeFeedForFTS calls = %d; want 1", delegate.readCalls)
	}
	if delegate.readArgs.cursor != 99 || delegate.readArgs.max != 500 {
		t.Fatalf("delegate args = (%d, %d); want (99, 500)", delegate.readArgs.cursor, delegate.readArgs.max)
	}
	if len(got) != 1 || got[0].Seq != 1 || got[0].EntityID != 42 {
		t.Fatalf("unexpected change feed result: %+v", got)
	}
}

func TestComposite_IndexThenQuery(t *testing.T) {
	idx := newIndex(t)
	delegate := &fakeFTSDelegate{}
	c := storefts.NewComposite(idx, delegate)

	// Use the principal-less IndexMessage signature exposed by store.FTS;
	// it delegates to Index.IndexMessageFull(0, ...) so the doc lands
	// under principal 0.
	msg := store.Message{
		ID:           5001,
		MailboxID:    1,
		UID:          1,
		InternalDate: time.Now(),
		Envelope:     store.Envelope{Subject: "tracer-token-zylophone"},
	}
	if err := c.IndexMessage(context.Background(), msg, "body content"); err != nil {
		t.Fatalf("Composite.IndexMessage: %v", err)
	}
	if err := c.Commit(context.Background()); err != nil {
		t.Fatalf("Composite.Commit: %v", err)
	}
	hits, err := c.Query(context.Background(), store.PrincipalID(0), store.Query{
		Subject: []string{"tracer-token-zylophone"},
		Limit:   5,
	})
	if err != nil {
		t.Fatalf("Composite.Query: %v", err)
	}
	if len(hits) != 1 || hits[0].MessageID != msg.ID {
		t.Fatalf("Composite.Query hits = %+v; want one hit on MessageID %d", hits, msg.ID)
	}
}

// Compile-time check: Composite implements store.FTS.
var _ store.FTS = (*storefts.Composite)(nil)
