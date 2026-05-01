package observe

// console_client.go — per-session reorder buffer for source=client console
// output (REQ-OPS-210) and TTY rendering for client log records
// (REQ-OPS-81a, REQ-OPS-204).
//
// Only console sinks (format=console or format=auto on a TTY) route
// source=client records through the reorder buffer. JSON file sinks and the
// OTLP fan-out are unaffected; they receive events in arrival order.
//
// Architecture: docs/design/server/architecture/10-client-log-pipeline.md
// §Concurrency and §Console rendering of client records.

import (
	"container/heap"
	"context"
	"log/slog"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
)

// ClientLogConfig holds the console-client rendering parameters read by this
// layer. Task #8 will add the full [clientlog] TOML block; until then these
// defaults are the only source of truth.
type ClientLogConfig struct {
	// ReorderWindowMS is how long (in ms) the reorder buffer holds events for
	// a session before flushing in effective-time order. Default 1000 ms
	// (REQ-OPS-210, REQ-OPS-219).
	ReorderWindowMS int64
	// ConsoleLevel is the minimum level for source=client kind=log records on
	// console sinks. kind=error and kind=vital always pass regardless of this
	// setting. Default "warn" (REQ-OPS-204).
	ConsoleLevel string
}

// DefaultClientLogConfig returns the defaults used when no [clientlog] block
// has been parsed yet (task #8 will supply real values).
func DefaultClientLogConfig() ClientLogConfig {
	return ClientLogConfig{
		ReorderWindowMS: 1000,
		ConsoleLevel:    "warn",
	}
}

// reorderWindowDuration converts ReorderWindowMS to a time.Duration.
func (c ClientLogConfig) reorderWindowDuration() time.Duration {
	if c.ReorderWindowMS <= 0 {
		return 0
	}
	return time.Duration(c.ReorderWindowMS) * time.Millisecond
}

// consoleLevel parses ConsoleLevel to a slog.Level.
func (c ClientLogConfig) consoleLevel() slog.Level {
	return parseLogLevel(c.ConsoleLevel)
}

// --- heap element ---

// reorderEntry is one pending client-log record in the min-heap.
type reorderEntry struct {
	// effectiveTime is client_ts + clock_skew_ms; used for heap ordering.
	effectiveTime time.Time
	// deadline is the wall-clock time at which this entry must be flushed.
	deadline time.Time
	// sessionID is the session this entry belongs to.
	sessionID string
	// record is the slog.Record to emit when flushed.
	record slog.Record
	// ctx is forwarded to the downstream Handle call.
	ctx context.Context
}

// reorderHeap implements heap.Interface on a slice of *reorderEntry,
// ordered by effectiveTime ascending (min-heap).
type reorderHeap []*reorderEntry

func (h reorderHeap) Len() int            { return len(h) }
func (h reorderHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h reorderHeap) Less(i, j int) bool  { return h[i].effectiveTime.Before(h[j].effectiveTime) }
func (h *reorderHeap) Push(x interface{}) { *h = append(*h, x.(*reorderEntry)) }
func (h *reorderHeap) Pop() interface{} {
	old := *h
	n := len(old)
	e := old[n-1]
	old[n-1] = nil
	*h = old[:n-1]
	return e
}

// --- per-session state ---

// sessionState tracks the per-session timer for the reorder buffer.
type sessionState struct {
	// nextDeadline is the earliest deadline the current timer was set for.
	nextDeadline time.Time
	// timer is the outstanding AfterFunc timer. Nil before the first entry
	// for this session arrives. After a timer fires, Stop returns false but
	// the field is non-nil; syncSessionTimers always re-registers after a
	// flush to handle this.
	timer clock.Timer
}

// --- ClientReorderHandler ---

// ClientReorderHandler wraps a downstream slog.Handler (always a console
// handler) and interposes the per-session reorder buffer for source=client
// records (REQ-OPS-210). All other records pass through directly.
//
// Call Start before the first Handle; Stop drains the buffer and shuts the
// internal goroutine down.
type ClientReorderHandler struct {
	downstream slog.Handler
	cfg        ClientLogConfig
	clk        clock.Clock

	// inCh receives entries from Handle; owned exclusively by the reorder
	// goroutine after Start.
	inCh chan *reorderEntry
	// flushCh receives wake-up signals from per-session deadline timers.
	flushCh chan struct{}
	// stopCh is closed when Stop is called.
	stopCh chan struct{}
	// doneCh is closed when the reorder goroutine has fully exited.
	doneCh chan struct{}
	// syncCh, when non-nil, receives one signal after the goroutine finishes
	// processing each item from inCh. Used only in tests; nil in production.
	syncCh chan struct{}
	// flushSyncCh, when non-nil, receives one signal after the goroutine
	// finishes processing each item from flushCh (i.e. after flushExpired has
	// returned and syncSessionTimers has rescheduled timers). Used only in
	// tests; nil in production.
	flushSyncCh chan struct{}

	// preAttrs holds pre-scoped attributes from WithAttrs calls.
	preAttrs []slog.Attr
}

