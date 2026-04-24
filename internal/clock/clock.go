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

// FakeClock is a deterministic Clock for tests. Its time advances only when
// Advance or SetNow is called. All operations are safe for concurrent use;
// Now never decreases. Construct one with NewFake.
type FakeClock struct {
	mu      sync.RWMutex
	now     time.Time
	waiters []fakeWaiter
}

type fakeWaiter struct {
	deadline time.Time
	ch       chan time.Time
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
	now := f.now
	f.mu.Unlock()
	for _, w := range fired {
		w.ch <- now
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
	now := f.now
	f.mu.Unlock()
	for _, w := range fired {
		w.ch <- now
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
