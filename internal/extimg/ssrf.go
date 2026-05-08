package extimg

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"time"
)

// SSRF guard. See docs/design/server/requirements/17-external-images.md
// REQ-EXTIMG-30..37 for the full contract.
//
// The guard's job is to refuse a fetch whose effective destination is
// inside the operator's network. Three places get this wrong:
//
//  1. Forgetting an obscure CIDR (203.0.113.0/24, the v6 unique-local
//     range, IPv4-mapped IPv6 like ::ffff:127.0.0.1) lets the attacker
//     thread the needle. We use IANA's special-purpose registries as
//     the source of truth (REQ-EXTIMG-32) and write the full list
//     once.
//
//  2. Resolving in the application then resolving again in the HTTP
//     client lets DNS rebinding fire between checks. We resolve once
//     in DialContext and dial the resolved IP literal, so the second
//     resolve never happens (REQ-EXTIMG-30).
//
//  3. Letting a 3xx redirect target through unchecked. The HTTP
//     client's CheckRedirect is the enforcement point; the guard's
//     ValidateURL is reused there (REQ-EXTIMG-34).

// ssrfBlockedReason categorises what made a fetch SSRF-ineligible.
// Surfaced through error wrapping so callers can map it to per-fetch
// audit outcomes.
type ssrfBlockedReason string

const (
	ssrfBadScheme  ssrfBlockedReason = "scheme"
	ssrfBadPort    ssrfBlockedReason = "port"
	ssrfHasUser    ssrfBlockedReason = "userinfo"
	ssrfNoHost     ssrfBlockedReason = "no_host"
	ssrfResolve    ssrfBlockedReason = "resolve_failed"
	ssrfDeniedIP   ssrfBlockedReason = "denied_ip"
	ssrfBadAddrLit ssrfBlockedReason = "bad_addr_literal"
)

// ErrSSRFBlocked is returned by the guarded transport when a fetch is
// refused. The error message contains the human-readable reason and
// the matching CIDR (when a deny-list match is what fired).
type ErrSSRFBlocked struct {
	URL    string
	Reason ssrfBlockedReason
	IP     string // resolved address that failed (when applicable)
	CIDR   string // matching range (when reason == denied_ip)
}

func (e *ErrSSRFBlocked) Error() string {
	switch e.Reason {
	case ssrfDeniedIP:
		return fmt.Sprintf("ssrf: %s resolves to blocked %s (matches %s)",
			e.URL, e.IP, e.CIDR)
	case ssrfBadScheme:
		return fmt.Sprintf("ssrf: %s: scheme not allowed", e.URL)
	case ssrfBadPort:
		return fmt.Sprintf("ssrf: %s: port not allowed", e.URL)
	case ssrfHasUser:
		return fmt.Sprintf("ssrf: %s: userinfo not allowed", e.URL)
	case ssrfNoHost:
		return fmt.Sprintf("ssrf: %s: empty host", e.URL)
	case ssrfResolve:
		return fmt.Sprintf("ssrf: %s: host resolution failed (%s)", e.URL, e.IP)
	case ssrfBadAddrLit:
		return fmt.Sprintf("ssrf: %s: %s is not a valid host", e.URL, e.IP)
	}
	return fmt.Sprintf("ssrf: %s: %s", e.URL, e.Reason)
}

// ipResolver is the resolver interface tests substitute to inject
// rebinding scenarios.
type ipResolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// SSRFGuard is the pre-dial validator + dialer wrapper. Construct one
// per server lifetime; safe for concurrent use.
type SSRFGuard struct {
	denyV4       []netip.Prefix
	denyV6       []netip.Prefix
	allowedPorts map[string]bool
	requireHTTPS bool
	allowPrivate bool
	dialer       *net.Dialer
	resolver     ipResolver
}