// NewClientReorderHandler creates a ClientReorderHandler. downstream must be
// a ready-to-use handler; cfg supplies window and level parameters; clk is
// injected for deterministic tests (nil uses the real clock).
//
// Call Start to launch the internal goroutine, Stop to drain and shut down.
func NewClientReorderHandler(downstream slog.Handler, cfg ClientLogConfig, clk clock.Clock) *ClientReorderHandler {
	if clk == nil {
		clk = clock.NewReal()
	}
	return &ClientReorderHandler{
		downstream: downstream,
		cfg:        cfg,
		clk:        clk,
		inCh:       make(chan *reorderEntry, 512),
		flushCh:    make(chan struct{}, 8),
		stopCh:     make(chan struct{}),
		doneCh:     make(chan struct{}),
	}
}

// Start launches the reorder goroutine. Must be called exactly once.
func (h *ClientReorderHandler) Start() {
	go h.run()
}

// Stop signals the reorder goroutine to flush all pending entries and exit.
// Blocks until the goroutine has fully exited. Safe to call multiple times;
// subsequent calls return immediately.
func (h *ClientReorderHandler) Stop() {
	select {
	case <-h.stopCh:
	default:
		close(h.stopCh)
	}
	<-h.doneCh
}

// Enabled implements slog.Handler.
func (h *ClientReorderHandler) Enabled(ctx context.Context, lvl slog.Level) bool {
	return h.downstream.Enabled(ctx, lvl)
}

// Handle routes the record through the reorder buffer when it is a
// source=client record, otherwise passes it to the downstream handler
// directly.
//
// Per-source level throttle: source=client records with kind=log below the
// console_level threshold are suppressed. kind=error and kind=vital always
// pass (REQ-OPS-204).
func (h *ClientReorderHandler) Handle(ctx context.Context, r slog.Record) error {
	attrs := collectRecordAttrs(h.preAttrs, r)

	if !isClientSource(attrs) {
		return h.downstream.Handle(ctx, r)
	}

	// Per-source level throttle.
	if shouldThrottleClientRecord(attrs, r.Level, h.cfg.consoleLevel()) {
		return nil
	}

	clientTSStr := attrStringValue(attrs, "client_ts")
	clockSkewMS := attrInt64Value(attrs, "clock_skew_ms")
	sessionID := attrStringValue(attrs, "client_session")

	effectiveTime := effectiveClientTime(clientTSStr, clockSkewMS, r.Time)
	window := h.cfg.reorderWindowDuration()
	deadline := effectiveTime.Add(window)
	now := h.clk.Now()

	// If the deadline has already passed, emit immediately with late=true.
	if !now.Before(deadline) {
		late := cloneWithAttr(r, slog.Bool("late", true))
		return h.downstream.Handle(ctx, late)
	}

	entry := &reorderEntry{
		effectiveTime: effectiveTime,
		deadline:      deadline,
		sessionID:     sessionID,
		record:        r,
		ctx:           ctx,
	}

	select {
	case h.inCh <- entry:
	case <-h.stopCh:
		// Buffer closed during shutdown; emit directly.
		return h.downstream.Handle(ctx, r)
	}
	return nil
}

// WithAttrs implements slog.Handler.
func (h *ClientReorderHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	nh := h.shallowClone()
	nh.preAttrs = append(nh.preAttrs, attrs...)
	nh.downstream = h.downstream.WithAttrs(attrs)
	return nh
}

// WithGroup implements slog.Handler.
func (h *ClientReorderHandler) WithGroup(name string) slog.Handler {
	nh := h.shallowClone()
	nh.downstream = h.downstream.WithGroup(name)
	return nh
}

