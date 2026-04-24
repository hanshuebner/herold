package protoimap

import (
	"context"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
)

// tokenBucket implements REQ-STORE-20..25 per-session download throttling.
// Tokens are bytes; replenishment is rate-per-second, capped at burst. The
// clock is injected so tests can control replenishment deterministically.
type tokenBucket struct {
	clk        clock.Clock
	ratePS     int64
	burst      int64
	tokens     int64
	lastRefill time.Time
}

func newTokenBucket(clk clock.Clock, ratePerSecond, burst int64) *tokenBucket {
	return &tokenBucket{
		clk:        clk,
		ratePS:     ratePerSecond,
		burst:      burst,
		tokens:     burst,
		lastRefill: clk.Now(),
	}
}

// consume blocks until n tokens are available, then deducts them. The
// bucket is not goroutine-safe; callers hold a per-session mutex around
// the sequence of FETCH writes.
//
// When n exceeds burst, consume drains the bucket in burst-sized slices:
// the burst caps idle-accumulated tokens, not the total we can eventually
// spend against the per-second rate.
func (b *tokenBucket) consume(ctx context.Context, n int64) error {
	if b.ratePS <= 0 {
		return nil
	}
	remaining := n
	for remaining > 0 {
		b.refill()
		step := remaining
		if step > b.burst {
			step = b.burst
		}
		if b.tokens >= step {
			b.tokens -= step
			remaining -= step
			continue
		}
		need := step - b.tokens
		wait := time.Duration(need) * time.Second / time.Duration(b.ratePS)
		if wait <= 0 {
			wait = time.Millisecond
		}
		select {
		case <-b.clk.After(wait):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
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
