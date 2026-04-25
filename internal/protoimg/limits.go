package protoimg

import (
	"strconv"
	"sync"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
)

// limiter holds the three rate-limit dimensions REQ-SEND-77 imposes:
// per-user RPM, per-(user, origin) RPM, and per-user concurrency. The
// per-minute counts are sliding-window: each bucket records the
// timestamps of in-window admissions and rejects the next request when
// the count meets the limit.
//
// Concurrency uses a buffered channel as a counting semaphore — one
// channel per principal, lazily allocated. Tokens are released on
// release; a denied acquire returns immediately rather than blocking.
type limiter struct {
	clk clock.Clock

	perUserPerMin       int
	perUserOriginPerMin int
	perUserConcurrent   int
	window              time.Duration

	mu          sync.Mutex
	user        map[store.PrincipalID]*ringBuf
	userOrigin  map[userOriginKey]*ringBuf
	concurrency map[store.PrincipalID]chan struct{}
}

type userOriginKey struct {
	pid    store.PrincipalID
	origin string
}

type ringBuf struct {
	stamps []time.Time
	head   int
}

func newLimiter(clk clock.Clock, perUserPerMin, perUserOriginPerMin, perUserConcurrent int) *limiter {
	if perUserPerMin <= 0 {
		perUserPerMin = 1
	}
	if perUserOriginPerMin <= 0 {
		perUserOriginPerMin = 1
	}
	if perUserConcurrent <= 0 {
		perUserConcurrent = 1
	}
	return &limiter{
		clk:                 clk,
		perUserPerMin:       perUserPerMin,
		perUserOriginPerMin: perUserOriginPerMin,
		perUserConcurrent:   perUserConcurrent,
		window:              time.Minute,
		user:                make(map[store.PrincipalID]*ringBuf),
		userOrigin:          make(map[userOriginKey]*ringBuf),
		concurrency:         make(map[store.PrincipalID]chan struct{}),
	}
}

// admit checks the per-user and per-(user, origin) sliding windows and
// the per-user concurrency semaphore. On success, returns a release
// function that frees the concurrency slot — callers must defer it.
//
// On rejection, the returned reason classifies the limit hit and
// retryAfter is the operator-visible Retry-After value.
type admitReason int

const (
	admitOK admitReason = iota
	admitDenyUser
	admitDenyOrigin
	admitDenyConcurrent
)

func (l *limiter) admit(pid store.PrincipalID, origin string) (release func(), reason admitReason, retryAfter time.Duration) {
	l.mu.Lock()
	now := l.clk.Now()
	if ok, retry := admitRing(l.user, pid, l.perUserPerMin, l.window, now); !ok {
		l.mu.Unlock()
		return nil, admitDenyUser, retry
	}
	uok := userOriginKey{pid: pid, origin: origin}
	if ok, retry := admitOriginRing(l.userOrigin, uok, l.perUserOriginPerMin, l.window, now); !ok {
		l.mu.Unlock()
		// Roll back the per-user window: the request did not run, so
		// counting it would silently shrink the per-user budget.
		rollbackRing(l.user[pid])
		return nil, admitDenyOrigin, retry
	}
	sem, ok := l.concurrency[pid]
	if !ok {
		sem = make(chan struct{}, l.perUserConcurrent)
		l.concurrency[pid] = sem
	}
	l.mu.Unlock()
	select {
	case sem <- struct{}{}:
		return func() { <-sem }, admitOK, 0
	default:
		// Concurrency saturated. Roll back both rings so the next
		// request still gets the full per-minute budget; otherwise the
		// caller is doubly punished by a transient burst.
		l.mu.Lock()
		rollbackRing(l.user[pid])
		rollbackRing(l.userOrigin[uok])
		l.mu.Unlock()
		return nil, admitDenyConcurrent, time.Second
	}
}

// admitRing and admitOriginRing handle the two map shapes (principal-
// keyed, (principal, origin)-keyed) without forcing the call site
// through a generic. The signatures are deliberately specialised so
// the caller does not invent a sentinel key value.
func admitRing(m map[store.PrincipalID]*ringBuf, pid store.PrincipalID, limit int, window time.Duration, now time.Time) (bool, time.Duration) {
	rb, ok := m[pid]
	if !ok {
		rb = &ringBuf{stamps: make([]time.Time, limit)}
		m[pid] = rb
	}
	return ringAdmit(rb, limit, window, now)
}

func admitOriginRing(m map[userOriginKey]*ringBuf, k userOriginKey, limit int, window time.Duration, now time.Time) (bool, time.Duration) {
	rb, ok := m[k]
	if !ok {
		rb = &ringBuf{stamps: make([]time.Time, limit)}
		m[k] = rb
	}
	return ringAdmit(rb, limit, window, now)
}

// ringAdmit counts in-window stamps; if below limit, records the new
// timestamp and returns ok. Otherwise computes the soonest the oldest
// in-window stamp falls out and returns that as Retry-After.
func ringAdmit(rb *ringBuf, limit int, window time.Duration, now time.Time) (bool, time.Duration) {
	oldest := now.Add(-window)
	inWindow := 0
	earliest := now
	for _, t := range rb.stamps {
		if t.IsZero() || t.Before(oldest) {
			continue
		}
		inWindow++
		if t.Before(earliest) {
			earliest = t
		}
	}
	if inWindow >= limit {
		retry := window - now.Sub(earliest)
		if retry < time.Second {
			retry = time.Second
		}
		return false, retry
	}
	rb.stamps[rb.head] = now
	rb.head = (rb.head + 1) % len(rb.stamps)
	return true, 0
}

// rollbackRing undoes the most recent stamp. Called when a downstream
// limit denied a request that earlier rings already counted.
func rollbackRing(rb *ringBuf) {
	if rb == nil || len(rb.stamps) == 0 {
		return
	}
	prev := rb.head - 1
	if prev < 0 {
		prev = len(rb.stamps) - 1
	}
	rb.stamps[prev] = time.Time{}
	rb.head = prev
}

// retryAfterSeconds renders a duration as the integer seconds the
// Retry-After header expects, with a minimum of 1.
func retryAfterSeconds(d time.Duration) string {
	s := int(d.Round(time.Second).Seconds())
	if s < 1 {
		s = 1
	}
	return strconv.Itoa(s)
}
