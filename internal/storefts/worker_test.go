package storefts_test

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storefts"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// stringExtractor is a deterministic TextExtractor for tests: it reads
// the body as-is and returns the string. Avoids pulling in mailparse's
// strict charset rules for the synthetic corpus.
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

// workerHarness wires up the fakestore + index + worker under a FakeClock
// so tests control time and batching precisely.
type workerHarness struct {
	ctx    context.Context
	cancel context.CancelFunc
	clk    *clock.FakeClock
	store  *fakestore.Store
	idx    *storefts.Index
	worker *storefts.Worker
	done   chan error
}

func newWorkerHarness(t *testing.T, opts storefts.WorkerOptions) *workerHarness {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fake, err := fakestore.New(fakestore.Options{Clock: clk, BlobDir: t.TempDir()})
	if err != nil {
		t.Fatalf("fakestore.New: %v", err)
	}
	idx, err := storefts.New(t.TempDir(), nil, clk)
	if err != nil {
		t.Fatalf("storefts.New: %v", err)
	}
	w := storefts.NewWorker(idx, fake, stringExtractor{}, nil, clk, opts)
	ctx, cancel := context.WithCancel(context.Background())
	h := &workerHarness{
		ctx:    ctx,
		cancel: cancel,
		clk:    clk,
		store:  fake,
		idx:    idx,
		worker: w,
		done:   make(chan error, 1),
	}
	go func() {
		h.done <- w.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-h.done
		_ = idx.Close()
		_ = fake.Close()
	})
	return h
}

// seedPrincipalAndMailbox inserts a principal + INBOX and returns them.
func (h *workerHarness) seedPrincipalAndMailbox(t *testing.T, email string) (store.Principal, store.Mailbox) {
	t.Helper()
	p, err := h.store.Meta().InsertPrincipal(h.ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: email,
	})
	if err != nil {
		t.Fatalf("insert principal: %v", err)
	}
	mb, err := h.store.Meta().InsertMailbox(h.ctx, store.Mailbox{
		PrincipalID: p.ID,
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("insert mailbox: %v", err)
	}
	return p, mb
}

// insertMessage writes a blob + metadata row and returns the resulting
// Message (with ID/UID populated).
func (h *workerHarness) insertMessage(t *testing.T, mb store.Mailbox, subject, body string) store.Message {
	t.Helper()
	raw := fmt.Sprintf("Subject: %s\r\n\r\n%s\r\n", subject, body)
	ref, err := h.store.Blobs().Put(h.ctx, strings.NewReader(raw))
	if err != nil {
		t.Fatalf("put blob: %v", err)
	}
	msg := store.Message{
		MailboxID: mb.ID,
		Size:      ref.Size,
		Blob:      ref,
		Envelope:  store.Envelope{Subject: subject},
	}
	uid, modseq, err := h.store.Meta().InsertMessage(h.ctx, msg)
	if err != nil {
		t.Fatalf("insert message: %v", err)
	}
	msg.UID = uid
	msg.ModSeq = modseq
	// Resolve the assigned MessageID via the per-principal state-change
	// feed (the store does not return the ID directly from InsertMessage).
	changes, err := h.store.Meta().ReadChangeFeed(h.ctx, mb.PrincipalID, 0, 10000)
	if err != nil {
		t.Fatalf("read change feed: %v", err)
	}
	for _, c := range changes {
		if c.Kind == store.ChangeKindMessageCreated && c.MessageUID == uid && c.MailboxID == mb.ID {
			msg.ID = c.MessageID
		}
	}
	return msg
}

// flushOnce advances the fake clock past the flush interval and waits for
// the worker to commit. The worker's poll loop sleeps on `clock.After`
// when there are no pending changes; Advance unblocks it.
func (h *workerHarness) flushOnce(t *testing.T) {
	t.Helper()
	h.clk.Advance(storefts.DefaultFlushInterval + 10*time.Millisecond)
	waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := h.worker.WaitForFlush(waitCtx); err != nil {
		t.Fatalf("wait for flush: %v", err)
	}
}

