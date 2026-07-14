package push

import (
	"encoding/json"
	"net"
	"testing"

	"github.com/hanshuebner/herold/internal/netguard"
)

// This file covers the re #211 SSRF egress policy at the
// PushSubscription/set { create } boundary: buildWebPushCreateRow
// consults h.endpointGuard before a row is ever persisted. The guard
// itself (Classify / DialContext / DNS-rebinding / allowlist
// semantics) is unit-tested exhaustively in internal/netguard; these
// tests confirm the JMAP handler actually wires it in and surfaces the
// refusal as an invalidProperties set-error rather than persisting the
// row.

func newGuardedFixture(t *testing.T, ip net.IP, allowedHosts ...string) (*fixture, *netguard.Guard) {
	t.Helper()
	g := netguard.NewGuard(netguard.GuardOptions{RequireHTTPS: true, AllowedHosts: allowedHosts})
	g.SetResolverIPsForTest(ip)
	return newFixtureWithGuard(t, g), g
}

func createOneRequest(url string) setRequest {
	return setRequest{
		Create: map[string]json.RawMessage{
			"a": mustJSON(map[string]any{
				"deviceClientId": "d1",
				"url":            url,
			}),
		},
	}
}

func assertRejectedAtCreate(t *testing.T, f *fixture, url string) {
	t.Helper()
	resp, merr := f.invokeSet(f.ctx(), createOneRequest(url))
	if merr != nil {
		t.Fatalf("Execute returned method error instead of a set-error: %+v", merr)
	}
	if len(resp.Created) != 0 {
		t.Fatalf("endpoint %q was accepted and persisted: %+v", url, resp.Created)
	}
	se, ok := resp.NotCreated["a"]
	if !ok {
		t.Fatalf("endpoint %q: expected a notCreated entry, got %+v", url, resp)
	}
	if se.Type != "invalidProperties" {
		t.Fatalf("endpoint %q: got set-error type %q, want invalidProperties", url, se.Type)
	}
}

func assertAcceptedAtCreate(t *testing.T, f *fixture, url string) {
	t.Helper()
	resp, merr := f.invokeSet(f.ctx(), createOneRequest(url))
	if merr != nil {
		t.Fatalf("Execute returned method error: %+v", merr)
	}
	if len(resp.NotCreated) != 0 {
		t.Fatalf("endpoint %q was rejected: %+v", url, resp.NotCreated)
	}
	if len(resp.Created) != 1 {
		t.Fatalf("endpoint %q: expected 1 created row, got %+v", url, resp.Created)
	}
}

func TestPushSet_Create_MetadataEndpointRejected(t *testing.T) {
	f, _ := newGuardedFixture(t, net.ParseIP("169.254.169.254"))
	assertRejectedAtCreate(t, f, "https://attacker.example.test/hook")
}

func TestPushSet_Create_LoopbackEndpointRejected(t *testing.T) {
	f, _ := newGuardedFixture(t, net.ParseIP("127.0.0.1"))
	assertRejectedAtCreate(t, f, "https://attacker.example.test/hook")
}

func TestPushSet_Create_RFC1918EndpointRejected(t *testing.T) {
	f, _ := newGuardedFixture(t, net.ParseIP("10.1.2.3"))
	assertRejectedAtCreate(t, f, "https://attacker.example.test/hook")
}

func TestPushSet_Create_IPv6EquivalentsRejected(t *testing.T) {
	for _, tc := range []struct {
		name string
		ip   string
	}{
		{"loopback", "::1"},
		{"link-local", "fe80::1"},
		{"ula", "fd00::1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := newGuardedFixture(t, net.ParseIP(tc.ip))
			assertRejectedAtCreate(t, f, "https://attacker.example.test/hook")
		})
	}
}

func TestPushSet_Create_IPv4MappedIPv6Rejected(t *testing.T) {
	for _, tc := range []struct {
		name string
		ip   string
	}{
		{"mapped-loopback", "::ffff:127.0.0.1"},
		{"mapped-metadata", "::ffff:169.254.169.254"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := newGuardedFixture(t, net.ParseIP(tc.ip))
			assertRejectedAtCreate(t, f, "https://attacker.example.test/hook")
		})
	}
}

func TestPushSet_Create_AllowlistedPrivateEndpointAccepted(t *testing.T) {
	f, _ := newGuardedFixture(t, net.ParseIP("10.9.9.9"), "push.internal.example")
	assertAcceptedAtCreate(t, f, "https://push.internal.example/UP/deadbeef")
}

func TestPushSet_Create_NonAllowlistedHostSharingAllowlistedIPStillRejected(t *testing.T) {
	// The allowlist exempts a hostname, not an IP; an attacker whose
	// endpoint happens to resolve to the same private address as the
	// operator's allowlisted distributor must still be refused.
	f, _ := newGuardedFixture(t, net.ParseIP("10.9.9.9"), "push.internal.example")
	assertRejectedAtCreate(t, f, "https://attacker.example.test/hook")
}