func (h *ClientReorderHandler) shallowClone() *ClientReorderHandler {
	preAttrs := make([]slog.Attr, len(h.preAttrs))
	copy(preAttrs, h.preAttrs)
	return &ClientReorderHandler{
		downstream:  h.downstream,
		cfg:         h.cfg,
		clk:         h.clk,
		inCh:        h.inCh,
		flushCh:     h.flushCh,
		stopCh:      h.stopCh,
		doneCh:      h.doneCh,
		syncCh:      h.syncCh,
		flushSyncCh: h.flushSyncCh,
		preAttrs:    preAttrs,
	}
}

// enableTestSync activates per-item sync signalling. Must be called before
// Start. Each item processed from inCh by the goroutine sends one value to
// syncCh. Callers receive from syncCh to know an item has been processed.
// This is a test-only seam; never called in production.
func (h *ClientReorderHandler) enableTestSync() {
	h.syncCh = make(chan struct{}, 64)
}

// enableTestFlushSync activates per-flush sync signalling. Must be called
// before Start. After each flushCh signal is processed (flushExpired called
// and syncSessionTimers rescheduled), one value is sent to flushSyncCh.
// Callers receive from flushSyncCh to know the flush cycle has completed.
// This is a test-only seam; never called in production.
func (h *ClientReorderHandler) enableTestFlushSync() {
	h.flushSyncCh = make(chan struct{}, 64)
}

// run is the single goroutine that owns the heap and per-session timers.
func (h *ClientReorderHandler) run() {
	defer close(h.doneCh)

	hp := make(reorderHeap, 0, 64)
	heap.Init(&hp)
	sessions := make(map[string]*sessionState)

	flushExpired := func() {
		now := h.clk.Now()
		for hp.Len() > 0 && !now.Before(hp[0].deadline) {
			e := heap.Pop(&hp).(*reorderEntry)
			_ = h.downstream.Handle(e.ctx, e.record)
		}
		syncSessionTimers(sessions, &hp, h.clk, h.flushCh)
	}

	for {
		select {
		case entry := <-h.inCh:
			heap.Push(&hp, entry)
			upsertSessionTimer(sessions, entry, h.clk, h.flushCh)
			if h.syncCh != nil {
				select {
				case h.syncCh <- struct{}{}:
				default:
				}
			}

		case <-h.flushCh:
			flushExpired()
			if h.flushSyncCh != nil {
				select {
				case h.flushSyncCh <- struct{}{}:
				default:
				}
			}

		case <-h.stopCh:
			// Drain in-channel before exiting.
		drain:
			for {
				select {
				case entry := <-h.inCh:
					heap.Push(&hp, entry)
				default:
					break drain
				}
			}
			// Flush everything in effective-time order.
			for hp.Len() > 0 {
				e := heap.Pop(&hp).(*reorderEntry)
				_ = h.downstream.Handle(e.ctx, e.record)
			}
			return
		}
	}
}

// upsertSessionTimer ensures a deadline timer is running for the session that
// owns entry. If the entry has an earlier deadline than the current timer for
// that session, the existing timer is replaced.
func upsertSessionTimer(sessions map[string]*sessionState, entry *reorderEntry, clk clock.Clock, flushCh chan struct{}) {
	sid := entry.sessionID
	st, ok := sessions[sid]
	if !ok {
		st = &sessionState{}
		sessions[sid] = st
	}
	if st.timer != nil && !entry.deadline.Before(st.nextDeadline) {
		// An existing timer covers this deadline or an earlier one; no change needed.
		return
	}
	if st.timer != nil {
		st.timer.Stop()
	}
	delay := entry.deadline.Sub(clk.Now())
	if delay < 0 {
		delay = 0
	}
	st.nextDeadline = entry.deadline
	st.timer = makeFlushTimer(clk, delay, flushCh)
}

// syncSessionTimers removes sessions with no pending heap entries and
// reschedules timers for sessions with remaining entries. It always
// re-registers the timer after a flush because the previous timer has
// already fired and must not be relied upon to fire again.
func syncSessionTimers(sessions map[string]*sessionState, hp *reorderHeap, clk clock.Clock, flushCh chan struct{}) {
	// Compute earliest deadline per session from the heap.
	earliest := make(map[string]time.Time, len(sessions))
	for _, e := range *hp {
		if d, ok := earliest[e.sessionID]; !ok || e.deadline.Before(d) {
			earliest[e.sessionID] = e.deadline
		}
	}
	for sid, st := range sessions {
		d, active := earliest[sid]
		if !active {
			if st.timer != nil {
				st.timer.Stop()
			}
			delete(sessions, sid)
			continue
		}
		// Always reschedule: the previous timer may have already fired (which
		// is why we are in syncSessionTimers). If a newer/earlier deadline
		// arrived via upsertSessionTimer, that timer is still pending; stop it
		// and re-register for the heap's earliest deadline.
		if st.timer != nil {
			st.timer.Stop()
		}
		delay := d.Sub(clk.Now())
		if delay < 0 {
			delay = 0
		}
		st.nextDeadline = d
		st.timer = makeFlushTimer(clk, delay, flushCh)
	}
}

