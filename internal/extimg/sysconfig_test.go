package extimg

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/hanshuebner/herold/internal/sysconfig"
)

// TestFromSysConfig_RequireHTTPSDefault pins the FromSysConfig-level
// default: when the operator supplies no Limits.RequireHTTPS override,
// the resulting Config.RequireHTTPS must be false, so a plain http://
// image origin is a fetch candidate rather than a permanent
// SSRF-scheme rejection (re #267).
func TestFromSysConfig_RequireHTTPSDefault(t *testing.T) {
	cfg := FromSysConfig(sysconfig.ExternalImagesConfig{}, "test.local")
	if cfg.RequireHTTPS {
		t.Fatalf("RequireHTTPS default: got true, want false")
	}
}

// TestFromSysConfig_RequireHTTPSOverride confirms the per-install
// override still works in both directions.
func TestFromSysConfig_RequireHTTPSOverride(t *testing.T) {
	tru := true
	cfg := FromSysConfig(sysconfig.ExternalImagesConfig{
		Limits: sysconfig.ExternalImagesLimits{RequireHTTPS: &tru},
	}, "test.local")
	if !cfg.RequireHTTPS {
		t.Fatalf("explicit RequireHTTPS=true override not honoured")
	}

	fls := false
	cfg2 := FromSysConfig(sysconfig.ExternalImagesConfig{
		Limits: sysconfig.ExternalImagesLimits{RequireHTTPS: &fls},
	}, "test.local")
	if cfg2.RequireHTTPS {
		t.Fatalf("explicit RequireHTTPS=false override not honoured")
	}
}

// TestSysConfigParse_RequireHTTPSDefault pins the production-path
// default. Every server startup builds its Config via sysconfig.Load ->
// Parse -> applyDefaults, which fills the RequireHTTPS pointer BEFORE
// FromSysConfig ever runs -- so this is the load-bearing default, not
// the FromSysConfig nil-check fallback alone. A system.toml with no
// [external_images.limits] require_https key (the ZEIT newsletter
// prod case, #267) must parse to require_https=false.
func TestSysConfigParse_RequireHTTPSDefault(t *testing.T) {
	const bare = `
[server]
hostname = "mail.example.com"
data_dir = "/var/lib/herold"

[server.admin_tls]
source = "file"
cert_file = "/etc/herold/admin.crt"
key_file  = "/etc/herold/admin.key"

[[listener]]
name = "smtp-relay"
address = "0.0.0.0:25"
protocol = "smtp"
tls = "starttls"
`
	cfg, err := sysconfig.Parse([]byte(bare))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.ExternalImages.Limits.RequireHTTPS == nil || *cfg.ExternalImages.Limits.RequireHTTPS {
		t.Fatalf("default require_https: got %v, want pointer to false",
			cfg.ExternalImages.Limits.RequireHTTPS)
	}
}

// allowedPortOf sets cfg.AllowedPorts to the httptest server's
// kernel-picked port so the SSRF guard's port allowlist doesn't refuse
// the test fixture itself.
func allowedPortOf(t *testing.T, cfg *Config, srv *httptest.Server) {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse httptest URL: %v", err)
	}
	var port int
	fmt.Sscanf(u.Port(), "%d", &port)
	cfg.AllowedPorts = []int{port}
}

// TestSysConfigDefault_HTTPOriginInternalizes exercises the fetch
// outcome end-to-end (re #267): with the sysconfig default -- no
// require_https override -- a plain http:// image origin must
// internalize (outcome ok), not blocked_ssrf. AllowPrivate is set
// explicitly only so this in-process httptest server (bound to
// 127.0.0.1) is reachable; that knob is orthogonal to the scheme
// default under test here (see the companion
// TestSysConfigDefault_InternalDestinationStillBlocked, which pins the
// destination policy with AllowPrivate left at its own default).
func TestSysConfigDefault_HTTPOriginInternalizes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(pngBytes())
	}))
	defer srv.Close()

	sc := sysconfig.ExternalImagesConfig{
		Network: sysconfig.ExternalImagesNetwork{AllowPrivate: true},
	}
	cfg := FromSysConfig(sc, "test.local")
	allowedPortOf(t, &cfg, srv)
	cfg.resolveOptional()

	f := NewFetcher(cfg)
	r := f.Fetch(context.Background(), srv.URL+"/x.png")
	if r.Outcome != FetchOK {
		t.Fatalf("default config: http origin outcome=%s reason=%q, want ok", r.Outcome, r.Reason)
	}
}

// TestSysConfigDefault_InternalDestinationStillBlocked pins the
// invariant this ticket must not disturb: with the sysconfig default
// throughout (AllowPrivate also left at its own default, false), an
// http:// URL resolving to a loopback address is still refused -- the
// scheme relaxation does not loosen the SSRF destination policy.
func TestSysConfigDefault_InternalDestinationStillBlocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(pngBytes())
	}))
	defer srv.Close()

	cfg := FromSysConfig(sysconfig.ExternalImagesConfig{}, "test.local")
	allowedPortOf(t, &cfg, srv)
	cfg.resolveOptional()

	f := NewFetcher(cfg)
	r := f.Fetch(context.Background(), srv.URL+"/x.png")
	if r.Outcome != FetchBlockedSSRF {
		t.Fatalf("default config: loopback outcome=%s reason=%q, want blocked_ssrf", r.Outcome, r.Reason)
	}
}
