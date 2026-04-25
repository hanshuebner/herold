// Package clock provides the Clock interface used by every subsystem so that
// tests can inject deterministic time. Production code constructs Real;
// tests construct Fake(start) and advance it explicitly with Advance.
//
// Rationale is in STANDARDS.md §5 (no wall-clock reads in deterministic code).
// All subsystems that need "now" take a Clock in their constructor rather
// than calling time.Now directly; that keeps tests free of sleeps, flakes,
// and real-time dependencies.
package clock

import (
	"sync"
	"time"
)

// Clock is the minimal time source every Herold subsystem accepts. It
// returns the current instant according to the clock's notion of time.
// Implementations must be safe for concurrent use.
type Clock interface {
	// Now returns the current time according to this clock.
	Now() time.Time
	// After returns a channel that receives the clock's current time once
	// the clock has advanced at least d past the moment After was called.
	// Production implementations delegate to time.After; FakeClock fires
	// only when Advance crosses the waiter's deadline.
	After(d time.Duration) <-chan time.Time
	// AfterFunc schedules f to run in its own goroutine after d has
	// elapsed on this clock. The returned Timer can be used to cancel
	// the firing before f has run; Stop returns true if it stopped the
	// timer before it could fire. Production implementations delegate to
	// time.AfterFunc; FakeClock fires only when Advance / SetNow crosses
	// the deadline. Implementations MUST run f outside any internal
	// lock so f can call back into the clock without deadlocking.
	AfterFunc(d time.Duration, f func()) Timer
}

// Timer is the cancel handle returned by Clock.AfterFunc. Stop returns
// true if the call stopped the timer before f ran; false if f has
// already fired (or has been scheduled to fire imminently). A Stop on
// an already-stopped timer is a no-op and returns false.
type Timer interface {
	Stop() bool
}

// Real is the production Clock implementation. It delegates to time.Now.
// The zero value is ready for use; Real() returns one for readability.
type Real struct{}

// NewReal returns a Clock backed by the host wall clock.
func NewReal() Clock { return Real{} }

// Now returns the host wall-clock time (time.Now).
func (Real) Now() time.Time { return time.Now() }

// After delegates to time.After.
func (Real) After(d time.Duration) <-chan time.Time { return time.After(d) }

// AfterFunc delegates to time.AfterFunc, wrapping the *time.Timer so it
// satisfies the Clock-package Timer contract.
func (Real) AfterFunc(d time.Duration, f func()) Timer {
	return realTimer{t: time.AfterFunc(d, f)}
}

// realTimer adapts *time.Timer to Timer. Stop matches time.Timer.Stop:
// true when the call stopped the timer before f ran.
type realTimer struct {
	t *time.Timer
}

func (r realTimer) Stop() bool { return r.t.Stop() }

// FakeClock is a deterministic Clock for tests. Its time advances only when
// Advance or SetNow is called. All operations are safe for concurrent use;
// Now never decreases. Construct one with NewFake.
type FakeClock struct {
	mu      sync.RWMutex
	now     time.Time
	waiters []fakeWaiter
	// timers holds outstanding AfterFunc registrations. A timer is
	// removed when it fires or is Stop'd. The order is preserved so a
	// single Advance fires timers in registration order, deterministic
	// regardless of map iteration.
	timers []*fakeTimer
}

type fakeWaiter struct {
	deadline time.Time
	ch       chan time.Time
}

// fakeTimer is FakeClock's Timer implementation. Deadline is the wall
// instant the timer should fire at; fired guards against double-fire
// across a Stop / Advance race; f is the user-supplied callback.
type fakeTimer struct {
	parent   *FakeClock
	deadline time.Time
	f        func()
	fired    bool
}

// Stop removes t from the parent's timer list if it has not fired yet.
// Returns true if the call stopped the timer before f could run.
func (t *fakeTimer) Stop() bool {
	t.parent.mu.Lock()
	defer t.parent.mu.Unlock()
	if t.fired {
		return false
	}
	for i, x := range t.parent.timers {
		if x == t {
			t.parent.timers = append(t.parent.timers[:i], t.parent.timers[i+1:]...)
			t.fired = true // prevent any later Advance from firing it
			return true
		}
	}
	return false
}

