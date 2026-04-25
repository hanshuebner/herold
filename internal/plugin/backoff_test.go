package plugin

// backoff_test.go covers the supervisor's restart-backoff jitter
// source. STANDARDS §5 forbids wall-clock injection inside
// deterministic code; STANDARDS §8.5 forbids non-deterministic tests.
// The backoff helper used to seed math/rand from time.Now() at
// construction, which violated both rules. The fix injects either a
// FakeClock-derived seed (production) or an explicit rand.Source
// (tests); these tests pin the determinism contract.

import (
	"math/rand"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
)

func TestBackoff_DeterministicWithInjectedSource(t *testing.T) {
	// Two backoffs constructed with the same source seed produce the
	// same delay sequence — pinning the determinism property tests
	// rely on.
	b1 := newBackoff(time.Second, 60*time.Second, nil, rand.NewSource(42))
	b2 := newBackoff(time.Second, 60*time.Second, nil, rand.NewSource(42))
	for i := 0; i < 8; i++ {
		d1 := b1.next()
		d2 := b2.next()
		if d1 != d2 {
			t.Fatalf("step %d: deterministic backoff diverged: b1=%v b2=%v", i, d1, d2)
		}
	}
}

func TestBackoff_DifferentSeedsDiffer(t *testing.T) {
	// Sanity: different seeds produce different first-jitter values.
	// Otherwise the source plumbing is no-op.
	b1 := newBackoff(time.Second, 60*time.Second, nil, rand.NewSource(1))
	b2 := newBackoff(time.Second, 60*time.Second, nil, rand.NewSource(7))
	// Skip the first call: jitter span is 0..base/2 - base/4, but
	// first call uses base directly. Compare a couple of subsequent
	// values to discover divergence reliably.
	_ = b1.next()
	_ = b2.next()
	any := false
	for i := 0; i < 4; i++ {
		if b1.next() != b2.next() {
			any = true
			break
		}
	}
	if !any {
		t.Fatal("expected at least one divergence between seeds 1 and 7")
	}
}

func TestBackoff_FakeClockSeed(t *testing.T) {
	// Production path: nil source pulls the seed from the injected
	// Clock, not the real wall clock. Two backoffs constructed against
	// FakeClocks at the same instant must produce identical sequences.
	clk1 := clock.NewFake(time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC))
	clk2 := clock.NewFake(time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC))
	b1 := newBackoff(time.Second, 60*time.Second, clk1, nil)
	b2 := newBackoff(time.Second, 60*time.Second, clk2, nil)
	for i := 0; i < 6; i++ {
		d1 := b1.next()
		d2 := b2.next()
		if d1 != d2 {
			t.Fatalf("step %d: FakeClock-seeded backoff diverged: b1=%v b2=%v", i, d1, d2)
		}
	}
}

func TestBackoff_BoundedAroundCurrent(t *testing.T) {
	// Sanity: each delay sits in [current*0.75, current*1.25].
	b := newBackoff(time.Second, 60*time.Second, nil, rand.NewSource(99))
	cur := time.Second
	for i := 0; i < 10; i++ {
		d := b.next()
		lo := cur - cur/4
		hi := cur + cur/4
		if d < lo || d > hi {
			t.Errorf("step %d: delay %v out of [%v,%v] for current=%v", i, d, lo, hi, cur)
		}
		// Track the doubling/cap that next() applies internally.
		if cur < 60*time.Second {
			cur *= 2
			if cur > 60*time.Second {
				cur = 60 * time.Second
			}
		}
	}
}
