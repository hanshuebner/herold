package extimg

import (
	"context"
	"errors"
	"net/netip"
	"net/url"
	"strings"
	"testing"
)

// fakeResolver lets tests inject a sequence of resolver answers,
// simulating DNS rebinding (different answers on subsequent calls)
// and multi-A-record poisoning (a single call returning safe + unsafe
// addresses simultaneously).
type fakeResolver struct {
	answers map[string][][]netip.Addr
	calls   map[string]int
}

func (f *fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	idx := f.calls[host]
	f.calls[host]++
	answers := f.answers[host]
	if len(answers) == 0 {
		return nil, errors.New("no answers configured for " + host)
	}
	if idx >= len(answers) {
		// After running out, repeat the last answer.
		return answers[len(answers)-1], nil
	}
	return answers[idx], nil
}

// guardForTest builds a guard with the documented defaults and the
// supplied resolver. RequireHTTPS is left enabled so tests must
// explicitly construct https URLs.
func guardForTest(r ipResolver, allowPrivate bool) *SSRFGuard {
	g := NewSSRFGuard(Config{
		RequireHTTPS: true,
		AllowPrivate: allowPrivate,
	})
	if r != nil {
		g = g.withResolver(r)
	}
	return g
}

func TestValidateURL_Schemes(t *testing.T) {
	g := guardForTest(nil, false)
	cases := []struct {
		raw     string
		wantErr bool
	}{
		{"https://example.com/x.png", false},
		{"http://example.com/x.png", true},  // RequireHTTPS
		{"file:///etc/passwd", true},        // bad scheme
		{"gopher://example.com/", true},     // bad scheme
		{"https://user:pw@example.com/x", true}, // userinfo
		{"https://example.com:9999/x", true},    // bad port
		{"https://:/", true},                    // no host
	}
	for _, c := range cases {
		u, perr := url.Parse(c.raw)
		if perr != nil {
			if !c.wantErr {
				t.Fatalf("parse %q: %v", c.raw, perr)
			}
			continue
		}
		err := g.ValidateURL(u)
		if c.wantErr && err == nil {
			t.Errorf("%s: expected error, got nil", c.raw)
		}
		if !c.wantErr && err != nil {
			t.Errorf("%s: unexpected error %v", c.raw, err)
		}
	}
}

func TestDialContext_BlockedRanges(t *testing.T) {
	cases := []struct {
		name string
		host string
		ips  []netip.Addr
	}{
		{"loopback4", "loop.example.test", []netip.Addr{netip.MustParseAddr("127.0.0.1")}},
		{"rfc1918", "rfc1918.example.test", []netip.Addr{netip.MustParseAddr("10.1.2.3")}},
		{"linkLocal", "ll.example.test", []netip.Addr{netip.MustParseAddr("169.254.169.254")}}, // AWS metadata
		{"cgnat", "cg.example.test", []netip.Addr{netip.MustParseAddr("100.64.1.1")}},
		{"loopback6", "loop6.example.test", []netip.Addr{netip.MustParseAddr("::1")}},
		{"v4mapped", "v4m.example.test", []netip.Addr{netip.MustParseAddr("::ffff:127.0.0.1")}},
		{"ula", "ula.example.test", []netip.Addr{netip.MustParseAddr("fc00::1")}},
		{"multicast4", "m4.example.test", []netip.Addr{netip.MustParseAddr("224.0.0.1")}},
		{"multicast6", "m6.example.test", []netip.Addr{netip.MustParseAddr("ff02::1")}},
		{"testNet1", "tn1.example.test", []netip.Addr{netip.MustParseAddr("192.0.2.5")}},
		{"testNet3", "tn3.example.test", []netip.Addr{netip.MustParseAddr("203.0.113.5")}},
		{"benchmark", "bm.example.test", []netip.Addr{netip.MustParseAddr("198.18.0.1")}},
		{"reservedFuture", "rf.example.test", []netip.Addr{netip.MustParseAddr("240.0.0.1")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeResolver{answers: map[string][][]netip.Addr{tc.host: {tc.ips}}}
			g := guardForTest(r, false)
			_, err := g.DialContext(context.Background(), "tcp", tc.host+":443")
			if err == nil {
				t.Fatalf("%s: expected SSRF refusal, got nil", tc.name)
			}
			var blocked *ErrSSRFBlocked
			if !errors.As(err, &blocked) {
				t.Fatalf("%s: error is not ErrSSRFBlocked: %T %v", tc.name, err, err)
			}
			if blocked.Reason != ssrfDeniedIP {
				t.Fatalf("%s: reason=%q, want denied_ip", tc.name, blocked.Reason)
			}
		})
	}
}

