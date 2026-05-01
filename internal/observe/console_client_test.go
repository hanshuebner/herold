package observe

// console_client_test.go — tests for the per-session reorder buffer and
// console rendering of source=client records (REQ-OPS-210, REQ-OPS-204).
//
// All tests use injected FakeClocks; no real time.Sleep.

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
)

// --- helpers ---

// anchor is a predictable test epoch.
var anchor = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// buildClientRecord returns a slog.Record with source=client attrs and the
// given effective timestamp (client_ts with no skew). The level string is
// parsed to derive the slog.Level used for r.Level so throttle tests work
// correctly.
func buildClientRecord(t time.Time, sessionID, kind, level, msg string) slog.Record {
	r := slog.NewRecord(t, parseLogLevel(level), msg, 0)
	r.AddAttrs(
		slog.String("source", "client"),
		slog.String("client_session", sessionID),
		slog.String("kind", kind),
		slog.String("level", level),
		slog.String("client_ts", t.Format(time.RFC3339Nano)),
		slog.Int64("clock_skew_ms", 0),
		slog.String("app", "suite"),
	)
	return r
}

// buildClientRecordWithSkew is like buildClientRecord but applies a
// clock_skew_ms offset so effective time = client_ts + skew.
func buildClientRecordWithSkew(clientTS time.Time, skewMS int64, sessionID, kind, level, msg string) slog.Record {
	r := slog.NewRecord(clientTS, parseLogLevel(level), msg, 0)
	r.AddAttrs(
		slog.String("source", "client"),
		slog.String("client_session", sessionID),
		slog.String("kind", kind),
		slog.String("level", level),
		slog.String("client_ts", clientTS.Format(time.RFC3339Nano)),
		slog.Int64("clock_skew_ms", skewMS),
		slog.String("app", "suite"),
	)
	return r
}

// buildServerRecord returns a slog.Record without source=client (a normal
// server-side record).
func buildServerRecord(lvl slog.Level, msg string) slog.Record {
	return slog.NewRecord(anchor, lvl, msg, 0)
}

// capturingHandler records every Handle call and the order messages arrived.
type capturingHandler struct {
	mu   sync.Mutex
	msgs []string
}

func (c *capturingHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (c *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Tag late records so tests can detect the attribute.
	late := false
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "late" && a.Value.Bool() {
			late = true
		}
		return true
	})
	s := r.Message
	if late {
		s = s + " [LATE]"
	}
	c.msgs = append(c.msgs, s)
	return nil
}
func (c *capturingHandler) WithAttrs(_ []slog.Attr) slog.Handler { return c }
func (c *capturingHandler) WithGroup(_ string) slog.Handler      { return c }

func (c *capturingHandler) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.msgs))
	copy(out, c.msgs)
	return out
}

// waitFor polls until cap has exactly n messages or the real deadline passes.
func waitFor(t *testing.T, cap *capturingHandler, n int) []string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got := cap.snapshot()
		if len(got) == n {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	got := cap.snapshot()
	t.Fatalf("timed out waiting for %d messages; got %d: %v", n, len(got), got)
	return nil
}

// drainSync reads n values from syncCh (item-processed signals) within a
// real-time deadline. Used by tests to ensure the reorder goroutine has
// consumed n items from inCh before advancing the fake clock.
func drainSync(t *testing.T, ch <-chan struct{}, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for i := 0; i < n; i++ {
		select {
		case <-ch:
		case <-time.After(time.Until(deadline)):
			t.Fatalf("timed out waiting for sync signal %d/%d", i+1, n)
		}
	}
}

// --- property test: any input order produces output sorted by effective time ---

// TestReorderBuffer_SortedOutput feeds events in reverse effective-time order
// and asserts the downstream handler sees them in forward order.
// Uses Stop() to drain: Stop always emits in heap (effective-time) order.
func TestReorderBuffer_SortedOutput(t *testing.T) {
	clk := clock.NewFake(anchor)
	cap := &capturingHandler{}
	cfg := ClientLogConfig{ReorderWindowMS: 500, ConsoleLevel: "trace"}
	h := NewClientReorderHandler(cap, cfg, clk)
	h.Start()

	ctx := context.Background()
	// Events at t+400, t+300, t+200, t+100 — feed in reverse order.
	times := []time.Duration{400, 300, 200, 100}
	for _, d := range times {
		ts := anchor.Add(d * time.Millisecond)
		r := buildClientRecord(ts, "sess-1", "log", "info", fmt.Sprintf("event-%dms", int(d)))
		if err := h.Handle(ctx, r); err != nil {
			t.Fatalf("Handle: %v", err)
		}
	}

	// Stop drains the heap in effective-time (sorted) order.
	h.Stop()

	got := cap.snapshot()
	if len(got) != 4 {
		t.Fatalf("expected 4 messages; got %d: %v", len(got), got)
	}
	// Assert ascending order: event-100ms, 200ms, 300ms, 400ms.
	want := []string{"event-100ms", "event-200ms", "event-300ms", "event-400ms"}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("position %d: got %q want %q (full: %v)", i, got[i], w, got)
		}
	}
}

