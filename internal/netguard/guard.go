package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Guard is a configurable SSRF policy for outbound requests to a
// caller-supplied endpoint URL — e.g. a push subscription's endpoint,
// which the server itself dials on every delivery. Unlike the
// package-level ControlContext (an unconditional block with no escape
// hatch), Guard supports an operator-declared allowlist of hostnames
// that are intentionally permitted to resolve into the deployment's
// own private network: the case a self-hosted push distributor (or
// any other operator-controlled internal service reached via a
// caller-supplied URL) requires.
//
// Two enforcement points, both required:
//
//   - ValidateURL / CheckHost: a fail-fast, pre-persistence check
//     (e.g. at PushSubscription/set { create }) so an obviously bad
//     registration is rejected immediately with a useful error.
//   - DialContext: the authoritative check. It resolves the hostname
//     exactly once and dials the validated IP literal, so the HTTP
//     client can never race a second DNS answer — the registration
//     check alone cannot close this gap (a rebinding attacker
//     controls the record between "validate" and "connect"), only
//     re-checking at the moment of connection can. Wire this into
//     http.Transport.DialContext for any client that fetches a
//     caller-supplied endpoint.
//
// The allowlist is matched against the URL's hostname (or IP-literal
// text) before resolution, never against the resolved address — an
// attacker-controlled hostname cannot borrow the exemption by
// resolving to the same IP as an allowlisted operator hostname,
// because DNS for the allowlisted name is not attacker-suppliable
// input.
type Guard struct {
	allowedHosts map[string]struct{}
	allowedPorts map[string]struct{}
	requireHTTPS bool

	dialer   *net.Dialer
	resolver hostResolver
	dial     dialFunc
}

// hostResolver is the minimal resolver interface Guard consumes; tests
// substitute a fake to script DNS answers (including a rebinding
// scenario: a different answer for the registration-time check than
// for the dial-time check).
type hostResolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// dialFunc is the low-level dial step Guard.DialContext performs once
// the target address has been validated. Defaults to
// (*net.Dialer).DialContext; tests substitute a stub so the "does the
// full guarded pipeline reach a real listener" assertion can run
// without outbound network access.
type dialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// GuardOptions configures NewGuard.
type GuardOptions struct {
	// AllowedHosts exempts specific hostnames or IP-literal strings
	// (matched case-insensitively, exact match on the URL's Hostname())
	// from the default loopback / link-local / private / ULA / CGNAT
	// block. Empty means no exemptions: the full Classify block list
	// applies to every target.
	AllowedHosts []string

	// RequireHTTPS rejects http:// endpoints when true. The zero value
	// (false) permits both schemes; callers wanting the RFC 8030-typical
	// "https only" default should set this explicitly.
	RequireHTTPS bool

	// AllowedPorts extends the default port set ({443}, plus {80} when
	// RequireHTTPS is false) with additional operator-declared ports,
	// e.g. a distributor listening on a nonstandard port.
	AllowedPorts []int

	// DialTimeout bounds the underlying TCP connect. Defaults to 30s.
	DialTimeout time.Duration
}

// NewGuard builds a Guard from opts. Safe for concurrent use.
func NewGuard(opts GuardOptions) *Guard {
	hosts := make(map[string]struct{}, len(opts.AllowedHosts))
	for _, h := range opts.AllowedHosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h != "" {
			hosts[h] = struct{}{}
		}
	}
	ports := map[string]struct{}{"443": {}}
	if !opts.RequireHTTPS {
		ports["80"] = struct{}{}
	}
	for _, p := range opts.AllowedPorts {
		if p > 0 && p <= 65535 {
			ports[strconv.Itoa(p)] = struct{}{}
		}
	}
	timeout := opts.DialTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	g := &Guard{
		allowedHosts: hosts,
		allowedPorts: ports,
		requireHTTPS: opts.RequireHTTPS,
		dialer:       dialer,
		resolver:     net.DefaultResolver,
	}
	g.dial = dialer.DialContext
	return g
}

