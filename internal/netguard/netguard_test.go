package netguard_test

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/hanshuebner/herold/internal/netguard"
)

func TestClassify_Loopback(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "127.0.0.42", "::1"} {
		ip := net.ParseIP(raw)
		if r, blocked := netguard.Classify(ip); !blocked || r != netguard.ReasonLoopback {
			t.Errorf("%s: got reason=%q blocked=%v, want loopback", raw, r, blocked)
		}
	}
}

func TestClassify_Private(t *testing.T) {
	cases := []struct {
		raw  string
		want netguard.Reason
	}{
		{"10.0.0.1", netguard.ReasonPrivate},
		{"10.255.255.255", netguard.ReasonPrivate},
		{"172.16.0.1", netguard.ReasonPrivate},
		{"172.31.255.254", netguard.ReasonPrivate},
		{"192.168.1.1", netguard.ReasonPrivate},
		{"fc00::1", netguard.ReasonULA},
		{"fdff::1", netguard.ReasonULA},
	}
	for _, tc := range cases {
		ip := net.ParseIP(tc.raw)
		r, blocked := netguard.Classify(ip)
		if !blocked {
			t.Errorf("%s: got blocked=false, want true", tc.raw)
		}
		if r != tc.want {
			t.Errorf("%s: got reason=%q, want %q", tc.raw, r, tc.want)
		}
	}
}

func TestClassify_LinkLocal(t *testing.T) {
	for _, raw := range []string{"169.254.169.254", "fe80::1", "fe80::cafe"} {
		ip := net.ParseIP(raw)
		r, blocked := netguard.Classify(ip)
		if !blocked {
			t.Errorf("%s: got blocked=false", raw)
		}
		if r != netguard.ReasonLinkLocal {
			t.Errorf("%s: got reason=%q, want link_local", raw, r)
		}
	}
}

func TestClassify_Multicast(t *testing.T) {
	for _, raw := range []string{"224.0.0.1", "239.255.255.255", "ff00::1"} {
		ip := net.ParseIP(raw)
		r, blocked := netguard.Classify(ip)
		if !blocked {
			t.Errorf("%s: got blocked=false", raw)
		}
		if r != netguard.ReasonMulticast && r != netguard.ReasonLinkLocal {
			t.Errorf("%s: got reason=%q, want multicast or link_local", raw, r)
		}
	}
}

func TestClassify_CGNAT(t *testing.T) {
	for _, raw := range []string{"100.64.0.1", "100.127.255.254"} {
		ip := net.ParseIP(raw)
		r, blocked := netguard.Classify(ip)
		if !blocked || r != netguard.ReasonCGNAT {
			t.Errorf("%s: got reason=%q blocked=%v, want cgnat", raw, r, blocked)
		}
	}
}

func TestClassify_Unspecified(t *testing.T) {
	for _, raw := range []string{"0.0.0.0", "::"} {
		ip := net.ParseIP(raw)
		r, blocked := netguard.Classify(ip)
		if !blocked || r != netguard.ReasonUnspecified {
			t.Errorf("%s: got reason=%q blocked=%v, want unspecified", raw, r, blocked)
		}
	}
}

func TestClassify_PublicAllowed(t *testing.T) {
	for _, raw := range []string{"8.8.8.8", "1.1.1.1", "203.0.113.42", "2606:4700::1"} {
		ip := net.ParseIP(raw)
		if _, blocked := netguard.Classify(ip); blocked {
			t.Errorf("%s: got blocked=true, want false", raw)
		}
	}
}

func TestCheckIP_WrapsErrBlockedIP(t *testing.T) {
	err := netguard.CheckIP(net.ParseIP("127.0.0.1"))
	if err == nil {
		t.Fatal("CheckIP(127.0.0.1): want non-nil error")
	}
	if !errors.Is(err, netguard.ErrBlockedIP) {
		t.Errorf("err=%v, want errors.Is ErrBlockedIP", err)
	}
}

func TestCheckHost_Literals(t *testing.T) {
	if err := netguard.CheckHost(context.Background(), nil, "127.0.0.1"); err == nil {
		t.Errorf("127.0.0.1 literal: got nil error, want blocked")
	}
	if err := netguard.CheckHost(context.Background(), nil, "8.8.8.8"); err != nil {
		t.Errorf("8.8.8.8 literal: got %v, want nil", err)
	}
	if err := netguard.CheckHost(context.Background(), nil, ""); err == nil {
		t.Errorf("empty host: got nil error, want blocked")
	}
}

func TestIsLocalhost(t *testing.T) {
	for _, raw := range []string{"localhost", "127.0.0.1", "::1", "127.42.0.1", "[::1]"} {
		if !netguard.IsLocalhost(raw) {
			t.Errorf("%s: IsLocalhost=false, want true", raw)
		}
	}
	for _, raw := range []string{"example.com", "8.8.8.8", "10.0.0.1", ""} {
		if netguard.IsLocalhost(raw) {
			t.Errorf("%s: IsLocalhost=true, want false", raw)
		}
	}
}