func TestPushSet_Create_LegitimatePublicEndpointStillAccepted(t *testing.T) {
	// Guards against an over-broad policy: a normal public Web Push
	// endpoint (standing in for a real gateway host such as
	// updates.push.services.mozilla.com) must not be caught by the
	// SSRF guard.
	f, _ := newGuardedFixture(t, net.ParseIP("203.0.113.55"))
	assertAcceptedAtCreate(t, f, "https://push.gateway.example.test/wpush/abc123")
}

func TestPushSet_Create_HTTPRejectedByDefault(t *testing.T) {
	g := netguard.NewGuard(netguard.GuardOptions{RequireHTTPS: true})
	g.SetResolverIPsForTest(net.ParseIP("203.0.113.55"))
	f := newFixtureWithGuard(t, g)
	assertRejectedAtCreate(t, f, "http://push.gateway.example.test/wpush/abc123")
}

// -- UnifiedPush transport (re #236) -------------------------------
//
// UnifiedPush shares buildEndpointCreateRow (and therefore
// h.endpointGuard) with Web Push, so the SSRF policy applies
// identically: an attacker-controlled UnifiedPush distributor endpoint
// resolving to an internal address is refused at create time the same
// way a malicious Web Push gateway endpoint would be, and an operator-
// allowlisted self-hosted distributor on a private address is still
// accepted.

func createOneUnifiedPushRequest(url string) setRequest {
	return setRequest{
		Create: map[string]json.RawMessage{
			"a": mustJSON(pushCreateInput{
				DeviceClientID: "d1",
				Kind:           "unifiedpush",
				URL:            url,
			}),
		},
	}
}

func assertUnifiedPushRejectedAtCreate(t *testing.T, f *fixture, url string) {
	t.Helper()
	resp, merr := f.invokeSet(f.ctx(), createOneUnifiedPushRequest(url))
	if merr != nil {
		t.Fatalf("Execute returned method error instead of a set-error: %+v", merr)
	}
	if len(resp.Created) != 0 {
		t.Fatalf("endpoint %q was accepted and persisted: %+v", url, resp.Created)
	}
	se, ok := resp.NotCreated["a"]
	if !ok {
		t.Fatalf("endpoint %q: expected a notCreated entry, got %+v", url, resp)
	}
	if se.Type != "invalidProperties" {
		t.Fatalf("endpoint %q: got set-error type %q, want invalidProperties", url, se.Type)
	}
}

func assertUnifiedPushAcceptedAtCreate(t *testing.T, f *fixture, url string) {
	t.Helper()
	resp, merr := f.invokeSet(f.ctx(), createOneUnifiedPushRequest(url))
	if merr != nil {
		t.Fatalf("Execute returned method error: %+v", merr)
	}
	if len(resp.NotCreated) != 0 {
		t.Fatalf("endpoint %q was rejected: %+v", url, resp.NotCreated)
	}
	if len(resp.Created) != 1 {
		t.Fatalf("endpoint %q: expected 1 created row, got %+v", url, resp.Created)
	}
	created := resp.Created["a"]
	if created.Kind != "unifiedpush" {
		t.Fatalf("endpoint %q: Kind = %q, want unifiedpush", url, created.Kind)
	}
}

func TestPushSet_Create_UnifiedPush_MetadataEndpointRejected(t *testing.T) {
	f, _ := newGuardedFixture(t, net.ParseIP("169.254.169.254"))
	assertUnifiedPushRejectedAtCreate(t, f, "https://attacker.example.test/UP/hook")
}

func TestPushSet_Create_UnifiedPush_RFC1918EndpointRejected(t *testing.T) {
	f, _ := newGuardedFixture(t, net.ParseIP("10.1.2.3"))
	assertUnifiedPushRejectedAtCreate(t, f, "https://attacker.example.test/UP/hook")
}

func TestPushSet_Create_UnifiedPush_AllowlistedPrivateEndpointAccepted(t *testing.T) {
	// Standing in for a real UnifiedPush distributor self-hosted on the
	// operator's own network -- the primary reason
	// [server.push.network].allowed_hosts exists (re #236 references
	// section pointing at #211's allowlist).
	f, _ := newGuardedFixture(t, net.ParseIP("10.9.9.9"), "push.internal.example")
	assertUnifiedPushAcceptedAtCreate(t, f, "https://push.internal.example/UP/deadbeef")
}

func TestPushSet_Create_UnifiedPush_LegitimatePublicEndpointAccepted(t *testing.T) {
	f, _ := newGuardedFixture(t, net.ParseIP("203.0.113.55"))
	assertUnifiedPushAcceptedAtCreate(t, f, "https://distributor.example.test/UP/abc123")
}
