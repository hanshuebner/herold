package maildmarc_test

import (
	"testing"

	"github.com/hanshuebner/herold/internal/maildmarc"
)

func TestOrganizationalDomain(t *testing.T) {
	cases := map[string]string{
		"":                          "",
		"com":                       "com",
		"example.com":               "example.com",
		"mail.example.com":          "example.com",
		"a.b.c.example.com":         "example.com",
		"EXAMPLE.COM":               "example.com",
		"example.com.":              "example.com",
		"bbc.co.uk":                 "bbc.co.uk",
		"news.bbc.co.uk":            "bbc.co.uk",
		"mail.bbc.co.uk":            "bbc.co.uk",
		"example.com.au":            "example.com.au",
		"mail.example.com.au":       "example.com.au",
		"foo.bar.baz.example.co.nz": "example.co.nz",
	}
	for in, want := range cases {
		if got := maildmarc.OrganizationalDomain(in); got != want {
			t.Errorf("OrganizationalDomain(%q) = %q want %q", in, got, want)
		}
	}
}