// TestDialContext_DNSRebindingDefeat verifies the resolve-once,
// dial-IP-literal pattern. The custom resolver returns a public IP on
// the first call and a private IP on subsequent calls; the guard must
// pass on the first answer and never re-resolve. We can't easily
// observe "didn't re-resolve" because the dial fails (203.0.113.5 is
// in the deny list) — instead we assert the resolver call count.
func TestDialContext_DNSRebindingResolveCount(t *testing.T) {
	// Public-looking IP that ALSO happens to be in the deny list
	// (TEST-NET-3) so the dial fails locally but for the right reason.
	publicLooking := netip.MustParseAddr("203.0.113.5")
	private := netip.MustParseAddr("10.1.2.3")
	r := &fakeResolver{answers: map[string][][]netip.Addr{
		"rebind.example.test": {{publicLooking}, {private}, {private}},
	}}
	g := guardForTest(r, false)
	_, _ = g.DialContext(context.Background(), "tcp", "rebind.example.test:443")
	// We expect exactly one resolve, regardless of whether the dial
	// succeeded or failed: the guard must not double-resolve.
	if r.calls["rebind.example.test"] != 1 {
		t.Fatalf("resolver called %d times, want 1 (DNS rebinding window)",
			r.calls["rebind.example.test"])
	}
}

// TestDialContext_MultiAnswer_Poisoned checks that any single deny-
// match in a multi-A response aborts the whole fetch. An attacker who
// answers DNS could return [public, 127.0.0.1] hoping we'd race the
// safe one — we don't (REQ-EXTIMG-35).
func TestDialContext_MultiAnswer_Poisoned(t *testing.T) {
	publicLooking := netip.MustParseAddr("198.51.100.5") // TEST-NET-2 (denied)
	loopback := netip.MustParseAddr("127.0.0.1")
	// Mix the supposedly-safe answer with a poisoned one.
	r := &fakeResolver{answers: map[string][][]netip.Addr{
		"poison.example.test": {{publicLooking, loopback}},
	}}
	g := guardForTest(r, false)
	_, err := g.DialContext(context.Background(), "tcp", "poison.example.test:443")
	if err == nil {
		t.Fatal("expected refusal of poisoned multi-A response")
	}
	var blocked *ErrSSRFBlocked
	if !errors.As(err, &blocked) {
		t.Fatalf("error not ErrSSRFBlocked: %v", err)
	}
}

// TestAllowPrivate verifies the operator override loosens the RFC
// 1918 / loopback fence but keeps the always-blocked ranges
// (multicast, IPv4-mapped IPv6, TEST-NETs).
func TestAllowPrivate(t *testing.T) {
	r := &fakeResolver{answers: map[string][][]netip.Addr{
		"private.example.test":   {{netip.MustParseAddr("10.1.2.3")}},
		"multicast.example.test": {{netip.MustParseAddr("224.0.0.1")}},
		"v4mapped.example.test":  {{netip.MustParseAddr("::ffff:10.0.0.1")}},
	}}
	g := guardForTest(r, true)
	// 10.1.2.3 is now allowed; the dial will fail at the connect
	// layer (no listener), but the guard must not refuse it.
	_, err := g.DialContext(context.Background(), "tcp", "private.example.test:443")
	if err != nil {
		// Connect failure is acceptable; SSRF refusal is not.
		var blocked *ErrSSRFBlocked
		if errors.As(err, &blocked) {
			t.Fatalf("AllowPrivate should accept 10.0.0.0/8: %v", err)
		}
	}
	// Multicast still refused.
	_, err = g.DialContext(context.Background(), "tcp", "multicast.example.test:443")
	if err == nil {
		t.Fatal("multicast must remain blocked even with AllowPrivate")
	}
	if !strings.Contains(err.Error(), "ssrf") {
		t.Fatalf("multicast: not an SSRF refusal: %v", err)
	}
	// v4-mapped IPv6 still refused (the tunneling-through-v6 attack).
	_, err = g.DialContext(context.Background(), "tcp", "v4mapped.example.test:443")
	if err == nil {
		t.Fatal("IPv4-mapped IPv6 must remain blocked even with AllowPrivate")
	}
}

func TestDialContext_LiteralIPs(t *testing.T) {
	g := guardForTest(nil, false)
	cases := []struct {
		addr    string
		wantErr bool
	}{
		{"127.0.0.1:443", true},
		{"[::1]:443", true},
		{"[::ffff:127.0.0.1]:443", true},
		{"169.254.169.254:443", true},
		{"203.0.113.5:443", true},
	}
	for _, c := range cases {
		_, err := g.DialContext(context.Background(), "tcp", c.addr)
		if c.wantErr && err == nil {
			t.Errorf("%s: expected refusal", c.addr)
		}
	}
}
