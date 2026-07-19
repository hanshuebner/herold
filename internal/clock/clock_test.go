package clock_test

import (
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
)

func TestRealNow(t *testing.T) {
	c := clock.NewReal()
	a := c.Now()
	b := c.Now()
	if b.Before(a) {
		t.Fatalf("Real.Now went backwards: %v < %v", b, a)
	}
}

func TestFakeStartsAtAnchor(t *testing.T) {
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	f := clock.NewFake(start)
	if got := f.Now(); !got.Equal(start) {
		t.Fatalf("FakeClock.Now() = %v, want %v", got, start)
	}
}

func TestFakeAdvance(t *testing.T) {
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	f := clock.NewFake(start)
	f.Advance(250 * time.Millisecond)
	want := start.Add(250 * time.Millisecond)
	if got := f.Now(); !got.Equal(want) {
		t.Fatalf("after Advance: got %v, want %v", got, want)
	}
	f.Advance(2 * time.Second)
	want = want.Add(2 * time.Second)
	if got := f.Now(); !got.Equal(want) {
		t.Fatalf("after second Advance: got %v, want %v", got, want)
	}
}

func TestFakeAdvanceNegativePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on negative Advance")
		}
	}()
	f := clock.NewFake(time.Now())
	f.Advance(-time.Second)
}

func TestFakeSetNow(t *testing.T) {
	f := clock.NewFake(time.Unix(0, 0).UTC())
	target := time.Date(2030, 6, 1, 0, 0, 0, 0, time.UTC)
	f.SetNow(target)
	if got := f.Now(); !got.Equal(target) {
		t.Fatalf("SetNow: got %v, want %v", got, target)
	}
	// SetNow may rewind; unlike Advance.
	earlier := target.Add(-1 * time.Hour)
	f.SetNow(earlier)
	if got := f.Now(); !got.Equal(earlier) {
		t.Fatalf("SetNow rewind: got %v, want %v", got, earlier)
	}
}

// TestFakeMonotonicUnderParallelReads asserts that concurrent readers never
// observe time going backwards while another goroutine advances. Run under
// -race to catch data races on the internal field.
func TestFakeMonotonicUnderParallelReads(t *testing.T) {
	f := clock.NewFake(time.Unix(0, 0).UTC())

	const readers = 8
	const iters = 10_000

	var wg sync.WaitGroup
	wg.Add(readers + 1)

	// Writer: advance repeatedly.
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			f.Advance(time.Microsecond)
		}
	}()

	for r := 0; r < readers; r++ {
		go func() {
			defer wg.Done()
			last := f.Now()
			for i := 0; i < iters; i++ {
				cur := f.Now()
				if cur.Before(last) {
					t.Errorf("FakeClock went backwards under parallel reads: %v < %v", cur, last)
					return
				}
				last = cur
			}
		}()
	}

	wg.Wait()
}

// TestFakeSatisfiesInterface pins that *FakeClock satisfies Clock (so tests
// can pass a *FakeClock anywhere a Clock is expected).
func TestFakeSatisfiesInterface(t *testing.T) {
	var c clock.Clock = clock.NewFake(time.Now())
	_ = c.Now()
}

func TestFakeClockAfter_FiresOnAdvance(t *testing.T) {
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	f := clock.NewFake(start)
	ch := f.After(500 * time.Millisecond)
	select {
	case <-ch:
		t.Fatal("After channel fired before Advance")
	default:
	}
	f.Advance(500 * time.Millisecond)
	select {
	case got := <-ch:
		want := start.Add(500 * time.Millisecond)
		if !got.Equal(want) {
			t.Fatalf("After channel delivered %v, want %v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("After channel did not fire after Advance crossed deadline")
	}
}

func TestFakeClockAfter_NotBeforeDeadline(t *testing.T) {
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	f := clock.NewFake(start)
	ch := f.After(time.Second)
	// Advance less than the deadline multiple times; waiter must not fire.
	f.Advance(250 * time.Millisecond)
	f.Advance(250 * time.Millisecond)
	f.Advance(499 * time.Millisecond)
	select {
	case <-ch:
		t.Fatal("After channel fired before deadline was crossed")
	default:
	}
	// Cross the deadline exactly.
	f.Advance(time.Millisecond)
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("After channel did not fire when deadline was crossed")
	}
}

func TestRealClockAfter_SmokeTest(t *testing.T) {
	c := clock.NewReal()
	ch := c.After(10 * time.Millisecond)
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("Real.After never fired within 1s for a 10ms deadline")
	}
}

