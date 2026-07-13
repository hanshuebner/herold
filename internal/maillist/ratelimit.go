package maillist

import (
	"sync"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
)

// ratelimit.go bounds the Stage 3 double opt-in confirmation-mail send
// rate per (list, address) (REQ-MLIST-61: "no mail is delivered to a
// pending member" combined with the general concern that a subscribe
// endpoint must not become an outbound-mail amplifier -- an attacker
// who repeatedly POSTs someone else's address must not be able to
// force herold to mail that address over and over). The shape mirrors
// internal/protoshare/ratelimit.go's own well-tested per-key sliding
// window rate limiter; duplicated rather than imported because
// protoshare is a sibling leaf package with no shared base to hang a
// common implementation off without a wider refactor.

// confirmSendLimit and confirmSendWindow bound confirmation-email sends
// to at most this many per (list, address) per window, across both
// entry points that mint a TokenPurposeConfirm token and mail it out:
// PublicServer.handleSubscribe (a fresh or repeated subscribe request)
// and doResubscribeRequest (the REQ-MLIST-59 management-page
// resubscribe button). A legitimate subscriber who does not receive
// the first email, or who mistypes their address the first time
// (mail rejected upstream, so no signal comes back on this bound), can
// still retry a couple of times within the hour; a repeat well beyond
// that is not a legitimate retry pattern and is silently debounced
// (no error surfaced to the caller -- REQ-MLIST-61's no-enumeration
// posture extends to "was this send suppressed by the rate limit").
const (
	confirmSendLimit  = 3
	confirmSendWindow = time.Hour
)

// rateLimiter is a per-key sliding-window counter: a call is allowed
// only if fewer than `limit` prior calls for the same key fall within
// the most recent `window`.
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
	if clk == nil {
		clk = clock.NewReal()
	}
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

// allow reports whether a call for key may proceed right now, and
// records it if so. Unlike protoshare's variant (which also reports a
// retry-after duration for a 429 response), the maillist caller never
// surfaces the rate limit to the HTTP client -- REQ-MLIST-61's no-
// enumeration stance means a debounced send looks identical to a
// freshly sent one -- so only the boolean is needed here.
func (rl *rateLimiter) allow(key string) bool {
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
		return false
	}
	rb.stamps[rb.head] = now
	rb.head = (rb.head + 1) % len(rb.stamps)
	return true
}
