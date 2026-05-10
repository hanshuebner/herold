package protojmap_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// flushRecorder is a minimal http.ResponseWriter + http.Flusher that
// records writes in-memory. The push-loop unit test asserts no SSE
// event lands in this buffer past the initial state push.
type flushRecorder struct {
	mu     sync.Mutex
	header http.Header
	buf    []byte
	wrote  atomic.Int32
	flush  atomic.Int32
}

func newFlushRecorder() *flushRecorder {
	return &flushRecorder{header: http.Header{}}
}

func (f *flushRecorder) Header() http.Header { return f.header }

func (f *flushRecorder) Write(b []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.buf = append(f.buf, b...)
	f.wrote.Add(1)
	return len(b), nil
}

func (f *flushRecorder) WriteHeader(int) {}

func (f *flushRecorder) Flush() { f.flush.Add(1) }

func (f *flushRecorder) bytes() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]byte, len(f.buf))
	copy(cp, f.buf)
	return cp
}

// TestEventSource_BackgroundChurnNoPush exercises
// REQ-EXTIMG-BG-INTERNAL-12 / -62: when the poll cycle surfaces only
// cause = 'background' state-change rows the EventSource loop advances
// its cursor silently -- no flush timer is armed and no SSE state
// event is emitted past the initial-state push every connection
// receives on connect. The synthetic poller stops returning rows
// after the first non-empty batch, so we let the push loop spin a few
// poll intervals before cancelling and observing exactly one
// StateChange event (the initial connect-time priming push).
func TestEventSource_BackgroundChurnNoPush(t *testing.T) {
	f := newFixture(t)
	p, err := f.store.Meta().GetPrincipalByID(context.Background(), f.pid)
	if err != nil {
		t.Fatalf("GetPrincipalByID: %v", err)
	}

	// Synthetic poll surface: one batch of background rows on the
	// Email kind, then empty forever.
	var pollCount atomic.Int32
	poll := func(_ context.Context, _ store.PrincipalID, _ store.ChangeSeq, _ int) ([]store.StateChange, error) {
		if pollCount.Add(1) == 1 {
			return []store.StateChange{
				{Seq: 11, PrincipalID: p.ID, Kind: store.EntityKindEmail, EntityID: 1, Op: store.ChangeOpUpdated, Cause: store.ChangeCauseBackground},
				{Seq: 12, PrincipalID: p.ID, Kind: store.EntityKindEmail, EntityID: 2, Op: store.ChangeOpUpdated, Cause: store.ChangeCauseBackground},
				{Seq: 13, PrincipalID: p.ID, Kind: store.EntityKindEmail, EntityID: 3, Op: store.ChangeOpUpdated, Cause: store.ChangeCauseBackground},
			}, nil
		}
		return nil, nil
	}

	rec := newFlushRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- f.srv.RunEventSourceForTest(
			ctx,
			slog.Default(),
			rec,
			rec,
			p,
			map[string]struct{}{"Email": {}},
			false, // closeAfterState
			60*time.Second,
			poll,
		)
	}()

	// Tick the fake clock past several poll intervals (default
	// pollInterval is 100ms inside runEventSource) and the coalesce
	// window so any flush we wrongly armed would have fired.
	tickStop := time.After(400 * time.Millisecond)
	tickLoop := true
	for tickLoop {
		select {
		case <-tickStop:
			tickLoop = false
		default:
			f.clk.Advance(50 * time.Millisecond)
			time.Sleep(2 * time.Millisecond)
		}
	}
	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("runEventSource returned: %v", err)
	}

	if pollCount.Load() < 2 {
		t.Fatalf("poller fired %d times, want at least 2 (so we cleared the first non-empty batch and saw an empty follow-up)", pollCount.Load())
	}

	body := rec.bytes()
	// The loop emits exactly one StateChange on connect (the
	// initial-state priming write at the top of runEventSource).
	// REQ-EXTIMG-BG-INTERNAL-12 mandates that subsequent
	// background-only polling cycles do NOT emit additional
	// StateChange events. We tolerate the one initial event.
	got := bytes.Count(body, []byte(`"@type":"StateChange"`))
	if got != 1 {
		t.Fatalf("StateChange event count = %d, want 1 (the initial-state push); body = %q", got, body)
	}
}
