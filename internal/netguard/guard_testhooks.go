package netguard

// Test-only injection points for internal/netguard's own tests and for
// the cross-package callers (internal/protojmap/push,
// internal/webpush) that need to script DNS answers and dial outcomes
// deterministically instead of touching the real network. There is no
// way to construct a fake hostResolver from outside the package
// without these constructors, so this file is effectively the
// package's test-support API.

import (
	"context"
	"net"
)

// fakeResolver is a scriptable hostResolver: it always answers with
// the configured IPs (or error), ignoring the requested host. A test
// can swap the IPs between two Guard calls to simulate a DNS answer
// that changes between registration time and delivery time (the
// rebinding scenario DialContext's re-resolution exists to catch).
type fakeResolver struct {
	ips []net.IP
	err error
}

func (f *fakeResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([]net.IPAddr, 0, len(f.ips))
	for _, ip := range f.ips {
		out = append(out, net.IPAddr{IP: ip})
	}
	return out, nil
}

// SetResolverIPsForTest points g at a fake resolver that always
// answers every lookup with ips. Exported (capitalised, "ForTest"
// suffix) so cross-package tests can call it; production code never
// does.
func (g *Guard) SetResolverIPsForTest(ips ...net.IP) {
	g.resolver = &fakeResolver{ips: ips}
}

// SetResolverErrForTest points g at a fake resolver that always fails
// lookups with err.
func (g *Guard) SetResolverErrForTest(err error) {
	g.resolver = &fakeResolver{err: err}
}

// SetDialFuncForTest overrides the low-level dial step DialContext
// performs once an address has been validated. Lets a test prove the
// full classify-then-dial pipeline reaches a real listener (e.g. an
// httptest.Server) for a legitimate, non-blocked target, without
// requiring outbound network access: the fake resolver answers with a
// realistic public IP so the classification step runs for real, and
// this hook redirects only the final TCP connect to the local
// listener.
func (g *Guard) SetDialFuncForTest(fn func(ctx context.Context, network, address string) (net.Conn, error)) {
	g.dial = fn
}