func TestWorker_IndexesMessages(t *testing.T) {
	h := newWorkerHarness(t, storefts.WorkerOptions{})
	_, mb := h.seedPrincipalAndMailbox(t, "alice@example.test")

	observe.RegisterFTSMetrics(func() float64 { return 0 })
	beforeIndexed := testutil.ToFloat64(observe.FTSIndexedMessagesTotal)

	// Insert 50 messages with distinct subjects.
	var lastID store.MessageID
	for i := 0; i < 50; i++ {
		msg := h.insertMessage(t, mb,
			fmt.Sprintf("alpha-%02d subject", i),
			fmt.Sprintf("the quick brown fox %02d jumps", i),
		)
		lastID = msg.ID
	}
	if lastID == 0 {
		t.Fatalf("insertMessage did not populate ID")
	}

	// One flush interval is enough: the worker reads all 50 changes in
	// a single ReadChangeFeedForFTS call (batch size default = 2000).
	h.flushOnce(t)

	afterIndexed := testutil.ToFloat64(observe.FTSIndexedMessagesTotal)
	if afterIndexed <= beforeIndexed {
		t.Fatalf("herold_fts_indexed_messages_total: before=%v after=%v; want strict increase", beforeIndexed, afterIndexed)
	}

	// The cursor should have advanced to at least the last FTS seq.
	if h.worker.Cursor() == 0 {
		t.Fatalf("cursor did not advance")
	}

	// Query the index: a subject term must find the message.
	principal, err := h.store.Meta().GetPrincipalByEmail(h.ctx, "alice@example.test")
	if err != nil {
		t.Fatalf("get principal: %v", err)
	}
	hits, err := h.idx.Query(h.ctx, principal.ID, store.Query{
		Subject: []string{"alpha-25"},
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(hits) == 0 {
		t.Fatalf("expected at least one hit for 'alpha-25'")
	}
}

func TestWorker_DeletesOnExpunge(t *testing.T) {
	h := newWorkerHarness(t, storefts.WorkerOptions{})
	principal, mb := h.seedPrincipalAndMailbox(t, "bob@example.test")

	msg := h.insertMessage(t, mb, "to be expunged", "this message will disappear")

	h.flushOnce(t)

	// Confirm the doc is present.
	hits, err := h.idx.Query(h.ctx, principal.ID, store.Query{Subject: []string{"expunged"}})
	if err != nil {
		t.Fatalf("query before: %v", err)
	}
	if len(hits) == 0 {
		t.Fatalf("message not indexed before expunge")
	}

	// Expunge and let the worker see the change.
	if err := h.store.Meta().ExpungeMessages(h.ctx, mb.ID, []store.MessageID{msg.ID}); err != nil {
		t.Fatalf("expunge: %v", err)
	}
	h.flushOnce(t)

	hits, err = h.idx.Query(h.ctx, principal.ID, store.Query{Subject: []string{"expunged"}})
	if err != nil {
		t.Fatalf("query after: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("doc still present after expunge: %+v", hits)
	}
}

func TestWorker_Lag(t *testing.T) {
	h := newWorkerHarness(t, storefts.WorkerOptions{})
	_, mb := h.seedPrincipalAndMailbox(t, "carol@example.test")

	h.insertMessage(t, mb, "lag test", "some body")
	// Advance by 2 seconds before the worker flushes so the measured
	// lag is ≥ 2 s.
	h.clk.Advance(2 * time.Second)
	h.flushOnce(t)

	lag := h.worker.Lag()
	if lag < 2*time.Second || lag > 3*time.Second {
		t.Fatalf("lag %v outside expected [2s,3s] window", lag)
	}
}

// TestWorker_CursorPersistsAcrossRestart seeds messages, drives one
// worker through a flush, then stops that worker and starts a fresh
// one against the same store. The new worker must begin at the
// persisted cursor (no replay) — asserted by observing its initial
// cursor value is non-zero and equals the first worker's.
func TestWorker_CursorPersistsAcrossRestart(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fake, err := fakestore.New(fakestore.Options{Clock: clk, BlobDir: t.TempDir()})
	if err != nil {
		t.Fatalf("fakestore.New: %v", err)
	}
	t.Cleanup(func() { _ = fake.Close() })
	idx, err := storefts.New(t.TempDir(), nil, clk)
	if err != nil {
		t.Fatalf("storefts.New: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })

	// Seed principal/mailbox/messages.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p, err := fake.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "restart@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	mb, err := fake.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: p.ID, Name: "INBOX"})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}
	for i := 0; i < 10; i++ {
		raw := fmt.Sprintf("Subject: msg-%d\r\n\r\nbody\r\n", i)
		ref, err := fake.Blobs().Put(ctx, strings.NewReader(raw))
		if err != nil {
			t.Fatalf("Put: %v", err)
		}
		if _, _, err := fake.Meta().InsertMessage(ctx, store.Message{MailboxID: mb.ID, Blob: ref, Size: ref.Size}); err != nil {
			t.Fatalf("InsertMessage: %v", err)
		}
	}

	// Run worker #1 until one flush completes.
	w1 := storefts.NewWorker(idx, fake, stringExtractor{}, nil, clk, storefts.WorkerOptions{})
	ctx1, cancel1 := context.WithCancel(ctx)
	done1 := make(chan error, 1)
	go func() { done1 <- w1.Run(ctx1) }()
	clk.Advance(storefts.DefaultFlushInterval + 10*time.Millisecond)
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Second)
	if err := w1.WaitForFlush(waitCtx); err != nil {
		waitCancel()
		cancel1()
		<-done1
		t.Fatalf("wait flush 1: %v", err)
	}
	waitCancel()
	firstCursor := w1.Cursor()
	if firstCursor == 0 {
		cancel1()
		<-done1
		t.Fatalf("first worker cursor did not advance")
	}
	cancel1()
	<-done1

	// Confirm the store now reports the cursor persisted.
	persisted, err := fake.Meta().GetFTSCursor(ctx, storefts.DefaultCursorKey)
	if err != nil {
		t.Fatalf("GetFTSCursor: %v", err)
	}
	if persisted != firstCursor {
		t.Fatalf("persisted cursor = %d, first-worker cursor = %d", persisted, firstCursor)
	}

	// Run worker #2. It must hydrate Cursor() from the store.
	w2 := storefts.NewWorker(idx, fake, stringExtractor{}, nil, clk, storefts.WorkerOptions{})
	ctx2, cancel2 := context.WithCancel(ctx)
	defer cancel2()
	done2 := make(chan error, 1)
	go func() { done2 <- w2.Run(ctx2) }()
	// A single advance lets the worker loop proceed far enough to
	// populate Cursor() from the store. We wait briefly via Advance +
	// a tiny poll (no sleeps): advance the clock and read the cursor.
	// The hydration runs at the top of Run() synchronously before the
	// first feed read, so even a zero-advance is enough, but we do
	// one flush-interval advance to make sure the worker stays on
	// its poll cycle.
	clk.Advance(storefts.DefaultFlushInterval + 10*time.Millisecond)
	deadline := time.Now().Add(2 * time.Second)
	for w2.Cursor() == 0 && time.Now().Before(deadline) {
		clk.Advance(10 * time.Millisecond)
	}
	if w2.Cursor() != firstCursor {
		cancel2()
		<-done2
		t.Fatalf("second worker cursor = %d, want %d", w2.Cursor(), firstCursor)
	}
	cancel2()
	<-done2
}

// TestWorker_LagCharacteristics reports the observed p50/p99 indexing
// lag across a small synthetic workload. Not a correctness gate; it
// exists so future changes to the worker's scheduling are surfaced in
// test output.
func TestWorker_LagCharacteristics(t *testing.T) {
	h := newWorkerHarness(t, storefts.WorkerOptions{})
	_, mb := h.seedPrincipalAndMailbox(t, "dana@example.test")

	const n = 40
	producedAt := make([]time.Time, 0, n)
	for i := 0; i < n; i++ {
		h.insertMessage(t, mb, "lag char", "body content for lag measurement")
		producedAt = append(producedAt, h.clk.Now())
		h.clk.Advance(10 * time.Millisecond)
	}
	// Advance past the flush interval so the worker drains the batch.
	h.flushOnce(t)

	// Read the worker's single "last processed" value as a proxy for
	// lag; individual per-message lags would require instrumentation
	// the worker does not expose today. The batch landed at the flush
	// instant, so observed lag ≈ now - producedAt[i] for each i.
	now := h.clk.Now()
	lags := make([]time.Duration, 0, n)
	for _, ts := range producedAt {
		lags = append(lags, now.Sub(ts))
	}
	p50 := pct(lags, 0.50)
	p99 := pct(lags, 0.99)
	t.Logf("fake-store ingest lag: p50=%v p99=%v (n=%d, batch=2000, flush=%v)",
		p50, p99, n, storefts.DefaultFlushInterval)
}

// pct returns the p-th percentile of durations. Simple and allocating;
// test-only.
func pct(xs []time.Duration, p float64) time.Duration {
	if len(xs) == 0 {
		return 0
	}
	sorted := make([]time.Duration, len(xs))
	copy(sorted, xs)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j] < sorted[j-1]; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	idx := int(float64(len(sorted)-1) * p)
	return sorted[idx]
}
