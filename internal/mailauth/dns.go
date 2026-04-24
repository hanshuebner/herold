package mailauth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
)

// ErrNoRecords is returned by Resolver implementations when a lookup
// succeeds at the protocol level but the RRset is empty. Callers use
// errors.Is to distinguish "no such record" from genuine DNS errors.
var ErrNoRecords = errors.New("mailauth: no records")

// Resolver is the DNS abstraction every mail-auth subsystem uses. The
// interface is deliberately small: DKIM needs TXT, SPF needs TXT + A +
// AAAA + MX, DMARC needs TXT, ARC needs TXT. Everything goes through this
// one surface so the test harness's fakedns satisfies it.
//
// Every method takes a context.Context and honours its cancellation. Host
// and domain names are canonicalised to lowercase without a trailing dot
// by implementations; callers may pass either form.
type Resolver interface {
	// TXTLookup returns TXT record values for name, or ErrNoRecords when
	// the name has no TXT records. Multi-string TXT records are returned
	// as one string per record (segments concatenated), matching
	// net.Resolver.LookupTXT semantics.
	TXTLookup(ctx context.Context, name string) ([]string, error)
	// MXLookup returns the MX records for domain sorted by Preference
	// ascending. ErrNoRecords indicates no MX is published.
	MXLookup(ctx context.Context, domain string) ([]*net.MX, error)
	// IPLookup returns the A and AAAA records for host combined, in that
	// order. ErrNoRecords indicates the host has no address records.
	IPLookup(ctx context.Context, host string) ([]net.IP, error)
}

// SystemResolver wraps the stdlib net.Resolver so production callers have
// a default. Tests should use internal/testharness/fakedns instead.
type SystemResolver struct {
	r *net.Resolver
}

// NewSystemResolver returns a Resolver backed by net.DefaultResolver.
func NewSystemResolver() *SystemResolver {
	return &SystemResolver{r: net.DefaultResolver}
}

// TXTLookup implements Resolver.
func (s *SystemResolver) TXTLookup(ctx context.Context, name string) ([]string, error) {
	txts, err := s.r.LookupTXT(ctx, canonicalise(name))
	if err != nil {
		return nil, translate(err)
	}
	if len(txts) == 0 {
		return nil, fmt.Errorf("%w: TXT %s", ErrNoRecords, name)
	}
	return txts, nil
}

// MXLookup implements Resolver.
func (s *SystemResolver) MXLookup(ctx context.Context, domain string) ([]*net.MX, error) {
	mxs, err := s.r.LookupMX(ctx, canonicalise(domain))
	if err != nil {
		return nil, translate(err)
	}
	if len(mxs) == 0 {
		return nil, fmt.Errorf("%w: MX %s", ErrNoRecords, domain)
	}
	return mxs, nil
}

// IPLookup implements Resolver.
func (s *SystemResolver) IPLookup(ctx context.Context, host string) ([]net.IP, error) {
	addrs, err := s.r.LookupIPAddr(ctx, canonicalise(host))
	if err != nil {
		return nil, translate(err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("%w: IP %s", ErrNoRecords, host)
	}
	out := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.IP)
	}
	return out, nil
}

// canonicalise lowercases name and strips a single trailing dot.
func canonicalise(name string) string {
	name = strings.ToLower(name)
	name = strings.TrimSuffix(name, ".")
	return name
}

// translate maps a net.DNSError's "not found" into ErrNoRecords and
// preserves the original error for temporary failures via %w. Callers
// that need to distinguish permanent from temporary errors should check
// the returned error with a *net.DNSError errors.As.
func translate(err error) error {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return fmt.Errorf("%w: %s", ErrNoRecords, dnsErr.Name)
	}
	return err
}

// IsTemporary reports whether err is a temporary DNS failure (timeout,
// server-side 5xx, transient network error). It is the shared predicate
// every mail-auth subsystem uses to pick between TempError and PermError
// verdicts.
func IsTemporary(err error) bool {
	if err == nil {
		return false
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return dnsErr.IsTimeout || dnsErr.IsTemporary
	}
	// net.Error covers the broader transport-level temporary conditions
	// that can surface before the DNS-error wrapper gets a chance.
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout()
	}
	return false
}
