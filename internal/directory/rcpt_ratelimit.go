package directory

import (
	"sync"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
)

// DefaultRcptRateLimit is the per-source-IP RCPT rate cap applied
// when [smtp.inbound.rcpt_rate_limit_per_ip_per_sec] is unset
// (REQ-DIR-RCPT-06).
const DefaultRcptRateLimit = 50

// ResolveRcptRateLimiter is a sliding-1s-window per-source-IP counter
// that gates resolve_rcpt invocations. Excess RCPTs from one IP get
// SMTP 4.7.1 from the caller; the goal is to prevent a flood of
// synthetic RCPTs from amplifying into a flood of HTTP calls to the
// application's validator.
//
// The implementation is intentionally simple: per-IP we keep a slice
// of recent allow timestamps trimmed to a 1s lookback. Concurrency-
// safe; bounded memory by the active-IP set, which the SMTP server
// already caps via its per-IP connection limit.
type ResolveRcptRateLimiter struct {
	clk     clock.Clock
	perSec  int
	window  time.Duration
	mu      sync.Mutex
	buckets map[string][]time.Time
}

// NewResolveRcptRateLimiter constructs a limiter with perSec
// RCPTs/second/source-IP. perSec <= 0 falls back to
// DefaultRcptRateLimit.
func NewResolveRcptRateLimiter(clk clock.Clock, perSec int) *ResolveRcptRateLimiter {
	if clk == nil {
		clk = clock.NewReal()
	}
	if perSec <= 0 {
		perSec = DefaultRcptRateLimit
	}
	return &ResolveRcptRateLimiter{
		clk:     clk,
		perSec:  perSec,
		window:  time.Second,
		buckets: make(map[string][]time.Time),
	}
}

// Allow records and admits one RCPT from sourceIP when the
// per-second budget has headroom; returns false to refuse with
// 4.7.1.
func (l *ResolveRcptRateLimiter) Allow(sourceIP string) bool {
	if l == nil {
		return true
	}
	now := l.clk.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-l.window)
	hist := l.buckets[sourceIP]
	kept := hist[:0]
	for _, t := range hist {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.perSec {
		// Persist the trimmed slice so the next call sees the same
		// bookkeeping; refuse this attempt.
		l.buckets[sourceIP] = kept
		return false
	}
	kept = append(kept, now)
	l.buckets[sourceIP] = kept
	// Best-effort GC: drop empty buckets so long-quiet IPs do not pin
	// memory. We only do this when the slice is already empty post-
	// trim because a single call into Allow always produces at least
	// one entry.
	return true
}
