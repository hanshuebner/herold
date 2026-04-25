package protocall

import (
	"sync"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
)

// rateLimiter implements a per-key sliding-window counter sized for
// the credential-mint endpoint (10 / minute). The implementation is
// the same shape as internal/protoadmin's; we keep it here to avoid
// a sibling-package dependency for one helper.
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

// allow returns true if the caller identified by key may proceed.
// retryAfter is the wait until the next slot frees on a deny, clamped
// to a one-second floor so clients do not retry hot.
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
		if !rb.stamps[i].IsZero() && !rb.stamps[i].Before(oldest) {
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
