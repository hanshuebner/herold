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
	// NewTimer returns a stoppable, channel-based timer that fires on
	// its channel once the clock has advanced at least d past the
	// moment NewTimer was called. Unlike After -- which mirrors
	// time.After's documented fire-and-forget behaviour, so a select's
	// losing case leaves its registration to fire into the void -- a
	// NewTimer's Stop lets the caller reclaim it before it fires.
	// Callers that race a NewTimer against another select case MUST
	// Stop it on every losing exit path, so a FakeClock's waiter
	// bookkeeping (NumWaiters, NextWaiterDeadline) reflects only the
	// wait the caller is still actually blocked on rather than an
	// abandoned registration from an earlier loop iteration (re #260).
	// Production implementations delegate to time.NewTimer; FakeClock
	// fires only when Advance / SetNow crosses the deadline, same as
	// After.
	NewTimer(d time.Duration) ChanTimer
}

// ChanTimer is the cancel-and-observe handle returned by Clock.NewTimer.
// It pairs a fire-once channel with a Stop method, mirroring the
// time.NewTimer pattern (as distinct from the fire-and-forget time.After
// pattern that Clock.After mirrors). Stop returns true if it stopped the
// timer before it fired; false if the timer had already fired or been
// stopped. A Stop on an already-stopped timer is a no-op returning false.
type ChanTimer interface {
	// C returns the channel the timer sends on exactly once, when the
	// clock has advanced past the timer's deadline. Nothing is ever
	// sent if Stop returns true first.
	C() <-chan time.Time
	Stop() bool
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

// NewTimer delegates to time.NewTimer, wrapping the *time.Timer so it
// satisfies the Clock-package ChanTimer contract.
func (Real) NewTimer(d time.Duration) ChanTimer {
	return realChanTimer{t: time.NewTimer(d)}
}

// realChanTimer adapts *time.Timer to ChanTimer.
type realChanTimer struct {
	t *time.Timer
}

func (r realChanTimer) C() <-chan time.Time { return r.t.C }
func (r realChanTimer) Stop() bool          { return r.t.Stop() }

// FakeClock is a deterministic Clock for tests. Its time advances only when
// Advance or SetNow is called. All operations are safe for concurrent use;
// Now never decreases. Construct one with NewFake.
type FakeClock struct {
	mu  sync.RWMutex
	now time.Time
	// waiters holds outstanding After and NewTimer registrations, as
	// pointers so a NewTimer's Stop can remove its own entry by identity
	// (re #260): value semantics would let a slice reallocation
	// invalidate any reference a Stop call saved.
	waiters []*fakeWaiter
	// timers holds outstanding AfterFunc registrations. A timer is
	// removed when it fires or is Stop'd. The order is preserved so a
	// single Advance fires timers in registration order, deterministic
	// regardless of map iteration.
	timers []*fakeTimer
}

// fakeWaiter backs both After (never stoppable, matching time.After's
// fire-and-forget contract) and NewTimer (stoppable via
// fakeChanTimer.Stop, matching time.NewTimer). fired guards against a
// racing Stop trying to remove a waiter Advance/SetNow has already
// partitioned into the fired set, or a racing Advance/SetNow trying to
// fire one Stop has already removed.
type fakeWaiter struct {
	deadline time.Time
	ch       chan time.Time
	fired    bool
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

// NumWaiters returns the number of After- and NewTimer-style waiters
// currently registered against the fake clock. Tests use this to
// synchronise against production goroutines that register their timer
// lazily on first iteration: poll NumWaiters until it reaches the
// expected count, then call Advance. Without this barrier, an Advance
// racing the goroutine's first After call leaves the just-registered
// waiter armed for `now+d` (post-advance), and no further wake-up is
// scheduled until the next Advance — which the test typically gates
// on a signal that the racing waiter was supposed to produce.
//
// A caller using raw After (not NewTimer) inside a select alongside
// other cases must still account for the fact that a losing case's
// waiter is never removed (re #260): NumWaiters() then counts that
// abandoned registration indistinguishably from a live one, so ">= n"
// guards built on After must count every registration guaranteed to
// have happened, not just "at least one". Callers using NewTimer and
// Stopping it on every losing exit path do not have this hazard: a
// Stopped waiter is removed immediately, so NumWaiters() reflects only
// waiters still actually live.
//
// AfterFunc-style timers are not counted: they are observed by their
// callback running, which is its own synchronisation primitive.
func (f *FakeClock) NumWaiters() int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return len(f.waiters)
}

// NextWaiterDeadline returns the earliest deadline among the After-style
// waiters currently registered, and ok=false when there are none. Tests
// that drive the clock from a background pump use it to advance toward the
// next due wake-up instead of jumping by a fixed large delta: a precise
// advance fires exactly the earliest waiter, so a self-renewing poll (a
// loop that re-registers After on each wake) is stepped one deadline at a
// time rather than fired repeatedly within a single oversized jump.
//
// AfterFunc-style timers are not considered: their firing is observed by
// the callback running, which is its own synchronisation primitive.
func (f *FakeClock) NextWaiterDeadline() (deadline time.Time, ok bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	for _, w := range f.waiters {
		if !ok || w.deadline.Before(deadline) {
			deadline = w.deadline
			ok = true
		}
	}
	return deadline, ok
}

// After registers a waiter whose channel fires once the fake clock has
// advanced past now+d. The returned channel is buffered size 1 so firing
// never blocks a concurrent Advance. The waiter can never be cancelled
// (matching time.After); use NewTimer for a stoppable equivalent.
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
	f.waiters = append(f.waiters, &fakeWaiter{deadline: deadline, ch: ch})
	f.mu.Unlock()
	return ch
}