// TestReorderBuffer_PropertyArbitraryOrder repeats the sort property across
// many permutations; each run uses Stop() to drain.
func TestReorderBuffer_PropertyArbitraryOrder(t *testing.T) {
	// Permutations of 4 events.
	perms := [][4]int{
		{0, 1, 2, 3},
		{3, 2, 1, 0},
		{1, 3, 0, 2},
		{2, 0, 3, 1},
		{0, 3, 1, 2},
		{2, 1, 0, 3},
	}
	offsets := [4]time.Duration{100, 250, 600, 800} // ms
	for _, perm := range perms {
		clk := clock.NewFake(anchor)
		cap := &capturingHandler{}
		cfg := ClientLogConfig{ReorderWindowMS: 1000, ConsoleLevel: "trace"}
		h := NewClientReorderHandler(cap, cfg, clk)
		h.Start()

		ctx := context.Background()
		for _, idx := range perm {
			ts := anchor.Add(offsets[idx] * time.Millisecond)
			r := buildClientRecord(ts, "sess-p", "log", "info", fmt.Sprintf("e%d", idx))
			if err := h.Handle(ctx, r); err != nil {
				h.Stop()
				t.Fatalf("Handle: %v", err)
			}
		}
		// Stop drains in effective-time order.
		h.Stop()

		got := cap.snapshot()
		if len(got) != 4 {
			t.Errorf("perm %v: got %d messages: %v", perm, len(got), got)
			continue
		}
		// Verify ascending offset order.
		for i := 1; i < len(got); i++ {
			var prevIdx, curIdx int
			fmt.Sscanf(got[i-1], "e%d", &prevIdx)
			fmt.Sscanf(got[i], "e%d", &curIdx)
			if offsets[prevIdx] > offsets[curIdx] {
				t.Errorf("perm %v: out of order at position %d: %q (offset %v) before %q (offset %v)",
					perm, i, got[i-1], offsets[prevIdx], got[i], offsets[curIdx])
			}
		}
	}
}

// --- late event tagging ---

// TestReorderBuffer_LateEventTagged verifies that an event arriving after its
// window has already closed is emitted immediately with late=true.
func TestReorderBuffer_LateEventTagged(t *testing.T) {
	clk := clock.NewFake(anchor)
	cap := &capturingHandler{}
	cfg := ClientLogConfig{ReorderWindowMS: 500, ConsoleLevel: "trace"}
	h := NewClientReorderHandler(cap, cfg, clk)
	h.Start()
	defer h.Stop()

	// Advance past the window before submitting the event.
	clk.Advance(2000 * time.Millisecond)

	ctx := context.Background()
	// Event with client_ts = anchor: its deadline = anchor+500ms, which is
	// before the current time (anchor+2000ms). Should be late.
	ts := anchor.Add(100 * time.Millisecond)
	r := buildClientRecord(ts, "sess-late", "log", "info", "late-event")
	if err := h.Handle(ctx, r); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	// The handler emits late events synchronously in Handle() (bypasses channel).
	got := waitFor(t, cap, 1)
	if !strings.Contains(got[0], "[LATE]") {
		t.Errorf("expected late tag; got %q", got[0])
	}
}

