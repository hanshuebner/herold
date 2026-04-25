package protochat

import (
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
	pending map[store.PrincipalID]chan struct{} // grace-period cancellation

	graceWindow time.Duration
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
		pending:     make(map[store.PrincipalID]chan struct{}),
		graceWindow: graceWindow,
	}
}

// Set records the presence state for pid as of the supplied
// timestamp. Cancels any in-flight offline-grace timer for pid; a
// presence.set frame from a reconnecting client supersedes the
// pending offline transition.
func (p *PresenceTracker) Set(pid store.PrincipalID, state string, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state[pid] = Presence{State: state, LastSeenAt: now}
	if cancel, ok := p.pending[pid]; ok {
		close(cancel)
		delete(p.pending, pid)
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
// "offline"). reconnected reports whether onExpire fired (false) or
// the grace was cancelled by a Set / further connect (true).
//
// The function spawns one goroutine per call. Callers MUST cancel
// outstanding pending records via Set or Cancel; long-lived servers
// are not affected because graceWindow is bounded.
func (p *PresenceTracker) ScheduleOffline(pid store.PrincipalID, onExpire func(now time.Time)) {
	cancel := make(chan struct{})
	p.mu.Lock()
	if existing, ok := p.pending[pid]; ok {
		close(existing)
	}
	p.pending[pid] = cancel
	p.mu.Unlock()

	go func() {
		select {
		case now := <-p.clk.After(p.graceWindow):
			p.mu.Lock()
			if cur, ok := p.pending[pid]; ok && cur == cancel {
				delete(p.pending, pid)
				p.state[pid] = Presence{State: "offline", LastSeenAt: now}
				p.mu.Unlock()
				if onExpire != nil {
					onExpire(now)
				}
				return
			}
			p.mu.Unlock()
		case <-cancel:
			// Reconnected within the grace window.
		}
	}()
}

// CancelOffline cancels a pending offline transition for pid, if
// any. Used when a fresh connection lands within the grace window.
func (p *PresenceTracker) CancelOffline(pid store.PrincipalID) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if cancel, ok := p.pending[pid]; ok {
		close(cancel)
		delete(p.pending, pid)
	}
}
