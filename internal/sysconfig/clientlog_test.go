package sysconfig

import (
	"strings"
	"testing"
	"time"
)

// minimalNoObsForClientLog is the minimal config without observability
// fields; used as a base for clientlog tests.
const minimalForClientLog = minimalNoObs

// TestClientLog_DefaultsApplied verifies that a config without any
// [clientlog] block receives the documented default values (REQ-OPS-219).
func TestClientLog_DefaultsApplied(t *testing.T) {
	cfg, err := Parse([]byte(minimalForClientLog))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	cl := cfg.ClientLog

	// Master switch defaults to true.
	if cl.Enabled == nil || !*cl.Enabled {
		t.Error("default clientlog.enabled: want true")
	}
	if !cl.ClientLogEnabled() {
		t.Error("ClientLogEnabled(): want true")
	}

	// Reorder window default.
	if cl.ReorderWindowMS != 1000 {
		t.Errorf("default reorder_window_ms: got %d, want 1000", cl.ReorderWindowMS)
	}

	// Livetail defaults.
	if cl.LivetailDefaultDuration != Duration(15*time.Minute) {
		t.Errorf("default livetail_default_duration: got %v, want 15m",
			cl.LivetailDefaultDuration.AsDuration())
	}
	if cl.LivetailMaxDuration != Duration(60*time.Minute) {
		t.Errorf("default livetail_max_duration: got %v, want 60m",
			cl.LivetailMaxDuration.AsDuration())
	}

	// Defaults sub-block.
	if cl.Defaults.TelemetryEnabled == nil || !*cl.Defaults.TelemetryEnabled {
		t.Error("default clientlog.defaults.telemetry_enabled: want true")
	}
	if !cl.TelemetryEnabledDefault() {
		t.Error("TelemetryEnabledDefault(): want true")
	}

	// Auth endpoint defaults (REQ-OPS-216).
	if cl.Auth.RingBufferRows != 100000 {
		t.Errorf("default auth.ring_buffer_rows: got %d, want 100000", cl.Auth.RingBufferRows)
	}
	if cl.Auth.RingBufferAge != Duration(168*time.Hour) {
		t.Errorf("default auth.ring_buffer_age: got %v, want 168h",
			cl.Auth.RingBufferAge.AsDuration())
	}
	if cl.Auth.RatePerSession.IsZero() {
		t.Error("default auth.rate_per_session: should not be zero")
	}
	if cl.Auth.RatePerSession.String() != "1000/5m" {
		t.Errorf("default auth.rate_per_session: got %q, want \"1000/5m\"",
			cl.Auth.RatePerSession.String())
	}
	if cl.Auth.BodyMaxBytes != 262144 {
		t.Errorf("default auth.body_max_bytes: got %d, want 262144", cl.Auth.BodyMaxBytes)
	}

	// Public endpoint defaults (REQ-OPS-216).
	if cl.Public.Enabled == nil || !*cl.Public.Enabled {
		t.Error("default clientlog.public.enabled: want true")
	}
	if !cl.ClientLogPublicEnabled() {
		t.Error("ClientLogPublicEnabled(): want true")
	}
	if cl.Public.OTLPEgress {
		t.Error("default clientlog.public.otlp_egress: want false")
	}
	if cl.Public.RingBufferRows != 10000 {
		t.Errorf("default public.ring_buffer_rows: got %d, want 10000", cl.Public.RingBufferRows)
	}
	if cl.Public.RingBufferAge != Duration(24*time.Hour) {
		t.Errorf("default public.ring_buffer_age: got %v, want 24h",
			cl.Public.RingBufferAge.AsDuration())
	}
	if cl.Public.RatePerIP.IsZero() {
		t.Error("default public.rate_per_ip: should not be zero")
	}
	if cl.Public.RatePerIP.String() != "10/m" {
		t.Errorf("default public.rate_per_ip: got %q, want \"10/m\"",
			cl.Public.RatePerIP.String())
	}
	if cl.Public.BodyMaxBytes != 8192 {
		t.Errorf("default public.body_max_bytes: got %d, want 8192", cl.Public.BodyMaxBytes)
	}
}

