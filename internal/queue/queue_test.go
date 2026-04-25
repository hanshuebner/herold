package queue_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/queue"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
)

// -- test fixtures ----------------------------------------------------

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

type fakeDeliverer struct {
	mu               sync.Mutex
	calls            []queue.DeliveryRequest
	hooks            func(req queue.DeliveryRequest, n int) (queue.DeliveryOutcome, error)
	concurrentByHost map[string]int
	maxByHost        map[string]int
	delay            time.Duration
	clk              clock.Clock
}

func newFakeDeliverer() *fakeDeliverer {
	return &fakeDeliverer{
		concurrentByHost: make(map[string]int),
		maxByHost:        make(map[string]int),
	}
}

func (f *fakeDeliverer) Deliver(ctx context.Context, req queue.DeliveryRequest) (queue.DeliveryOutcome, error) {
	f.mu.Lock()
	host := hostOf(req.Recipient)
	f.concurrentByHost[host]++
	if f.concurrentByHost[host] > f.maxByHost[host] {
		f.maxByHost[host] = f.concurrentByHost[host]
	}
	f.calls = append(f.calls, req)
	n := len(f.calls)
	hooks := f.hooks
	f.mu.Unlock()

	defer func() {
		f.mu.Lock()
		f.concurrentByHost[host]--
		f.mu.Unlock()
	}()

	if f.delay > 0 {
		select {
		case <-ctx.Done():
			return queue.DeliveryOutcome{}, ctx.Err()
		case <-time.After(f.delay):
		}
	}
	if hooks != nil {
		return hooks(req, n)
	}
	return queue.DeliveryOutcome{Status: queue.DeliveryStatusSuccess}, nil
}

func (f *fakeDeliverer) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeDeliverer) callsCopy() []queue.DeliveryRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]queue.DeliveryRequest, len(f.calls))
	copy(out, f.calls)
	return out
}

func (f *fakeDeliverer) maxConcurrent(host string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxByHost[host]
}

func hostOf(rcpt string) string {
	at := strings.LastIndexByte(rcpt, '@')
	if at < 0 {
		return ""
	}
	return strings.ToLower(rcpt[at+1:])
}

type recordingSigner struct {
	calls atomic.Int32
	tag   string
}

func (s *recordingSigner) Sign(ctx context.Context, domain string, message []byte) ([]byte, error) {
	s.calls.Add(1)
	out := append([]byte("X-Test-Signed: "+domain+";\r\n"), message...)
	if s.tag != "" {
		out = append([]byte("X-Tag: "+s.tag+"\r\n"), out...)
	}
	return out, nil
}

type fixture struct {
	t      *testing.T
	clk    *clock.FakeClock
	store  *fakestore.Store
	deliv  *fakeDeliverer
	queue  *queue.Queue
	cancel context.CancelFunc
	ctx    context.Context
	wg     sync.WaitGroup
}

type fixtureOpts struct {
	concurrency  int
	perHost      int
	pollInterval time.Duration
	retry        queue.RetryPolicy
	signer       queue.Signer
	skipRun      bool
}

