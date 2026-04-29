package queue_test

// activity_test.go: focused unit tests that assert REQ-OPS-86 activity
// tagging on high-value log records emitted by the queue package.
//
// Each test uses observe.AssertActivityTagged so CI catches missing tags
// automatically, and also verifies the specific activity value and slog
// level for the records the spec calls out.

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/queue"
	"github.com/hanshuebner/herold/internal/store"
)

// captureHandler is a minimal slog.Handler that records every emitted
// log record for post-test assertion.
type captureHandler struct {
	mu      sync.Mutex
	records []capturedRec
}

type capturedRec struct {
	level   slog.Level
	message string
	attrs   map[string]string
}

func (h *captureHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	cr := capturedRec{
		level:   r.Level,
		message: r.Message,
		attrs:   make(map[string]string),
	}
	r.Attrs(func(a slog.Attr) bool {
		cr.attrs[a.Key] = a.Value.String()
		return true
	})
	h.mu.Lock()
	h.records = append(h.records, cr)
	h.mu.Unlock()
	return nil
}

func (h *captureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	pre := make(map[string]string, len(attrs))
	for _, a := range attrs {
		pre[a.Key] = a.Value.String()
	}
	return &captureChildHandler{parent: h, pre: pre}
}

func (h *captureHandler) WithGroup(_ string) slog.Handler { return h }

func (h *captureHandler) snapshot() []capturedRec {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]capturedRec, len(h.records))
	copy(out, h.records)
	return out
}

type captureChildHandler struct {
	parent *captureHandler
	pre    map[string]string
}

