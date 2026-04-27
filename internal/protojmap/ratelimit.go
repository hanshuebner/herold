package protojmap

import (
	"sync"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
)

// tokenBucket implements REQ-STORE-20..25 per-principal download
// throttling. Tokens are bytes; replenishment is rate-per-second,
// capped at burst. The clock is injected so tests advance deterministic
// time. Mirrors protoimap's tokenBucket so tests share a mental model
// across protocols.
type tokenBucket struct {
	clk    clock.Clock
	ratePS int64
	burst  int64

	mu         sync.Mutex
	tokens     int64
	lastRefill time.Time
}

func newTokenBucket(clk clock.Clock, ratePerSecond, burst int64) *tokenBucket {
	if burst <= 0 {
		burst = 1
	}
	return &tokenBucket{
		clk:        clk,
		ratePS:     ratePerSecond,
		burst:      burst,
		tokens:     burst,
		lastRefill: clk.Now(),
	}
}

// tryConsume attempts to consume n tokens without blocking. Returns
// (true, 0) on success, (false, retryAfter) when the bucket is short.
// Used by the download handler to pre-check the request size and
// surface a 429 with Retry-After before opening the response stream.
func (b *tokenBucket) tryConsume(n int64) (bool, time.Duration) {
	if b == nil || b.ratePS <= 0 {
		return true, 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refill()
	step := n
	if step > b.burst {
		step = b.burst
	}
	if b.tokens >= step {
		b.tokens -= step
		return true, 0
	}
	need := step - b.tokens
	wait := time.Duration(need) * time.Second / time.Duration(b.ratePS)
	if wait <= 0 {
		wait = time.Millisecond
	}
	return false, wait
}

func (b *tokenBucket) refill() {
	now := b.clk.Now()
	elapsed := now.Sub(b.lastRefill)
	if elapsed <= 0 {
		return
	}
	add := int64(elapsed.Seconds() * float64(b.ratePS))
	if add <= 0 {
		return
	}
	b.tokens += add
	if b.tokens > b.burst {
		b.tokens = b.burst
	}
	b.lastRefill = now
}
