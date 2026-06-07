// Tests for [server.attachment_shares] system-config block
// (docs/design/server/requirements/25-attachment-shares.md REQ-SHARE-30..52).

package sysconfig

import (
	"strings"
	"testing"
	"time"
)

// bareForShares is a minimal valid config without any attachment_shares block,
// used as the base for attachment-share-specific tests.
const bareForShares = `
[server]
hostname = "mail.example.com"
data_dir = "/var/lib/herold"

[server.admin_tls]
source = "file"
cert_file = "/a"
key_file  = "/b"

[[listener]]
name = "l"
address = ":25"
protocol = "smtp"
tls = "starttls"
`

// TestAttachmentShares_Defaults verifies that a missing [server.attachment_shares]
// block produces the documented default values (REQ-SHARE-30..52).
func TestAttachmentShares_Defaults(t *testing.T) {
	cfg, err := Parse([]byte(bareForShares))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	as := &cfg.Server.AttachmentShares

	// enabled defaults true.
	if !as.AttachmentSharesEnabled() {
		t.Error("AttachmentSharesEnabled() should default to true")
	}
	// Feature is inert without public_base_url.
	if as.AttachmentSharesActive() {
		t.Error("AttachmentSharesActive() should be false when public_base_url is unset")
	}
	if as.PublicBaseURL != "" {
		t.Errorf("PublicBaseURL: got %q, want empty", as.PublicBaseURL)
	}
	// default_ttl = 30d.
	if got, want := as.DefaultTTL.AsDuration(), 30*24*time.Hour; got != want {
		t.Errorf("DefaultTTL: got %s, want %s", got, want)
	}
	// max_ttl = 90d.
	if got, want := as.MaxTTL.AsDuration(), 90*24*time.Hour; got != want {
		t.Errorf("MaxTTL: got %s, want %s", got, want)
	}
	// pending_ttl = 1h.
	if got, want := as.PendingTTL.AsDuration(), time.Hour; got != want {
		t.Errorf("PendingTTL: got %s, want %s", got, want)
	}
	// revoked_grace = 24h.
	if got, want := as.RevokedGrace.AsDuration(), 24*time.Hour; got != want {
		t.Errorf("RevokedGrace: got %s, want %s", got, want)
	}
	// max_shares_per_principal = 1000.
	if got, want := as.MaxSharesPerPrincipal, 1000; got != want {
		t.Errorf("MaxSharesPerPrincipal: got %d, want %d", got, want)
	}
	// share_quota_per_principal = 5 GiB.
	const fiveGiB = int64(5 * 1024 * 1024 * 1024)
	if got := as.ShareQuotaPerPrincipal.AsInt64(); got != fiveGiB {
		t.Errorf("ShareQuotaPerPrincipal: got %d, want %d", got, fiveGiB)
	}
	// download_requests_per_ip_per_share = 50.
	if got, want := as.DownloadRequestsPerIPPerShare, 50; got != want {
		t.Errorf("DownloadRequestsPerIPPerShare: got %d, want %d", got, want)
	}
	// download_requests_window = 10m.
	if got, want := as.DownloadRequestsWindow.AsDuration(), 10*time.Minute; got != want {
		t.Errorf("DownloadRequestsWindow: got %s, want %s", got, want)
	}
}

