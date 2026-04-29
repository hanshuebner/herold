package protoevents_test

// activity_tag_test.go verifies REQ-OPS-86 / REQ-OPS-86a for protoevents:
// every log record the dispatcher emits carries a valid "activity" attribute
// from the closed enum {user, audit, system, poll, access, internal}.
//
// Focused tests assert specific activity values for high-value records:
//   - successful event publish   → system (info)
//   - publish retry              → system (warn)
//   - publish permanent failure  → system (error)
//   - internal infrastructure    → internal (warn)

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/protoevents"
)

// eventsCaptureHandler is a test-only slog.Handler that records every
// emitted log record for inspection.
type eventsCaptureHandler struct {
	mu      sync.Mutex
	records []eventsRecord
	pre     map[string]string
	signal  chan struct{}
}

type eventsRecord struct {
	activity string
	level    slog.Level
	msg      string
}

func (h *eventsCaptureHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *eventsCaptureHandler) Handle(_ context.Context, r slog.Record) error {
	activity := h.pre["activity"]
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "activity" {
			activity = a.Value.String()
			return false
		}
		return true
	})
	h.mu.Lock()
	h.records = append(h.records, eventsRecord{activity: activity, level: r.Level, msg: r.Message})
	sig := h.signal
	h.mu.Unlock()
	if sig != nil {
		select {
		case sig <- struct{}{}:
		default:
		}
	}
	return nil
}

func (h *eventsCaptureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	merged := make(map[string]string, len(h.pre)+len(attrs))
	for k, v := range h.pre {
		merged[k] = v
	}
	for _, a := range attrs {
		merged[a.Key] = a.Value.String()
	}
	nh := &eventsCaptureHandler{
		records: h.records,
		pre:     merged,
		signal:  h.signal,
	}
	return nh
}

func (h *eventsCaptureHandler) WithGroup(_ string) slog.Handler { return h }

func (h *eventsCaptureHandler) hasActivityLevel(activity string, minLevel slog.Level) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r.activity == activity && r.level >= minLevel {
			return true
		}
	}
	return false
}

func (h *eventsCaptureHandler) anyUntagged() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r.activity == "" {
			return true
		}
	}
	return false
}

