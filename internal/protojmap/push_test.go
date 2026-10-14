package protojmap_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/protojmap"
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

// sseStateChangeEvent is the subset of the wire StateChange shape the
// tests below decode: just enough to inspect which JMAP type names
// rode along in "changed" on a given push.
type sseStateChangeEvent struct {
	AtType  string                       `json:"@type"`
	Changed map[string]map[string]string `json:"changed"`
}

// parseSSEStateChanges extracts every "event: state" / "data: ..."
// frame from a recorded SSE body and decodes its JSON payload.
func parseSSEStateChanges(t *testing.T, body []byte) []sseStateChangeEvent {
	t.Helper()
	var out []sseStateChangeEvent
	for _, frame := range strings.Split(string(body), "\n\n") {
		idx := strings.Index(frame, "data: ")
		if idx < 0 {
			continue
		}
		var ev sseStateChangeEvent
		if err := json.Unmarshal([]byte(frame[idx+len("data: "):]), &ev); err != nil {
			t.Fatalf("decode SSE frame %q: %v", frame, err)
		}
		out = append(out, ev)
	}
	return out
}

// waitForSSEEventCount advances the fake clock in small increments
// (mirroring the production pollInterval/PushCoalesceWindow cadence)
// until rec has recorded at least want StateChange events or the
// real-time budget elapses.
func waitForSSEEventCount(t *testing.T, clk interface{ Advance(time.Duration) }, rec *flushRecorder, want int, budget time.Duration) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if bytes.Count(rec.bytes(), []byte(`"@type":"StateChange"`)) >= want {
			return
		}
		clk.Advance(50 * time.Millisecond)
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d StateChange event(s); body = %q", want, rec.bytes())
}

// TestEventSource_DeliveryPushesOnlyChangedTypes pins the fix for
// #524 (RFC 8620 §7.1): a push StateChange must list a type only when
// its state differs from the state last pushed to THIS subscription.
// A mail delivery bumps the Email change-feed seq but never touches
// jmap_states.internalize_status_state, so the push the delivery
// triggers must carry Email alone -- InternalizeStatus (already seen
// once, unchanged) must be absent, even though both types are
// subscribed to and InternalizeStatus is still a nonzero value.
func TestEventSource_DeliveryPushesOnlyChangedTypes(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	p, err := f.store.Meta().GetPrincipalByID(ctx, f.pid)
	if err != nil {
		t.Fatalf("GetPrincipalByID: %v", err)
	}

	// Give InternalizeStatus a nonzero baseline before the subscription
	// opens, so the regression (every push repeats every subscribed
	// type) and the fix (repeats are suppressed) produce visibly
	// different bodies rather than both showing the vacuous value "0".
	if _, err := f.store.Meta().IncrementJMAPState(ctx, f.pid, store.JMAPStateKindInternalizeStatus); err != nil {
		t.Fatalf("IncrementJMAPState: %v", err)
	}

	types := map[string]struct{}{"Email": {}, "InternalizeStatus": {}}
	rec := newFlushRecorder()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- f.srv.RunEventSourceForTest(
			runCtx, slog.Default(), rec, rec, p, types,
			false, 60*time.Second, f.store.Meta().ReadChangeFeed,
		)
	}()

	// Wait for the initial connect-time push (carries both types: the
	// subscription has seen nothing yet).
	waitForSSEEventCount(t, f.clk, rec, 1, 2*time.Second)

	// Deliver a message into INBOX via the same store.InsertMessage
	// path inbound delivery (protosmtp) uses.
	mboxes, err := f.store.Meta().ListMailboxes(ctx, f.pid)
	if err != nil {
		t.Fatalf("ListMailboxes: %v", err)
	}
	var inboxID store.MailboxID
	for _, m := range mboxes {
		if m.Name == "INBOX" {
			inboxID = m.ID
			break
		}
	}
	if inboxID == 0 {
		t.Fatalf("INBOX not found")
	}
	ref, err := f.store.Blobs().Put(ctx, strings.NewReader("body"))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	now := f.clk.Now().UTC()
	if _, _, err := f.store.Meta().InsertMessage(ctx, store.Message{
		Blob:         ref,
		Size:         ref.Size,
		InternalDate: now,
		ReceivedAt:   now,
	}, []store.MessageMailbox{{MailboxID: inboxID}}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}

	// Let the push loop's poll + coalesce cadence notice the delivery
	// and flush a second StateChange.
	waitForSSEEventCount(t, f.clk, rec, 2, 2*time.Second)

	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("runEventSource returned: %v", err)
	}

	events := parseSSEStateChanges(t, rec.bytes())
	if len(events) != 2 {
		t.Fatalf("got %d StateChange events, want 2; body = %q", len(events), rec.bytes())
	}
	acct := string(protojmap.AccountIDForPrincipal(f.pid))

	initial := events[0].Changed[acct]
	if _, ok := initial["Email"]; !ok {
		t.Fatalf("initial push missing Email: %v", initial)
	}
	if _, ok := initial["InternalizeStatus"]; !ok {
		t.Fatalf("initial push missing InternalizeStatus (first push after connect must carry every subscribed type): %v", initial)
	}

	deliveryPush := events[1].Changed[acct]
	if _, ok := deliveryPush["Email"]; !ok {
		t.Fatalf("delivery push missing Email: %v", deliveryPush)
	}
	if _, ok := deliveryPush["InternalizeStatus"]; ok {
		t.Fatalf("delivery push must not repeat InternalizeStatus (its state did not change): %v", deliveryPush)
	}
}

