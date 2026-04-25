package protochat

import (
	"sync"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
)

// rateLimiter is a per-principal token-bucket gate on inbound
// frames. Default policy is 60 tokens/s sustained with a 120-token
// burst (REQ-CHAT-43 sets typing/presence at 1/s but our cost
// weighting models cheap and expensive frames in a single budget).
//
// Frame weights:
//   - typing.start / typing.stop / ping               : 1
//   - presence.set / subscribe / unsubscribe          : 2
//   - call.signal                                     : 5
//
// presence.set carries weight 2 because the broadcaster fans the
// frame out to every peer the publisher shares a conversation with;
// charging a higher weight than typing/ping reins in a hostile client
// that would otherwise spam presence transitions to amplify outbound
// traffic.
//
// A frame whose admission would push tokens negative is dropped
// (the read pump emits an "error{rate_limited}" frame to the client
// and continues processing; we do NOT close the connection on a
// single drop because clients can momentarily burst).
type rateLimiter struct {
	clk   clock.Clock
	rate  float64 // tokens per second
	burst float64

	mu      sync.Mutex
	buckets map[store.PrincipalID]*rateBucket
}

type rateBucket struct {
	tokens float64
	last   time.Time
}

// rateLimiterIdleEvictAfter is the threshold past which a bucket whose
// last update is older than this is considered abandoned and may be
// dropped. One hour is well past any reasonable user think-time and far
// past the reconnect grace window; a returning user simply re-enters at
// burst capacity, which is the same admission profile they would have
// received if their previous session had timed out.
const rateLimiterIdleEvictAfter = time.Hour

// rateLimiterEvictPerCall caps the per-allow() eviction work so the
// hot path stays O(1) amortised even when an attacker creates millions
// of bucket entries before going idle. The limiter walks at most this
// many entries on any single allow().
const rateLimiterEvictPerCall = 16

// newRateLimiter constructs a limiter; rate and burst are in tokens
// (frames). Zero or negative values fall through to the defaults.
func newRateLimiter(clk clock.Clock, rate, burst float64) *rateLimiter {
	if clk == nil {
		clk = clock.NewReal()
	}
	if rate <= 0 {
		rate = 60
	}
	if burst <= 0 {
		burst = 120
	}
	return &rateLimiter{
		clk:     clk,
		rate:    rate,
		burst:   burst,
		buckets: make(map[store.PrincipalID]*rateBucket),
	}
}

// allow returns true if a frame of the given weight can be admitted
// for pid. State mutates: a successful admission deducts the weight
// from the bucket; a denial leaves the bucket alone.
//
// Each call also performs an opportunistic LRU-ish sweep: at most
// rateLimiterEvictPerCall entries from the buckets map are inspected,
// and any whose last update is older than rateLimiterIdleEvictAfter
// are dropped. The walk uses Go's randomised map iteration order, so
// over time every entry is visited; combined with the small batch
// limit this keeps the buckets map bounded under churn without a
// dedicated sweeper goroutine. STANDARDS §6 (bounded goroutines).
func (r *rateLimiter) allow(pid store.PrincipalID, weight float64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.clk.Now()
	b, ok := r.buckets[pid]
	if !ok {
		b = &rateBucket{tokens: r.burst, last: now}
		r.buckets[pid] = b
	} else {
		elapsed := now.Sub(b.last).Seconds()
		if elapsed > 0 {
			b.tokens += elapsed * r.rate
			if b.tokens > r.burst {
				b.tokens = r.burst
			}
			b.last = now
		}
	}
	r.evictIdleLocked(pid, now)
	if b.tokens < weight {
		return false
	}
	b.tokens -= weight
	return true
}

// evictIdleLocked walks at most rateLimiterEvictPerCall entries from
// r.buckets and drops any whose last update is older than
// rateLimiterIdleEvictAfter. The current pid is preserved unconditionally
// so an admission never frees the bucket it is about to charge against.
//
// Caller holds r.mu.
func (r *rateLimiter) evictIdleLocked(skip store.PrincipalID, now time.Time) {
	if len(r.buckets) == 0 {
		return
	}
	cutoff := now.Add(-rateLimiterIdleEvictAfter)
	visited := 0
	for pid, b := range r.buckets {
		if visited >= rateLimiterEvictPerCall {
			return
		}
		visited++
		if pid == skip {
			continue
		}
		if b.last.Before(cutoff) {
			delete(r.buckets, pid)
		}
	}
}

// frameWeight maps a client frame Type to its rate-limit cost.
// Unknown types weigh 1 — they will be rejected by the dispatcher
// with bad_frame anyway, but we still want to charge against the
// limiter so a client cannot flood with junk.
func frameWeight(typ string) float64 {
	switch typ {
	case clientTypeCallSignal:
		return 5
	case clientTypeSubscribe, clientTypeUnsubscribe, clientTypePresenceSet:
		return 2
	default:
		return 1
	}
}