// TestClientLog_ExplicitValues verifies that explicit values in the
// [clientlog] block override the defaults.
func TestClientLog_ExplicitValues(t *testing.T) {
	const config = minimalNoObs + `
[clientlog]
enabled = true
reorder_window_ms = 2000
livetail_default_duration = "10m"
livetail_max_duration = "30m"

[clientlog.defaults]
telemetry_enabled = false

[clientlog.auth]
ring_buffer_rows = 50000
ring_buffer_age = "72h"
rate_per_session = "500/5m"
body_max_bytes = 131072

[clientlog.public]
enabled = false
otlp_egress = true
ring_buffer_rows = 5000
ring_buffer_age = "12h"
rate_per_ip = "20/m"
body_max_bytes = 4096
`
	cfg, err := Parse([]byte(config))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	cl := cfg.ClientLog

	if cl.Enabled == nil || !*cl.Enabled {
		t.Error("clientlog.enabled: want true")
	}
	if cl.ReorderWindowMS != 2000 {
		t.Errorf("reorder_window_ms: got %d, want 2000", cl.ReorderWindowMS)
	}
	if cl.LivetailDefaultDuration != Duration(10*time.Minute) {
		t.Errorf("livetail_default_duration: got %v, want 10m",
			cl.LivetailDefaultDuration.AsDuration())
	}
	if cl.LivetailMaxDuration != Duration(30*time.Minute) {
		t.Errorf("livetail_max_duration: got %v, want 30m",
			cl.LivetailMaxDuration.AsDuration())
	}
	if cl.Defaults.TelemetryEnabled == nil || *cl.Defaults.TelemetryEnabled {
		t.Error("clientlog.defaults.telemetry_enabled: want false")
	}
	if cl.TelemetryEnabledDefault() {
		t.Error("TelemetryEnabledDefault(): want false when telemetry_enabled=false")
	}

	if cl.Auth.RingBufferRows != 50000 {
		t.Errorf("auth.ring_buffer_rows: got %d, want 50000", cl.Auth.RingBufferRows)
	}
	if cl.Auth.RingBufferAge != Duration(72*time.Hour) {
		t.Errorf("auth.ring_buffer_age: got %v, want 72h",
			cl.Auth.RingBufferAge.AsDuration())
	}
	if cl.Auth.RatePerSession.String() != "500/5m" {
		t.Errorf("auth.rate_per_session: got %q, want \"500/5m\"",
			cl.Auth.RatePerSession.String())
	}
	if cl.Auth.BodyMaxBytes != 131072 {
		t.Errorf("auth.body_max_bytes: got %d, want 131072", cl.Auth.BodyMaxBytes)
	}

	if cl.Public.Enabled == nil || *cl.Public.Enabled {
		t.Error("clientlog.public.enabled: want false")
	}
	if cl.ClientLogPublicEnabled() {
		t.Error("ClientLogPublicEnabled(): want false when public.enabled=false")
	}
	if !cl.Public.OTLPEgress {
		t.Error("clientlog.public.otlp_egress: want true")
	}
	if cl.Public.RingBufferRows != 5000 {
		t.Errorf("public.ring_buffer_rows: got %d, want 5000", cl.Public.RingBufferRows)
	}
	if cl.Public.RingBufferAge != Duration(12*time.Hour) {
		t.Errorf("public.ring_buffer_age: got %v, want 12h",
			cl.Public.RingBufferAge.AsDuration())
	}
	if cl.Public.RatePerIP.String() != "20/m" {
		t.Errorf("public.rate_per_ip: got %q, want \"20/m\"",
			cl.Public.RatePerIP.String())
	}
	if cl.Public.BodyMaxBytes != 4096 {
		t.Errorf("public.body_max_bytes: got %d, want 4096", cl.Public.BodyMaxBytes)
	}
}

// TestClientLog_EnabledFalse verifies the kill-switch path: when
// clientlog.enabled = false, both ClientLogEnabled and the bootstrap
// helper report false (REQ-OPS-219, REQ-CLOG-12).
func TestClientLog_EnabledFalse(t *testing.T) {
	const config = minimalNoObs + `
[clientlog]
enabled = false
`
	cfg, err := Parse([]byte(config))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.ClientLog.ClientLogEnabled() {
		t.Error("ClientLogEnabled(): want false when enabled=false")
	}
}

// TestClientLog_UnknownKeyRejected verifies REQ-OPS-05: strict parsing
// rejects unknown keys inside the [clientlog] block.
func TestClientLog_UnknownKeyRejected(t *testing.T) {
	const config = minimalNoObs + `
[clientlog]
enabled = true
future_feature = "oops"
`
	_, err := Parse([]byte(config))
	if err == nil {
		t.Fatal("expected unknown-key error, got nil")
	}
	if !strings.Contains(err.Error(), "future_feature") {
		t.Errorf("error should mention offending key, got: %v", err)
	}
}