// NewTimer returns a stoppable timer that fires on its channel once the
// fake clock has advanced past now+d. Stop removes the waiter from the
// fake clock's list if it has not fired yet, so an abandoned timer (the
// losing case of a select) does not linger as a phantom entry in
// NumWaiters() or NextWaiterDeadline() the way an abandoned After waiter
// does (re #260): callers that Stop on every losing exit path get
// waiter bookkeeping that reflects only the wait they are still
// actually blocked on.
func (f *FakeClock) NewTimer(d time.Duration) ChanTimer {
	ch := make(chan time.Time, 1)
	f.mu.Lock()
	deadline := f.now.Add(d)
	w := &fakeWaiter{deadline: deadline, ch: ch}
	if !deadline.After(f.now) {
		// Already due: fire immediately, matching After's zero/negative
		// delay behaviour. Mark fired so a subsequent Stop correctly
		// reports it could not stop an already-fired timer, without
		// needing to search a list the waiter was never added to.
		w.fired = true
		ch <- f.now
		f.mu.Unlock()
		return &fakeChanTimer{parent: f, w: w}
	}
	f.waiters = append(f.waiters, w)
	f.mu.Unlock()
	return &fakeChanTimer{parent: f, w: w}
}

// fakeChanTimer is FakeClock's ChanTimer implementation. It shares the
// fakeWaiter type with After so Advance/SetNow fire both identically;
// Stop is the only behavioural difference from a plain After waiter.
type fakeChanTimer struct {
	parent *FakeClock
	w      *fakeWaiter
}

func (t *fakeChanTimer) C() <-chan time.Time { return t.w.ch }

// Stop removes t's waiter from the parent's list if it has not fired
// yet. Returns true if the call stopped the timer before it could fire.
func (t *fakeChanTimer) Stop() bool {
	t.parent.mu.Lock()
	defer t.parent.mu.Unlock()
	if t.w.fired {
		return false
	}
	for i, w := range t.parent.waiters {
		if w == t.w {
			t.parent.waiters = append(t.parent.waiters[:i], t.parent.waiters[i+1:]...)
			t.w.fired = true // prevent a racing Advance from also firing it
			return true
		}
	}
	return false
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

// partitionWaiters splits ws into the waiters whose deadline has been
// reached at now (fired) and those still pending (kept). Fired waiters
// have their fired flag set so a racing Stop (on a NewTimer-backed
// waiter) sees it could not stop an already-fired timer instead of
// removing it out from under this partition.
func partitionWaiters(ws []*fakeWaiter, now time.Time) (fired, kept []*fakeWaiter) {
	for _, w := range ws {
		if !now.Before(w.deadline) {
			w.fired = true
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
