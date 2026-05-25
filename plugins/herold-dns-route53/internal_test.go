package main

import "testing"

// TestComposeRecordName locks in the FQDN-composition behaviour that
// keeps ACME's dns-01 challenge working: the challenger passes a
// relative name ("_acme-challenge") and the zone (= certificate
// domain); the plugin has to compose the full DNS name before
// handing it to Route53.
func TestComposeRecordName(t *testing.T) {
	cases := []struct {
		name string
		zone string
		want string
	}{
		// The ACME failure mode this fix targets: relative challenge
		// name + certificate domain as zone.
		{"_acme-challenge", "mx.netzhansa.com", "_acme-challenge.mx.netzhansa.com"},
		// Already-FQDN name within the zone: pass through unchanged.
		{"_acme-challenge.mx.netzhansa.com", "mx.netzhansa.com", "_acme-challenge.mx.netzhansa.com"},
		// Trailing dots on either input are normalised away.
		{"_acme-challenge.mx.netzhansa.com.", "mx.netzhansa.com.", "_acme-challenge.mx.netzhansa.com"},
		// Apex sentinels.
		{"@", "netzhansa.com", "netzhansa.com"},
		{"", "netzhansa.com", "netzhansa.com"},
		// No zone: name passes through untouched.
		{"sub.example.com", "", "sub.example.com"},
		// Name == zone (operator-style apex form).
		{"netzhansa.com", "netzhansa.com", "netzhansa.com"},
		// Case-insensitive suffix match so a mixed-case caller doesn't
		// trigger an accidental double-append.
		{"_ACME-Challenge.MX.netzhansa.com", "mx.netzhansa.com", "_ACME-Challenge.MX.netzhansa.com"},
	}
	for _, tc := range cases {
		got := composeRecordName(tc.name, tc.zone)
		if got != tc.want {
			t.Errorf("composeRecordName(%q, %q) = %q, want %q",
				tc.name, tc.zone, got, tc.want)
		}
	}
}
