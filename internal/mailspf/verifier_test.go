package mailspf_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/mailspf"
	"github.com/hanshuebner/herold/internal/testharness/fakedns"
)

type fakeResolver struct{ inner *fakedns.Resolver }

func (f fakeResolver) TXTLookup(ctx context.Context, name string) ([]string, error) {
	v, err := f.inner.LookupTXT(ctx, name)
	if err != nil {
		return nil, mailauth.ErrNoRecords
	}
	return v, nil
}

func (f fakeResolver) MXLookup(ctx context.Context, domain string) ([]*net.MX, error) {
	rrs, err := f.inner.LookupMX(ctx, domain)
	if err != nil {
		return nil, mailauth.ErrNoRecords
	}
	out := make([]*net.MX, len(rrs))
	for i, r := range rrs {
		out[i] = &net.MX{Host: r.Host, Pref: r.Preference}
	}
	return out, nil
}

func (f fakeResolver) IPLookup(ctx context.Context, host string) ([]net.IP, error) {
	var ips []net.IP
	if v4, err := f.inner.LookupA(ctx, host); err == nil {
		ips = append(ips, v4...)
	}
	if v6, err := f.inner.LookupAAAA(ctx, host); err == nil {
		ips = append(ips, v6...)
	}
	if len(ips) == 0 {
		return nil, mailauth.ErrNoRecords
	}
	return ips, nil
}

func newVerifier(t *testing.T, dns *fakedns.Resolver) *mailspf.Verifier {
	t.Helper()
	return mailspf.New(
		fakeResolver{inner: dns},
		clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
	)
}

func TestCheck_Mechanisms(t *testing.T) {
	type step func(*fakedns.Resolver)

	cases := []struct {
		name     string
		setup    step
		mailFrom string
		helo     string
		clientIP string
		want     mailauth.AuthStatus
	}{
		{
			name: "ip4 match",
			setup: func(r *fakedns.Resolver) {
				r.AddTXT("example.com", "v=spf1 ip4:192.0.2.0/24 -all")
			},
			mailFrom: "alice@example.com",
			clientIP: "192.0.2.10",
			want:     mailauth.AuthPass,
		},
		{
			name: "ip4 miss -> -all fails",
			setup: func(r *fakedns.Resolver) {
				r.AddTXT("example.com", "v=spf1 ip4:192.0.2.0/24 -all")
			},
			mailFrom: "alice@example.com",
			clientIP: "198.51.100.5",
			want:     mailauth.AuthFail,
		},
		{
			name: "ip6 match",
			setup: func(r *fakedns.Resolver) {
				r.AddTXT("example.com", "v=spf1 ip6:2001:db8::/32 -all")
			},
			mailFrom: "alice@example.com",
			clientIP: "2001:db8::1",
			want:     mailauth.AuthPass,
		},
		{
			name: "a mechanism matches",
			setup: func(r *fakedns.Resolver) {
				r.AddTXT("example.com", "v=spf1 a -all")
				r.AddA("example.com", net.ParseIP("192.0.2.1"))
			},
			mailFrom: "alice@example.com",
			clientIP: "192.0.2.1",
			want:     mailauth.AuthPass,
		},
		{
			name: "a mechanism miss -> -all",
			setup: func(r *fakedns.Resolver) {
				r.AddTXT("example.com", "v=spf1 a -all")
				r.AddA("example.com", net.ParseIP("192.0.2.1"))
			},
			mailFrom: "alice@example.com",
			clientIP: "192.0.2.2",
			want:     mailauth.AuthFail,
		},
		{
			name: "mx mechanism matches",
			setup: func(r *fakedns.Resolver) {
				r.AddTXT("example.com", "v=spf1 mx -all")
				r.AddMX("example.com", 10, "mx.example.com")
				r.AddA("mx.example.com", net.ParseIP("192.0.2.5"))
			},
			mailFrom: "alice@example.com",
			clientIP: "192.0.2.5",
			want:     mailauth.AuthPass,
		},
		{
			name: "include passes -> outer passes",
			setup: func(r *fakedns.Resolver) {
				r.AddTXT("example.com", "v=spf1 include:_spf.example.net -all")
				r.AddTXT("_spf.example.net", "v=spf1 ip4:203.0.113.0/24 -all")
			},
			mailFrom: "alice@example.com",
			clientIP: "203.0.113.4",
			want:     mailauth.AuthPass,
		},
		{
			name: "include fails -> outer falls through to -all",
			setup: func(r *fakedns.Resolver) {
				r.AddTXT("example.com", "v=spf1 include:_spf.example.net -all")
				r.AddTXT("_spf.example.net", "v=spf1 ip4:203.0.113.0/24 -all")
			},
			mailFrom: "alice@example.com",
			clientIP: "198.51.100.10",
			want:     mailauth.AuthFail,
		},
		{
			name: "redirect",
			setup: func(r *fakedns.Resolver) {
				r.AddTXT("example.com", "v=spf1 redirect=_spf.example.net")
				r.AddTXT("_spf.example.net", "v=spf1 ip4:203.0.113.0/24 -all")
			},
			mailFrom: "alice@example.com",
			clientIP: "203.0.113.4",
			want:     mailauth.AuthPass,
		},
		{
			name: "no record -> none",
			setup: func(r *fakedns.Resolver) {
				// Intentionally empty.
			},
			mailFrom: "alice@example.com",
			clientIP: "192.0.2.1",
			want:     mailauth.AuthNone,
		},
		{
			name: "softfail",
			setup: func(r *fakedns.Resolver) {
				r.AddTXT("example.com", "v=spf1 ip4:192.0.2.0/24 ~all")
			},
			mailFrom: "alice@example.com",
			clientIP: "198.51.100.1",
			want:     mailauth.AuthSoftFail,
		},
		{
			name: "neutral-qualifier",
			setup: func(r *fakedns.Resolver) {
				r.AddTXT("example.com", "v=spf1 ?all")
			},
			mailFrom: "alice@example.com",
			clientIP: "192.0.2.1",
			want:     mailauth.AuthNeutral,
		},
		{
			name: "null mail from falls back to HELO",
			setup: func(r *fakedns.Resolver) {
				r.AddTXT("mail.example.org", "v=spf1 ip4:192.0.2.1 -all")
			},
			mailFrom: "",
			helo:     "mail.example.org",
			clientIP: "192.0.2.1",
			want:     mailauth.AuthPass,
		},
		{
			name: "exists matches",
			setup: func(r *fakedns.Resolver) {
				r.AddTXT("example.com", "v=spf1 exists:%{d} -all")
				r.AddA("example.com", net.ParseIP("192.0.2.99"))
			},
			mailFrom: "alice@example.com",
			clientIP: "203.0.113.10",
			want:     mailauth.AuthPass,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dns := fakedns.New()
			tc.setup(dns)
			v := newVerifier(t, dns)
			res, err := v.Check(context.Background(), tc.mailFrom, tc.helo, tc.clientIP)
			if err != nil {
				t.Fatalf("check: %v", err)
			}
			if res.Status != tc.want {
				t.Fatalf("status = %v (%q) want %v", res.Status, res.Reason, tc.want)
			}
		})
	}
}

