// Package netguard provides shared SSRF and URL-validation helpers that
// outbound HTTP clients install on net.Dialer.ControlContext to refuse
// dialing private, loopback, link-local, multicast, or otherwise
// unsafe IP ranges.
//
// The motivating use cases are the inbound HTML image proxy
// (internal/protoimg) and the LLM categoriser (internal/categorise);
// both accept a target URL from store-side configuration and must not
// be turned into an SSRF reflector against the operator's internal
// network.
//
// The helpers here are deliberately tiny and dependency-free: they
// take a string IP / *net.IPAddr and return a sentinel error from the
// IsBlocked* family. Callers compose them into a Dialer.ControlContext
// or a redirect predicate.
package netguard
