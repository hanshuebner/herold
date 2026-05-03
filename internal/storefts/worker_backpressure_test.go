package storefts_test

import (
	"context"
	"crypto/rand"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storefts"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// backpressureStore wraps a store.Store and records the number of
// times ReadChangeFeedForFTS has been called. Lets the backpressure test
// assert the worker is sleeping on the clock (one poll per flush
// interval) rather than spinning.
type backpressureStore struct {
	store.Store
	fts *backpressureFTS
}

type backpressureFTS struct {
	inner store.FTS
	calls chan struct{}
}

func (b *backpressureStore) FTS() store.FTS { return b.fts }

func (b *backpressureFTS) IndexMessage(ctx context.Context, msg store.Message, text string) error {
	return b.inner.IndexMessage(ctx, msg, text)
}

func (b *backpressureFTS) RemoveMessage(ctx context.Context, id store.MessageID) error {
	return b.inner.RemoveMessage(ctx, id)
}

func (b *backpressureFTS) Query(ctx context.Context, principalID store.PrincipalID, q store.Query) ([]store.MessageRef, error) {
	return b.inner.Query(ctx, principalID, q)
}

func (b *backpressureFTS) ReadChangeFeedForFTS(ctx context.Context, cursor uint64, max int) ([]store.FTSChange, error) {
	// Non-blocking record of the call; buffered so the worker never
	// stalls on a full channel.
	select {
	case b.calls <- struct{}{}:
	default:
	}
	return b.inner.ReadChangeFeedForFTS(ctx, cursor, max)
}

func (b *backpressureFTS) Commit(ctx context.Context) error { return b.inner.Commit(ctx) }

func TestWorker_BackpressureOnEmptyFeed(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	fake, err := storesqlite.OpenWithRand(context.Background(), dbPath, nil, clk, rand.Reader)
	if err != nil {
		t.Fatalf("storesqlite.OpenWithRand: %v", err)
	}
	t.Cleanup(func() { _ = fake.Close() })

	bp := &backpressureStore{
		Store: fake,
		fts: &backpressureFTS{
			inner: fake.FTS(),
			calls: make(chan struct{}, 64),
		},
	}

	idx, err := storefts.New(t.TempDir(), nil, clk)
	if err != nil {
		t.Fatalf("storefts.New: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })

	w := storefts.NewWorker(idx, bp, stringExtractor{}, nil, clk, storefts.WorkerOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	// First poll happens at startup. Drain it so the next polls are
	// the sleep-driven ones we want to count.
	select {
	case <-bp.fts.calls:
	case <-time.After(time.Second):
		t.Fatalf("worker did not perform initial feed poll")
	}

	// With no changes, the worker must be parked on clock.After. If it
	// were spinning, new calls would arrive immediately; we give it a
	// generous real-time window to observe any busy-loop activity.
	select {
	case <-bp.fts.calls:
		t.Fatalf("worker polled again without clock advance — busy loop")
	case <-time.After(50 * time.Millisecond):
	}

	// Advance past one flush interval. The worker should wake, poll
	// once, see nothing, and sleep again. We expect exactly one
	// additional call per advance.
	for i := 0; i < 3; i++ {
		clk.Advance(storefts.DefaultFlushInterval + time.Millisecond)
		select {
		case <-bp.fts.calls:
		case <-time.After(time.Second):
			t.Fatalf("iteration %d: worker did not wake after clock advance", i)
		}
		// Same anti-busy check as above.
		select {
		case <-bp.fts.calls:
			t.Fatalf("iteration %d: worker spun without a new clock advance", i)
		case <-time.After(20 * time.Millisecond):
		}
	}

	// Cursor never advanced (no changes were produced).
	if w.Cursor() != 0 {
		t.Fatalf("cursor moved despite empty feed: %d", w.Cursor())
	}
}
