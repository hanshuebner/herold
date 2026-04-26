package webpush

import (
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
)

func TestRateLimiter_BurstThenSustained(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	clk := clock.NewFake(start)
	r := newRateLimiter(clk, 60, 1000, 5*time.Minute)

	// Burst of 60 must succeed; the 61st must be rate-limited.
	for i := 0; i < 60; i++ {
		out, _ := r.Allow(store.PushSubscriptionID(1))
		if out != rateOK {
			t.Fatalf("burst %d: outcome=%d (want OK)", i, out)
		}
	}
	out, _ := r.Allow(store.PushSubscriptionID(1))
	if out != rateBucketExhausted {
		t.Fatalf("61st: outcome=%d (want rateBucketExhausted)", out)
	}
	// Advance 1 second; one token regenerates.
	clk.Advance(time.Second)
	out, _ = r.Allow(store.PushSubscriptionID(1))
	if out != rateOK {
		t.Fatalf("after 1s refill: outcome=%d (want OK)", out)
	}
	// Bucket is empty again.
	out, _ = r.Allow(store.PushSubscriptionID(1))
	if out != rateBucketExhausted {
		t.Fatalf("after empty: outcome=%d", out)
	}
}

func TestRateLimiter_DailyCap(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	clk := clock.NewFake(start)
	r := newRateLimiter(clk, 1000, 5, 5*time.Minute)

	for i := 0; i < 5; i++ {
		out, _ := r.Allow(store.PushSubscriptionID(2))
		if out != rateOK {
			t.Fatalf("attempt %d: outcome=%d (want OK)", i, out)
		}
	}
	out, _ := r.Allow(store.PushSubscriptionID(2))
	if out != rateDailyExhausted {
		t.Fatalf("over daily cap: outcome=%d (want rateDailyExhausted)", out)
	}
	// Cross UTC midnight; counter resets.
	clk.Advance(24 * time.Hour)
	out, _ = r.Allow(store.PushSubscriptionID(2))
	if out != rateOK {
		t.Fatalf("after midnight: outcome=%d (want OK)", out)
	}
}

func TestRateLimiter_CooldownOnSustainedExcess(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	clk := clock.NewFake(start)
	r := newRateLimiter(clk, 60, 100000, 5*time.Minute)

	// Drain bucket.
	for i := 0; i < 60; i++ {
		out, _ := r.Allow(store.PushSubscriptionID(3))
		if out != rateOK {
			t.Fatalf("drain %d: %d", i, out)
		}
	}
	var enteredCooldownAt int = -1
	for i := 0; i < grossExcessRejected+5; i++ {
		out, entered := r.Allow(store.PushSubscriptionID(3))
		if out == rateCooldown {
			if enteredCooldownAt < 0 {
				enteredCooldownAt = i
				if !entered {
					t.Fatalf("first rateCooldown should report enteredCooldown=true")
				}
			}
		}
	}
	if enteredCooldownAt < 0 {
		t.Fatalf("never entered cooldown")
	}
	// Verify the subscription is in cooldown until the timeout.
	out, _ := r.Allow(store.PushSubscriptionID(3))
	if out != rateCooldown {
		t.Fatalf("after entering: outcome=%d", out)
	}
	if got := r.CooldownsActive(); got != 1 {
		t.Fatalf("CooldownsActive=%d want 1", got)
	}
	// Advance past the cooldown; the subscription should resume
	// (but the token bucket is still empty so the next Allow may
	// still bucket-exhaust until refills accumulate).
	clk.Advance(5*time.Minute + time.Second)
	if got := r.CooldownsActive(); got != 0 {
		t.Fatalf("CooldownsActive after cooldown=%d want 0", got)
	}
	// Refill some tokens — 5 minutes of refill should saturate the
	// bucket back to 60 tokens.
	out, _ = r.Allow(store.PushSubscriptionID(3))
	if out != rateOK {
		t.Fatalf("after cooldown + refill: outcome=%d", out)
	}
}

func TestRateLimiter_Forget(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	clk := clock.NewFake(start)
	r := newRateLimiter(clk, 60, 1000, 5*time.Minute)

	r.Allow(store.PushSubscriptionID(7))
	if _, ok := r.subs[7]; !ok {
		t.Fatalf("subscription 7 not tracked")
	}
	r.Forget(store.PushSubscriptionID(7))
	if _, ok := r.subs[7]; ok {
		t.Fatalf("subscription 7 still tracked after Forget")
	}
}