// TestClientLog_UnknownKeyInAuthRejected verifies strict parsing in
// the [clientlog.auth] sub-block.
func TestClientLog_UnknownKeyInAuthRejected(t *testing.T) {
	const config = minimalNoObs + `
[clientlog.auth]
ring_buffer_rows = 100
unknown_auth_key = true
`
	_, err := Parse([]byte(config))
	if err == nil {
		t.Fatal("expected unknown-key error, got nil")
	}
}

// TestClientLog_UnknownKeyInPublicRejected verifies strict parsing in
// the [clientlog.public] sub-block.
func TestClientLog_UnknownKeyInPublicRejected(t *testing.T) {
	const config = minimalNoObs + `
[clientlog.public]
ring_buffer_rows = 1000
mystery = "field"
`
	_, err := Parse([]byte(config))
	if err == nil {
		t.Fatal("expected unknown-key error, got nil")
	}
}

// TestClientLog_UnknownKeyInDefaultsRejected verifies strict parsing
// in the [clientlog.defaults] sub-block.
func TestClientLog_UnknownKeyInDefaultsRejected(t *testing.T) {
	const config = minimalNoObs + `
[clientlog.defaults]
telemetry_enabled = true
no_such_field = 42
`
	_, err := Parse([]byte(config))
	if err == nil {
		t.Fatal("expected unknown-key error, got nil")
	}
}

// TestRateLimit_Parse exercises the rate string parser.
func TestRateLimit_Parse(t *testing.T) {
	cases := []struct {
		input   string
		count   int
		window  time.Duration
		wantErr bool
	}{
		{"10/m", 10, time.Minute, false},
		{"1000/5m", 1000, 5 * time.Minute, false},
		{"100/s", 100, time.Second, false},
		{"50/h", 50, time.Hour, false},
		{"10/1m", 10, time.Minute, false},
		{"200/30s", 200, 30 * time.Second, false},
		// Error cases.
		{"", 0, 0, true},       // empty string is invalid for parse
		{"/m", 0, 0, true},     // missing count
		{"10/", 0, 0, true},    // missing unit
		{"0/m", 0, 0, true},    // zero count
		{"-5/m", 0, 0, true},   // negative count
		{"abc/m", 0, 0, true},  // non-numeric count
		{"10/xyz", 0, 0, true}, // unknown unit
		{"10/0s", 0, 0, true},  // zero window
	}
	for _, c := range cases {
		t.Run(c.input, func(t *testing.T) {
			count, window, err := parseRateLimit(c.input)
			if c.wantErr {
				if err == nil {
					t.Errorf("parseRateLimit(%q): expected error, got count=%d window=%v",
						c.input, count, window)
				}
				return
			}
			if err != nil {
				t.Errorf("parseRateLimit(%q): unexpected error: %v", c.input, err)
				return
			}
			if count != c.count {
				t.Errorf("parseRateLimit(%q): count=%d, want %d", c.input, count, c.count)
			}
			if window != c.window {
				t.Errorf("parseRateLimit(%q): window=%v, want %v", c.input, window, c.window)
			}
		})
	}
}

// TestRateLimit_UnmarshalText verifies round-trip via the encoding
// interface (as used by go-toml strict decoding).
func TestRateLimit_UnmarshalText(t *testing.T) {
	var r RateLimit
	if err := r.UnmarshalText([]byte("10/m")); err != nil {
		t.Fatalf("UnmarshalText: %v", err)
	}
	if r.Count != 10 {
		t.Errorf("count=%d, want 10", r.Count)
	}
	if r.Window != time.Minute {
		t.Errorf("window=%v, want 1m", r.Window)
	}
	if r.String() != "10/m" {
		t.Errorf("String()=%q, want \"10/m\"", r.String())
	}
	if r.IsZero() {
		t.Error("IsZero(): want false for a populated RateLimit")
	}

	b, _ := r.MarshalText()
	if string(b) != "10/m" {
		t.Errorf("MarshalText()=%q, want \"10/m\"", string(b))
	}
}

// TestRateLimit_UnmarshalText_Empty verifies that an empty string
// produces a zero RateLimit without error (nil/unset in TOML).
func TestRateLimit_UnmarshalText_Empty(t *testing.T) {
	var r RateLimit
	if err := r.UnmarshalText([]byte("")); err != nil {
		t.Fatalf("UnmarshalText empty: %v", err)
	}
	if !r.IsZero() {
		t.Error("IsZero(): want true for empty RateLimit")
	}
}

