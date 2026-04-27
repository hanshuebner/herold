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

// TLSARecord is a single TLSA record (RFC 6698 §2.1) returned by a
// TLSAResolver. The fields mirror the on-wire encoding: Usage (PKIX-TA /
// PKIX-EE / DANE-TA / DANE-EE), Selector (full cert / SubjectPublicKeyInfo),
// and MatchingType (exact / SHA-256 / SHA-512). Data is the certificate
// association data — its length and meaning depend on Selector and
// MatchingType.
type TLSARecord struct {
	Usage        uint8
	Selector     uint8
	MatchingType uint8
	Data         []byte
}

// TLSAResolver is the optional DANE extension to Resolver. Implementations
// resolve TLSA records and report whether the answer was DNSSEC-validated
// (Authentic Data); both signals are required for RFC 7672 to apply. We
// keep this as a sibling interface rather than folding it into Resolver
// because most subsystems do not need it and the stdlib net.Resolver
// cannot honour the DNSSEC AD bit at all — only operators running a
// validating resolver beneath the stub library will see Authentic=true.
type TLSAResolver interface {
	// LookupTLSA returns TLSA records for name and reports whether the
	// answer was DNSSEC-validated. ErrNoRecords indicates no TLSA RRset
	// is published; non-nil error otherwise is a transport / temp error.
	LookupTLSA(ctx context.Context, name string) (records []TLSARecord, authentic bool, err error)
}

// LookupTLSA implements TLSAResolver. The stdlib net.Resolver does not
// surface TLSA records; SystemResolver therefore returns ErrNoRecords
// unconditionally and authentic=false. Operators wanting DANE outbound
// must inject a Resolver implementation backed by a DNSSEC-validating
// library; the server falls back to MTA-STS and opportunistic TLS in the
// meantime per the precedence rules in docs/design/server/architecture/04.
func (s *SystemResolver) LookupTLSA(ctx context.Context, name string) ([]TLSARecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	return nil, false, fmt.Errorf("%w: TLSA %s (stdlib resolver does not support TLSA)", ErrNoRecords, name)
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