// TestAttachmentShares_FullBlock verifies that a fully-specified
// [server.attachment_shares] block round-trips correctly.
func TestAttachmentShares_FullBlock(t *testing.T) {
	const full = bareForShares + `
[server.attachment_shares]
enabled = true
public_base_url = "https://mail.example.com"
default_ttl  = "30d"
max_ttl      = "90d"
pending_ttl  = "1h"
revoked_grace = "24h"
max_shares_per_principal = 1000
share_quota_per_principal = "5 GiB"
download_requests_per_ip_per_share = 50
download_requests_window = "10m"
`
	cfg, err := Parse([]byte(full))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	as := &cfg.Server.AttachmentShares

	if !as.AttachmentSharesEnabled() {
		t.Error("AttachmentSharesEnabled() should be true")
	}
	if !as.AttachmentSharesActive() {
		t.Error("AttachmentSharesActive() should be true when public_base_url is set")
	}
	if got, want := as.PublicBaseURL, "https://mail.example.com"; got != want {
		t.Errorf("PublicBaseURL: got %q, want %q", got, want)
	}
	if got, want := as.DefaultTTL.AsDuration(), 30*24*time.Hour; got != want {
		t.Errorf("DefaultTTL: got %s, want %s", got, want)
	}
	if got, want := as.MaxTTL.AsDuration(), 90*24*time.Hour; got != want {
		t.Errorf("MaxTTL: got %s, want %s", got, want)
	}
	if got, want := as.PendingTTL.AsDuration(), time.Hour; got != want {
		t.Errorf("PendingTTL: got %s, want %s", got, want)
	}
	if got, want := as.RevokedGrace.AsDuration(), 24*time.Hour; got != want {
		t.Errorf("RevokedGrace: got %s, want %s", got, want)
	}
	if got, want := as.MaxSharesPerPrincipal, 1000; got != want {
		t.Errorf("MaxSharesPerPrincipal: got %d, want %d", got, want)
	}
	const fiveGiB = int64(5 * 1024 * 1024 * 1024)
	if got := as.ShareQuotaPerPrincipal.AsInt64(); got != fiveGiB {
		t.Errorf("ShareQuotaPerPrincipal: got %d, want %d", got, fiveGiB)
	}
	if got, want := as.DownloadRequestsPerIPPerShare, 50; got != want {
		t.Errorf("DownloadRequestsPerIPPerShare: got %d, want %d", got, want)
	}
	if got, want := as.DownloadRequestsWindow.AsDuration(), 10*time.Minute; got != want {
		t.Errorf("DownloadRequestsWindow: got %s, want %s", got, want)
	}
}

// TestAttachmentShares_EnabledFalse verifies that the feature can be explicitly
// disabled without requiring public_base_url.
func TestAttachmentShares_EnabledFalse(t *testing.T) {
	const disabled = bareForShares + `
[server.attachment_shares]
enabled = false
`
	cfg, err := Parse([]byte(disabled))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	as := &cfg.Server.AttachmentShares
	if as.AttachmentSharesEnabled() {
		t.Error("AttachmentSharesEnabled() should be false")
	}
	if as.AttachmentSharesActive() {
		t.Error("AttachmentSharesActive() should be false when disabled")
	}
}

// TestAttachmentShares_HTTPBaseURL verifies that an http (not https)
// public_base_url is rejected (REQ-SHARE-11d).
func TestAttachmentShares_HTTPBaseURL(t *testing.T) {
	const httpURL = bareForShares + `
[server.attachment_shares]
enabled = true
public_base_url = "http://mail.example.com"
`
	_, err := Parse([]byte(httpURL))
	if err == nil {
		t.Fatal("expected error for http public_base_url")
	}
	if !strings.Contains(err.Error(), "public_base_url") {
		t.Errorf("error %q should mention public_base_url", err.Error())
	}
	if !strings.Contains(err.Error(), "https") {
		t.Errorf("error %q should mention https scheme requirement", err.Error())
	}
}

// TestAttachmentShares_DefaultTTLExceedsMaxTTL verifies that default_ttl >
// max_ttl is rejected (ordering constraint).
func TestAttachmentShares_DefaultTTLExceedsMaxTTL(t *testing.T) {
	const bad = bareForShares + `
[server.attachment_shares]
public_base_url = "https://mail.example.com"
default_ttl = "91d"
max_ttl     = "90d"
`
	_, err := Parse([]byte(bad))
	if err == nil {
		t.Fatal("expected error for default_ttl > max_ttl")
	}
	if !strings.Contains(err.Error(), "default_ttl") {
		t.Errorf("error %q should mention default_ttl", err.Error())
	}
	if !strings.Contains(err.Error(), "max_ttl") {
		t.Errorf("error %q should mention max_ttl", err.Error())
	}
}

// TestAttachmentShares_PendingTTLExceedsDefaultTTL verifies that pending_ttl >
// default_ttl is rejected (ordering constraint).
func TestAttachmentShares_PendingTTLExceedsDefaultTTL(t *testing.T) {
	const bad = bareForShares + `
[server.attachment_shares]
public_base_url = "https://mail.example.com"
pending_ttl  = "31d"
default_ttl  = "30d"
max_ttl      = "90d"
`
	_, err := Parse([]byte(bad))
	if err == nil {
		t.Fatal("expected error for pending_ttl > default_ttl")
	}
	if !strings.Contains(err.Error(), "pending_ttl") {
		t.Errorf("error %q should mention pending_ttl", err.Error())
	}
	if !strings.Contains(err.Error(), "default_ttl") {
		t.Errorf("error %q should mention default_ttl", err.Error())
	}
}

