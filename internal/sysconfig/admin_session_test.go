// Tests for the admin-listener session-lifetime knobs (REQ-AUTH-72,
// issue #12 slice 1):
//
//   [server.ui]
//   session.admin_idle_ttl     — default 1h, ceiling 12h
//   session.admin_absolute_ttl — default 8h, ceiling 12h
//
// Validate also rejects an idle TTL greater than the absolute TTL, since
// that combination would mean the idle gate never trips before the
// absolute deadline arrives.

package sysconfig

import (
	"strings"
	"testing"
	"time"
)

const adminSessionTTLBare = `
[server]
hostname = "mail.example.com"
data_dir = "/var/lib/herold"

[server.admin_tls]
source = "file"
cert_file = "/a"
key_file = "/b"

[[listener]]
name = "l"
address = ":25"
protocol = "smtp"
tls = "starttls"
`

func TestParse_AdminSessionTTLDefaults(t *testing.T) {
	cfg, err := Parse([]byte(adminSessionTTLBare))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got, want := cfg.Server.UI.AdminIdleTTL.AsDuration(), 1*time.Hour; got != want {
		t.Errorf("default admin_idle_ttl: got %s, want %s", got, want)
	}
	if got, want := cfg.Server.UI.AdminAbsoluteTTL.AsDuration(), 8*time.Hour; got != want {
		t.Errorf("default admin_absolute_ttl: got %s, want %s", got, want)
	}
}

func TestParse_AdminSessionTTLExplicit(t *testing.T) {
	const explicit = adminSessionTTLBare + `
[server.ui]
admin_idle_ttl     = "30m"
admin_absolute_ttl = "4h"
`
	cfg, err := Parse([]byte(explicit))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got, want := cfg.Server.UI.AdminIdleTTL.AsDuration(), 30*time.Minute; got != want {
		t.Errorf("admin_idle_ttl: got %s, want %s", got, want)
	}
	if got, want := cfg.Server.UI.AdminAbsoluteTTL.AsDuration(), 4*time.Hour; got != want {
		t.Errorf("admin_absolute_ttl: got %s, want %s", got, want)
	}
}

func TestValidate_RejectsAdminIdleTTLAboveCeiling(t *testing.T) {
	const bad = adminSessionTTLBare + `
[server.ui]
admin_idle_ttl = "13h"
`
	_, err := Parse([]byte(bad))
	if err == nil {
		t.Fatalf("expected error for admin_idle_ttl > 12h")
	}
	if !strings.Contains(err.Error(), "admin_idle_ttl") {
		t.Errorf("error %q should mention admin_idle_ttl", err.Error())
	}
	if !strings.Contains(err.Error(), "12h") {
		t.Errorf("error %q should mention the 12h ceiling", err.Error())
	}
}

func TestValidate_RejectsAdminAbsoluteTTLAboveCeiling(t *testing.T) {
	const bad = adminSessionTTLBare + `
[server.ui]
admin_absolute_ttl = "24h"
`
	_, err := Parse([]byte(bad))
	if err == nil {
		t.Fatalf("expected error for admin_absolute_ttl > 12h")
	}
	if !strings.Contains(err.Error(), "admin_absolute_ttl") {
		t.Errorf("error %q should mention admin_absolute_ttl", err.Error())
	}
	if !strings.Contains(err.Error(), "12h") {
		t.Errorf("error %q should mention the 12h ceiling", err.Error())
	}
}

func TestValidate_RejectsAdminIdleAboveAbsolute(t *testing.T) {
	const bad = adminSessionTTLBare + `
[server.ui]
admin_idle_ttl     = "2h"
admin_absolute_ttl = "1h"
`
	_, err := Parse([]byte(bad))
	if err == nil {
		t.Fatalf("expected error for admin_idle_ttl > admin_absolute_ttl")
	}
	if !strings.Contains(err.Error(), "admin_idle_ttl") || !strings.Contains(err.Error(), "admin_absolute_ttl") {
		t.Errorf("error %q should mention both knobs", err.Error())
	}
}

func TestValidate_AcceptsAdminTTLsAtTheCeiling(t *testing.T) {
	const okAtCeiling = adminSessionTTLBare + `
[server.ui]
admin_idle_ttl     = "12h"
admin_absolute_ttl = "12h"
`
	if _, err := Parse([]byte(okAtCeiling)); err != nil {
		t.Fatalf("Parse: expected no error at 12h ceiling, got %v", err)
	}
}
