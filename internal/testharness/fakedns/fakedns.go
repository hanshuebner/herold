// Package fakedns implements a deterministic in-memory DNS resolver used by
// the test harness. Tests register records against a Resolver instance and
// subsystems under test resolve through it; nothing touches the host's DNS
// configuration. The zero value is ready for use (empty answer set).
package fakedns

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
)

// ErrNoRecords is returned by lookup methods when no record of the
// requested type exists for the queried name. It mirrors the observable
// behaviour of the real DNS stack (NXDOMAIN-like) without committing to a
// particular DNS error code; callers use errors.Is to branch.
var ErrNoRecords = errors.New("fakedns: no records")

// MX is a single MX record (RFC 1035 §3.3.9). Preference lower is more
// preferred; Host is the target hostname (without trailing dot).
type MX struct {
	Preference uint16
	Host       string
}

// TLSA is a TLSA record (RFC 6698 §2.1). All fields are as defined by the
// RFC; the harness does not interpret Data.
type TLSA struct {
	Usage        uint8
	Selector     uint8
	MatchingType uint8
	Data         []byte
}

// Resolver is the in-memory DNS resolver. All methods are safe for
// concurrent use. Names are canonicalized to lowercase with no trailing
// dot before lookup.
type Resolver struct {
	mu   sync.RWMutex
	mx   map[string][]MX
	a    map[string][]net.IP
	aaaa map[string][]net.IP
	txt  map[string][]string
	tlsa map[string][]TLSA
}

// New returns a Resolver with empty answer sets.
func New() *Resolver {
	return &Resolver{
		mx:   make(map[string][]MX),
		a:    make(map[string][]net.IP),
		aaaa: make(map[string][]net.IP),
		txt:  make(map[string][]string),
		tlsa: make(map[string][]TLSA),
	}
}

// canonical lowercases name and strips a single trailing dot.
func canonical(name string) string {
	name = strings.ToLower(name)
	name = strings.TrimSuffix(name, ".")
	return name
}

// AddMX appends one MX record for name. Records are returned from LookupMX
// sorted by Preference ascending (ties preserved in insertion order).
func (r *Resolver) AddMX(name string, preference uint16, host string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := canonical(name)
	r.mx[n] = append(r.mx[n], MX{Preference: preference, Host: canonical(host)})
}

// AddA appends one IPv4 A record for name.
func (r *Resolver) AddA(name string, ip net.IP) {
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.a[canonical(name)] = append(r.a[canonical(name)], ip)
}

// AddAAAA appends one IPv6 AAAA record for name.
func (r *Resolver) AddAAAA(name string, ip net.IP) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.aaaa[canonical(name)] = append(r.aaaa[canonical(name)], ip)
}

// AddTXT appends one TXT record value for name.
func (r *Resolver) AddTXT(name, value string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.txt[canonical(name)] = append(r.txt[canonical(name)], value)
}

// AddTLSA appends one TLSA record for name.
func (r *Resolver) AddTLSA(name string, t TLSA) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tlsa[canonical(name)] = append(r.tlsa[canonical(name)], t)
}

// AddRecord is the generic entry point used by the test harness's
// AddDNSRecord shim. rrtype is one of MX, A, AAAA, TXT (case-insensitive).
// For MX, value is "preference host" (e.g. "10 mx.example.net").
// TLSA is not supported here; use AddTLSA directly.
func (r *Resolver) AddRecord(name, rrtype, value string) error {
	switch strings.ToUpper(rrtype) {
	case "MX":
		var pref uint16
		var host string
		if _, err := fmt.Sscanf(value, "%d %s", &pref, &host); err != nil {
			return fmt.Errorf("fakedns: bad MX value %q: %w", value, err)
		}
		r.AddMX(name, pref, host)
	case "A":
		ip := net.ParseIP(value)
		if ip == nil || ip.To4() == nil {
			return fmt.Errorf("fakedns: bad A value %q", value)
		}
		r.AddA(name, ip)
	case "AAAA":
		ip := net.ParseIP(value)
		if ip == nil || ip.To4() != nil {
			return fmt.Errorf("fakedns: bad AAAA value %q", value)
		}
		r.AddAAAA(name, ip)
	case "TXT":
		r.AddTXT(name, value)
	default:
		return fmt.Errorf("fakedns: unsupported rrtype %q", rrtype)
	}
	return nil
}

// LookupMX returns MX records for name sorted by Preference ascending.
// Returns ErrNoRecords when no MX is registered for name.
func (r *Resolver) LookupMX(ctx context.Context, name string) ([]MX, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	rr, ok := r.mx[canonical(name)]
	if !ok || len(rr) == 0 {
		return nil, fmt.Errorf("%w: MX %s", ErrNoRecords, name)
	}
	out := make([]MX, len(rr))
	copy(out, rr)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Preference < out[j].Preference })
	return out, nil
}

// LookupA returns A records for name, or ErrNoRecords.
func (r *Resolver) LookupA(ctx context.Context, name string) ([]net.IP, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	rr, ok := r.a[canonical(name)]
	if !ok || len(rr) == 0 {
		return nil, fmt.Errorf("%w: A %s", ErrNoRecords, name)
	}
	out := make([]net.IP, len(rr))
	copy(out, rr)
	return out, nil
}

// LookupAAAA returns AAAA records for name, or ErrNoRecords.
func (r *Resolver) LookupAAAA(ctx context.Context, name string) ([]net.IP, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	rr, ok := r.aaaa[canonical(name)]
	if !ok || len(rr) == 0 {
		return nil, fmt.Errorf("%w: AAAA %s", ErrNoRecords, name)
	}
	out := make([]net.IP, len(rr))
	copy(out, rr)
	return out, nil
}

// LookupTXT returns TXT record values for name, or ErrNoRecords.
func (r *Resolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	rr, ok := r.txt[canonical(name)]
	if !ok || len(rr) == 0 {
		return nil, fmt.Errorf("%w: TXT %s", ErrNoRecords, name)
	}
	out := make([]string, len(rr))
	copy(out, rr)
	return out, nil
}

// LookupTLSA returns TLSA records for name, or ErrNoRecords.
func (r *Resolver) LookupTLSA(ctx context.Context, name string) ([]TLSA, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	rr, ok := r.tlsa[canonical(name)]
	if !ok || len(rr) == 0 {
		return nil, fmt.Errorf("%w: TLSA %s", ErrNoRecords, name)
	}
	out := make([]TLSA, len(rr))
	copy(out, rr)
	return out, nil
}
