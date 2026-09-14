// Tests for the OAuth2 native-client grant's token-lifetime knobs
// (REQ-AND-AUTH-02, issue #358):
//
//   [server.auth]
//   oauth2_access_token_ttl  — default 1h, floor 1m
//   oauth2_refresh_token_ttl — default 720h (30 days), floor 1m
//
// Both knobs were compile-time constants in internal/directory until this
// section made them operator-settable so a deployment can shorten access-
// token lifetime, and a dev/test instance can make expiry happen quickly
// enough to exercise a client's refresh path (scripts/dev-instance.sh).

package sysconfig

import (
	"strings"
	"testing"
	"time"
)

const oauth2AuthBare = `
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

func TestParse_OAuth2TokenTTLDefaults(t *testing.T) {
	cfg, err := Parse([]byte(oauth2AuthBare))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got, want := cfg.Server.Auth.OAuth2AccessTokenTTL.AsDuration(), time.Hour; got != want {
		t.Errorf("default oauth2_access_token_ttl: got %s, want %s", got, want)
	}
	if got, want := cfg.Server.Auth.OAuth2RefreshTokenTTL.AsDuration(), 720*time.Hour; got != want {
		t.Errorf("default oauth2_refresh_token_ttl: got %s, want %s", got, want)
	}
}

func TestParse_OAuth2TokenTTLExplicit(t *testing.T) {
	const explicit = oauth2AuthBare + `
[server.auth]
oauth2_access_token_ttl  = "2m"
oauth2_refresh_token_ttl = "48h"
`
	cfg, err := Parse([]byte(explicit))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got, want := cfg.Server.Auth.OAuth2AccessTokenTTL.AsDuration(), 2*time.Minute; got != want {
		t.Errorf("oauth2_access_token_ttl: got %s, want %s", got, want)
	}
	if got, want := cfg.Server.Auth.OAuth2RefreshTokenTTL.AsDuration(), 48*time.Hour; got != want {
		t.Errorf("oauth2_refresh_token_ttl: got %s, want %s", got, want)
	}
}

func TestValidate_RejectsOAuth2AccessTokenTTLBelowFloor(t *testing.T) {
	const bad = oauth2AuthBare + `
[server.auth]
oauth2_access_token_ttl = "30s"
`
	_, err := Parse([]byte(bad))
	if err == nil {
		t.Fatalf("expected error for oauth2_access_token_ttl below the 1m floor")
	}
	if !strings.Contains(err.Error(), "oauth2_access_token_ttl") {
		t.Errorf("error %q should mention oauth2_access_token_ttl", err.Error())
	}
}

func TestValidate_RejectsOAuth2RefreshTokenTTLBelowFloor(t *testing.T) {
	const bad = oauth2AuthBare + `
[server.auth]
oauth2_refresh_token_ttl = "10s"
`
	_, err := Parse([]byte(bad))
	if err == nil {
		t.Fatalf("expected error for oauth2_refresh_token_ttl below the 1m floor")
	}
	if !strings.Contains(err.Error(), "oauth2_refresh_token_ttl") {
		t.Errorf("error %q should mention oauth2_refresh_token_ttl", err.Error())
	}
}

func TestValidate_AcceptsOAuth2AccessTokenTTLAtFloor(t *testing.T) {
	const okAtFloor = oauth2AuthBare + `
[server.auth]
oauth2_access_token_ttl = "1m"
`
	if _, err := Parse([]byte(okAtFloor)); err != nil {
		t.Fatalf("Parse: expected no error at the 1m floor, got %v", err)
	}
}

// TestParse_OAuth2AuthSectionStrictDecode confirms the [server.auth]
// section rejects an unknown key, matching every other strict-decoded
// config block (STANDARDS §on strict decoding).
func TestParse_OAuth2AuthSectionStrictDecode(t *testing.T) {
	const bad = oauth2AuthBare + `
[server.auth]
oauth2_access_token_ttl = "1h"
bogus_key = "x"
`
	_, err := Parse([]byte(bad))
	if err == nil {
		t.Fatalf("expected strict-decode error for unknown [server.auth] key")
	}
	if !strings.Contains(err.Error(), "bogus_key") {
		t.Errorf("error %q should mention the unknown key", err.Error())
	}
}
