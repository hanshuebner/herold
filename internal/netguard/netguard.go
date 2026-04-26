package netguard

// The package-level documentation lives in doc.go; this file is the
// exported-symbol table starting at ErrBlockedIP.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
)

// ErrBlockedIP is the sentinel returned by Control / CheckIP when an
// outbound dial targets an address that fails the SSRF predicate.
// Callers wrap it with the upstream URL via fmt.Errorf("...: %w", ...)
// so logs surface both the host and the violation reason.
var ErrBlockedIP = errors.New("netguard: target address is in a blocked range")

// Reason is a stable machine-readable token classifying why an IP was
// rejected. Used in log fields and tests.
type Reason string

const (
	ReasonLoopback    Reason = "loopback"
	ReasonLinkLocal   Reason = "link_local"
	ReasonPrivate     Reason = "private"
	ReasonMulticast   Reason = "multicast"
	ReasonUnspecified Reason = "unspecified"
	ReasonULA         Reason = "ipv6_ula"
	ReasonCGNAT       Reason = "cgnat"
)

// Classify returns ("", "") if ip is an acceptable public address, or
// the matching Reason otherwise. The check covers RFC 1918 (10/8,
// 172.16/12, 192.168/16), RFC 6598 (100.64/10 — CGNAT), 127/8,
// 169.254/16, 0.0.0.0, IPv4 multicast (224/4), IPv6 ::1, fe80::/10,
// fc00::/7 (ULA), IPv6 multicast ff00::/8, and the IPv6 unspecified
// address ::.
func Classify(ip net.IP) (Reason, bool) {
	if ip == nil {
		return ReasonUnspecified, true
	}
	if ip.IsUnspecified() {
		return ReasonUnspecified, true
	}
	if ip.IsLoopback() {
		return ReasonLoopback, true
	}
	if ip.IsMulticast() {
		return ReasonMulticast, true
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return ReasonLinkLocal, true
	}
	if ip.IsPrivate() {
		// stdlib IsPrivate covers 10/8, 172.16/12, 192.168/16, fc00::/7.
		if ip.To4() == nil {
			return ReasonULA, true
		}
		return ReasonPrivate, true
	}
	if v4 := ip.To4(); v4 != nil {
		// RFC 6598 carrier-grade NAT range (100.64.0.0/10) is not
		// covered by stdlib IsPrivate; reject it because operators
		// using CGNAT typically still consider the range internal.
		if v4[0] == 100 && (v4[1]&0xC0) == 64 {
			return ReasonCGNAT, true
		}
	}
	return "", false
}

// CheckIP returns a wrapped ErrBlockedIP if ip is in a blocked range.
// Convenience over Classify for call sites that want a single
// error-or-nil shape.
func CheckIP(ip net.IP) error {
	if r, blocked := Classify(ip); blocked {
		return fmt.Errorf("%w (%s %s)", ErrBlockedIP, r, ip.String())
	}
	return nil
}

// CheckHost resolves host (IP literal or DNS name) via the given
// resolver and returns a wrapped ErrBlockedIP if any resolved address
// is in a blocked range. resolver may be nil to use net.DefaultResolver.
//
// Used by the categoriser before issuing an HTTP request, where we
// must reject hostnames that resolve to internal addresses without
// having to wait for the dial to fail.
func CheckHost(ctx context.Context, resolver *net.Resolver, host string) error {
	if host == "" {
		return fmt.Errorf("%w (empty host)", ErrBlockedIP)
	}
	if ip := net.ParseIP(host); ip != nil {
		return CheckIP(ip)
	}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	addrs, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("netguard: lookup %q: %w", host, err)
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

// IsLocalhost reports whether host is a textual representation of a
// loopback address. Used by the categoriser's http:// allow-list.
func IsLocalhost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	// Strip any zone identifier on IPv6 (e.g. "::1%lo0").
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i]
	}
	switch host {
	case "localhost", "127.0.0.1", "::1", "[::1]":
		return true
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return true
	}
	return false
}

// ControlContext returns a function suitable for net.Dialer.ControlContext
// that rejects connections to IPs in the blocked ranges. Go invokes
// the callback after DNS resolution but before the connect(2) syscall;
// returning a non-nil error aborts the dial.
//
// The returned closure is safe for concurrent use.
func ControlContext() func(ctx context.Context, network, address string, c syscall.RawConn) error {
	return func(_ context.Context, _, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			// Address is malformed; let the dial fail with the original
			// error rather than masking it.
			return nil
		}
		ip := net.ParseIP(host)
		if ip == nil {
			// Should not happen — Go resolves DNS before invoking
			// ControlContext — but be defensive.
			return fmt.Errorf("%w (unresolved host %q)", ErrBlockedIP, host)
		}
		return CheckIP(ip)
	}
}
