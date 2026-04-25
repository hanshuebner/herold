package protosend

import (
	"sync"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
)

// rateLimiter is a per-key sliding-window counter used for the per-API-
// key request rate limit (REQ-SEND-32). Mirrors the protoadmin
// implementation; kept in-package rather than shared so the two HTTP
// surfaces can tune their limits independently.
//
// Implementation note: protoadmin/ratelimit.go is the spiritual twin.
// A future cleanup can converge both onto an internal/httplimit helper
// once a third caller arrives.
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
	oldest := now.Add(-rl.window)
	inWindow := 0
	for i := range rb.stamps {
		if !rb.stamps[i].Before(oldest) && !rb.stamps[i].IsZero() {
			inWindow++
		}
	}
	if inWindow >= rl.limit {
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

// count returns the number of timestamps currently in the window for
// the given key. Used by /quota to surface short-window usage.
func (rl *rateLimiter) count(key string) int {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rb, ok := rl.buckets[key]
	if !ok {
		return 0
	}
	now := rl.clk.Now()
	oldest := now.Add(-rl.window)
	c := 0
	for _, t := range rb.stamps {
		if !t.IsZero() && !t.Before(oldest) {
			c++
		}
	}
	return c
}