// NewSSRFGuard builds a guard from cfg. The deny list is the IANA
// special-purpose registry (full list — REQ-EXTIMG-32) plus operator
// extensions, with one carve-out: when cfg.AllowPrivate is set the
// RFC-1918 / loopback / link-local ranges are removed (REQ-EXTIMG-33).
// The IPv4-mapped-IPv6, multicast, and TEST-NET ranges remain blocked
// even with AllowPrivate.
func NewSSRFGuard(cfg Config) *SSRFGuard {
	v4 := append([]netip.Prefix(nil), defaultDenyV4Always...)
	v6 := append([]netip.Prefix(nil), defaultDenyV6Always...)
	if !cfg.AllowPrivate {
		v4 = append(v4, defaultDenyV4Private...)
		v6 = append(v6, defaultDenyV6Private...)
	}
	for _, n := range cfg.DenyCIDRs {
		// Convert *net.IPNet to netip.Prefix for the runtime check.
		// The slice type is preserved on Config for sysconfig
		// compatibility, but the hot path uses netip for speed.
		ones, _ := n.Mask.Size()
		ip, ok := netip.AddrFromSlice(n.IP)
		if !ok {
			continue
		}
		ip = ip.Unmap()
		p := netip.PrefixFrom(ip, ones)
		if ip.Is4() {
			v4 = append(v4, p)
		} else {
			v6 = append(v6, p)
		}
	}
	connectTO := cfg.PerImageConnectTimeout
	if connectTO == 0 {
		connectTO = 5 * time.Second
	}
	allowed := map[string]bool{"80": true, "443": true}
	for _, p := range cfg.AllowedPorts {
		if p > 0 && p <= 65535 {
			allowed[strconv.Itoa(p)] = true
		}
	}
	return &SSRFGuard{
		denyV4:       v4,
		denyV6:       v6,
		allowedPorts: allowed,
		requireHTTPS: cfg.RequireHTTPS,
		allowPrivate: cfg.AllowPrivate,
		dialer:       &net.Dialer{Timeout: connectTO, KeepAlive: 30 * time.Second},
		resolver:     net.DefaultResolver,
	}
}

// withResolver returns a copy of the guard with the resolver replaced;
// used by tests to inject rebinding behaviour. Not exported.
func (g *SSRFGuard) withResolver(r ipResolver) *SSRFGuard {
	cp := *g
	cp.resolver = r
	return &cp
}

// ValidateURL enforces the scheme / port / userinfo rules without
// touching DNS. Used by CheckRedirect (REQ-EXTIMG-34) to refuse
// 3xx redirects before they hit the network. The DNS-aware part of
// the check happens in DialContext.
func (g *SSRFGuard) ValidateURL(u *url.URL) error {
	if u == nil {
		return &ErrSSRFBlocked{Reason: ssrfNoHost}
	}
	scheme := u.Scheme
	if scheme != "http" && scheme != "https" {
		return &ErrSSRFBlocked{URL: u.String(), Reason: ssrfBadScheme}
	}
	if g.requireHTTPS && scheme != "https" {
		return &ErrSSRFBlocked{URL: u.String(), Reason: ssrfBadScheme}
	}
	if u.User != nil {
		return &ErrSSRFBlocked{URL: u.String(), Reason: ssrfHasUser}
	}
	host := u.Hostname()
	if host == "" {
		return &ErrSSRFBlocked{URL: u.String(), Reason: ssrfNoHost}
	}
	port := u.Port()
	if port == "" {
		port = defaultPort(scheme)
	}
	if !g.allowedPorts[port] {
		return &ErrSSRFBlocked{URL: u.String(), Reason: ssrfBadPort}
	}
	return nil
}

// DialContext is the dialer wired into the http.Transport. Resolves
// the hostname once, validates every returned address, and dials the
// validated IP literal. The HTTP client never re-resolves because we
// pass back a connection it cannot re-route. This defeats DNS
// rebinding (REQ-EXTIMG-30).
func (g *SSRFGuard) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, &ErrSSRFBlocked{URL: addr, Reason: ssrfBadAddrLit, IP: addr}
	}
	if !g.allowedPorts[port] {
		return nil, &ErrSSRFBlocked{URL: addr, Reason: ssrfBadPort}
	}
	// If the host is already an IP literal, validate directly.
	if ip, perr := netip.ParseAddr(host); perr == nil {
		ip = ip.Unmap()
		if cidr := g.deniedBy(ip); cidr != "" {
			return nil, &ErrSSRFBlocked{URL: addr, Reason: ssrfDeniedIP,
				IP: ip.String(), CIDR: cidr}
		}
		return g.dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
	}
	// Hostname: resolve once, validate every result (REQ-EXTIMG-35),
	// dial the first one. If any returned address is in the deny
	// list we abort — we don't "race the safe ones", because an
	// attacker who answers DNS can mix poisoned results.
	ips, err := g.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, &ErrSSRFBlocked{URL: addr, Reason: ssrfResolve, IP: err.Error()}
	}
	if len(ips) == 0 {
		return nil, &ErrSSRFBlocked{URL: addr, Reason: ssrfResolve, IP: "no addresses"}
	}
	for _, ip := range ips {
		ip = ip.Unmap()
		if cidr := g.deniedBy(ip); cidr != "" {
			return nil, &ErrSSRFBlocked{URL: addr, Reason: ssrfDeniedIP,
				IP: ip.String(), CIDR: cidr}
		}
	}
	first := ips[0].Unmap()
	return g.dialer.DialContext(ctx, network, net.JoinHostPort(first.String(), port))
}

