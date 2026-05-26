// Tests that the admin-listener session-cookie config pulls both TTLs
// from sysconfig (REQ-AUTH-72, issue #12 slice 2). adminSessionCookieConfig
// is the only call site that bridges sysconfig → authsession.SessionConfig
// for the admin listener; any drift between the two would silently fall
// back to the old hardcoded 24h ceiling embedded in protoadmin's session
// issuance path.

package admin

import (
	"log/slog"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/sysconfig"
)

func TestAdminSessionCookieConfig_PullsBothTTLsFromSysconfig(t *testing.T) {
	cfg := newAdminTTLTestConfig()
	cfg.Server.UI.AdminAbsoluteTTL = sysconfig.Duration(6 * time.Hour)
	cfg.Server.UI.AdminIdleTTL = sysconfig.Duration(45 * time.Minute)

	sc := adminSessionCookieConfig(cfg, slog.Default())

	if got, want := sc.TTL, 6*time.Hour; got != want {
		t.Errorf("SessionConfig.TTL (absolute): got %s, want %s", got, want)
	}
	if got, want := sc.IdleTTL, 45*time.Minute; got != want {
		t.Errorf("SessionConfig.IdleTTL: got %s, want %s", got, want)
	}
}

func TestAdminSessionCookieConfig_UsesSpecDefaults(t *testing.T) {
	// applyDefaults runs at sysconfig.Parse time; here we just exercise
	// the wiring with explicit spec defaults to lock in the bridge.
	cfg := newAdminTTLTestConfig()
	cfg.Server.UI.AdminAbsoluteTTL = sysconfig.Duration(8 * time.Hour)
	cfg.Server.UI.AdminIdleTTL = sysconfig.Duration(1 * time.Hour)

	sc := adminSessionCookieConfig(cfg, slog.Default())

	if got, want := sc.TTL, 8*time.Hour; got != want {
		t.Errorf("admin TTL default: got %s, want %s", got, want)
	}
	if got, want := sc.IdleTTL, 1*time.Hour; got != want {
		t.Errorf("admin IdleTTL default: got %s, want %s", got, want)
	}
}

// newAdminTTLTestConfig returns the minimal sysconfig.Config needed to
// exercise adminSessionCookieConfig. It carries a 32-byte signing key
// in HEROLD_UI_SESSION_KEY via an env override is overkill for the
// wiring assertion — leaving the key unresolved is fine because
// resolveSessionSigningKey returns a random ephemeral key.
func newAdminTTLTestConfig() *sysconfig.Config {
	return &sysconfig.Config{
		Server: sysconfig.ServerConfig{
			UI: sysconfig.UIConfig{
				CookieName:     "herold_ui_session",
				CSRFCookieName: "herold_ui_csrf",
			},
		},
	}
}
