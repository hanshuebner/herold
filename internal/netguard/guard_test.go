package netguard_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanshuebner/herold/internal/netguard"
)

// -- ValidateURL --------------------------------------------------------

func TestGuard_ValidateURL_RejectsHTTPWhenHTTPSRequired(t *testing.T) {
	g := netguard.NewGuard(netguard.GuardOptions{RequireHTTPS: true})
	if _, err := g.ValidateURL("http://push.example.test/x"); err == nil {
		t.Fatal("http:// endpoint accepted despite RequireHTTPS")
	}
	if _, err := g.ValidateURL("https://push.example.test/x"); err != nil {
		t.Fatalf("https:// endpoint rejected: %v", err)
	}
}

func TestGuard_ValidateURL_AllowsHTTPWhenPermitted(t *testing.T) {
	g := netguard.NewGuard(netguard.GuardOptions{RequireHTTPS: false})
	if _, err := g.ValidateURL("http://push.example.test/x"); err != nil {
		t.Fatalf("http:// endpoint rejected despite RequireHTTPS=false: %v", err)
	}
}

func TestGuard_ValidateURL_RejectsUnexpectedPort(t *testing.T) {
	g := netguard.NewGuard(netguard.GuardOptions{RequireHTTPS: true})
	if _, err := g.ValidateURL("https://push.example.test:9999/x"); err == nil {
		t.Fatal("nonstandard port accepted with no allowlist entry")
	}
}

func TestGuard_ValidateURL_AllowsConfiguredPort(t *testing.T) {
	g := netguard.NewGuard(netguard.GuardOptions{RequireHTTPS: true, AllowedPorts: []int{9999}})
	if _, err := g.ValidateURL("https://push.example.test:9999/x"); err != nil {
		t.Fatalf("operator-allowlisted port rejected: %v", err)
	}
}

func TestGuard_ValidateURL_RejectsUserinfo(t *testing.T) {
	g := netguard.NewGuard(netguard.GuardOptions{})
	if _, err := g.ValidateURL("https://user:pass@push.example.test/x"); err == nil {
		t.Fatal("endpoint with embedded userinfo accepted")
	}
}

// -- CheckHost / DialContext: the required deny set ---------------------
//
// Each case below is asserted against BOTH CheckHost (the
// registration-time fail-fast check) and DialContext (the dial-time
// check that is the actual security boundary), because a guard that
// only rejects at registration and not at dial time has not closed
// the SSRF hole -- a row written before a policy tightened, or a
// future code path that skips the registration check, would still
// reach the network.

func mustParseIP(t *testing.T, s string) net.IP {
	t.Helper()
	ip := net.ParseIP(s)
	if ip == nil {
		t.Fatalf("net.ParseIP(%q) returned nil", s)
	}
	return ip
}

func assertBothLayersBlock(t *testing.T, name string, ip net.IP) {
	t.Helper()
	t.Run(name+"/CheckHost", func(t *testing.T) {
		g := netguard.NewGuard(netguard.GuardOptions{})
		g.SetResolverIPsForTest(ip)
		if err := g.CheckHost(context.Background(), "attacker.example.test"); err == nil {
			t.Fatalf("CheckHost: %s (%s) was NOT refused", name, ip)
		} else if !errors.Is(err, netguard.ErrBlockedIP) {
			t.Fatalf("CheckHost: %s (%s): got %v, want errors.Is ErrBlockedIP", name, ip, err)
		}
	})
	t.Run(name+"/DialContext", func(t *testing.T) {
		g := netguard.NewGuard(netguard.GuardOptions{})
		g.SetResolverIPsForTest(ip)
		dialed := false
		g.SetDialFuncForTest(func(ctx context.Context, network, address string) (net.Conn, error) {
			dialed = true
			return nil, errors.New("should never be called")
		})
		_, err := g.DialContext(context.Background(), "tcp", "attacker.example.test:443")
		if err == nil {
			t.Fatalf("DialContext: %s (%s) was NOT refused", name, ip)
		}
		if !errors.Is(err, netguard.ErrBlockedIP) {
			t.Fatalf("DialContext: %s (%s): got %v, want errors.Is ErrBlockedIP", name, ip, err)
		}
		if dialed {
			t.Fatalf("DialContext: %s (%s): underlying dial was attempted despite being blocked -- the guard let a connect happen", name, ip)
		}
	})
}

func TestGuard_MetadataIPRefused(t *testing.T) {
	assertBothLayersBlock(t, "cloud-metadata", mustParseIP(t, "169.254.169.254"))
}

func TestGuard_LoopbackRefused(t *testing.T) {
	assertBothLayersBlock(t, "loopback-v4", mustParseIP(t, "127.0.0.1"))
	assertBothLayersBlock(t, "loopback-v6", mustParseIP(t, "::1"))
}

func TestGuard_RFC1918Refused(t *testing.T) {
	assertBothLayersBlock(t, "10-8", mustParseIP(t, "10.1.2.3"))
	assertBothLayersBlock(t, "172-16-12", mustParseIP(t, "172.20.0.5"))
	assertBothLayersBlock(t, "192-168-16", mustParseIP(t, "192.168.1.1"))
}

func TestGuard_IPv6EquivalentsRefused(t *testing.T) {
	assertBothLayersBlock(t, "ipv6-link-local", mustParseIP(t, "fe80::1"))
	assertBothLayersBlock(t, "ipv6-ula", mustParseIP(t, "fd00::1"))
}