// hostAllowed reports whether host is on the operator's exemption
// list. Matching is exact and case-insensitive against the raw
// hostname/IP-literal text supplied in the endpoint URL — never
// against a resolved address.
func (g *Guard) hostAllowed(host string) bool {
	_, ok := g.allowedHosts[strings.ToLower(host)]
	return ok
}

func (g *Guard) portAllowed(port string) bool {
	_, ok := g.allowedPorts[port]
	return ok
}

func defaultPortFor(scheme string) string {
	if scheme == "http" {
		return "80"
	}
	return "443"
}

// ValidateURL performs the scheme / userinfo / port checks that do not
// require DNS. Returns the parsed URL on success so callers can reuse
// u.Hostname() without re-parsing.
func (g *Guard) ValidateURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("netguard: parse url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("netguard: scheme %q not permitted", u.Scheme)
	}
	if g.requireHTTPS && u.Scheme != "https" {
		return nil, errors.New("netguard: endpoint must use https")
	}
	if u.User != nil {
		return nil, errors.New("netguard: endpoint must not carry userinfo")
	}
	host := u.Hostname()
	if host == "" {
		return nil, errors.New("netguard: endpoint has no host")
	}
	port := u.Port()
	if port == "" {
		port = defaultPortFor(u.Scheme)
	}
	if !g.portAllowed(port) {
		return nil, fmt.Errorf("netguard: port %s not permitted", port)
	}
	return u, nil
}

// CheckHost resolves host (an IP literal or DNS name) and returns a
// wrapped ErrBlockedIP if any resolved address is blocked, UNLESS host
// is on the allowlist. This is the fail-fast, pre-persistence check —
// it is NOT sufficient on its own as a security boundary (the DNS
// answer can legitimately differ by the time delivery dials), so
// every caller that also performs a later network fetch of the same
// endpoint MUST additionally route that fetch through DialContext.
func (g *Guard) CheckHost(ctx context.Context, host string) error {
	if host == "" {
		return fmt.Errorf("%w (empty host)", ErrBlockedIP)
	}
	if g.hostAllowed(host) {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil {
		return CheckIP(ip)
	}
	addrs, err := g.resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("netguard: resolve %q: %w", host, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("%w (no addresses for %s)", ErrBlockedIP, host)
	}
	for _, a := range addrs {
		if err := CheckIP(a.IP); err != nil {
			return err
		}
	}
	return nil
}

// DialContext is the authoritative check: wire it into
// http.Transport.DialContext (not just net.Dialer.ControlContext) so
// it runs BEFORE Go's own resolution, receiving the original
// "host:port" string. It resolves the host exactly once here,
// validates every candidate address (skipping the block when host is
// allowlisted), and dials the validated literal IP — so the HTTP
// client performs no further resolution and cannot be raced by a
// changed DNS answer between validation and connection (the
// rebinding / TOCTOU class this package exists to close).
func (g *Guard) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("netguard: split host:port %q: %w", addr, err)
	}
	if !g.portAllowed(port) {
		return nil, fmt.Errorf("netguard: port %s not permitted", port)
	}
	allowed := g.hostAllowed(host)
	if ip := net.ParseIP(host); ip != nil {
		if !allowed {
			if err := CheckIP(ip); err != nil {
				return nil, err
			}
		}
		return g.dial(ctx, network, net.JoinHostPort(ip.String(), port))
	}
	addrs, err := g.resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("netguard: resolve %q: %w", host, err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("%w (no addresses for %s)", ErrBlockedIP, host)
	}
	if !allowed {
		// Validate every candidate, not just the first: an attacker who
		// controls DNS can answer with a mix of clean and internal
		// addresses hoping we "race the safe one". Any blocked address
		// in the result set aborts the dial.
		for _, a := range addrs {
			if err := CheckIP(a.IP); err != nil {
				return nil, err
			}
		}
	}
	first := addrs[0].IP
	return g.dial(ctx, network, net.JoinHostPort(first.String(), port))
}
