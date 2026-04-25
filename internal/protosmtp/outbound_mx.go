package protosmtp

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"

	"github.com/hanshuebner/herold/internal/mailauth"
)

// mxCandidate is one MX target the Client may attempt. Pref is the
// declared preference (lower = more preferred); host is the canonical
// lower-case DNS name. Implicit indicates the candidate was synthesised
// from the recipient domain's A/AAAA records (RFC 5321 §5.1) because no
// MX was published.
type mxCandidate struct {
	pref     uint16
	host     string
	implicit bool
}

// resolveMX returns the ordered list of MX candidates for domain.
//
//   - Records are sorted by preference ascending; equal-preference groups
//     are randomised per RFC 5321 §5.1 to spread load across operator
//     pools.
//   - When the recipient domain has no MX records, RFC 5321 §5.1 mandates
//     an implicit MX of preference 0 pointing at the domain itself; we
//     verify A/AAAA exist before returning the synthetic candidate, so a
//     domain with neither MX nor address records yields ErrNoRecords.
//   - The "null MX" sentinel (a single MX with preference 0 and host ".")
//     is interpreted per RFC 7505 as "this domain does not accept mail":
//     resolveMX returns a permanent-shaped error so callers map it to
//     5.1.10 in the outcome.
func (c *Client) resolveMX(ctx context.Context, domain string) ([]mxCandidate, error) {
	mxs, err := c.resolver.MXLookup(ctx, domain)
	if err == nil && len(mxs) == 1 && (mxs[0].Host == "." || mxs[0].Host == "") && mxs[0].Pref == 0 {
		return nil, fmt.Errorf("%w: null MX (RFC 7505) for %s", errNullMX, domain)
	}
	if err == nil && len(mxs) > 0 {
		// Group by preference, then shuffle within each group.
		// SliceStable on Pref preserves insertion order; the per-group
		// shuffle uses a Client-local PRNG (math/rand/v2) — load
		// distribution doesn't need cryptographic randomness.
		out := make([]mxCandidate, 0, len(mxs))
		for _, m := range mxs {
			out = append(out, mxCandidate{pref: m.Pref, host: lowerHost(m.Host)})
		}
		sort.SliceStable(out, func(i, j int) bool { return out[i].pref < out[j].pref })
		shuffleEqualPref(out)
		return out, nil
	}
	// MX missing → fall back to the implicit MX, but only when the
	// recipient domain itself has A/AAAA records.
	if err != nil && !errors.Is(err, mailauth.ErrNoRecords) {
		return nil, err
	}
	ips, ipErr := c.resolver.IPLookup(ctx, domain)
	if ipErr != nil {
		// Surface the original MX error if more informative.
		if errors.Is(ipErr, mailauth.ErrNoRecords) {
			return nil, fmt.Errorf("%w: no MX or A/AAAA for %s", mailauth.ErrNoRecords, domain)
		}
		return nil, ipErr
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("%w: no MX or A/AAAA for %s", mailauth.ErrNoRecords, domain)
	}
	return []mxCandidate{{pref: 0, host: lowerHost(domain), implicit: true}}, nil
}

// errNullMX is a sentinel returned by resolveMX when the recipient domain
// publishes the RFC 7505 null-MX. Deliver maps it onto a Permanent outcome
// with enhanced status 5.1.10.
var errNullMX = errors.New("protosmtp: null MX (RFC 7505)")

// shuffleEqualPref shuffles consecutive runs of equal-preference entries
// in place. The shuffle uses math/rand/v2 with default-seed semantics;
// determinism in tests comes from the test seeding the package-global
// PRNG via rand.Seed (not exposed in v2) — for the test suite we only
// have one record per preference, so the shuffle is a no-op there.
func shuffleEqualPref(c []mxCandidate) {
	for i := 0; i < len(c); {
		j := i + 1
		for j < len(c) && c[j].pref == c[i].pref {
			j++
		}
		if j-i > 1 {
			rand.Shuffle(j-i, func(a, b int) { c[i+a], c[i+b] = c[i+b], c[i+a] })
		}
		i = j
	}
}

// lowerHost lower-cases h and trims a trailing dot. We reuse the
// canonicaliseHost helper from outbound.go but accept arbitrary input
// (including the bare domain for implicit MX) here.
func lowerHost(h string) string {
	return canonicaliseHost(h)
}