func TestGuard_IPv4MappedIPv6Refused(t *testing.T) {
	assertBothLayersBlock(t, "mapped-loopback", mustParseIP(t, "::ffff:127.0.0.1"))
	assertBothLayersBlock(t, "mapped-metadata", mustParseIP(t, "::ffff:169.254.169.254"))
	assertBothLayersBlock(t, "mapped-rfc1918", mustParseIP(t, "::ffff:10.0.0.1"))
}

func TestGuard_CGNATRefused(t *testing.T) {
	assertBothLayersBlock(t, "cgnat", mustParseIP(t, "100.64.0.1"))
}

// -- DNS rebinding: registration-time and dial-time see different
// answers; only the dial-time re-resolution can catch the switch. ----

func TestGuard_DNSRebinding_RefusedAtDialTimeEvenIfRegistrationSawPublicIP(t *testing.T) {
	g := netguard.NewGuard(netguard.GuardOptions{})

	// "Registration time": DNS answers with a clean public address, so
	// the fail-fast check passes -- this is the row an operator would
	// see accepted.
	g.SetResolverIPsForTest(mustParseIP(t, "203.0.113.7"))
	if err := g.CheckHost(context.Background(), "rebind.example.test"); err != nil {
		t.Fatalf("CheckHost at registration time unexpectedly blocked: %v", err)
	}

	// "Delivery time": the attacker has since repointed the record at
	// an internal address. DialContext performs its own resolution --
	// it does not trust or reuse the registration-time answer -- so it
	// must independently refuse this dial.
	g.SetResolverIPsForTest(mustParseIP(t, "169.254.169.254"))
	dialed := false
	g.SetDialFuncForTest(func(ctx context.Context, network, address string) (net.Conn, error) {
		dialed = true
		return nil, errors.New("should never be called")
	})
	_, err := g.DialContext(context.Background(), "tcp", "rebind.example.test:443")
	if err == nil {
		t.Fatal("DialContext: rebound endpoint was NOT refused -- the dial-time recheck did not catch the changed DNS answer")
	}
	if !errors.Is(err, netguard.ErrBlockedIP) {
		t.Fatalf("got %v, want errors.Is ErrBlockedIP", err)
	}
	if dialed {
		t.Fatal("underlying dial was attempted against the rebound internal address")
	}
}

// -- Allowlist: the operator's escape hatch for a self-hosted
// distributor on their own network. -------------------------------------

func TestGuard_AllowlistedHostPermitted(t *testing.T) {
	g := netguard.NewGuard(netguard.GuardOptions{AllowedHosts: []string{"push.internal.example"}})
	g.SetResolverIPsForTest(mustParseIP(t, "10.9.9.9")) // private, would normally be blocked

	if err := g.CheckHost(context.Background(), "push.internal.example"); err != nil {
		t.Fatalf("CheckHost: allowlisted host was refused: %v", err)
	}

	var dialedAddr string
	g.SetDialFuncForTest(func(ctx context.Context, network, address string) (net.Conn, error) {
		dialedAddr = address
		return nil, nil // the test only cares that the guard let the dial proceed
	})
	if _, err := g.DialContext(context.Background(), "tcp", "push.internal.example:443"); err != nil {
		t.Fatalf("DialContext: allowlisted host was refused: %v", err)
	}
	if dialedAddr != "10.9.9.9:443" {
		t.Fatalf("dialed %q, want the resolved literal 10.9.9.9:443", dialedAddr)
	}
}

func TestGuard_AllowlistDoesNotExemptOtherHosts(t *testing.T) {
	// An attacker's endpoint, sharing the SAME resolved private IP as
	// the operator's allowlisted distributor, must still be refused --
	// the allowlist is matched on hostname, not on IP, precisely so an
	// attacker cannot borrow the exemption by resolving to that address.
	g := netguard.NewGuard(netguard.GuardOptions{AllowedHosts: []string{"push.internal.example"}})
	g.SetResolverIPsForTest(mustParseIP(t, "10.9.9.9"))
	if err := g.CheckHost(context.Background(), "attacker.example.test"); err == nil {
		t.Fatal("non-allowlisted host resolving to the allowlisted target's IP was NOT refused")
	}
}

// -- Legitimate public endpoints must not regress. -----------------------

func TestGuard_LegitimatePublicEndpointDeliversEndToEnd(t *testing.T) {
	// Stand-in "real external push gateway": an httptest.Server, which
	// only ever binds to loopback. To exercise the guard's classify
	// step against a genuine public-range address (proving the policy
	// does not over-block) while still running fully offline, the fake
	// resolver answers with a real public IP (used ONLY for the
	// classification decision) and SetDialFuncForTest redirects the
	// resulting validated dial to the local httptest listener so the
	// test can assert a complete HTTP round trip.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(201)
	}))
	defer srv.Close()
	listenerAddr := srv.Listener.Addr().String()

	g := netguard.NewGuard(netguard.GuardOptions{RequireHTTPS: false})
	g.SetResolverIPsForTest(mustParseIP(t, "203.0.113.55")) // TEST-NET-3, stands in for a real public gateway IP
	g.SetDialFuncForTest(func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "203.0.113.55:80" {
			t.Fatalf("guard dialed %q, want the classified-safe literal 203.0.113.55:80", address)
		}
		var d net.Dialer
		return d.DialContext(ctx, network, listenerAddr)
	})

	client := &http.Client{Transport: &http.Transport{DialContext: g.DialContext}}
	resp, err := client.Get("http://push.gateway.example.test/deliver")
	if err != nil {
		t.Fatalf("legitimate public endpoint delivery failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("got status %d, want 201", resp.StatusCode)
	}
}