func TestFakeClockNewTimer_FiresOnAdvance(t *testing.T) {
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	f := clock.NewFake(start)
	timer := f.NewTimer(500 * time.Millisecond)
	select {
	case <-timer.C():
		t.Fatal("ChanTimer fired before Advance")
	default:
	}
	f.Advance(500 * time.Millisecond)
	select {
	case got := <-timer.C():
		want := start.Add(500 * time.Millisecond)
		if !got.Equal(want) {
			t.Fatalf("ChanTimer delivered %v, want %v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("ChanTimer did not fire after Advance crossed deadline")
	}
	// Stopping an already-fired timer is a no-op that reports false,
	// matching time.Timer.Stop.
	if timer.Stop() {
		t.Fatal("Stop on an already-fired ChanTimer returned true")
	}
}

func TestFakeClockNewTimer_StopBeforeFireRemovesWaiter(t *testing.T) {
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	f := clock.NewFake(start)
	timer := f.NewTimer(time.Second)
	if got := f.NumWaiters(); got != 1 {
		t.Fatalf("NumWaiters before Stop = %d, want 1", got)
	}
	if !timer.Stop() {
		t.Fatal("Stop before firing returned false, want true")
	}
	// The waiter must be gone immediately (re #260): NumWaiters must not
	// count a Stopped timer, unlike an abandoned After registration.
	if got := f.NumWaiters(); got != 0 {
		t.Fatalf("NumWaiters after Stop = %d, want 0", got)
	}
	// A second Stop is a no-op.
	if timer.Stop() {
		t.Fatal("second Stop returned true, want false")
	}
	// Advancing past the original deadline must not deliver anything.
	f.Advance(2 * time.Second)
	select {
	case <-timer.C():
		t.Fatal("Stopped ChanTimer fired after Advance")
	default:
	}
}

func TestFakeClockNewTimer_StopDoesNotDisturbOtherWaiters(t *testing.T) {
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	f := clock.NewFake(start)
	afterCh := f.After(time.Second)
	timer := f.NewTimer(time.Second)
	if got := f.NumWaiters(); got != 2 {
		t.Fatalf("NumWaiters with one After and one NewTimer = %d, want 2", got)
	}
	if !timer.Stop() {
		t.Fatal("Stop before firing returned false, want true")
	}
	if got := f.NumWaiters(); got != 1 {
		t.Fatalf("NumWaiters after stopping the ChanTimer = %d, want 1 (the After waiter)", got)
	}
	f.Advance(time.Second)
	select {
	case <-afterCh:
	case <-time.After(time.Second):
		t.Fatal("unrelated After waiter did not fire after Advance")
	}
}

func TestFakeClockNewTimer_ZeroDelayFiresImmediatelyAndStopReturnsFalse(t *testing.T) {
	f := clock.NewFake(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	timer := f.NewTimer(0)
	select {
	case <-timer.C():
	default:
		t.Fatal("zero-delay ChanTimer did not fire immediately")
	}
	if timer.Stop() {
		t.Fatal("Stop on an already-fired zero-delay ChanTimer returned true")
	}
	// A zero-delay timer is never added to the live waiter set.
	if got := f.NumWaiters(); got != 0 {
		t.Fatalf("NumWaiters after a zero-delay NewTimer = %d, want 0", got)
	}
}

func TestRealClockNewTimer_SmokeTest(t *testing.T) {
	c := clock.NewReal()
	timer := c.NewTimer(10 * time.Millisecond)
	select {
	case <-timer.C():
	case <-time.After(time.Second):
		t.Fatal("Real.NewTimer never fired within 1s for a 10ms deadline")
	}
}

func TestRealClockNewTimer_StopBeforeFire(t *testing.T) {
	c := clock.NewReal()
	timer := c.NewTimer(time.Hour)
	if !timer.Stop() {
		t.Fatal("Stop on an unfired Real ChanTimer returned false")
	}
	if timer.Stop() {
		t.Fatal("second Stop on an already-stopped Real ChanTimer returned true")
	}
}

// TestFakeClockNewTimer_ConcurrentStopAdvanceRace exercises the fired-flag
// guard (mirroring FakeClock.AfterFunc's fakeTimer) that keeps a racing
// Stop and a racing Advance from double-processing the same waiter. Run
// under -race.
func TestFakeClockNewTimer_ConcurrentStopAdvanceRace(t *testing.T) {
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for i := 0; i < 200; i++ {
		f := clock.NewFake(start)
		timer := f.NewTimer(time.Millisecond)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			timer.Stop()
		}()
		go func() {
			defer wg.Done()
			f.Advance(time.Millisecond)
		}()
		wg.Wait()
	}
}

func TestFakeNextWaiterDeadline(t *testing.T) {
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	f := clock.NewFake(start)

	// No waiters yet.
	if _, ok := f.NextWaiterDeadline(); ok {
		t.Fatal("NextWaiterDeadline reported a deadline with no waiters")
	}

	// Register out of order; the earliest deadline must win.
	_ = f.After(5 * time.Second)
	_ = f.After(2 * time.Second)
	_ = f.After(9 * time.Second)
	got, ok := f.NextWaiterDeadline()
	if !ok {
		t.Fatal("NextWaiterDeadline reported no deadline with three waiters")
	}
	if want := start.Add(2 * time.Second); !got.Equal(want) {
		t.Fatalf("NextWaiterDeadline = %v; want earliest %v", got, want)
	}

	// Firing the earliest waiter exposes the next one.
	f.Advance(2 * time.Second)
	got, ok = f.NextWaiterDeadline()
	if !ok {
		t.Fatal("NextWaiterDeadline reported no deadline with two waiters left")
	}
	if want := start.Add(5 * time.Second); !got.Equal(want) {
		t.Fatalf("NextWaiterDeadline after advance = %v; want %v", got, want)
	}

	// Drain the rest; back to no waiters.
	f.Advance(10 * time.Second)
	if _, ok := f.NextWaiterDeadline(); ok {
		t.Fatal("NextWaiterDeadline reported a deadline after all waiters fired")
	}
}
