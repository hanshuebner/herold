package webpush

import (
	"net/http"
	"strconv"
	"sync"
	"testing"

	"github.com/hanshuebner/herold/internal/netguard"
)

// This file proves the re #211 SSRF egress policy behaves correctly
// when wired into the dispatcher exactly as admin/server.go wires it
// in production: HTTPDoer is a real *http.Client whose Transport's
// DialContext is a netguard.Guard, not the interface-level stub the
// rest of dispatcher_test.go uses. Both fixtures POST to
// f.gateway.URL() -- an httptest.Server, which net/http/httptest binds
// to the loopback address 127.0.0.1 (or ::1) on a kernel-assigned
// port -- so the two cases below are:
//
//   - default policy (no allowlist): loopback is refused at DIAL time,
//     so the dispatcher never reaches the gateway -- confirms the
//     guard actually blocks delivery, not just registration.
//   - loopback explicitly allowlisted (an operator declaring "this
//     specific internal endpoint is my self-hosted distributor" per
//     [server.push.network].allowed_hosts): delivery reaches the
//     gateway exactly as it would with no guard at all -- confirms the
//     guard, correctly configured, does not silently kill push.
//
// lazyPortGuardTransport builds its netguard.Guard on the first
// RoundTrip, allowlisting whichever port the request targets. The
// dispatcher fixture's httptest.Server port is only known once the
// fixture is constructed, so the guard cannot be built any earlier
// without reaching into the fixture's unexported gateway field; this
// keeps the guard's actual security-relevant behaviour (host
// allowlist, private-range block) identical to production while
// working around that test-ordering constraint.
type lazyPortGuardTransport struct {
	allowedHosts []string

	mu    sync.Mutex
	inner http.RoundTripper
}

func (t *lazyPortGuardTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	if t.inner == nil {
		port, _ := strconv.Atoi(req.URL.Port())
		g := netguard.NewGuard(netguard.GuardOptions{
			RequireHTTPS: false, // httptest.Server is plain HTTP
			AllowedHosts: t.allowedHosts,
			AllowedPorts: []int{port},
		})
		t.inner = &http.Transport{DialContext: g.DialContext}
	}
	inner := t.inner
	t.mu.Unlock()
	return inner.RoundTrip(req)
}

func guardedHTTPClient(allowedHosts ...string) *http.Client {
	return &http.Client{Transport: &lazyPortGuardTransport{allowedHosts: allowedHosts}}
}

func TestDispatcher_DefaultPolicyRefusesLoopbackTargetAtDialTime(t *testing.T) {
	t.Parallel()
	f := newDispatcherFixture(t, http.StatusCreated, func(o *Options) {
		o.HTTPDoer = guardedHTTPClient() // no allowlist: loopback is blocked
	})
	f.triggerEmailChange(t)
	if calls := f.gateway.Calls(); len(calls) != 0 {
		t.Fatalf("gateway received %d call(s); the dial-time guard should have refused the connection before any request reached it: %+v",
			len(calls), calls)
	}
}

func TestDispatcher_AllowlistedLoopbackTargetStillDelivers(t *testing.T) {
	t.Parallel()
	f := newDispatcherFixture(t, http.StatusCreated, func(o *Options) {
		o.HTTPDoer = guardedHTTPClient("127.0.0.1", "::1")
	})
	f.triggerEmailChange(t)
	calls := f.gateway.Calls()
	if len(calls) == 0 {
		t.Fatalf("expected at least one POST to reach the gateway with the endpoint allowlisted")
	}
	c := calls[len(calls)-1]
	if c.Encoding != "aes128gcm" {
		t.Fatalf("Content-Encoding=%q want aes128gcm -- delivery through the guarded client must be byte-identical to the unguarded path", c.Encoding)
	}
}