// TestClientLog_DurationParsed verifies that Duration fields in the
// clientlog block are parsed correctly.
func TestClientLog_DurationParsed(t *testing.T) {
	const config = minimalNoObs + `
[clientlog]
livetail_default_duration = "20m"
livetail_max_duration = "90m"

[clientlog.auth]
ring_buffer_age = "336h"

[clientlog.public]
ring_buffer_age = "48h"
`
	cfg, err := Parse([]byte(config))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	cl := cfg.ClientLog
	if cl.LivetailDefaultDuration != Duration(20*time.Minute) {
		t.Errorf("livetail_default_duration: got %v, want 20m",
			cl.LivetailDefaultDuration.AsDuration())
	}
	if cl.LivetailMaxDuration != Duration(90*time.Minute) {
		t.Errorf("livetail_max_duration: got %v, want 90m",
			cl.LivetailMaxDuration.AsDuration())
	}
	if cl.Auth.RingBufferAge != Duration(336*time.Hour) {
		t.Errorf("auth.ring_buffer_age: got %v, want 336h",
			cl.Auth.RingBufferAge.AsDuration())
	}
	if cl.Public.RingBufferAge != Duration(48*time.Hour) {
		t.Errorf("public.ring_buffer_age: got %v, want 48h",
			cl.Public.RingBufferAge.AsDuration())
	}
}

// TestClientLog_RateStringInToml verifies that the rate_per_session
// and rate_per_ip fields accept the "N/unit" string format from TOML.
func TestClientLog_RateStringInToml(t *testing.T) {
	const config = minimalNoObs + `
[clientlog.auth]
rate_per_session = "500/5m"

[clientlog.public]
rate_per_ip = "20/m"
`
	cfg, err := Parse([]byte(config))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.ClientLog.Auth.RatePerSession.String() != "500/5m" {
		t.Errorf("auth.rate_per_session: got %q, want \"500/5m\"",
			cfg.ClientLog.Auth.RatePerSession.String())
	}
	if cfg.ClientLog.Auth.RatePerSession.Count != 500 {
		t.Errorf("auth.rate_per_session.Count: got %d, want 500",
			cfg.ClientLog.Auth.RatePerSession.Count)
	}
	if cfg.ClientLog.Auth.RatePerSession.Window != 5*time.Minute {
		t.Errorf("auth.rate_per_session.Window: got %v, want 5m",
			cfg.ClientLog.Auth.RatePerSession.Window)
	}
	if cfg.ClientLog.Public.RatePerIP.String() != "20/m" {
		t.Errorf("public.rate_per_ip: got %q, want \"20/m\"",
			cfg.ClientLog.Public.RatePerIP.String())
	}
	if cfg.ClientLog.Public.RatePerIP.Count != 20 {
		t.Errorf("public.rate_per_ip.Count: got %d, want 20",
			cfg.ClientLog.Public.RatePerIP.Count)
	}
}

// TestClientLog_SIGHUPReload_Diff verifies that a change to the
// [clientlog] block produces a ChangeFieldUpdate diff entry with path
// "clientlog" (REQ-OPS-30).
func TestClientLog_SIGHUPReload_Diff(t *testing.T) {
	oldCfg, err := Parse([]byte(minimalForClientLog))
	if err != nil {
		t.Fatal(err)
	}

	// Simulate writing a new config to a temp file and loading it —
	// this is the SIGHUP apply path described in REQ-OPS-30.
	newRaw := minimalNoObs + `
[clientlog]
enabled = false
`
	newCfg, err := Parse([]byte(newRaw))
	if err != nil {
		t.Fatal(err)
	}

	changes, err := Diff(oldCfg, newCfg)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}

	found := false
	for _, ch := range changes {
		if ch.Path == "clientlog" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a clientlog diff entry, got: %+v", changes)
	}
}

// TestClientLog_SIGHUPReload_NoChangeNoDiff verifies that identical
// [clientlog] blocks produce no diff entry.
func TestClientLog_SIGHUPReload_NoChangeNoDiff(t *testing.T) {
	cfg, err := Parse([]byte(minimalForClientLog))
	if err != nil {
		t.Fatal(err)
	}
	changes, err := Diff(cfg, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range changes {
		if ch.Path == "clientlog" {
			t.Errorf("unexpected clientlog diff entry for identical configs: %+v", ch)
		}
	}
}
