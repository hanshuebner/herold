package directory

import (
	"sync"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/observe"
)

// breakerState is the lifecycle position of one plugin's circuit
// breaker. Numeric values match the gauge encoding emitted by
// observe.DirectoryPluginBreakerState (0=closed, 1=half-open, 2=open)
// per REQ-DIR-RCPT-05.
type breakerState int

const (
	breakerClosed breakerState = iota
	breakerHalfOpen
	breakerOpen
)

// Defaults from REQ-DIR-RCPT-05.
const (
	defaultBreakerWindow       = 30 * time.Second
	defaultBreakerCooldown     = 60 * time.Second
	defaultBreakerMinCalls     = 20
	defaultBreakerErrThreshold = 0.5
)

// breakerSample records one outcome inside the sliding window.
type breakerSample struct {
	at  time.Time
	err bool
}

// pluginBreaker is one plugin's sliding-window failure counter.
// Concurrency-safe; one instance per plugin name lives inside
// ResolveRcptBreakers.
type pluginBreaker struct {
	name     string
	clk      clock.Clock
	window   time.Duration
	cooldown time.Duration
	minCalls int
	errFrac  float64

	mu       sync.Mutex
	samples  []breakerSample
	state    breakerState
	openedAt time.Time
	// halfOpenInFlight gates the half-open probe: after cooldown, the
	// breaker permits exactly one call through; subsequent callers see
	// the breaker still reporting open until that probe returns.
	halfOpenInFlight bool
}

// newPluginBreaker returns a breaker with the REQ-DIR-RCPT-05
// defaults bound to clk for deterministic tests.
func newPluginBreaker(name string, clk clock.Clock) *pluginBreaker {
	if clk == nil {
		clk = clock.NewReal()
	}
	b := &pluginBreaker{
		name:     name,
		clk:      clk,
		window:   defaultBreakerWindow,
		cooldown: defaultBreakerCooldown,
		minCalls: defaultBreakerMinCalls,
		errFrac:  defaultBreakerErrThreshold,
		state:    breakerClosed,
	}
	b.publishState()
	return b
}

// allow reports whether a call may proceed against the plugin. When
// the breaker is open, allow returns false until cooldown elapses;
// after cooldown, exactly one probe call is permitted (half-open) and
// the rest see false until the probe completes.
func (b *pluginBreaker) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case breakerClosed:
		return true
	case breakerOpen:
		now := b.clk.Now()
		if now.Sub(b.openedAt) >= b.cooldown {
			b.state = breakerHalfOpen
			b.halfOpenInFlight = true
			b.publishStateLocked()
			return true
		}
		return false
	case breakerHalfOpen:
		if b.halfOpenInFlight {
			return false
		}
		b.halfOpenInFlight = true
		return true
	}
	return false
}

// record observes one call outcome. Errors include timeouts, transport
// failures, and disabled-plugin replies — anything that would map to
// defer 4.4.3 at the SMTP layer.
func (b *pluginBreaker) record(err bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clk.Now()
	switch b.state {
	case breakerHalfOpen:
		// One probe completed: success closes, failure re-opens for a
		// fresh cooldown.
		b.halfOpenInFlight = false
		if err {
			b.state = breakerOpen
			b.openedAt = now
			b.publishStateLocked()
			return
		}
		b.state = breakerClosed
		b.samples = b.samples[:0]
		b.publishStateLocked()
		return
	case breakerOpen:
		// Stray record while open; ignore.
		return
	}
	// breakerClosed — append, prune, evaluate.
	b.samples = append(b.samples, breakerSample{at: now, err: err})
	cutoff := now.Add(-b.window)
	kept := b.samples[:0]
	for _, s := range b.samples {
		if s.at.After(cutoff) {
			kept = append(kept, s)
		}
	}
	b.samples = kept
	if len(b.samples) < b.minCalls {
		return
	}
	errs := 0
	for _, s := range b.samples {
		if s.err {
			errs++
		}
	}
	if float64(errs)/float64(len(b.samples)) >= b.errFrac {
		b.state = breakerOpen
		b.openedAt = now
		b.publishStateLocked()
	}
}

// State returns the current state for tests / observability code.
func (b *pluginBreaker) State() breakerState {
	b.mu.Lock()
	defer b.mu.Unlock()
	// Promote open->half-open when cooldown has elapsed so observers
	// see the gauge transition without waiting for the next allow().
	if b.state == breakerOpen && b.clk.Now().Sub(b.openedAt) >= b.cooldown {
		// Don't actually flip to half-open here; that requires a probe
		// caller. Just report what allow() would observe.
		return breakerOpen
	}
	return b.state
}

func (b *pluginBreaker) publishState() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.publishStateLocked()
}

func (b *pluginBreaker) publishStateLocked() {
	if observe.DirectoryPluginBreakerState == nil {
		return
	}
	observe.DirectoryPluginBreakerState.WithLabelValues(b.name).Set(float64(b.state))
}

// ResolveRcptBreakers is the per-plugin breaker registry consumed by
// the SMTP RCPT path. One instance is shared across the inbound
// listener; callers acquire a *pluginBreaker via Get(name).
type ResolveRcptBreakers struct {
	clk clock.Clock
	mu  sync.Mutex
	m   map[string]*pluginBreaker
}

// NewResolveRcptBreakers constructs a registry. Pass clk = nil for the
// real clock; tests inject a FakeClock.
func NewResolveRcptBreakers(clk clock.Clock) *ResolveRcptBreakers {
	if clk == nil {
		clk = clock.NewReal()
	}
	return &ResolveRcptBreakers{clk: clk, m: make(map[string]*pluginBreaker)}
}

// Get returns the breaker for plugin name, creating it on first use.
func (r *ResolveRcptBreakers) Get(name string) *pluginBreaker {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.m[name]
	if !ok {
		b = newPluginBreaker(name, r.clk)
		r.m[name] = b
	}
	return b
}