func TestCheck_LookupLimit(t *testing.T) {
	// Construct a record that forces >10 lookups: chain of includes.
	dns := fakedns.New()
	dns.AddTXT("0.example.com", "v=spf1 include:1.example.com -all")
	for i := 1; i <= 20; i++ {
		next := i + 1
		dns.AddTXT(labelN(i), "v=spf1 include:"+labelN(next)+" -all")
	}
	dns.AddTXT(labelN(21), "v=spf1 ip4:192.0.2.1 -all")

	v := newVerifier(t, dns)
	res, err := v.Check(context.Background(), "alice@0.example.com", "", "192.0.2.1")
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if res.Status != mailauth.AuthPermError {
		t.Fatalf("status = %v (%q) want permerror (lookup-limit exceeded)", res.Status, res.Reason)
	}
}

func labelN(i int) string { return itoa(i) + ".example.com" }

func itoa(i int) string {
	const digits = "0123456789"
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{digits[i%10]}, b...)
		i /= 10
	}
	return string(b)
}

func TestCheck_BadIP(t *testing.T) {
	dns := fakedns.New()
	v := newVerifier(t, dns)
	res, err := v.Check(context.Background(), "alice@example.com", "", "not-an-ip")
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if res.Status != mailauth.AuthPermError {
		t.Fatalf("status = %v want permerror", res.Status)
	}
}

func TestCheck_IncludeLoop(t *testing.T) {
	dns := fakedns.New()
	dns.AddTXT("a.example.com", "v=spf1 include:b.example.com -all")
	dns.AddTXT("b.example.com", "v=spf1 include:a.example.com -all")
	v := newVerifier(t, dns)
	res, err := v.Check(context.Background(), "alice@a.example.com", "", "192.0.2.1")
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if res.Status != mailauth.AuthPermError {
		t.Fatalf("status = %v (%q) want permerror", res.Status, res.Reason)
	}
}