// TestAttachmentShares_ZeroMaxShares verifies that max_shares_per_principal = 0
// is rejected.
func TestAttachmentShares_ZeroMaxShares(t *testing.T) {
	const bad = bareForShares + `
[server.attachment_shares]
public_base_url = "https://mail.example.com"
max_shares_per_principal = 0
`
	// applyDefaults would have set it to 1000; to override with 0 the test
	// must use a negative value since TOML 0 and "absent" are indistinguishable
	// at the Go struct level (both are the zero value). Use -1 instead.
	_, err := Parse([]byte(strings.ReplaceAll(bad,
		"max_shares_per_principal = 0",
		"max_shares_per_principal = -1")))
	if err == nil {
		t.Fatal("expected error for max_shares_per_principal <= 0")
	}
	if !strings.Contains(err.Error(), "max_shares_per_principal") {
		t.Errorf("error %q should mention max_shares_per_principal", err.Error())
	}
}

// TestAttachmentShares_InvalidQuota verifies that a negative quota is rejected.
func TestAttachmentShares_InvalidQuota(t *testing.T) {
	const bad = bareForShares + `
[server.attachment_shares]
public_base_url = "https://mail.example.com"
share_quota_per_principal = "-1"
`
	_, err := Parse([]byte(bad))
	// A negative byte size will fail during TOML decode (UnmarshalText parses
	// digits only — a leading "-" will cause a parse error before validation).
	if err == nil {
		t.Fatal("expected error for negative share_quota_per_principal")
	}
}

// TestAttachmentShares_UnknownKey verifies that the strict TOML parser rejects
// unknown keys in the [server.attachment_shares] block.
func TestAttachmentShares_UnknownKey(t *testing.T) {
	const bad = bareForShares + `
[server.attachment_shares]
public_base_url = "https://mail.example.com"
totally_unknown_key = true
`
	_, err := Parse([]byte(bad))
	if err == nil {
		t.Fatal("expected error for unknown key in [server.attachment_shares]")
	}
	if !strings.Contains(err.Error(), "unknown") {
		t.Errorf("error %q should mention unknown key", err.Error())
	}
}

// TestAttachmentShares_DaySuffixDuration verifies that "d"-suffix values parse
// correctly for attachment-share TTL fields.
func TestAttachmentShares_DaySuffixDuration(t *testing.T) {
	cases := []struct {
		input string
		want  time.Duration
	}{
		{"1d", 24 * time.Hour},
		{"7d", 7 * 24 * time.Hour},
		{"30d", 30 * 24 * time.Hour},
		{"90d", 90 * 24 * time.Hour},
		{"24h", 24 * time.Hour},
		{"1h", time.Hour},
		{"10m", 10 * time.Minute},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.input, func(t *testing.T) {
			var d DurationExtended
			if err := d.UnmarshalText([]byte(tc.input)); err != nil {
				t.Fatalf("UnmarshalText(%q): %v", tc.input, err)
			}
			if got := d.AsDuration(); got != tc.want {
				t.Errorf("AsDuration(): got %s, want %s", got, tc.want)
			}
		})
	}
}

// TestByteSize_ParseUnits verifies that the ByteSize type parses all
// documented unit suffixes correctly.
func TestByteSize_ParseUnits(t *testing.T) {
	cases := []struct {
		input string
		want  int64
	}{
		{"1024", 1024},
		{"1 B", 1},
		{"1 KiB", 1024},
		{"1 MiB", 1024 * 1024},
		{"1 GiB", 1024 * 1024 * 1024},
		{"1 TiB", 1024 * 1024 * 1024 * 1024},
		{"5 GiB", 5 * 1024 * 1024 * 1024},
		{"512 MiB", 512 * 1024 * 1024},
		{"1 KB", 1000},
		{"1 MB", 1000 * 1000},
		{"1 GB", 1000 * 1000 * 1000},
		{"1 TB", 1000 * 1000 * 1000 * 1000},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.input, func(t *testing.T) {
			var b ByteSize
			if err := b.UnmarshalText([]byte(tc.input)); err != nil {
				t.Fatalf("UnmarshalText(%q): %v", tc.input, err)
			}
			if got := b.AsInt64(); got != tc.want {
				t.Errorf("AsInt64(): got %d, want %d", got, tc.want)
			}
		})
	}
}

