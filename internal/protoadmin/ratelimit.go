package protoadmin

import (
	"sync"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
)

// rateLimiter is a per-key sliding-window counter. Each key tracks a
// ring of timestamps; the limiter allows a request only if the ring has
// fewer than `limit` timestamps inside the most recent `window`.
//
// The implementation uses a circular buffer keyed by caller ID so
// memory does not grow unboundedly with traffic: once the ring fills,
// subsequent requests overwrite the oldest slot and allow the new
// request only if the slot being overwritten is outside the window.
type rateLimiter struct {
	clk    clock.Clock
	limit  int
	window time.Duration

	mu      sync.Mutex
	buckets map[string]*ringBuf
}

type ringBuf struct {
	stamps []time.Time
	head   int
}

func newRateLimiter(clk clock.Clock, limit int, window time.Duration) *rateLimiter {
	if limit <= 0 {
		limit = 1
	}
	if window <= 0 {
		window = time.Minute
	}
	return &rateLimiter{
		clk:     clk,
		limit:   limit,
		window:  window,
		buckets: make(map[string]*ringBuf),
	}
}

// allow returns true if the caller identified by key may proceed; it
// also records the request in the caller's ring. Returns the duration
// after which the request would have been allowed when denying.
func (rl *rateLimiter) allow(key string) (ok bool, retryAfter time.Duration) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := rl.clk.Now()
	rb, ok2 := rl.buckets[key]
	if !ok2 {
		rb = &ringBuf{stamps: make([]time.Time, rl.limit)}
		rl.buckets[key] = rb
	}
	// Count timestamps inside the window, dropping expired entries in place.
	oldest := now.Add(-rl.window)
	inWindow := 0
	for i := range rb.stamps {
		if !rb.stamps[i].Before(oldest) && !rb.stamps[i].IsZero() {
			inWindow++
		}
	}
	if inWindow >= rl.limit {
		// Find the earliest timestamp still in window; that is the slot
		// that unlocks next.
		earliest := now
		for _, t := range rb.stamps {
			if !t.IsZero() && !t.Before(oldest) && t.Before(earliest) {
				earliest = t
			}
		}
		retry := rl.window - now.Sub(earliest)
		if retry < time.Second {
			retry = time.Second
		}
		return false, retry
	}
	rb.stamps[rb.head] = now
	rb.head = (rb.head + 1) % len(rb.stamps)
	return true, 0
}
