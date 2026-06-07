package protoshare

import (
	"sync"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
)

// rateLimiter is a per-key sliding-window counter. Each key tracks a
// ring of timestamps; a request is allowed only if fewer than `limit`
// timestamps fall within the most recent `window`.
//
// The implementation mirrors the one in protoadmin so both surfaces use
// the same well-tested shape.
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

// allow returns (true, 0) when the request may proceed; (false, retryAfter)
// when the rate limit has been exceeded.
func (rl *rateLimiter) allow(key string) (ok bool, retryAfter time.Duration) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := rl.clk.Now()
	rb, exists := rl.buckets[key]
	if !exists {
		rb = &ringBuf{stamps: make([]time.Time, rl.limit)}
		rl.buckets[key] = rb
	}
	oldest := now.Add(-rl.window)
	inWindow := 0
	for i := range rb.stamps {
		if !rb.stamps[i].IsZero() && !rb.stamps[i].Before(oldest) {
			inWindow++
		}
	}
	if inWindow >= rl.limit {
		// Return the wait duration until the earliest in-window stamp exits.
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