// deniedBy returns the matching prefix's String() form when ip is in
// the deny list, "" when it passes.
func (g *SSRFGuard) deniedBy(ip netip.Addr) string {
	if ip.Is4() {
		for _, p := range g.denyV4 {
			if p.Contains(ip) {
				return p.String()
			}
		}
		return ""
	}
	for _, p := range g.denyV6 {
		if p.Contains(ip) {
			return p.String()
		}
	}
	return ""
}

// defaultPort maps a URL scheme to its default port string.
func defaultPort(scheme string) string {
	switch scheme {
	case "http":
		return "80"
	case "https":
		return "443"
	}
	return ""
}

// IsBlockedSSRF reports whether err is an SSRF refusal. Used by the
// fetcher to map errors to per-fetch audit outcomes.
func IsBlockedSSRF(err error) bool {
	if err == nil {
		return false
	}
	var blocked *ErrSSRFBlocked
	return errors.As(err, &blocked)
}

// portCompat exists so older callers that pass int port can still
// look up the allowed-ports map via strconv.Itoa. Keeping the helper
// in the same package leaves the test surface tidy.
func portCompat(p int) string { return strconv.Itoa(p) }

var _ = portCompat // referenced by future per-port allowlist plumbing

// -----------------------------------------------------------------------
// Built-in deny list. Sources:
//   - IANA IPv4 Special-Purpose Address Registry
//     https://www.iana.org/assignments/iana-ipv4-special-registry
//   - IANA IPv6 Special-Purpose Address Registry
//     https://www.iana.org/assignments/iana-ipv6-special-registry
//
// Split into "Always" (refused even when AllowPrivate is set) and
// "Private" (refused only when AllowPrivate is false). The split
// matches REQ-EXTIMG-33's narrowed set.
// -----------------------------------------------------------------------

// defaultDenyV4Always are IPv4 ranges refused unconditionally — any
// address inside them is unsuitable as a fetch destination regardless
// of operator policy: TEST-NETs, multicast, broadcast, reserved-future.
var defaultDenyV4Always = []netip.Prefix{
	netipFrom4("0.0.0.0/8"),         // "this network"
	netipFrom4("192.0.0.0/24"),      // IETF protocol assignments
	netipFrom4("192.0.2.0/24"),      // TEST-NET-1
	netipFrom4("198.18.0.0/15"),     // benchmarking (RFC 2544)
	netipFrom4("198.51.100.0/24"),   // TEST-NET-2
	netipFrom4("203.0.113.0/24"),    // TEST-NET-3
	netipFrom4("224.0.0.0/4"),       // multicast
	netipFrom4("240.0.0.0/4"),       // future use / reserved
	netipFrom4("255.255.255.255/32"), // limited broadcast
}

// defaultDenyV4Private are RFC-1918 / loopback / link-local / CGNAT.
// Operators with legitimate internal-fetch needs may opt out via
// Config.AllowPrivate (REQ-EXTIMG-33).
var defaultDenyV4Private = []netip.Prefix{
	netipFrom4("10.0.0.0/8"),        // RFC 1918
	netipFrom4("100.64.0.0/10"),     // CGNAT (RFC 6598)
	netipFrom4("127.0.0.0/8"),       // loopback
	netipFrom4("169.254.0.0/16"),    // link-local + AWS metadata
	netipFrom4("172.16.0.0/12"),     // RFC 1918
	netipFrom4("192.168.0.0/16"),    // RFC 1918
}

// defaultDenyV6Always are IPv6 ranges refused unconditionally. The
// IPv4-mapped range (::ffff:0:0/96) is the most common omission in
// homegrown guards — without it the attacker can write
// ::ffff:127.0.0.1 and bypass the v4 checks. Multicast and discard
// are also unconditional.
var defaultDenyV6Always = []netip.Prefix{
	netipFrom6("::/128"),            // unspecified
	netipFrom6("::ffff:0:0/96"),     // IPv4-mapped (bypass guard)
	netipFrom6("64:ff9b::/96"),      // NAT64
	netipFrom6("100::/64"),          // discard-only
	netipFrom6("2001::/23"),         // IETF protocol assignments
	netipFrom6("2001:db8::/32"),     // documentation
	netipFrom6("ff00::/8"),          // multicast
}

// defaultDenyV6Private are loopback, link-local, ULA — refused unless
// AllowPrivate is set (REQ-EXTIMG-33).
var defaultDenyV6Private = []netip.Prefix{
	netipFrom6("::1/128"),           // loopback
	netipFrom6("fc00::/7"),          // ULA
	netipFrom6("fe80::/10"),         // link-local
}
