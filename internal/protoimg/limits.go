package protoimg

import (
	"container/list"
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
//
// Each of the three dimensional maps is bounded by a soft entry cap
// (limitsSoftCap, default 4096). When admit pushes a map past the cap
// we evict the least-recently-used entries in batches of
// limitsEvictBatch (default 64) to keep the steady-state work O(1)
// amortised. Evicting a per-user concurrency channel is only safe when
// no slots are in flight, which we approximate by skipping channels
// whose buffer length is non-zero — a pinned principal stays resident
// even if older. The eviction is opportunistic, not scheduled, so we
// do not need a sweeper goroutine bound to a server lifecycle.
type limiter struct {
	clk clock.Clock

	perUserPerMin       int
	perUserOriginPerMin int
	perUserConcurrent   int
	window              time.Duration

	mu          sync.Mutex
	user        map[store.PrincipalID]*list.Element // value: *userEntry
	userOrigin  map[userOriginKey]*list.Element     // value: *userOriginEntry
	concurrency map[store.PrincipalID]*list.Element // value: *concurrencyEntry

	userOrder        *list.List // most-recent at Front
	userOriginOrder  *list.List
	concurrencyOrder *list.List
}

// limitsSoftCap is the per-map entry ceiling above which admit evicts a
// batch of LRU entries. 4096 keys x ~16 bytes/key + ringbuf overhead
// stays well under a megabyte; the cap exists to keep memory bounded
// for a hostile or buggy client population, not to enforce policy.
const limitsSoftCap = 4096

// limitsEvictBatch is the number of LRU entries dropped on each over-
// cap admit. Larger than 1 so we don't pay map-walk overhead on every
// admission once the cap is reached; small enough that one admit's
// worst-case cost remains constant.
const limitsEvictBatch = 64

type userOriginKey struct {
	pid    store.PrincipalID
	origin string
}

type ringBuf struct {
	stamps []time.Time
	head   int
}

// userEntry / userOriginEntry / concurrencyEntry are the values we
// store in each map's *list.Element so eviction can recover the key.
type userEntry struct {
	pid store.PrincipalID
	rb  *ringBuf
}

type userOriginEntry struct {
	key userOriginKey
	rb  *ringBuf
}

type concurrencyEntry struct {
	pid store.PrincipalID
	sem chan struct{}
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
		user:                make(map[store.PrincipalID]*list.Element),
		userOrigin:          make(map[userOriginKey]*list.Element),
		concurrency:         make(map[store.PrincipalID]*list.Element),
		userOrder:           list.New(),
		userOriginOrder:     list.New(),
		concurrencyOrder:    list.New(),
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
	userRB := l.touchUser(pid)
	if ok, retry := ringAdmit(userRB, l.perUserPerMin, l.window, now); !ok {
		l.evictIfOver()
		l.mu.Unlock()
		return nil, admitDenyUser, retry
	}
	uok := userOriginKey{pid: pid, origin: origin}
	originRB := l.touchUserOrigin(uok)
	if ok, retry := ringAdmit(originRB, l.perUserOriginPerMin, l.window, now); !ok {
		// Roll back the per-user window: the request did not run, so
		// counting it would silently shrink the per-user budget.
		rollbackRing(userRB)
		l.evictIfOver()
		l.mu.Unlock()
		return nil, admitDenyOrigin, retry
	}
	sem := l.touchConcurrency(pid)
	l.evictIfOver()
	l.mu.Unlock()
	select {
	case sem <- struct{}{}:
		return func() { <-sem }, admitOK, 0
	default:
		// Concurrency saturated. Roll back both rings so the next
		// request still gets the full per-minute budget; otherwise the
		// caller is doubly punished by a transient burst.
		l.mu.Lock()
		rollbackRing(userRB)
		rollbackRing(originRB)
		l.mu.Unlock()
		return nil, admitDenyConcurrent, time.Second
	}
}

// touchUser returns the per-user ring buffer for pid, creating one on
// first use, and promotes the entry to the front of the LRU order.
// Caller holds l.mu.
func (l *limiter) touchUser(pid store.PrincipalID) *ringBuf {
	if el, ok := l.user[pid]; ok {
		l.userOrder.MoveToFront(el)
		return el.Value.(*userEntry).rb
	}
	rb := &ringBuf{stamps: make([]time.Time, l.perUserPerMin)}
	el := l.userOrder.PushFront(&userEntry{pid: pid, rb: rb})
	l.user[pid] = el
	return rb
}

// touchUserOrigin mirrors touchUser for the (pid, origin) keyed map.
func (l *limiter) touchUserOrigin(k userOriginKey) *ringBuf {
	if el, ok := l.userOrigin[k]; ok {
		l.userOriginOrder.MoveToFront(el)
		return el.Value.(*userOriginEntry).rb
	}
	rb := &ringBuf{stamps: make([]time.Time, l.perUserOriginPerMin)}
	el := l.userOriginOrder.PushFront(&userOriginEntry{key: k, rb: rb})
	l.userOrigin[k] = el
	return rb
}

// touchConcurrency returns the per-user semaphore channel for pid,
// creating one on first use, and promotes the entry to the front of
// the LRU order.
func (l *limiter) touchConcurrency(pid store.PrincipalID) chan struct{} {
	if el, ok := l.concurrency[pid]; ok {
		l.concurrencyOrder.MoveToFront(el)
		return el.Value.(*concurrencyEntry).sem
	}
	sem := make(chan struct{}, l.perUserConcurrent)
	el := l.concurrencyOrder.PushFront(&concurrencyEntry{pid: pid, sem: sem})
	l.concurrency[pid] = el
	return sem
}

// evictIfOver drops up to limitsEvictBatch LRU entries from each map
// that is past limitsSoftCap. Caller holds l.mu.
//
// The concurrency map skips entries whose semaphore is currently held
// (len(sem) > 0): forgetting an in-flight reservation would let the
// owning principal's release() write to a freed channel. Skipped
// entries are pulled to the front so they are not retried on every
// over-cap admission; if every entry is held, the soft cap is the
// effective floor (which is fine — concurrency is bounded by the
// per-user limit anyway, so the worst case is still small).
func (l *limiter) evictIfOver() {
	if l.userOrder.Len() > limitsSoftCap {
		evictRingLRU(l.userOrder, func(el *list.Element) {
			delete(l.user, el.Value.(*userEntry).pid)
		})
	}
	if l.userOriginOrder.Len() > limitsSoftCap {
		evictRingLRU(l.userOriginOrder, func(el *list.Element) {
			delete(l.userOrigin, el.Value.(*userOriginEntry).key)
		})
	}
	if l.concurrencyOrder.Len() > limitsSoftCap {
		evictConcurrencyLRU(l.concurrencyOrder, func(el *list.Element) {
			delete(l.concurrency, el.Value.(*concurrencyEntry).pid)
		})
	}
}

// evictRingLRU drops up to limitsEvictBatch ring-buffer entries from
// the back of order, calling delMap for each so the corresponding map
// entry goes too.
func evictRingLRU(order *list.List, delMap func(*list.Element)) {
	for i := 0; i < limitsEvictBatch; i++ {
		back := order.Back()
		if back == nil {
			return
		}
		order.Remove(back)
		delMap(back)
	}
}

// evictConcurrencyLRU mirrors evictRingLRU but skips entries whose
// semaphore is currently in use; see evictIfOver for the rationale.
func evictConcurrencyLRU(order *list.List, delMap func(*list.Element)) {
	skipped := 0
	for i := 0; i < limitsEvictBatch; i++ {
		back := order.Back()
		if back == nil {
			return
		}
		ce := back.Value.(*concurrencyEntry)
		if len(ce.sem) > 0 {
			// Held: promote so we don't retry the same victim on the
			// next admission. Bound the skip walk so a fully-held map
			// returns instead of looping forever.
			order.MoveToFront(back)
			skipped++
			if skipped >= order.Len() {
				return
			}
			continue
		}
		order.Remove(back)
		delMap(back)
	}
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
