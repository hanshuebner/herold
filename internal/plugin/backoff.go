package plugin

import (
	"math/rand"
	"time"
)

// backoff produces a capped exponential-with-jitter delay sequence used by
// the supervisor between plugin restarts (REQ-PLUG-05).
type backoff struct {
	base    time.Duration
	cap     time.Duration
	current time.Duration
	rng     *rand.Rand
}

// newBackoff constructs a backoff anchored at base and capped at maxDelay.
// The first call to next returns roughly base; subsequent calls double up
// to maxDelay, with +/-25% jitter applied.
func newBackoff(base, maxDelay time.Duration) *backoff {
	if base <= 0 {
		base = time.Second
	}
	if maxDelay <= 0 || maxDelay < base {
		maxDelay = 60 * time.Second
	}
	return &backoff{
		base:    base,
		cap:     maxDelay,
		current: 0,
		// A per-backoff source keeps jitter deterministic for unit tests
		// that inject a seeded variant via Reset.
		rng: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func (b *backoff) next() time.Duration {
	if b.current == 0 {
		b.current = b.base
	} else {
		b.current *= 2
		if b.current > b.cap {
			b.current = b.cap
		}
	}
	// +/-25% jitter around current.
	delta := time.Duration(b.rng.Int63n(int64(b.current/2))) - b.current/4
	return b.current + delta
}

func (b *backoff) reset() { b.current = 0 }
