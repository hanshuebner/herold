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