// TestEventSource_InternalizeStatusTransitionPushesInternalizeStatus
// is the mirror case: an internalize-status transition (the extimg
// worker's IncrementJMAPState + AppendStateChange pair) must push
// InternalizeStatus, and must NOT repeat Email (whose change-feed seq
// did not move).
func TestEventSource_InternalizeStatusTransitionPushesInternalizeStatus(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	p, err := f.store.Meta().GetPrincipalByID(ctx, f.pid)
	if err != nil {
		t.Fatalf("GetPrincipalByID: %v", err)
	}

	types := map[string]struct{}{"Email": {}, "InternalizeStatus": {}}
	rec := newFlushRecorder()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- f.srv.RunEventSourceForTest(
			runCtx, slog.Default(), rec, rec, p, types,
			false, 60*time.Second, f.store.Meta().ReadChangeFeed,
		)
	}()

	waitForSSEEventCount(t, f.clk, rec, 1, 2*time.Second)

	// Mirror the extimg internalizeworker batch-end sequence (re
	// internal/extimg/internalizeworker/worker.go): bump the counter,
	// then append the paired EntityKindInternalizeStatus change-feed
	// row that arms the EventSource flush timer.
	if _, err := f.store.Meta().IncrementJMAPState(ctx, f.pid, store.JMAPStateKindInternalizeStatus); err != nil {
		t.Fatalf("IncrementJMAPState: %v", err)
	}
	if err := f.store.Meta().AppendStateChange(ctx, store.StateChange{
		PrincipalID: f.pid,
		Kind:        store.EntityKindInternalizeStatus,
		Op:          store.ChangeOpUpdated,
		Cause:       store.ChangeCauseUser,
	}); err != nil {
		t.Fatalf("AppendStateChange: %v", err)
	}

	waitForSSEEventCount(t, f.clk, rec, 2, 2*time.Second)

	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("runEventSource returned: %v", err)
	}

	events := parseSSEStateChanges(t, rec.bytes())
	if len(events) != 2 {
		t.Fatalf("got %d StateChange events, want 2; body = %q", len(events), rec.bytes())
	}
	acct := string(protojmap.AccountIDForPrincipal(f.pid))

	transitionPush := events[1].Changed[acct]
	if _, ok := transitionPush["InternalizeStatus"]; !ok {
		t.Fatalf("transition push missing InternalizeStatus: %v", transitionPush)
	}
	if _, ok := transitionPush["Email"]; ok {
		t.Fatalf("transition push must not repeat Email (its state did not change): %v", transitionPush)
	}
}