func newFixture(t *testing.T, opts fixtureOpts) *fixture {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := fakestore.New(fakestore.Options{Clock: clk, BlobDir: t.TempDir()})
	if err != nil {
		t.Fatalf("fakestore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	deliv := newFakeDeliverer()
	if opts.pollInterval == 0 {
		opts.pollInterval = 50 * time.Millisecond
	}
	q := queue.New(queue.Options{
		Store:          st,
		Deliverer:      deliv,
		Signer:         opts.signer,
		Logger:         discardLogger(),
		Clock:          clk,
		Concurrency:    opts.concurrency,
		PerHostMax:     opts.perHost,
		Retry:          opts.retry,
		PollInterval:   opts.pollInterval,
		Hostname:       "mail.test.example",
		DSNFromAddress: "postmaster@mail.test.example",
		ShutdownGrace:  2 * time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	f := &fixture{
		t:      t,
		clk:    clk,
		store:  st,
		deliv:  deliv,
		queue:  q,
		cancel: cancel,
		ctx:    ctx,
	}
	if !opts.skipRun {
		f.wg.Add(1)
		go func() {
			defer f.wg.Done()
			_ = q.Run(ctx)
		}()
	}
	t.Cleanup(func() {
		cancel()
		f.wg.Wait()
	})
	return f
}

func (f *fixture) submit(t *testing.T, sub queue.Submission) queue.EnvelopeID {
	t.Helper()
	envID, err := f.queue.Submit(f.ctx, sub)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	return envID
}

// -- tests ------------------------------------------------------------

func TestSubmitSuccess(t *testing.T) {
	f := newFixture(t, fixtureOpts{concurrency: 4, perHost: 2})
	envID := f.submit(t, queue.Submission{
		MailFrom:   "alice@local.test",
		Recipients: []string{"bob@dest.test"},
		Body:       strings.NewReader("Subject: hi\r\n\r\nbody\r\n"),
		Sign:       false,
	})
	if envID == "" {
		t.Fatal("expected envelope id")
	}
	// Tick until delivery completes.
	if !waitFor(t, 2*time.Second, func() bool {
		return f.deliv.callCount() >= 1
	}) {
		t.Fatal("deliver never called")
	}
	// Drive the scheduler and let the worker complete: advance the
	// clock once to wake the scheduler poll, then poll for done state.
	if !waitFor(t, 2*time.Second, func() bool {
		s, err := f.queue.Stats(f.ctx)
		if err != nil {
			return false
		}
		return s.Done >= 1
	}) {
		t.Fatalf("row never reached done state: %+v", mustStats(t, f))
	}

	rows, err := f.store.Meta().ListQueueItems(f.ctx, store.QueueFilter{EnvelopeID: envID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row; got %d", len(rows))
	}
	if rows[0].State != store.QueueStateDone {
		t.Fatalf("state: got %s want done", rows[0].State)
	}
}

func TestSubmitTransientThenSuccess(t *testing.T) {
	f := newFixture(t, fixtureOpts{
		concurrency: 4,
		perHost:     2,
		retry:       queue.RetryPolicy{Schedule: []time.Duration{30 * time.Second, time.Minute}},
	})
	first := atomic.Int32{}
	f.deliv.hooks = func(req queue.DeliveryRequest, n int) (queue.DeliveryOutcome, error) {
		if first.Add(1) == 1 {
			return queue.DeliveryOutcome{
				Status: queue.DeliveryStatusTransient,
				Code:   451,
				Detail: "try again",
			}, nil
		}
		return queue.DeliveryOutcome{Status: queue.DeliveryStatusSuccess}, nil
	}

	envID := f.submit(t, queue.Submission{
		MailFrom:   "alice@local.test",
		Recipients: []string{"bob@dest.test"},
		Body:       strings.NewReader("Subject: hi\r\n\r\nbody\r\n"),
	})
	// Wait for transient outcome to be observed.
	if !waitFor(t, 2*time.Second, func() bool {
		rows, _ := f.store.Meta().ListQueueItems(f.ctx, store.QueueFilter{EnvelopeID: envID})
		return len(rows) == 1 && rows[0].State == store.QueueStateDeferred && rows[0].Attempts == 1
	}) {
		t.Fatalf("never observed deferred state")
	}

	// Advance the clock past the next-attempt window.
	f.clk.Advance(45 * time.Second)
	if !waitFor(t, 2*time.Second, func() bool {
		rows, _ := f.store.Meta().ListQueueItems(f.ctx, store.QueueFilter{EnvelopeID: envID})
		return len(rows) == 1 && rows[0].State == store.QueueStateDone
	}) {
		t.Fatalf("never re-delivered after clock advance")
	}
	if got := f.deliv.callCount(); got != 2 {
		t.Fatalf("expected 2 deliver calls; got %d", got)
	}
}

func TestSubmitIdempotent(t *testing.T) {
	f := newFixture(t, fixtureOpts{skipRun: true})
	body := "Subject: hi\r\n\r\nhello\r\n"
	envID1, err := f.queue.Submit(f.ctx, queue.Submission{
		MailFrom:       "alice@local.test",
		Recipients:     []string{"bob@dest.test"},
		Body:           strings.NewReader(body),
		IdempotencyKey: "k1",
	})
	if err != nil {
		t.Fatalf("first submit: %v", err)
	}
	envID2, err := f.queue.Submit(f.ctx, queue.Submission{
		MailFrom:       "alice@local.test",
		Recipients:     []string{"bob@dest.test"},
		Body:           strings.NewReader(body),
		IdempotencyKey: "k1",
	})
	if !errors.Is(err, queue.ErrConflict) {
		t.Fatalf("second submit err: got %v want %v", err, queue.ErrConflict)
	}
	if envID1 != envID2 {
		t.Fatalf("envelope ids differ: %s vs %s", envID1, envID2)
	}
	rows, _ := f.store.Meta().ListQueueItems(f.ctx, store.QueueFilter{EnvelopeID: envID1})
	if len(rows) != 1 {
		t.Fatalf("expected 1 row; got %d", len(rows))
	}
}

func TestPermanentFailureEmitsDSN(t *testing.T) {
	f := newFixture(t, fixtureOpts{concurrency: 4, perHost: 2})
	f.deliv.hooks = func(req queue.DeliveryRequest, n int) (queue.DeliveryOutcome, error) {
		// First delivery: permanent. Second delivery (the DSN): success.
		if !strings.HasPrefix(req.Recipient, "alice@") {
			return queue.DeliveryOutcome{
				Status:       queue.DeliveryStatusPermanent,
				Code:         550,
				EnhancedCode: "5.1.1",
				Detail:       "no such user",
			}, nil
		}
		return queue.DeliveryOutcome{Status: queue.DeliveryStatusSuccess}, nil
	}
	envID := f.submit(t, queue.Submission{
		MailFrom:   "alice@local.test",
		Recipients: []string{"ghost@dest.test"},
		Body:       strings.NewReader("Subject: hi\r\n\r\nhello\r\n"),
		DSNNotify:  store.DSNNotifyFailure,
	})
	// Wait for the original to fail and for the DSN to enqueue.
	if !waitFor(t, 3*time.Second, func() bool {
		rows, _ := f.store.Meta().ListQueueItems(f.ctx, store.QueueFilter{EnvelopeID: envID})
		if len(rows) != 1 {
			return false
		}
		return rows[0].State == store.QueueStateFailed
	}) {
		t.Fatalf("original never failed")
	}

	// Find the DSN row. It is addressed to alice@local.test with null sender.
	var dsnRow store.QueueItem
	if !waitFor(t, 3*time.Second, func() bool {
		rows, _ := f.store.Meta().ListQueueItems(f.ctx, store.QueueFilter{})
		for _, r := range rows {
			if r.RcptTo == "alice@local.test" && r.MailFrom == "" {
				dsnRow = r
				return true
			}
		}
		return false
	}) {
		t.Fatal("DSN row never enqueued")
	}

	// Read back the DSN body and assert structure.
	rdr, err := f.store.Blobs().Get(f.ctx, dsnRow.BodyBlobHash)
	if err != nil {
		t.Fatalf("get dsn body: %v", err)
	}
	defer rdr.Close()
	body, err := io.ReadAll(rdr)
	if err != nil {
		t.Fatalf("read dsn body: %v", err)
	}
	bodyStr := string(body)
	for _, want := range []string{
		"multipart/report",
		"report-type=delivery-status",
		"Auto-Submitted: auto-replied",
		"From: postmaster@mail.test.example",
		"To: alice@local.test",
		"message/delivery-status",
		"Reporting-MTA: dns; mail.test.example",
		"Final-Recipient: rfc822;ghost@dest.test",
		"Action: failed",
		"Status: 5.1.1",
		"Diagnostic-Code: smtp; 550 5.1.1 no such user",
		"message/rfc822-headers",
	} {
		if !strings.Contains(bodyStr, want) {
			t.Errorf("dsn body missing %q\n--BODY--\n%s\n--END--", want, bodyStr)
		}
	}
}

func TestPerHostConcurrencyCap(t *testing.T) {
	f := newFixture(t, fixtureOpts{
		concurrency: 16,
		perHost:     2,
	})
	gate := make(chan struct{})
	f.deliv.hooks = func(req queue.DeliveryRequest, n int) (queue.DeliveryOutcome, error) {
		<-gate
		return queue.DeliveryOutcome{Status: queue.DeliveryStatusSuccess}, nil
	}
	for i := 0; i < 5; i++ {
		f.submit(t, queue.Submission{
			MailFrom:   "alice@local.test",
			Recipients: []string{fmt.Sprintf("user%d@busy.test", i)},
			Body:       strings.NewReader("Subject: hi\r\n\r\nbody\r\n"),
		})
	}
	// Drive the FakeClock so the scheduler polls. PerHostMax = 2
	// caps the in-flight count even though all 5 rows are due.
	for tries := 0; tries < 30 && f.deliv.maxConcurrent("busy.test") < 2; tries++ {
		f.clk.Advance(60 * time.Millisecond)
		time.Sleep(20 * time.Millisecond)
	}
	if !waitFor(t, 2*time.Second, func() bool {
		return f.deliv.maxConcurrent("busy.test") >= 2
	}) {
		t.Fatal("never reached perHost=2 concurrency")
	}
	// At this point exactly 2 calls are in-flight; the other 3 rows
	// were deferred by the scheduler. Release the gate.
	close(gate)
	// Drive the FakeClock forward so each subsequent poll picks up
	// the deferred rows; cap iterations to avoid infinite loops on
	// regression.
	for tries := 0; tries < 50 && f.deliv.callCount() < 5; tries++ {
		time.Sleep(20 * time.Millisecond)
		f.clk.Advance(60 * time.Millisecond)
	}
	if got := f.deliv.callCount(); got < 5 {
		t.Fatalf("calls: got %d want 5", got)
	}
	if got := f.deliv.maxConcurrent("busy.test"); got > 2 {
		t.Fatalf("perHost cap breached: %d > 2", got)
	}
}

func TestRetryExhaustionEmitsFailureDSN(t *testing.T) {
	f := newFixture(t, fixtureOpts{
		concurrency: 4,
		perHost:     2,
		retry: queue.RetryPolicy{Schedule: []time.Duration{
			time.Minute, time.Minute, time.Minute,
		}},
	})
	f.deliv.hooks = func(req queue.DeliveryRequest, n int) (queue.DeliveryOutcome, error) {
		if strings.HasPrefix(req.Recipient, "alice@") {
			return queue.DeliveryOutcome{Status: queue.DeliveryStatusSuccess}, nil
		}
		return queue.DeliveryOutcome{
			Status: queue.DeliveryStatusTransient,
			Code:   421,
			Detail: "service unavailable",
		}, nil
	}
	envID := f.submit(t, queue.Submission{
		MailFrom:   "alice@local.test",
		Recipients: []string{"bob@dest.test"},
		Body:       strings.NewReader("Subject: hi\r\n\r\nbody\r\n"),
		DSNNotify:  store.DSNNotifyFailure,
	})
	// Initial attempt + 3 reschedules = 4 total.
	for i := 0; i < 4; i++ {
		// Wait for the (i+1)-th call.
		if !waitFor(t, 2*time.Second, func() bool {
			return f.deliv.callCount() >= i+1
		}) {
			t.Fatalf("attempt %d never observed", i+1)
		}
		// After the call, advance to release the next schedule.
		f.clk.Advance(2 * time.Minute)
	}
	if !waitFor(t, 3*time.Second, func() bool {
		rows, _ := f.store.Meta().ListQueueItems(f.ctx, store.QueueFilter{EnvelopeID: envID})
		return len(rows) == 1 && rows[0].State == store.QueueStateFailed
	}) {
		t.Fatalf("never failed: %+v", mustStats(t, f))
	}
	// DSN should be present.
	if !waitFor(t, 3*time.Second, func() bool {
		rows, _ := f.store.Meta().ListQueueItems(f.ctx, store.QueueFilter{})
		for _, r := range rows {
			if r.RcptTo == "alice@local.test" && r.MailFrom == "" {
				return true
			}
		}
		return false
	}) {
		t.Fatalf("DSN never enqueued after exhaustion")
	}
}

func TestStaleInflightRecovery(t *testing.T) {
	// Build a fixture but do not start Run yet.
	f := newFixture(t, fixtureOpts{
		concurrency:  4,
		perHost:      2,
		pollInterval: 50 * time.Millisecond,
		skipRun:      true,
	})

	// Persist a body and pre-stage a row in inflight state with old
	// LastAttemptAt.
	body := "Subject: hi\r\n\r\nbody\r\n"
	bref, err := f.store.Blobs().Put(f.ctx, strings.NewReader(body))
	if err != nil {
		t.Fatalf("put blob: %v", err)
	}
	id, err := f.store.Meta().EnqueueMessage(f.ctx, store.QueueItem{
		MailFrom:      "alice@local.test",
		RcptTo:        "bob@dest.test",
		EnvelopeID:    "env-stale",
		BodyBlobHash:  bref.Hash,
		State:         store.QueueStateQueued,
		NextAttemptAt: f.clk.Now(),
		CreatedAt:     f.clk.Now(),
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	// Force the row into inflight via the claim+rewrite path: claim it first.
	if _, err := f.store.Meta().ClaimDueQueueItems(f.ctx, f.clk.Now(), 10); err != nil {
		t.Fatalf("claim: %v", err)
	}
	// Now advance the clock so the LastAttemptAt looks stale relative
	// to the new "now" once Run starts.
	f.clk.Advance(10 * time.Minute)

	// Start the queue worker.
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		_ = f.queue.Run(f.ctx)
	}()

	// The recovery sweep should kick the row back to queued, then the
	// scheduler picks it up and the worker delivers it.
	if !waitFor(t, 3*time.Second, func() bool {
		row, err := f.store.Meta().GetQueueItem(f.ctx, id)
		if err != nil {
			return false
		}
		return row.State == store.QueueStateDone
	}) {
		row, _ := f.store.Meta().GetQueueItem(f.ctx, id)
		t.Fatalf("stale row not recovered: %+v", row)
	}
}

func TestSchedulerResumesOnClockAdvance(t *testing.T) {
	f := newFixture(t, fixtureOpts{
		concurrency:  4,
		perHost:      2,
		pollInterval: time.Hour, // huge so we depend on clock advance
	})
	f.submit(t, queue.Submission{
		MailFrom:   "alice@local.test",
		Recipients: []string{"bob@dest.test"},
		Body:       strings.NewReader("Subject: hi\r\n\r\nbody\r\n"),
	})
	// Without advancing the clock, the scheduler may have already
	// completed one initial poll. Allow up to one poll in: but on
	// FakeClock + huge interval, the second poll is gated on the
	// clock. The submission's NextAttemptAt = now() so the first
	// post-Run poll picks it up.
	if !waitFor(t, 2*time.Second, func() bool {
		s, _ := f.queue.Stats(f.ctx)
		return s.Done >= 1
	}) {
		// Fall back: advance the clock to simulate elapsed pollInterval.
		f.clk.Advance(2 * time.Hour)
	}
	if !waitFor(t, 2*time.Second, func() bool {
		s, _ := f.queue.Stats(f.ctx)
		return s.Done >= 1
	}) {
		t.Fatal("delivery never completed")
	}
}

func TestGracefulShutdownDrainsInflight(t *testing.T) {
	f := newFixture(t, fixtureOpts{
		concurrency: 4,
		perHost:     2,
		skipRun:     true,
	})
	gate := make(chan struct{})
	completed := atomic.Int32{}
	f.deliv.hooks = func(req queue.DeliveryRequest, n int) (queue.DeliveryOutcome, error) {
		<-gate
		completed.Add(1)
		return queue.DeliveryOutcome{Status: queue.DeliveryStatusSuccess}, nil
	}
	f.submit(t, queue.Submission{
		MailFrom:   "alice@local.test",
		Recipients: []string{"bob@dest.test"},
		Body:       strings.NewReader("Subject: hi\r\n\r\nbody\r\n"),
	})

	runDone := make(chan struct{})
	go func() {
		_ = f.queue.Run(f.ctx)
		close(runDone)
	}()

	// Wait until the worker is in the Deliver call (gated).
	if !waitFor(t, 2*time.Second, func() bool {
		return f.deliv.callCount() >= 1
	}) {
		t.Fatal("deliver never called")
	}
	// Trigger graceful shutdown.
	f.cancel()
	// Release the deliverer.
	close(gate)
	select {
	case <-runDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after shutdown")
	}
	if completed.Load() != 1 {
		t.Fatalf("delivery did not complete during drain: %d", completed.Load())
	}
}

func TestSignerInvoked(t *testing.T) {
	signer := &recordingSigner{}
	f := newFixture(t, fixtureOpts{
		concurrency: 4,
		perHost:     2,
		signer:      signer,
	})
	f.submit(t, queue.Submission{
		MailFrom:      "alice@local.test",
		Recipients:    []string{"bob@dest.test"},
		Body:          strings.NewReader("Subject: hi\r\n\r\nbody\r\n"),
		Sign:          true,
		SigningDomain: "local.test",
	})
	if !waitFor(t, 2*time.Second, func() bool {
		return f.deliv.callCount() >= 1
	}) {
		t.Fatal("deliver never called")
	}
	if signer.calls.Load() != 1 {
		t.Fatalf("signer call count: got %d want 1", signer.calls.Load())
	}
	calls := f.deliv.callsCopy()
	if len(calls) != 1 {
		t.Fatalf("delivery calls: %d", len(calls))
	}
	if !bytes.Contains(calls[0].Message, []byte("X-Test-Signed: local.test")) {
		t.Fatalf("delivered body lacks signing header: %q", calls[0].Message)
	}
}

func TestRetryPolicyNext(t *testing.T) {
	p := queue.RetryPolicy{Schedule: []time.Duration{time.Minute, 5 * time.Minute}}
	d, ok := p.Next(0)
	if !ok || d != time.Minute {
		t.Fatalf("Next(0): got %v %v", d, ok)
	}
	d, ok = p.Next(1)
	if !ok || d != 5*time.Minute {
		t.Fatalf("Next(1): got %v %v", d, ok)
	}
	if _, ok := p.Next(2); ok {
		t.Fatalf("Next(2): expected exhausted")
	}
}

func TestRetryPolicyDefault(t *testing.T) {
	p := queue.RetryPolicy{} // nil schedule uses default
	if d, ok := p.Next(0); !ok || d != 5*time.Minute {
		t.Fatalf("default first delay: got %v %v", d, ok)
	}
	if p.MaxAttempts() != len(queue.DefaultRetrySchedule)+1 {
		t.Fatalf("MaxAttempts: got %d", p.MaxAttempts())
	}
}

// -- REQ-PROTO-58 / REQ-FLOW-63 sendAt + Cancel ----------------------

func TestSubmit_SendAt_FutureTime_HoldsItem(t *testing.T) {
	f := newFixture(t, fixtureOpts{
		concurrency:  4,
		perHost:      2,
		pollInterval: 50 * time.Millisecond,
	})
	sendAt := f.clk.Now().Add(10 * time.Minute)
	envID := f.submit(t, queue.Submission{
		MailFrom:   "alice@local.test",
		Recipients: []string{"bob@dest.test"},
		Body:       strings.NewReader("Subject: scheduled\r\n\r\nbody\r\n"),
		SendAt:     sendAt,
	})
	// Run a few scheduler ticks; the row must stay in queued state and
	// the deliverer must not be called.
	for i := 0; i < 5; i++ {
		f.clk.Advance(60 * time.Millisecond)
		time.Sleep(20 * time.Millisecond)
	}
	if got := f.deliv.callCount(); got != 0 {
		t.Fatalf("deliverer called before sendAt: got %d", got)
	}
	rows, err := f.store.Meta().ListQueueItems(f.ctx, store.QueueFilter{EnvelopeID: envID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 || rows[0].State != store.QueueStateQueued {
		t.Fatalf("row state before sendAt: got %+v", rows)
	}
	if !rows[0].NextAttemptAt.Equal(sendAt) {
		t.Fatalf("NextAttemptAt: got %v want %v", rows[0].NextAttemptAt, sendAt)
	}
	stats, _ := f.queue.Stats(f.ctx)
	if stats.Queued != 1 || stats.Inflight != 0 {
		t.Fatalf("stats before sendAt: %+v", stats)
	}
	// Advance past sendAt; delivery should fire.
	f.clk.Advance(11 * time.Minute)
	if !waitFor(t, 2*time.Second, func() bool {
		return f.deliv.callCount() >= 1
	}) {
		t.Fatalf("deliverer not called after clock advance: stats %+v", mustStats(t, f))
	}
	if !waitFor(t, 2*time.Second, func() bool {
		s, _ := f.queue.Stats(f.ctx)
		return s.Done >= 1
	}) {
		t.Fatalf("row never reached done: %+v", mustStats(t, f))
	}
}

func TestSubmit_SendAt_PastTime_DeliversImmediately(t *testing.T) {
	f := newFixture(t, fixtureOpts{concurrency: 4, perHost: 2})
	past := f.clk.Now().Add(-time.Hour)
	envID := f.submit(t, queue.Submission{
		MailFrom:   "alice@local.test",
		Recipients: []string{"bob@dest.test"},
		Body:       strings.NewReader("Subject: past\r\n\r\nbody\r\n"),
		SendAt:     past,
	})
	if !waitFor(t, 2*time.Second, func() bool {
		s, _ := f.queue.Stats(f.ctx)
		return s.Done >= 1
	}) {
		t.Fatalf("past-sendAt row never delivered: %+v", mustStats(t, f))
	}
	rows, _ := f.store.Meta().ListQueueItems(f.ctx, store.QueueFilter{EnvelopeID: envID})
	if len(rows) != 1 || rows[0].State != store.QueueStateDone {
		t.Fatalf("row: %+v", rows)
	}
}

func TestSubmit_SendAt_ZeroTime_DeliversImmediately(t *testing.T) {
	f := newFixture(t, fixtureOpts{concurrency: 4, perHost: 2})
	envID := f.submit(t, queue.Submission{
		MailFrom:   "alice@local.test",
		Recipients: []string{"bob@dest.test"},
		Body:       strings.NewReader("Subject: now\r\n\r\nbody\r\n"),
		// SendAt left zero — must behave like "deliver now".
	})
	if !waitFor(t, 2*time.Second, func() bool {
		s, _ := f.queue.Stats(f.ctx)
		return s.Done >= 1
	}) {
		t.Fatalf("zero-sendAt row never delivered: %+v", mustStats(t, f))
	}
	rows, _ := f.store.Meta().ListQueueItems(f.ctx, store.QueueFilter{EnvelopeID: envID})
	if len(rows) != 1 || rows[0].State != store.QueueStateDone {
		t.Fatalf("row: %+v", rows)
	}
}

func TestCancel_BeforeDue_RemovesAllRows(t *testing.T) {
	f := newFixture(t, fixtureOpts{
		concurrency:  4,
		perHost:      2,
		pollInterval: 50 * time.Millisecond,
	})
	sendAt := f.clk.Now().Add(10 * time.Minute)
	envID := f.submit(t, queue.Submission{
		MailFrom:   "alice@local.test",
		Recipients: []string{"bob@dest.test", "carol@dest.test"},
		Body:       strings.NewReader("Subject: hold\r\n\r\nbody\r\n"),
		SendAt:     sendAt,
	})
	// Confirm both rows are queued.
	rows, _ := f.store.Meta().ListQueueItems(f.ctx, store.QueueFilter{EnvelopeID: envID})
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows pre-cancel, got %d", len(rows))
	}
	cancelled, inflight, err := f.queue.Cancel(f.ctx, envID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if cancelled != 2 || inflight != 0 {
		t.Fatalf("cancel counts: got cancelled=%d inflight=%d want 2/0", cancelled, inflight)
	}
	rows, _ = f.store.Meta().ListQueueItems(f.ctx, store.QueueFilter{EnvelopeID: envID})
	if len(rows) != 0 {
		t.Fatalf("expected 0 rows post-cancel, got %d", len(rows))
	}
	// Advance past the original sendAt; the deliverer must NOT fire.
	f.clk.Advance(15 * time.Minute)
	for i := 0; i < 5; i++ {
		time.Sleep(20 * time.Millisecond)
		f.clk.Advance(60 * time.Millisecond)
	}
	if got := f.deliv.callCount(); got != 0 {
		t.Fatalf("deliverer called after cancel: got %d", got)
	}
}

func TestCancel_AfterDelivery_NoOp(t *testing.T) {
	f := newFixture(t, fixtureOpts{concurrency: 4, perHost: 2})
	envID := f.submit(t, queue.Submission{
		MailFrom:   "alice@local.test",
		Recipients: []string{"bob@dest.test"},
		Body:       strings.NewReader("Subject: now\r\n\r\nbody\r\n"),
	})
	if !waitFor(t, 2*time.Second, func() bool {
		s, _ := f.queue.Stats(f.ctx)
		return s.Done >= 1
	}) {
		t.Fatalf("row never delivered: %+v", mustStats(t, f))
	}
	cancelled, inflight, err := f.queue.Cancel(f.ctx, envID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if cancelled != 0 || inflight != 0 {
		t.Fatalf("cancel after terminal: got cancelled=%d inflight=%d want 0/0",
			cancelled, inflight)
	}
}

func TestCancel_DuringInflight_ReportsInflight(t *testing.T) {
	f := newFixture(t, fixtureOpts{concurrency: 4, perHost: 2})
	gate := make(chan struct{})
	released := atomic.Bool{}
	f.deliv.hooks = func(req queue.DeliveryRequest, n int) (queue.DeliveryOutcome, error) {
		<-gate
		released.Store(true)
		return queue.DeliveryOutcome{Status: queue.DeliveryStatusSuccess}, nil
	}
	envID := f.submit(t, queue.Submission{
		MailFrom:   "alice@local.test",
		Recipients: []string{"bob@dest.test"},
		Body:       strings.NewReader("Subject: slow\r\n\r\nbody\r\n"),
	})
	// Wait for the worker to enter Deliver (gated).
	if !waitFor(t, 2*time.Second, func() bool {
		return f.deliv.callCount() >= 1
	}) {
		close(gate)
		t.Fatal("deliver never called")
	}
	cancelled, inflight, err := f.queue.Cancel(f.ctx, envID)
	if err != nil {
		close(gate)
		t.Fatalf("cancel: %v", err)
	}
	if cancelled != 0 || inflight < 1 {
		close(gate)
		t.Fatalf("cancel mid-inflight: got cancelled=%d inflight=%d want 0/>=1",
			cancelled, inflight)
	}
	// Release the deliverer so the test does not leak the gated worker.
	close(gate)
	if !waitFor(t, 2*time.Second, func() bool {
		return released.Load()
	}) {
		t.Fatal("worker did not complete after gate close")
	}
}

// -- helpers ----------------------------------------------------------

func waitFor(t *testing.T, timeout time.Duration, pred func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pred() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return pred()
}

func mustStats(t *testing.T, f *fixture) queue.Stats {
	t.Helper()
	s, err := f.queue.Stats(f.ctx)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	return s
}
