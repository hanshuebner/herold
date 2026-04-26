package webpush

import (
	"sync"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
)

// RateLimitDefaults expresses the REQ-PROTO-126 ceilings:
//
//   - per-minute sustained:   60 / minute (token bucket: refill 1 per
//     second, burst 60).
//   - per-day:                1000 / day (UTC midnight reset).
//   - cooldown on excess:     5 minutes.
//
// The grossExcessThreshold drives the warn-level operator log when a
// subscription is rejecting tokens at a rate that suggests a runaway
// notification scenario (a malformed Sieve filter, a buggy chat bot).
// REQ-PROTO-126 calls for surface-level visibility "so operators can
// triage" without blocking the hot path; we count rejects-per-minute
// in the bucket window and log when it crosses the threshold.
const (
	defaultPerMinute    = 60
	defaultPerDay       = 1000
	defaultCooldownDur  = 5 * time.Minute
	grossExcessRejected = 100
)

// rateOutcome tags an Allow() decision.
type rateOutcome int

const (
	// rateOK means the push may proceed; a token has been deducted.
	rateOK rateOutcome = iota
	// rateBucketExhausted means the per-minute bucket is empty (60/min).
	// The caller logs and skips; no cooldown is entered yet — the token
	// will refill on its own. The cooldown is reserved for sustained
	// excess.
	rateBucketExhausted
	// rateDailyExhausted means the day counter has reached the cap; the
	// caller logs and skips. The counter resets at the next UTC
	// midnight crossing.
	rateDailyExhausted
	// rateCooldown means the subscription is in a 5-minute cooldown
	// triggered by a sustained excess. The caller skips silently
	// (one warn log fires when the cooldown starts).
	rateCooldown
)

// rateLimiter is the per-subscription token bucket + daily counter +
// cooldown manager. State lives in memory; restart resets the buckets
// per the design note in the spec (rare flood-on-restart scenario is
// acceptable). Concurrent access from multiple dispatcher workers is
// safe via mu.
type rateLimiter struct {
	mu sync.Mutex

	clock     clock.Clock
	perMinute int
	perDay    int
	cooldown  time.Duration

	subs map[store.PushSubscriptionID]*subState
}

// subState holds one subscription's rate-limit counters.
type subState struct {
	// Token bucket: tokens regenerate at perMinute / 60 per second
	// up to perMinute capacity. lastRefill is the last instant the
	// bucket was advanced. The float lets us model fractional refills
	// over arbitrary intervals without integer rounding bias.
	tokens     float64
	lastRefill time.Time

	// Daily counter: count of pushes attempted since the last UTC
	// midnight crossing in dayBoundary. dayBoundary is the start of
	// the day in UTC; once clock.Now crosses it, the counter resets.
	dayCount    int
	dayBoundary time.Time

	// Cooldown: when cooldownUntil is non-zero and clock.Now is before
	// it, the subscription is in cooldown. The dispatcher does not
	// enter the encrypt + sign + POST path while cooldown is active.
	cooldownUntil time.Time

	// rejectWindowStart / rejectsInWindow track the rolling 60-second
	// reject window for the gross-excess log. Reset every time the
	// window elapses.
	rejectWindowStart time.Time
	rejectsInWindow   int
}

// newRateLimiter returns an empty rateLimiter using the supplied
// caps. A non-positive cap falls back to the documented default.
func newRateLimiter(clk clock.Clock, perMinute, perDay int, cooldown time.Duration) *rateLimiter {
	if perMinute <= 0 {
		perMinute = defaultPerMinute
	}
	if perDay <= 0 {
		perDay = defaultPerDay
	}
	if cooldown <= 0 {
		cooldown = defaultCooldownDur
	}
	return &rateLimiter{
		clock:     clk,
		perMinute: perMinute,
		perDay:    perDay,
		cooldown:  cooldown,
		subs:      make(map[store.PushSubscriptionID]*subState),
	}
}

