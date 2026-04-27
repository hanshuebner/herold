package plugin

import (
	"math/rand"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
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
//
// src seeds the jitter PRNG. Production callers pass nil; that path
// derives a per-backoff seed from clk.Now() so the jitter source is
// reproducible across replays of a fixed clock and never reads the
// real wall clock (STANDARDS §5: no wall-clock injection in
// deterministic code). Tests pass an explicit rand.Source for byte-
// exact determinism. clk may be nil; nil falls back to clock.NewReal.
//
// The jitter is a non-cryptographic delay smear; using math/rand here
// is intentional. Cryptographic RNG would only obscure the schedule
// without affecting the security posture of plugin restart timing.
func newBackoff(base, maxDelay time.Duration, clk clock.Clock, src rand.Source) *backoff {
	if base <= 0 {
		base = time.Second
	}
	if maxDelay <= 0 || maxDelay < base {
		maxDelay = 60 * time.Second
	}
	if src == nil {
		if clk == nil {
			clk = clock.NewReal()
		}
		src = rand.NewSource(clk.Now().UnixNano())
	}
	return &backoff{
		base:    base,
		cap:     maxDelay,
		current: 0,
		rng:     rand.New(src),
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