func (c *captureChildHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (c *captureChildHandler) Handle(_ context.Context, r slog.Record) error {
	cr := capturedRec{
		level:   r.Level,
		message: r.Message,
		attrs:   make(map[string]string, len(c.pre)),
	}
	for k, v := range c.pre {
		cr.attrs[k] = v
	}
	r.Attrs(func(a slog.Attr) bool {
		cr.attrs[a.Key] = a.Value.String()
		return true
	})
	c.parent.mu.Lock()
	c.parent.records = append(c.parent.records, cr)
	c.parent.mu.Unlock()
	return nil
}

func (c *captureChildHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	merged := make(map[string]string, len(c.pre)+len(attrs))
	for k, v := range c.pre {
		merged[k] = v
	}
	for _, a := range attrs {
		merged[a.Key] = a.Value.String()
	}
	return &captureChildHandler{parent: c.parent, pre: merged}
}

func (c *captureChildHandler) WithGroup(_ string) slog.Handler { return c }

// newCaptureLogger returns a logger backed by a captureHandler plus a
// pointer to the handler for post-test inspection.
func newCaptureLogger() (*slog.Logger, *captureHandler) {
	h := &captureHandler{}
	return slog.New(h), h
}

// findRecord returns the first captured record whose message matches the
// predicate, or (zero, false) if not found.
func findRecord(recs []capturedRec, pred func(capturedRec) bool) (capturedRec, bool) {
	for _, r := range recs {
		if pred(r) {
			return r, true
		}
	}
	return capturedRec{}, false
}

// TestActivityTagged_DeliverySuccess verifies that the "queue: delivery
// success" record carries activity=system at info level (REQ-OPS-86).
func TestActivityTagged_DeliverySuccess(t *testing.T) {
	observe.AssertActivityTagged(t, func(log *slog.Logger) {
		f := newFixtureWithLogger(t, fixtureOpts{concurrency: 2, perHost: 1}, log)
		f.submit(t, queue.Submission{
			MailFrom:   "alice@local.test",
			Recipients: []string{"bob@dest.test"},
			Body:       strings.NewReader("Subject: hi\r\n\r\nbody\r\n"),
		})
		waitFor(t, 2*time.Second, func() bool {
			s, _ := f.queue.Stats(f.ctx)
			return s.Done >= 1
		})
	})

	// Also assert specific level + activity on the success record.
	log, cap := newCaptureLogger()
	f := newFixtureWithLogger(t, fixtureOpts{concurrency: 2, perHost: 1}, log)
	f.submit(t, queue.Submission{
		MailFrom:   "alice@local.test",
		Recipients: []string{"bob@dest.test"},
		Body:       strings.NewReader("Subject: hi\r\n\r\nbody\r\n"),
	})
	waitFor(t, 2*time.Second, func() bool {
		s, _ := f.queue.Stats(f.ctx)
		return s.Done >= 1
	})
	recs := cap.snapshot()
	r, ok := findRecord(recs, func(r capturedRec) bool {
		return r.message == "queue: delivery success"
	})
	if !ok {
		t.Fatal("delivery success log record not found")
	}
	if got := r.attrs["activity"]; got != observe.ActivitySystem {
		t.Errorf("delivery success activity: got %q want %q", got, observe.ActivitySystem)
	}
	if r.level != slog.LevelInfo {
		t.Errorf("delivery success level: got %v want Info", r.level)
	}
}

// TestActivityTagged_PollTick verifies that the queue poll tick record
// carries activity=poll at debug level (REQ-OPS-86).
func TestActivityTagged_PollTick(t *testing.T) {
	log, cap := newCaptureLogger()
	f := newFixtureWithLogger(t, fixtureOpts{
		concurrency:  2,
		perHost:      1,
		pollInterval: 20 * time.Millisecond,
	}, log)
	// Advance the clock repeatedly until the poll-tick log fires. The
	// scheduler's select blocks on clk.After(pollInterval); we must
	// give it a real moment to enter the wait before advancing.
	if !waitFor(t, 3*time.Second, func() bool {
		// Each iteration: nudge the fake clock past the poll interval
		// so the After channel fires; a real sleep gives the scheduler
		// goroutine time to reach the select before the next advance.
		f.clk.Advance(25 * time.Millisecond)
		time.Sleep(10 * time.Millisecond)
		for _, r := range cap.snapshot() {
			if r.message == "queue: poll tick" {
				return true
			}
		}
		return false
	}) {
		t.Fatal("poll tick log record never emitted")
	}
	r, ok := findRecord(cap.snapshot(), func(r capturedRec) bool {
		return r.message == "queue: poll tick"
	})
	if !ok {
		t.Fatal("poll tick record not found after wait")
	}
	if got := r.attrs["activity"]; got != observe.ActivityPoll {
		t.Errorf("poll tick activity: got %q want %q", got, observe.ActivityPoll)
	}
	if r.level != slog.LevelDebug {
		t.Errorf("poll tick level: got %v want Debug", r.level)
	}
}

// TestActivityTagged_TransientFailure verifies that transient failure
// records carry activity=system at warn level (REQ-OPS-86).
func TestActivityTagged_TransientFailure(t *testing.T) {
	log, cap := newCaptureLogger()
	f := newFixtureWithLogger(t, fixtureOpts{
		concurrency: 2,
		perHost:     1,
		retry:       queue.RetryPolicy{Schedule: []time.Duration{time.Hour}},
	}, log)
	f.deliv.hooks = func(req queue.DeliveryRequest, n int) (queue.DeliveryOutcome, error) {
		return queue.DeliveryOutcome{
			Status: queue.DeliveryStatusTransient,
			Code:   451,
			Detail: "try again",
		}, nil
	}
	f.submit(t, queue.Submission{
		MailFrom:   "alice@local.test",
		Recipients: []string{"bob@dest.test"},
		Body:       strings.NewReader("Subject: hi\r\n\r\nbody\r\n"),
		DSNNotify:  store.DSNNotifyFailure,
	})
	if !waitFor(t, 2*time.Second, func() bool {
		rows, _ := f.store.Meta().ListQueueItems(f.ctx, store.QueueFilter{})
		for _, r := range rows {
			if r.State == store.QueueStateDeferred {
				return true
			}
		}
		return false
	}) {
		t.Fatal("row never reached deferred state")
	}
	r, ok := findRecord(cap.snapshot(), func(r capturedRec) bool {
		return r.message == "queue: delivery transient failure; rescheduled"
	})
	if !ok {
		t.Fatal("transient failure log record not found")
	}
	if got := r.attrs["activity"]; got != observe.ActivitySystem {
		t.Errorf("transient failure activity: got %q want %q", got, observe.ActivitySystem)
	}
	if r.level != slog.LevelWarn {
		t.Errorf("transient failure level: got %v want Warn", r.level)
	}
}

// newFixtureWithLogger is like newFixture but injects an explicit logger.
func newFixtureWithLogger(t *testing.T, opts fixtureOpts, log *slog.Logger) *fixture {
	t.Helper()
	f := newFixture(t, fixtureOpts{
		concurrency:  opts.concurrency,
		perHost:      opts.perHost,
		pollInterval: opts.pollInterval,
		retry:        opts.retry,
		signer:       opts.signer,
		skipRun:      true,
	})
	// Re-create the queue with the provided logger.
	pollInterval := opts.pollInterval
	if pollInterval == 0 {
		pollInterval = 50 * time.Millisecond
	}
	f.queue = queue.New(queue.Options{
		Store:          f.store,
		Deliverer:      f.deliv,
		Signer:         opts.signer,
		Logger:         log,
		Clock:          f.clk,
		Concurrency:    opts.concurrency,
		PerHostMax:     opts.perHost,
		Retry:          opts.retry,
		PollInterval:   pollInterval,
		Hostname:       "mail.test.example",
		DSNFromAddress: "postmaster@mail.test.example",
		ShutdownGrace:  2 * time.Second,
	})
	if !opts.skipRun {
		f.wg.Add(1)
		go func() {
			defer f.wg.Done()
			_ = f.queue.Run(f.ctx)
		}()
	} else {
		// Always run for activity tests unless skipRun is set.
	}
	// Start the queue run for all tests here (activity tests need it running).
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		_ = f.queue.Run(f.ctx)
	}()
	return f
}
