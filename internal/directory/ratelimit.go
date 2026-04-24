package directory

import (
	"sync"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
)

// Rate limiter defaults. Chosen to match REQ-AUTH-23's spirit while
// staying conservative for Wave 1; these are tunable later via config.
const (
	rlMaxFailures = 5
	rlWindow      = time.Minute
	rlCooldown    = 5 * time.Minute
)

type rlKey struct {
	email  string
	source string
}

type rlEntry struct {
	failures    []time.Time
	cooldown    time.Time // if non-zero, attempts rejected until this instant
	hasCooldown bool
}

// rateLimiter implements a sliding-window failure counter keyed by
// (email, source). It is safe for concurrent use.
type rateLimiter struct {
	clk clock.Clock
	mu  sync.Mutex
	m   map[rlKey]*rlEntry
}

func newRateLimiter(clk clock.Clock) *rateLimiter {
	return &rateLimiter{clk: clk, m: make(map[rlKey]*rlEntry)}
}

// allow returns true when the caller may proceed with an auth attempt.
// It does NOT record a failure; callers record via record() after a
// failed attempt.
func (r *rateLimiter) allow(k rlKey) bool {
	now := r.clk.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.m[k]
	if !ok {
		return true
	}
	if e.hasCooldown {
		if now.Before(e.cooldown) {
			return false
		}
		// Cooldown elapsed; reset.
		e.hasCooldown = false
		e.failures = nil
	}
	return true
}

// record counts a failed auth attempt. On the rlMaxFailures-th failure
// within rlWindow, the key enters cooldown until now+rlCooldown.
func (r *rateLimiter) record(k rlKey) {
	now := r.clk.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.m[k]
	if !ok {
		e = &rlEntry{}
		r.m[k] = e
	}
	cutoff := now.Add(-rlWindow)
	kept := e.failures[:0]
	for _, t := range e.failures {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	e.failures = append(kept, now)
	if len(e.failures) >= rlMaxFailures {
		e.cooldown = now.Add(rlCooldown)
		e.hasCooldown = true
	}
}

// clear removes rate-limit state on a successful attempt.
func (r *rateLimiter) clear(k rlKey) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.m, k)
}