// TestByteSize_InvalidInputs verifies that malformed byte-size strings are
// rejected with an error. Note: an empty string is NOT invalid; it maps to
// the zero value (same convention as Duration.UnmarshalText).
func TestByteSize_InvalidInputs(t *testing.T) {
	bad := []string{
		"abc",
		"5 XiB",
		"GiB",
		"5 GiB extra",
	}
	for _, s := range bad {
		s := s
		t.Run(s, func(t *testing.T) {
			var b ByteSize
			if err := b.UnmarshalText([]byte(s)); err == nil {
				t.Errorf("UnmarshalText(%q): expected error, got nil", s)
			}
		})
	}
}

// TestDurationExtended_InvalidInputs verifies that malformed extended-duration
// strings are rejected.
func TestDurationExtended_InvalidInputs(t *testing.T) {
	bad := []string{
		"abc",
		"1x",
		"not-a-duration",
	}
	for _, s := range bad {
		s := s
		t.Run(s, func(t *testing.T) {
			var d DurationExtended
			if err := d.UnmarshalText([]byte(s)); err == nil {
				t.Errorf("UnmarshalText(%q): expected error, got nil", s)
			}
		})
	}
}

// TestAttachmentShares_NoBaseURLIsNotError verifies that omitting
// public_base_url does NOT produce a validation error; the feature is simply
// inert in that state.
func TestAttachmentShares_NoBaseURLIsNotError(t *testing.T) {
	const noURL = bareForShares + `
[server.attachment_shares]
enabled = true
`
	cfg, err := Parse([]byte(noURL))
	if err != nil {
		t.Fatalf("Parse: expected no error for missing public_base_url, got: %v", err)
	}
	if cfg.Server.AttachmentShares.AttachmentSharesActive() {
		t.Error("feature should be inert without public_base_url")
	}
}

// TestAttachmentShares_CustomValues verifies that non-default values survive
// the parse + apply + validate pipeline unchanged.
func TestAttachmentShares_CustomValues(t *testing.T) {
	const custom = bareForShares + `
[server.attachment_shares]
enabled = true
public_base_url = "https://shares.example.com"
default_ttl  = "7d"
max_ttl      = "14d"
pending_ttl  = "30m"
revoked_grace = "48h"
max_shares_per_principal = 500
share_quota_per_principal = "1 GiB"
download_requests_per_ip_per_share = 20
download_requests_window = "5m"
`
	cfg, err := Parse([]byte(custom))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	as := &cfg.Server.AttachmentShares

	if got, want := as.DefaultTTL.AsDuration(), 7*24*time.Hour; got != want {
		t.Errorf("DefaultTTL: got %s, want %s", got, want)
	}
	if got, want := as.MaxTTL.AsDuration(), 14*24*time.Hour; got != want {
		t.Errorf("MaxTTL: got %s, want %s", got, want)
	}
	if got, want := as.PendingTTL.AsDuration(), 30*time.Minute; got != want {
		t.Errorf("PendingTTL: got %s, want %s", got, want)
	}
	if got, want := as.RevokedGrace.AsDuration(), 48*time.Hour; got != want {
		t.Errorf("RevokedGrace: got %s, want %s", got, want)
	}
	if got, want := as.MaxSharesPerPrincipal, 500; got != want {
		t.Errorf("MaxSharesPerPrincipal: got %d, want %d", got, want)
	}
	const oneGiB = int64(1 * 1024 * 1024 * 1024)
	if got := as.ShareQuotaPerPrincipal.AsInt64(); got != oneGiB {
		t.Errorf("ShareQuotaPerPrincipal: got %d, want %d", got, oneGiB)
	}
	if got, want := as.DownloadRequestsPerIPPerShare, 20; got != want {
		t.Errorf("DownloadRequestsPerIPPerShare: got %d, want %d", got, want)
	}
	if got, want := as.DownloadRequestsWindow.AsDuration(), 5*time.Minute; got != want {
		t.Errorf("DownloadRequestsWindow: got %s, want %s", got, want)
	}
	if got, want := as.PublicBaseURL, "https://shares.example.com"; got != want {
		t.Errorf("PublicBaseURL: got %q, want %q", got, want)
	}
}