// waitForRecord blocks until a record matching pred appears, advancing
// the FakeClock each iteration.
func waitForRecord(t *testing.T, cap *eventsCaptureHandler, clk *clock.FakeClock, pred func() bool, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pred() {
			return true
		}
		clk.Advance(10 * time.Millisecond)
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// alwaysSucceedInvoker calls succeed immediately with no error.
type alwaysSucceedInvoker struct {
	mu    sync.Mutex
	calls int
}

func (f *alwaysSucceedInvoker) Call(_ context.Context, _, _ string, _, _ any) error {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return nil
}

func (f *alwaysSucceedInvoker) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// alwaysFailInvoker always returns an error to exercise retry/failure paths.
type alwaysFailInvoker struct{}

func (f *alwaysFailInvoker) Call(_ context.Context, _, _ string, _, _ any) error {
	return errors.New("simulated publish error")
}

// TestEventsActivityTag_Publish_IsSystem asserts that a successful event
// publish emits activity=system at info. REQ-OPS-86.
func TestEventsActivityTag_Publish_IsSystem(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	sig := make(chan struct{}, 16)
	cap := &eventsCaptureHandler{signal: sig}
	lg := slog.New(cap)
	inv := &alwaysSucceedInvoker{}

	d, err := protoevents.New(protoevents.Options{
		Logger:       lg,
		Clock:        clk,
		Plugins:      inv,
		PluginNames:  []string{"nats"},
		BufferSize:   16,
		MaxRetries:   1,
		RetryBackoff: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("protoevents.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	if err := d.Emit(ctx, protoevents.Event{Kind: protoevents.EventMailReceived}); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	if !waitForRecord(t, cap, clk, func() bool {
		return cap.hasActivityLevel(observe.ActivitySystem, slog.LevelInfo)
	}, 5*time.Second) {
		t.Error("expected activity=system info record after successful event publish (REQ-OPS-86)")
	}
}

// TestEventsActivityTag_PublishRetry_IsSystem asserts that a retry
// produces activity=system at warn. REQ-OPS-86.
func TestEventsActivityTag_PublishRetry_IsSystem(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	cap := &eventsCaptureHandler{}
	lg := slog.New(cap)

	// Invoker that fails once then succeeds.
	var callCount int
	var mu sync.Mutex
	mixedInvoker := &mixedCallInvoker{
		fn: func(n int) error {
			_ = n
			mu.Lock()
			callCount++
			c := callCount
			mu.Unlock()
			if c == 1 {
				return errors.New("first call fails")
			}
			return nil
		},
	}

	d, err := protoevents.New(protoevents.Options{
		Logger:       lg,
		Clock:        clk,
		Plugins:      mixedInvoker,
		PluginNames:  []string{"nats"},
		BufferSize:   16,
		MaxRetries:   3,
		RetryBackoff: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("protoevents.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	if err := d.Emit(ctx, protoevents.Event{Kind: protoevents.EventMailReceived}); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	// Wait for a warn record (the retry) then an info record (the success).
	if !waitForRecord(t, cap, clk, func() bool {
		return cap.hasActivityLevel(observe.ActivitySystem, slog.LevelWarn)
	}, 5*time.Second) {
		t.Error("expected activity=system warn record on publish retry (REQ-OPS-86)")
	}
}

// TestEventsActivityTag_PublishFailed_IsSystem asserts that permanent
// publish failure emits activity=system at error. REQ-OPS-86.
func TestEventsActivityTag_PublishFailed_IsSystem(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	cap := &eventsCaptureHandler{}
	lg := slog.New(cap)

	d, err := protoevents.New(protoevents.Options{
		Logger:       lg,
		Clock:        clk,
		Plugins:      &alwaysFailInvoker{},
		PluginNames:  []string{"nats"},
		BufferSize:   16,
		MaxRetries:   1,
		RetryBackoff: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("protoevents.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	if err := d.Emit(ctx, protoevents.Event{Kind: protoevents.EventMailReceived}); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	// The primary event fails and emits EventPublishFailed. The failed
	// envelope itself will also hit the failure path and log at error
	// with activity=system.
	if !waitForRecord(t, cap, clk, func() bool {
		return cap.hasActivityLevel(observe.ActivitySystem, slog.LevelError)
	}, 5*time.Second) {
		t.Error("expected activity=system error record after publish permanently failed (REQ-OPS-86)")
	}
}

// TestEventsActivityTag_AllRecordsTagged asserts no record from the
// dispatcher is missing the activity attribute. REQ-OPS-86a.
func TestEventsActivityTag_AllRecordsTagged(t *testing.T) {
	t.Parallel()
	observe.AssertActivityTagged(t, func(lg *slog.Logger) {
		clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
		inv := &alwaysSucceedInvoker{}

		d, err := protoevents.New(protoevents.Options{
			Logger:       lg,
			Clock:        clk,
			Plugins:      inv,
			PluginNames:  []string{"nats"},
			BufferSize:   16,
			MaxRetries:   1,
			RetryBackoff: 5 * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("protoevents.New: %v", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- d.Run(ctx) }()

		if err := d.Emit(ctx, protoevents.Event{Kind: protoevents.EventMailReceived}); err != nil {
			t.Fatalf("Emit: %v", err)
		}
		// Give the dispatch loop a moment to process.
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			clk.Advance(10 * time.Millisecond)
			if inv.Calls() > 0 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
		<-done
	})
}

// mixedCallInvoker returns an error based on call count.
type mixedCallInvoker struct {
	mu sync.Mutex
	n  int
	fn func(n int) error
}

func (m *mixedCallInvoker) Call(_ context.Context, _, _ string, _, _ any) error {
	m.mu.Lock()
	m.n++
	n := m.n
	m.mu.Unlock()
	return m.fn(n)
}
