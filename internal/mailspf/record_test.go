package mailspf_test

import (
	"errors"
	"testing"

	"github.com/hanshuebner/herold/internal/mailspf"
)

func TestParseRecord_Valid(t *testing.T) {
	cases := []string{
		"v=spf1 -all",
		"v=spf1 ip4:192.0.2.0/24 -all",
		"v=spf1 ip6:2001:db8::/32 -all",
		"v=spf1 a mx -all",
		"v=spf1 a:example.com/24 mx:mail.example.com -all",
		"v=spf1 a/24//64 mx -all",
		"v=spf1 include:_spf.example.net ~all",
		"v=spf1 exists:%{d} -all",
		"v=spf1 redirect=_spf.example.net",
		"v=spf1 +all",
		"v=spf1 ?all",
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			if _, err := mailspf.ParseRecord(c); err != nil {
				t.Fatalf("parse %q: %v", c, err)
			}
		})
	}
}

func TestParseRecord_Invalid(t *testing.T) {
	cases := []string{
		"",
		"garbage",
		"v=spf2 -all",
		"v=spf1 ip4:",
		"v=spf1 ip4:not-an-ip -all",
		"v=spf1 ip6:192.0.2.1 -all",
		"v=spf1 include:",
		"v=spf1 a/33",
		"v=spf1 a//129",
		"v=spf1 foo -all",
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			_, err := mailspf.ParseRecord(c)
			if err == nil {
				t.Fatalf("parse %q: want error", c)
			}
			if !errors.Is(err, mailspf.ErrMalformedRecord) {
				t.Fatalf("parse %q: err=%v want ErrMalformedRecord", c, err)
			}
		})
	}
}