// TestReorderBuffer_LateEventFlushedSeparately verifies that a late event that
// arrives after the window for its session has closed is emitted immediately
// with late=true, while previously buffered events within-window are emitted
// in order via the timer mechanism.
func TestReorderBuffer_LateEventFlushedSeparately(t *testing.T) {
	clk := clock.NewFake(anchor)
	cap := &capturingHandler{}
	cfg := ClientLogConfig{ReorderWindowMS: 500, ConsoleLevel: "trace"}
	h := NewClientReorderHandler(cap, cfg, clk)
	h.enableTestSync()
	h.enableTestFlushSync()
	h.Start()
	defer h.Stop()

	ctx := context.Background()

	// Submit two events within the reorder window.
	// r1: effective=anchor+100ms, deadline=anchor+600ms.
	// r2: effective=anchor+200ms, deadline=anchor+700ms.
	r1 := buildClientRecord(anchor.Add(100*time.Millisecond), "sess-mix", "log", "info", "first")
	r2 := buildClientRecord(anchor.Add(200*time.Millisecond), "sess-mix", "log", "info", "second")
	if err := h.Handle(ctx, r1); err != nil {
		t.Fatal(err)
	}
	if err := h.Handle(ctx, r2); err != nil {
		t.Fatal(err)
	}
	// Wait for the goroutine to consume both items and register timers.
	drainSync(t, h.syncCh, 2)

	// Advance past r1's deadline (anchor+600ms). The timer fires, goroutine
	// flushes r1 and reschedules for r2's deadline (anchor+700ms).
	clk.Advance(650 * time.Millisecond)
	// Wait for the flush cycle to complete (timer fired, flushExpired ran,
	// syncSessionTimers rescheduled the next timer).
	drainSync(t, h.flushSyncCh, 1)

	// Advance past r2's deadline (anchor+700ms). r2 is flushed.
	clk.Advance(100 * time.Millisecond) // now at anchor+750ms
	drainSync(t, h.flushSyncCh, 1)

	// Both buffered events must have been emitted now.
	if got := cap.snapshot(); len(got) != 2 {
		t.Fatalf("buffered events not flushed; got %d: %v", len(got), got)
	}

	// Now submit the late event (current clock = anchor+750ms, well past its
	// deadline of anchor+500ms). It must be emitted immediately with late=true.
	rLate := buildClientRecord(anchor, "sess-mix", "log", "info", "very-late")
	if err := h.Handle(ctx, rLate); err != nil {
		t.Fatal(err)
	}

	got := cap.snapshot()
	if len(got) != 3 {
		t.Fatalf("want 3 events total; got %d: %v", len(got), got)
	}
	// The late event should carry the [LATE] tag.
	lateCount := 0
	for _, m := range got {
		if strings.Contains(m, "[LATE]") {
			lateCount++
		}
	}
	if lateCount != 1 {
		t.Errorf("expected exactly 1 late event; got %d in %v", lateCount, got)
	}
}

// --- session idle flush ---

// TestReorderBuffer_SessionIdleFlush verifies that a session idle for more
// than 2*window is flushed by the timer mechanism.
// Strategy: submit one event, then advance the clock past its deadline in
// small steps to trigger each generated timer.
func TestReorderBuffer_SessionIdleFlush(t *testing.T) {
	clk := clock.NewFake(anchor)
	cap := &capturingHandler{}
	window := int64(500) // ms
	cfg := ClientLogConfig{ReorderWindowMS: window, ConsoleLevel: "trace"}
	h := NewClientReorderHandler(cap, cfg, clk)
	h.enableTestSync()
	h.Start()
	defer h.Stop()

	ctx := context.Background()
	// One event: effective time = anchor+100ms, deadline = anchor+600ms.
	r := buildClientRecord(anchor.Add(100*time.Millisecond), "sess-idle", "log", "info", "idle-event")
	if err := h.Handle(ctx, r); err != nil {
		t.Fatal(err)
	}
	// Wait for the goroutine to consume the item and register the timer.
	drainSync(t, h.syncCh, 1)

	// Advance past the deadline (anchor+600ms). The timer fires, the goroutine
	// flushes the event.
	clk.Advance(700 * time.Millisecond)

	// Wait for the flush via the timer mechanism.
	got := waitFor(t, cap, 1)
	if got[0] != "idle-event" {
		t.Errorf("unexpected message: %q", got[0])
	}
}

