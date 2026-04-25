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
	if b.tokens < weight {
		return false
	}
	b.tokens -= weight
	return true
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