// Allow attempts to deduct one push from sub's bucket. Returns the
// outcome (one of rateOK / rateBucketExhausted / rateDailyExhausted /
// rateCooldown). The caller logs and / or skips according to the
// outcome; on rateOK the caller proceeds to encrypt + POST.
//
// `enteredCooldown` is set when this Allow call transitioned the
// subscription INTO cooldown; the caller emits a one-shot warn log so
// operators see the trigger event.
func (r *rateLimiter) Allow(id store.PushSubscriptionID) (out rateOutcome, enteredCooldown bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.clock.Now()
	st := r.subs[id]
	if st == nil {
		st = &subState{
			tokens:      float64(r.perMinute),
			lastRefill:  now,
			dayBoundary: utcMidnight(now),
		}
		r.subs[id] = st
	}
	r.refill(st, now)

	if !st.cooldownUntil.IsZero() {
		if now.Before(st.cooldownUntil) {
			return rateCooldown, false
		}
		// Cooldown elapsed; clear it and continue with normal limit.
		st.cooldownUntil = time.Time{}
	}

	if st.dayCount >= r.perDay {
		return r.recordReject(st, now, rateDailyExhausted)
	}
	if st.tokens < 1 {
		return r.recordReject(st, now, rateBucketExhausted)
	}

	st.tokens -= 1
	st.dayCount++
	// Successful Allow: clear the rolling reject window so a later
	// burst is measured fresh.
	st.rejectWindowStart = time.Time{}
	st.rejectsInWindow = 0
	return rateOK, false
}

// refill advances the token bucket and rolls over the daily counter
// if the UTC midnight boundary has been crossed since the last refill.
func (r *rateLimiter) refill(st *subState, now time.Time) {
	if !now.After(st.lastRefill) {
		st.lastRefill = now
		return
	}
	delta := now.Sub(st.lastRefill).Seconds()
	rate := float64(r.perMinute) / 60.0
	st.tokens += delta * rate
	if st.tokens > float64(r.perMinute) {
		st.tokens = float64(r.perMinute)
	}
	st.lastRefill = now

	// Daily counter rollover. utcMidnight(now) returns the start of
	// "now" in UTC; if it differs from dayBoundary the day has rolled.
	mid := utcMidnight(now)
	if mid.After(st.dayBoundary) {
		st.dayCount = 0
		st.dayBoundary = mid
	}
}

// recordReject increments the rolling 60s reject window; when the
// counter exceeds grossExcessRejected the subscription enters the
// cooldown and the caller is told via enteredCooldown=true so it can
// emit a single warn-level log (the limiter does not own a logger).
func (r *rateLimiter) recordReject(st *subState, now time.Time, baseOutcome rateOutcome) (rateOutcome, bool) {
	if st.rejectWindowStart.IsZero() || now.Sub(st.rejectWindowStart) > time.Minute {
		st.rejectWindowStart = now
		st.rejectsInWindow = 0
	}
	st.rejectsInWindow++
	if st.rejectsInWindow >= grossExcessRejected && st.cooldownUntil.IsZero() {
		st.cooldownUntil = now.Add(r.cooldown)
		return rateCooldown, true
	}
	return baseOutcome, false
}

// CooldownsActive returns the number of subscriptions currently in
// cooldown (now < cooldownUntil). Used by the metrics gauge.
func (r *rateLimiter) CooldownsActive() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.clock.Now()
	n := 0
	for _, st := range r.subs {
		if !st.cooldownUntil.IsZero() && now.Before(st.cooldownUntil) {
			n++
		}
	}
	return n
}

// Forget evicts a subscription's state. Called by the dispatcher when
// a subscription is destroyed (410 / 404 from the gateway, /set
// destroy, principal delete) so the in-memory map does not grow
// unboundedly.
func (r *rateLimiter) Forget(id store.PushSubscriptionID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.subs, id)
}

// utcMidnight returns the start-of-day UTC instant for t.
func utcMidnight(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}