// TestReorderBuffer_SessionIdleFor2xWindowFlushes verifies that a session with
// no new arrivals for more than 2*window is fully drained by the timer. We
// advance in two steps: once past the deadline, then verify all events flush.
func TestReorderBuffer_SessionIdleFor2xWindowFlushes(t *testing.T) {
	clk := clock.NewFake(anchor)
	cap := &capturingHandler{}
	window := int64(200) // 200ms window for a compact test
	cfg := ClientLogConfig{ReorderWindowMS: window, ConsoleLevel: "trace"}
	h := NewClientReorderHandler(cap, cfg, clk)
	h.enableTestSync()
	h.enableTestFlushSync()
	h.Start()
	defer h.Stop()

	ctx := context.Background()
	// Three events with window=200ms:
	//   event-1: ts=anchor+100ms, deadline=anchor+300ms
	//   event-2: ts=anchor+200ms, deadline=anchor+400ms
	//   event-3: ts=anchor+300ms, deadline=anchor+500ms
	// upsertSessionTimer only sets the timer once (for the earliest deadline
	// anchor+300ms); syncSessionTimers reschedules after each flush.
	for i := 1; i <= 3; i++ {
		ts := anchor.Add(time.Duration(i*100) * time.Millisecond)
		r := buildClientRecord(ts, "sess-2x", "log", "info", fmt.Sprintf("event-%d", i))
		if err := h.Handle(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	// Ensure all three items are in the heap and the first timer is registered.
	drainSync(t, h.syncCh, 3)

	// Advance past event-1's deadline (anchor+300ms). The timer fires,
	// flushExpired emits event-1, syncSessionTimers schedules next at 400ms.
	clk.Advance(350 * time.Millisecond)
	drainSync(t, h.flushSyncCh, 1)

	// Advance past event-2's deadline (anchor+400ms). event-2 is emitted,
	// syncSessionTimers schedules next at 500ms.
	clk.Advance(100 * time.Millisecond) // now at anchor+450ms
	drainSync(t, h.flushSyncCh, 1)

	// Advance past event-3's deadline (anchor+500ms). event-3 is emitted.
	clk.Advance(100 * time.Millisecond) // now at anchor+550ms
	drainSync(t, h.flushSyncCh, 1)

	got := cap.snapshot()
	if len(got) != 3 {
		t.Fatalf("expected 3 events; got %d: %v", len(got), got)
	}
	// All three events must be present in effective-time order.
	for i, want := range []string{"event-1", "event-2", "event-3"} {
		if got[i] != want {
			t.Errorf("position %d: got %q want %q", i, got[i], want)
		}
	}
}

// --- throttle ---

// TestThrottle_AppliesOnlyToClientSource verifies that server-side records are
// NEVER throttled by the per-source throttle — they pass straight through the
// downstream without inspection by shouldThrottleClientRecord.
func TestThrottle_AppliesOnlyToClientSource(t *testing.T) {
	clk := clock.NewFake(anchor)
	cap := &capturingHandler{}
	// consoleLevel=warn: kind=log below warn would be suppressed for source=client.
	cfg := ClientLogConfig{ReorderWindowMS: 500, ConsoleLevel: "warn"}
	h := NewClientReorderHandler(cap, cfg, clk)
	h.Start()
	defer h.Stop()

	ctx := context.Background()

	// Server record at debug level: must NOT be throttled.
	sr := buildServerRecord(slog.LevelDebug, "server-debug")
	if err := h.Handle(ctx, sr); err != nil {
		t.Fatal(err)
	}
	// Server records pass through synchronously (bypass the channel).
	time.Sleep(20 * time.Millisecond)

	got := cap.snapshot()
	if len(got) != 1 || got[0] != "server-debug" {
		t.Errorf("server-side debug record should not be throttled; got: %v", got)
	}
}

// TestThrottle_KindLogBelowConsoleLevelSuppressed verifies that source=client
// kind=log records below the console level threshold are dropped.
func TestThrottle_KindLogBelowConsoleLevelSuppressed(t *testing.T) {
	clk := clock.NewFake(anchor)
	cap := &capturingHandler{}
	cfg := ClientLogConfig{ReorderWindowMS: 500, ConsoleLevel: "warn"}
	h := NewClientReorderHandler(cap, cfg, clk)
	h.Start()
	defer h.Stop()

	ctx := context.Background()
	// kind=log at info — below warn threshold — should be suppressed.
	r := buildClientRecord(anchor.Add(100*time.Millisecond), "sess-t", "log", "info", "should-be-suppressed")
	if err := h.Handle(ctx, r); err != nil {
		t.Fatal(err)
	}

	// Advance past window; any buffered entry would have been emitted by now.
	clk.Advance(700 * time.Millisecond)
	time.Sleep(50 * time.Millisecond)

	got := cap.snapshot()
	if len(got) != 0 {
		t.Errorf("expected 0 events after throttle; got %d: %v", len(got), got)
	}
}

// TestThrottle_KindLogAtOrAboveConsoleLevelPasses verifies that kind=log at
// or above the console level threshold passes.
func TestThrottle_KindLogAtOrAboveConsoleLevelPasses(t *testing.T) {
	clk := clock.NewFake(anchor)
	cap := &capturingHandler{}
	cfg := ClientLogConfig{ReorderWindowMS: 500, ConsoleLevel: "warn"}
	h := NewClientReorderHandler(cap, cfg, clk)
	h.enableTestSync()
	h.Start()
	defer h.Stop()

	ctx := context.Background()
	r := buildClientRecord(anchor.Add(100*time.Millisecond), "sess-w", "log", "warn", "warn-passes")
	if err := h.Handle(ctx, r); err != nil {
		t.Fatal(err)
	}
	drainSync(t, h.syncCh, 1)

	clk.Advance(700 * time.Millisecond)
	got := waitFor(t, cap, 1)
	if got[0] != "warn-passes" {
		t.Errorf("expected warn-level event to pass; got: %v", got)
	}
}

// TestThrottle_KindErrorAlwaysPasses verifies that kind=error records always
// pass regardless of the console level threshold (REQ-OPS-204).
func TestThrottle_KindErrorAlwaysPasses(t *testing.T) {
	clk := clock.NewFake(anchor)
	cap := &capturingHandler{}
	// consoleLevel=error but at slog.LevelWarn: kind=log at warn would be
	// suppressed. kind=error must pass regardless.
	cfg := ClientLogConfig{ReorderWindowMS: 500, ConsoleLevel: "error"}
	h := NewClientReorderHandler(cap, cfg, clk)
	h.enableTestSync()
	h.Start()
	defer h.Stop()

	ctx := context.Background()
	// kind=error at debug level: should pass (kind=error bypasses throttle).
	r := buildClientRecord(anchor.Add(100*time.Millisecond), "sess-err", "error", "debug", "error-event")
	if err := h.Handle(ctx, r); err != nil {
		t.Fatal(err)
	}
	drainSync(t, h.syncCh, 1)

	clk.Advance(700 * time.Millisecond)
	got := waitFor(t, cap, 1)
	if got[0] != "error-event" {
		t.Errorf("expected kind=error to pass throttle; got: %v", got)
	}
}

// TestThrottle_KindVitalAlwaysPasses verifies that kind=vital records always
// pass regardless of the console level threshold (REQ-OPS-204).
func TestThrottle_KindVitalAlwaysPasses(t *testing.T) {
	clk := clock.NewFake(anchor)
	cap := &capturingHandler{}
	// Set a very high console level to exercise the vital bypass.
	cfg := ClientLogConfig{ReorderWindowMS: 500, ConsoleLevel: "error"}
	h := NewClientReorderHandler(cap, cfg, clk)
	h.enableTestSync()
	h.Start()
	defer h.Stop()

	ctx := context.Background()
	// kind=vital at debug level: must pass.
	r := buildClientRecord(anchor.Add(100*time.Millisecond), "sess-vital", "vital", "debug", "vital-event")
	if err := h.Handle(ctx, r); err != nil {
		t.Fatal(err)
	}
	drainSync(t, h.syncCh, 1)

	clk.Advance(700 * time.Millisecond)
	got := waitFor(t, cap, 1)
	if got[0] != "vital-event" {
		t.Errorf("expected kind=vital to pass throttle; got: %v", got)
	}
}

// TestThrottle_MixedKindsBothPaths submits kind=log (suppressed), kind=error
// (passes), and kind=vital (passes) in the same session and verifies only
// the error and vital come out.
func TestThrottle_MixedKindsBothPaths(t *testing.T) {
	clk := clock.NewFake(anchor)
	cap := &capturingHandler{}
	cfg := ClientLogConfig{ReorderWindowMS: 500, ConsoleLevel: "error"} // very high threshold
	h := NewClientReorderHandler(cap, cfg, clk)
	h.enableTestSync()
	h.Start()
	defer h.Stop()

	ctx := context.Background()
	// kind=log at info — suppressed (never enters inCh).
	rLog := buildClientRecord(anchor.Add(100*time.Millisecond), "sess-mixed", "log", "info", "log-suppressed")
	// kind=error — passes, enters inCh.
	rErr := buildClientRecord(anchor.Add(200*time.Millisecond), "sess-mixed", "error", "debug", "err-passes")
	// kind=vital — passes, enters inCh.
	rVital := buildClientRecord(anchor.Add(300*time.Millisecond), "sess-mixed", "vital", "debug", "vital-passes")

	for _, r := range []slog.Record{rLog, rErr, rVital} {
		if err := h.Handle(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	// Only rErr and rVital enter inCh (rLog is throttled before the channel).
	drainSync(t, h.syncCh, 2)

	clk.Advance(1000 * time.Millisecond)
	got := waitFor(t, cap, 2)
	// The two passing events should be in effective-time order.
	if got[0] != "err-passes" || got[1] != "vital-passes" {
		t.Errorf("unexpected events: %v", got)
	}
}

// --- JSON sink bypass ---

// TestReorderBuffer_JSONSinkBypass verifies that a plain JSON handler (not
// wrapped in ClientReorderHandler) receives events in arrival order.
// The reorder buffer is exclusively wired around ConsoleHandler via
// wrapConsoleForClientLogs.
func TestReorderBuffer_JSONSinkBypass(t *testing.T) {
	var buf bytes.Buffer
	jsonHandler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})

	ctx := context.Background()
	// Submit in reverse effective-time order.
	times := []time.Duration{300, 200, 100}
	msgs := []string{"t300", "t200", "t100"}
	for i, d := range times {
		ts := anchor.Add(d * time.Millisecond)
		r := buildClientRecord(ts, "sess-json", "log", "info", msgs[i])
		if err := jsonHandler.Handle(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	// JSON sink should have received them in arrival order (t300 first).
	out := buf.String()
	i300 := strings.Index(out, "t300")
	i200 := strings.Index(out, "t200")
	i100 := strings.Index(out, "t100")
	if i300 < 0 || i200 < 0 || i100 < 0 {
		t.Fatalf("missing expected messages in JSON output: %q", out)
	}
	// Arrival order: t300 came first, then t200, then t100.
	if !(i300 < i200 && i200 < i100) {
		t.Errorf("JSON sink should preserve arrival order: positions t300=%d t200=%d t100=%d",
			i300, i200, i100)
	}
}

// TestReorderBuffer_ConsoleOnlyWrapped verifies that wrapConsoleForClientLogs
// wraps ConsoleHandler instances with the reorder buffer, and that a zero
// window returns the base handler unwrapped.
func TestReorderBuffer_ConsoleOnlyWrapped(t *testing.T) {
	var buf bytes.Buffer
	base := NewConsoleHandlerWithClock(&buf, &slog.HandlerOptions{}, clock.NewFake(anchor), &boolFalse)

	// Non-zero window: wrapping applied.
	cfg := ClientLogConfig{ReorderWindowMS: 1000, ConsoleLevel: "warn"}
	wrapped, reorder := wrapConsoleForClientLogs(base, cfg, clock.NewFake(anchor))
	if reorder == nil {
		t.Error("expected non-nil ClientReorderHandler for non-zero window")
	}
	if _, ok := wrapped.(*ClientReorderHandler); !ok {
		t.Errorf("wrapped handler should be *ClientReorderHandler; got %T", wrapped)
	}
	reorder.Stop()

	// Zero window: no wrapping; base returned as-is.
	cfgZero := ClientLogConfig{ReorderWindowMS: 0, ConsoleLevel: "warn"}
	wrappedZero, reorderZero := wrapConsoleForClientLogs(base, cfgZero, clock.NewFake(anchor))
	if reorderZero != nil {
		t.Error("expected nil ClientReorderHandler for zero window")
	}
	if wrappedZero != slog.Handler(base) {
		t.Errorf("zero-window wrap should return base handler; got %T", wrappedZero)
	}
}

// --- clock_skew_ms in effective time ---

// TestReorderBuffer_ClockSkewAffectsOrder verifies that clock_skew_ms is
// applied when computing effective time. Uses Stop() to drain so the sorted
// output is deterministic.
func TestReorderBuffer_ClockSkewAffectsOrder(t *testing.T) {
	clk := clock.NewFake(anchor)
	cap := &capturingHandler{}
	cfg := ClientLogConfig{ReorderWindowMS: 1000, ConsoleLevel: "trace"}
	h := NewClientReorderHandler(cap, cfg, clk)
	h.Start()

	ctx := context.Background()

	// Event A: client_ts = anchor+500ms, skew = -400ms → effective = anchor+100ms
	rA := buildClientRecordWithSkew(anchor.Add(500*time.Millisecond), -400, "sess-skew", "log", "info", "eventA")
	// Event B: client_ts = anchor+200ms, skew = 0 → effective = anchor+200ms
	rB := buildClientRecordWithSkew(anchor.Add(200*time.Millisecond), 0, "sess-skew", "log", "info", "eventB")

	// Submit B first, then A.
	if err := h.Handle(ctx, rB); err != nil {
		t.Fatal(err)
	}
	if err := h.Handle(ctx, rA); err != nil {
		t.Fatal(err)
	}

	// Stop drains in effective-time order.
	h.Stop()

	got := cap.snapshot()
	if len(got) != 2 {
		t.Fatalf("expected 2 events; got %d: %v", len(got), got)
	}
	// eventA has effective time anchor+100ms; eventB has anchor+200ms.
	// So order must be: eventA, eventB.
	if got[0] != "eventA" || got[1] != "eventB" {
		t.Errorf("wrong order after skew: got %v, want [eventA eventB]", got)
	}
}

// --- multi-session isolation ---

// TestReorderBuffer_MultiSessionIsolation verifies that events from different
// sessions are each ordered within their session. Uses Stop() to drain.
func TestReorderBuffer_MultiSessionIsolation(t *testing.T) {
	clk := clock.NewFake(anchor)
	cap := &capturingHandler{}
	cfg := ClientLogConfig{ReorderWindowMS: 500, ConsoleLevel: "trace"}
	h := NewClientReorderHandler(cap, cfg, clk)
	h.Start()

	ctx := context.Background()
	// Session A: t+300, t+100 (submitted in wrong order).
	// Session B: t+400, t+200 (submitted in wrong order).
	// Interleaved submission.
	rA2 := buildClientRecord(anchor.Add(300*time.Millisecond), "sessA", "log", "info", "A-300")
	rA1 := buildClientRecord(anchor.Add(100*time.Millisecond), "sessA", "log", "info", "A-100")
	rB2 := buildClientRecord(anchor.Add(400*time.Millisecond), "sessB", "log", "info", "B-400")
	rB1 := buildClientRecord(anchor.Add(200*time.Millisecond), "sessB", "log", "info", "B-200")

	for _, r := range []slog.Record{rA2, rB2, rA1, rB1} {
		if err := h.Handle(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	// Stop drains in heap order.
	h.Stop()

	got := cap.snapshot()
	if len(got) != 4 {
		t.Fatalf("expected 4 events; got %d: %v", len(got), got)
	}

	// Within session A, A-100 must come before A-300.
	idxA1, idxA2 := -1, -1
	for i, m := range got {
		if m == "A-100" {
			idxA1 = i
		}
		if m == "A-300" {
			idxA2 = i
		}
	}
	if idxA1 < 0 || idxA2 < 0 {
		t.Fatalf("missing session A events: %v", got)
	}
	if idxA1 > idxA2 {
		t.Errorf("session A ordering wrong: A-100 at %d, A-300 at %d", idxA1, idxA2)
	}

	// Within session B, B-200 must come before B-400.
	idxB1, idxB2 := -1, -1
	for i, m := range got {
		if m == "B-200" {
			idxB1 = i
		}
		if m == "B-400" {
			idxB2 = i
		}
	}
	if idxB1 < 0 || idxB2 < 0 {
		t.Fatalf("missing session B events: %v", got)
	}
	if idxB1 > idxB2 {
		t.Errorf("session B ordering wrong: B-200 at %d, B-400 at %d", idxB1, idxB2)
	}
}

// --- Stop drains buffer ---

// TestReorderBuffer_StopFlushesAll verifies that calling Stop flushes all
// pending events before the goroutine exits, even if the window has not expired.
func TestReorderBuffer_StopFlushesAll(t *testing.T) {
	clk := clock.NewFake(anchor)
	cap := &capturingHandler{}
	cfg := ClientLogConfig{ReorderWindowMS: 10000, ConsoleLevel: "trace"} // very long window
	h := NewClientReorderHandler(cap, cfg, clk)
	h.Start()

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		ts := anchor.Add(time.Duration(i*100) * time.Millisecond)
		r := buildClientRecord(ts, "sess-stop", "log", "info", fmt.Sprintf("e%d", i))
		if err := h.Handle(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	// Do NOT advance the clock. Stop must drain the heap anyway.
	h.Stop()

	got := cap.snapshot()
	if len(got) != 5 {
		t.Errorf("Stop should flush all pending events; got %d: %v", len(got), got)
	}
	// Should come out in effective-time order (sorted on Stop drain).
	for i, m := range got {
		want := fmt.Sprintf("e%d", i)
		if m != want {
			t.Errorf("position %d: got %q want %q", i, m, want)
		}
	}
}

// --- shouldThrottleClientRecord unit tests ---

func TestShouldThrottleClientRecord(t *testing.T) {
	warnLevel := slog.LevelWarn

	cases := []struct {
		kind         string
		lvl          slog.Level
		consoleLvl   slog.Level
		wantThrottle bool
	}{
		// kind=error always passes.
		{"error", slog.LevelDebug, warnLevel, false},
		{"error", slog.LevelInfo, warnLevel, false},
		{"error", slog.LevelWarn, warnLevel, false},
		// kind=vital always passes.
		{"vital", slog.LevelDebug, warnLevel, false},
		{"vital", slog.LevelInfo, warnLevel, false},
		// kind=log: below threshold is throttled.
		{"log", slog.LevelDebug, warnLevel, true},
		{"log", slog.LevelInfo, warnLevel, true},
		// kind=log: at or above threshold passes.
		{"log", slog.LevelWarn, warnLevel, false},
		{"log", slog.LevelError, warnLevel, false},
	}
	for _, tc := range cases {
		attrs := map[string]string{"kind": tc.kind}
		got := shouldThrottleClientRecord(attrs, tc.lvl, tc.consoleLvl)
		if got != tc.wantThrottle {
			t.Errorf("shouldThrottleClientRecord(kind=%q, lvl=%v, consoleLvl=%v) = %v, want %v",
				tc.kind, tc.lvl, tc.consoleLvl, got, tc.wantThrottle)
		}
	}
}

// --- effectiveClientTime unit tests ---

func TestEffectiveClientTime(t *testing.T) {
	base := anchor

	// Valid RFC3339Nano with skew.
	ts := base.Add(100 * time.Millisecond)
	got := effectiveClientTime(ts.Format(time.RFC3339Nano), 50, base)
	want := ts.Add(50 * time.Millisecond)
	if !got.Equal(want) {
		t.Errorf("with skew: got %v want %v", got, want)
	}

	// Empty client_ts falls back to fallback.
	got = effectiveClientTime("", 100, base)
	if !got.Equal(base) {
		t.Errorf("empty client_ts: got %v want %v", got, base)
	}

	// Unparseable client_ts falls back.
	got = effectiveClientTime("not-a-time", 0, base)
	if !got.Equal(base) {
		t.Errorf("bad client_ts: got %v want %v", got, base)
	}

	// Zero skew: effective time == client_ts.
	got = effectiveClientTime(base.Format(time.RFC3339Nano), 0, time.Time{})
	if !got.Equal(base) {
		t.Errorf("zero skew: got %v want %v", got, base)
	}

	// Negative skew: effective time is earlier than client_ts.
	ts2 := base.Add(300 * time.Millisecond)
	got = effectiveClientTime(ts2.Format(time.RFC3339Nano), -100, base)
	want2 := ts2.Add(-100 * time.Millisecond)
	if !got.Equal(want2) {
		t.Errorf("negative skew: got %v want %v", got, want2)
	}
}

// --- console rendering of source=client records ---

// TestConsoleHandler_ClientSourceMarker verifies the [suite]/[admin] TTY
// marker is rendered for source=client records (REQ-OPS-81a, REQ-OPS-204).
func TestConsoleHandler_ClientSourceMarker(t *testing.T) {
	cases := []struct {
		app    string
		marker string
	}{
		{"suite", "[suite]"},
		{"admin", "[admin]"},
		{"", "[suite]"}, // unknown defaults to [suite]
	}
	for _, tc := range cases {
		var buf bytes.Buffer
		h := NewConsoleHandlerWithClock(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}, clock.NewFake(anchor), &boolFalse)
		r := slog.NewRecord(anchor, slog.LevelInfo, "test-msg", 0)
		r.AddAttrs(
			slog.String("source", "client"),
			slog.String("app", tc.app),
			slog.String("kind", "log"),
		)
		if err := h.Handle(context.Background(), r); err != nil {
			t.Fatal(err)
		}
		out := buf.String()
		if !strings.Contains(out, tc.marker) {
			t.Errorf("app=%q: expected marker %q in %q", tc.app, tc.marker, out)
		}
		// The 'source' and 'app' keys must NOT appear as key=value pairs.
		if strings.Contains(out, "source=") {
			t.Errorf("app=%q: source should not appear as key=value: %q", tc.app, out)
		}
		if strings.Contains(out, "app=") {
			t.Errorf("app=%q: app should not appear as key=value: %q", tc.app, out)
		}
	}
}

// TestConsoleHandler_ClientStackIndented verifies that the stack field is
// rendered as indented continuation lines under the parent line
// (REQ-OPS-81a, architecture §Console rendering).
func TestConsoleHandler_ClientStackIndented(t *testing.T) {
	var buf bytes.Buffer
	h := NewConsoleHandlerWithClock(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}, clock.NewFake(anchor), &boolFalse)
	r := slog.NewRecord(anchor, slog.LevelError, "js crash", 0)
	r.AddAttrs(
		slog.String("source", "client"),
		slog.String("app", "suite"),
		slog.String("kind", "error"),
		slog.String("stack", "Error: bad\n  at foo (app.js:10)\n  at bar (app.js:20)"),
	)
	if err := h.Handle(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "  | Error: bad") {
		t.Errorf("first stack line missing: %q", out)
	}
	if !strings.Contains(out, "  |   at foo (app.js:10)") {
		t.Errorf("second stack line missing: %q", out)
	}
	if !strings.Contains(out, "  |   at bar (app.js:20)") {
		t.Errorf("third stack line missing: %q", out)
	}
	// 'stack' key must NOT appear inline as stack=...
	if strings.Contains(out, "stack=") {
		t.Errorf("stack field should not appear inline: %q", out)
	}
}

// TestConsoleHandler_ClientMarkerWithColor verifies that the [suite]/[admin]
// marker uses the magenta ANSI escape when color is forced on.
func TestConsoleHandler_ClientMarkerWithColor(t *testing.T) {
	var buf bytes.Buffer
	h := NewConsoleHandlerWithClock(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}, clock.NewFake(anchor), &boolTrue)
	r := slog.NewRecord(anchor, slog.LevelInfo, "colorful-client", 0)
	r.AddAttrs(
		slog.String("source", "client"),
		slog.String("app", "suite"),
		slog.String("kind", "log"),
	)
	if err := h.Handle(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	// Magenta escape before the marker.
	if !strings.Contains(out, ansiMagenta) {
		t.Errorf("expected magenta escape for client marker in %q", out)
	}
	if !strings.Contains(out, "[suite]") {
		t.Errorf("expected [suite] marker in %q", out)
	}
}

// TestDefaultClientLogConfig verifies defaults match the requirements.
func TestDefaultClientLogConfig(t *testing.T) {
	cfg := DefaultClientLogConfig()
	if cfg.ReorderWindowMS != 1000 {
		t.Errorf("ReorderWindowMS: got %d want 1000", cfg.ReorderWindowMS)
	}
	if cfg.ConsoleLevel != "warn" {
		t.Errorf("ConsoleLevel: got %q want %q", cfg.ConsoleLevel, "warn")
	}
}
