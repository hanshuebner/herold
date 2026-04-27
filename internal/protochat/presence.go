package protochat

import (
	"context"
	"sync"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
)

// Presence is the public-facing snapshot of a principal's current
// presence state.
type Presence struct {
	State      string
	LastSeenAt time.Time
}

// PresenceEntry is the row shape returned by ListSubscribed: a
// (principal, presence) pair. A subscriber asking for the presence
// of N peers gets at most N entries; principals with no recorded
// state are omitted (the client renders them as offline).
type PresenceEntry struct {
	PrincipalID store.PrincipalID
	Presence    Presence
}

// PresenceTracker keeps an in-memory map of per-principal presence.
// State is lost on restart by design — REQ-CHAT-54 marks presence as
// non-durable.
type PresenceTracker struct {
	clk clock.Clock

	mu      sync.RWMutex
	state   map[store.PrincipalID]Presence
	pending map[store.PrincipalID]*pendingOffline

	graceWindow time.Duration

	// wg tracks every pending grace-period timer so the owning
	// Server.Shutdown can wait for in-flight onExpire callbacks to
	// drain. wg.Add(1) lands when ScheduleOffline schedules a timer;
	// wg.Done lands when the timer fires (whether the callback runs or
	// is short-circuited by ctx) or is Stop'd before firing.
	wg sync.WaitGroup
}

// pendingOffline tracks a single in-flight grace-period timer.
type pendingOffline struct {
	timer clock.Timer
	ctx   context.Context
}

// NewPresenceTracker returns a tracker with a 30-second
// disconnect-grace window: the last connection drop schedules an
// offline transition that fires after the window unless a
// reconnection cancels it.
func NewPresenceTracker(clk clock.Clock, graceWindow time.Duration) *PresenceTracker {
	if clk == nil {
		clk = clock.NewReal()
	}
	if graceWindow <= 0 {
		graceWindow = 30 * time.Second
	}
	return &PresenceTracker{
		clk:         clk,
		state:       make(map[store.PrincipalID]Presence),
		pending:     make(map[store.PrincipalID]*pendingOffline),
		graceWindow: graceWindow,
	}
}

// Set records the presence state for pid as of the supplied
// timestamp. Cancels any in-flight offline-grace timer for pid; a
// presence.set frame from a reconnecting client supersedes the
// pending offline transition.
func (p *PresenceTracker) Set(pid store.PrincipalID, state string, now time.Time) {
	p.mu.Lock()
	p.state[pid] = Presence{State: state, LastSeenAt: now}
	po, ok := p.pending[pid]
	if ok {
		delete(p.pending, pid)
	}
	p.mu.Unlock()
	if ok {
		p.stopAndAccount(po)
	}
}

// Get returns the recorded presence for pid. The bool is false when
// no record exists (caller renders as offline).
func (p *PresenceTracker) Get(pid store.PrincipalID) (Presence, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	pr, ok := p.state[pid]
	return pr, ok
}

// ListSubscribed returns the recorded presence rows for the
// principals embedded in conversationMembers. Used by the connect
// path to send a snapshot of peer presence right after a subscribe.
//
// We accept the principals directly rather than the conversation
// ids so the caller can dedupe across overlapping conversations
// before paying for the lookup.
func (p *PresenceTracker) ListSubscribed(principals []store.PrincipalID) []PresenceEntry {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]PresenceEntry, 0, len(principals))
	for _, pid := range principals {
		if pr, ok := p.state[pid]; ok {
			out = append(out, PresenceEntry{PrincipalID: pid, Presence: pr})
		}
	}
	return out
}

// ScheduleOffline starts the disconnect-grace window for pid. After
// graceWindow has elapsed without a reconnect, onExpire is invoked
// (typically: the broadcaster emits a presence-update with state
// "offline"). The callback short-circuits when ctx is already
// cancelled at fire time (Server.Shutdown), preserving the in-memory
// state without firing onExpire so a subsequent restart does not see
// a spurious offline transition.
//
// Registration with the clock is synchronous: by the time
// ScheduleOffline returns, the timer is observable to Clock.Advance
// in tests. Callers MUST cancel outstanding pending records via Set,
// CancelOffline, or Drain so long-lived servers do not accumulate
// timers; graceWindow is bounded so a self-fire eventually clears
// each entry.
func (p *PresenceTracker) ScheduleOffline(ctx context.Context, pid store.PrincipalID, onExpire func(now time.Time)) {
	po := &pendingOffline{ctx: ctx}
	p.mu.Lock()
	if existing, ok := p.pending[pid]; ok {
		delete(p.pending, pid)
		go p.stopAndAccount(existing)
	}
	p.wg.Add(1)
	po.timer = p.clk.AfterFunc(p.graceWindow, func() {
		defer p.wg.Done()
		// Acquire the tracker mutex once: we must validate that this
		// timer is still the live pending entry for pid before it can
		// safely emit an offline transition. A superseding Set /
		// ScheduleOffline / CancelOffline either wins the lock and
		// removes us first (we observe ok=false / cur != po) or loses
		// it and proceeds against a fresh schedule.
		p.mu.Lock()
		cur, ok := p.pending[pid]
		if !ok || cur != po {
			p.mu.Unlock()
			return
		}
		// If the supplied ctx has already been cancelled (server
		// shutdown), drop the pending entry without firing onExpire.
		// The tracker's in-memory state is non-durable so a synthetic
		// offline transition during drain would only confuse
		// subscribers.
		if ctx.Err() != nil {
			delete(p.pending, pid)
			p.mu.Unlock()
			return
		}
		delete(p.pending, pid)
		now := p.clk.Now()
		p.state[pid] = Presence{State: "offline", LastSeenAt: now}
		p.mu.Unlock()
		if onExpire != nil {
			onExpire(now)
		}
	})
	p.pending[pid] = po
	p.mu.Unlock()
}

// stopAndAccount stops a pending timer and decrements the wg if the
// stop happened before the timer's callback could run. The callback
// owns its own wg.Done in the fire path; double-counting is avoided
// by inspecting Stop's return value.
func (p *PresenceTracker) stopAndAccount(po *pendingOffline) {
	if po.timer.Stop() {
		p.wg.Done()
	}
}

// Drain stops every pending grace-period timer. Used by
// Server.Shutdown to bound the drain: without it, real-clock pending
// timers would only fire when their graceWindow elapsed, and Wait
// would block on them. Drain is safe to call concurrently with
// ScheduleOffline, but new schedules after Drain may still leak.
func (p *PresenceTracker) Drain() {
	p.mu.Lock()
	pending := make([]*pendingOffline, 0, len(p.pending))
	for _, po := range p.pending {
		pending = append(pending, po)
	}
	p.pending = make(map[store.PrincipalID]*pendingOffline)
	p.mu.Unlock()
	for _, po := range pending {
		p.stopAndAccount(po)
	}
}

// Wait blocks until every in-flight ScheduleOffline timer has either
// fired (and its callback has returned) or been Stop'd. Used by
// Server.Shutdown after Drain so callers can bound the drain by their
// own timeout.
func (p *PresenceTracker) Wait() {
	p.wg.Wait()
}

// CancelOffline cancels a pending offline transition for pid, if
// any. Used when a fresh connection lands within the grace window.
func (p *PresenceTracker) CancelOffline(pid store.PrincipalID) {
	p.mu.Lock()
	po, ok := p.pending[pid]
	if ok {
		delete(p.pending, pid)
	}
	p.mu.Unlock()
	if ok {
		p.stopAndAccount(po)
	}
}