// NewFake returns a FakeClock anchored at start. Callers advance time
// explicitly via Advance or SetNow; time never moves on its own.
func NewFake(start time.Time) *FakeClock {
	return &FakeClock{now: start}
}

// Now returns the fake clock's current instant. Safe for concurrent use.
func (f *FakeClock) Now() time.Time {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.now
}

// After registers a waiter whose channel fires once the fake clock has
// advanced past now+d. The returned channel is buffered size 1 so firing
// never blocks a concurrent Advance.
func (f *FakeClock) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	f.mu.Lock()
	deadline := f.now.Add(d)
	// If the fake clock has already passed the deadline (d <= 0), fire
	// immediately so callers do not wait forever on a zero-delay After.
	if !deadline.After(f.now) {
		ch <- f.now
		f.mu.Unlock()
		return ch
	}
	f.waiters = append(f.waiters, fakeWaiter{deadline: deadline, ch: ch})
	f.mu.Unlock()
	return ch
}

// AfterFunc registers a one-shot timer that calls f after d has elapsed
// on this fake clock. f runs synchronously inside Advance / SetNow when
// the deadline is crossed; that mirrors how production code behaves
// from the caller's perspective (a timer fires at most once and the
// callback runs in its own goroutine in production), and keeps tests
// fully deterministic without extra synchronisation. f is invoked
// without holding FakeClock's mutex so the callback can read or
// reschedule against the clock without deadlocking.
//
// Zero or negative durations fire on the next Advance / SetNow that
// touches the clock.
func (f *FakeClock) AfterFunc(d time.Duration, fn func()) Timer {
	f.mu.Lock()
	deadline := f.now.Add(d)
	t := &fakeTimer{parent: f, deadline: deadline, f: fn}
	f.timers = append(f.timers, t)
	f.mu.Unlock()
	return t
}

// Advance moves the fake clock forward by d. Negative durations are
// rejected (the clock is monotonic): callers must use SetNow to rewind,
// which is intentionally verbose because rewinding time in tests usually
// indicates a test bug. Waiters whose deadline has been crossed fire in a
// single pass with the new now value.
func (f *FakeClock) Advance(d time.Duration) {
	if d < 0 {
		panic("clock: FakeClock.Advance with negative duration; use SetNow to rewind")
	}
	f.mu.Lock()
	f.now = f.now.Add(d)
	fired, kept := partitionWaiters(f.waiters, f.now)
	f.waiters = kept
	firedTimers, keptTimers := partitionTimers(f.timers, f.now)
	f.timers = keptTimers
	now := f.now
	f.mu.Unlock()
	for _, w := range fired {
		w.ch <- now
	}
	for _, t := range firedTimers {
		t.f()
	}
}

// SetNow sets the fake clock to t unconditionally. Unlike Advance this
// permits rewinding; use it only when a test genuinely needs to reset the
// clock between phases. Waiters whose deadline has been crossed fire.
func (f *FakeClock) SetNow(t time.Time) {
	f.mu.Lock()
	f.now = t
	fired, kept := partitionWaiters(f.waiters, f.now)
	f.waiters = kept
	firedTimers, keptTimers := partitionTimers(f.timers, f.now)
	f.timers = keptTimers
	now := f.now
	f.mu.Unlock()
	for _, w := range fired {
		w.ch <- now
	}
	for _, ti := range firedTimers {
		ti.f()
	}
}

func partitionWaiters(ws []fakeWaiter, now time.Time) (fired, kept []fakeWaiter) {
	for _, w := range ws {
		if !now.Before(w.deadline) {
			fired = append(fired, w)
		} else {
			kept = append(kept, w)
		}
	}
	return fired, kept
}

// partitionTimers splits ts into the timers whose deadline has been
// reached at now (fired) and those still pending (kept). Fired timers
// have their fired flag set so a subsequent Stop returns false.
func partitionTimers(ts []*fakeTimer, now time.Time) (fired, kept []*fakeTimer) {
	for _, t := range ts {
		if !now.Before(t.deadline) {
			t.fired = true
			fired = append(fired, t)
		} else {
			kept = append(kept, t)
		}
	}
	return fired, kept
}