// makeFlushTimer registers a one-shot AfterFunc timer that sends to flushCh.
func makeFlushTimer(clk clock.Clock, delay time.Duration, flushCh chan struct{}) clock.Timer {
	return clk.AfterFunc(delay, func() {
		select {
		case flushCh <- struct{}{}:
		default:
		}
	})
}

// --- attribute helpers ---

// collectRecordAttrs merges pre-scoped attrs with the record's attrs into a
// flat map. Record attrs overwrite pre-scoped attrs for the same key.
func collectRecordAttrs(pre []slog.Attr, r slog.Record) map[string]string {
	m := make(map[string]string, len(pre)+r.NumAttrs())
	for _, a := range pre {
		if a.Key != "" {
			m[a.Key] = a.Value.String()
		}
	}
	r.Attrs(func(a slog.Attr) bool {
		if a.Key != "" {
			m[a.Key] = a.Value.String()
		}
		return true
	})
	return m
}

// isClientSource reports whether attrs contain source=client.
func isClientSource(attrs map[string]string) bool {
	return attrs["source"] == "client"
}

// shouldThrottleClientRecord reports whether a source=client record should be
// suppressed on the console sink. Only kind=log records below the console
// level are throttled; kind=error and kind=vital always pass.
func shouldThrottleClientRecord(attrs map[string]string, lvl slog.Level, consoleLevel slog.Level) bool {
	kind := attrs["kind"]
	if kind == "error" || kind == "vital" {
		return false
	}
	return lvl < consoleLevel
}

// attrStringValue returns the string value of key from attrs, or "".
func attrStringValue(attrs map[string]string, key string) string {
	return attrs[key]
}

// attrInt64Value parses an int64 from attrs[key], returning 0 on failure.
func attrInt64Value(attrs map[string]string, key string) int64 {
	v, ok := attrs[key]
	if !ok || v == "" {
		return 0
	}
	var n int64
	neg := false
	i := 0
	if len(v) > 0 && v[0] == '-' {
		neg = true
		i++
	}
	for ; i < len(v); i++ {
		c := v[i]
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int64(c-'0')
	}
	if neg {
		n = -n
	}
	return n
}

// effectiveClientTime computes the effective client time:
// client_ts + clock_skew_ms. Falls back to fallback on parse failure.
func effectiveClientTime(clientTSStr string, clockSkewMS int64, fallback time.Time) time.Time {
	if clientTSStr == "" {
		return fallback
	}
	t, err := time.Parse(time.RFC3339Nano, clientTSStr)
	if err != nil {
		t, err = time.Parse(time.RFC3339, clientTSStr)
		if err != nil {
			return fallback
		}
	}
	if clockSkewMS != 0 {
		t = t.Add(time.Duration(clockSkewMS) * time.Millisecond)
	}
	return t
}

// cloneWithAttr returns a shallow clone of r with attr appended.
func cloneWithAttr(r slog.Record, attr slog.Attr) slog.Record {
	clone := r.Clone()
	clone.AddAttrs(attr)
	return clone
}

// --- wiring into buildSinkHandler ---

// wrapConsoleForClientLogs wraps a ConsoleHandler with the reorder buffer for
// source=client records. Returns (wrappedHandler, reorderHandler) where
// reorderHandler is non-nil when a buffer was created (callers must call Stop
// to drain it on shutdown). clk may be nil (uses real clock).
func wrapConsoleForClientLogs(base *ConsoleHandler, cfg ClientLogConfig, clk clock.Clock) (slog.Handler, *ClientReorderHandler) {
	if cfg.reorderWindowDuration() == 0 {
		return base, nil
	}
	reorder := NewClientReorderHandler(base, cfg, clk)
	reorder.Start()
	return reorder, reorder
}
