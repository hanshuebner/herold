// Tests for the [imap_import] system-config block
// (docs/design/server/requirements/19-imap-import.md
// REQ-IMAP-IMP-03, -05, -23, -26).

package sysconfig

import (
	"strings"
	"testing"
	"time"
)

// bareForIMAP is a minimal valid config without any [imap_import] block,
// used as the base for IMAP-import-specific tests.
const bareForIMAP = `
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

// TestIMAPImport_Defaults verifies that a missing [imap_import] block produces
// the documented default values (REQ-IMAP-IMP-05, -23, -26).
func TestIMAPImport_Defaults(t *testing.T) {
	cfg, err := Parse([]byte(bareForIMAP))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	ii := &cfg.IMAPImport

	// allow_password defaults to true (REQ-IMAP-IMP-05).
	if !ii.IMAPImportAllowPassword() {
		t.Error("AllowPassword: want true (default), got false")
	}
	// concurrent_accounts defaults to 16 (REQ-IMAP-IMP-26).
	if got, want := ii.ConcurrentAccounts, 16; got != want {
		t.Errorf("ConcurrentAccounts: got %d, want %d", got, want)
	}
	// poll_interval defaults to 60s (REQ-IMAP-IMP-23).
	if got, want := ii.PollInterval.AsDuration(), 60*time.Second; got != want {
		t.Errorf("PollInterval: got %s, want %s", got, want)
	}
	// OAuth map is empty when absent.
	if len(ii.OAuth) != 0 {
		t.Errorf("OAuth: got %d entries, want 0", len(ii.OAuth))
	}
}

// TestIMAPImport_ExplicitValues verifies that operator-supplied values survive
// the parse + apply + validate pipeline unchanged.
func TestIMAPImport_ExplicitValues(t *testing.T) {
	const cfg = bareForIMAP + `
[imap_import]
allow_password = false
concurrent_accounts = 32
poll_interval = "120s"
`
	parsed, err := Parse([]byte(cfg))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	ii := &parsed.IMAPImport

	if ii.IMAPImportAllowPassword() {
		t.Error("AllowPassword: want false (explicit), got true")
	}
	if got, want := ii.ConcurrentAccounts, 32; got != want {
		t.Errorf("ConcurrentAccounts: got %d, want %d", got, want)
	}
	if got, want := ii.PollInterval.AsDuration(), 120*time.Second; got != want {
		t.Errorf("PollInterval: got %s, want %s", got, want)
	}
}

// TestIMAPImport_AllowPasswordExplicitTrue verifies that allow_password = true
// is accepted and the knob value preserved.
func TestIMAPImport_AllowPasswordExplicitTrue(t *testing.T) {
	const cfg = bareForIMAP + `
[imap_import]
allow_password = true
`
	parsed, err := Parse([]byte(cfg))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !parsed.IMAPImport.IMAPImportAllowPassword() {
		t.Error("AllowPassword: want true (explicit), got false")
	}
}

// TestIMAPImport_ZeroConcurrentAccountsRejected verifies that
// concurrent_accounts = 0 is rejected (REQ-IMAP-IMP-26).
func TestIMAPImport_ZeroConcurrentAccountsRejected(t *testing.T) {
	// applyDefaults sets 16 when 0, so we must supply a negative to force an
	// error — the same pattern used by TestAttachmentShares_ZeroMaxShares.
	const cfg = bareForIMAP + `
[imap_import]
concurrent_accounts = -1
`
	_, err := Parse([]byte(cfg))
	if err == nil {
		t.Fatal("expected error for concurrent_accounts < 1")
	}
	if !strings.Contains(err.Error(), "concurrent_accounts") {
		t.Errorf("error %q should mention concurrent_accounts", err.Error())
	}
}

// TestIMAPImport_OAuthGoogle verifies a fully-specified [imap_import.oauth.google]
// block is accepted and round-trips correctly (REQ-IMAP-IMP-03).
func TestIMAPImport_OAuthGoogle(t *testing.T) {
	const cfg = bareForIMAP + `
[imap_import.oauth.google]
client_id         = "123456789012-abc.apps.googleusercontent.com"
client_secret_ref = "$HEROLD_GOOGLE_IMAP_CLIENT_SECRET"
token_endpoint    = "https://oauth2.googleapis.com/token"
scope             = "https://mail.google.com/"
`
	parsed, err := Parse([]byte(cfg))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	ii := &parsed.IMAPImport

	p, ok := ii.OAuth["google"]
	if !ok {
		t.Fatalf("OAuth[\"google\"]: not found (keys: %v)", maps(ii.OAuth))
	}
	if got, want := p.ClientID, "123456789012-abc.apps.googleusercontent.com"; got != want {
		t.Errorf("ClientID: got %q, want %q", got, want)
	}
	if got, want := p.ClientSecretRef, "$HEROLD_GOOGLE_IMAP_CLIENT_SECRET"; got != want {
		t.Errorf("ClientSecretRef: got %q, want %q", got, want)
	}
	if got, want := p.TokenEndpoint, "https://oauth2.googleapis.com/token"; got != want {
		t.Errorf("TokenEndpoint: got %q, want %q", got, want)
	}
	if got, want := p.Scope, "https://mail.google.com/"; got != want {
		t.Errorf("Scope: got %q, want %q", got, want)
	}
}

// TestIMAPImport_OAuthMicrosoftFileRef verifies that a file:/path secret
// reference is accepted (REQ-IMAP-IMP-03; STANDARDS §9).
func TestIMAPImport_OAuthMicrosoftFileRef(t *testing.T) {
	const cfg = bareForIMAP + `
[imap_import.oauth.microsoft]
client_id         = "aaaabbbb-cccc-dddd-eeee-ffffgggghhhh"
client_secret_ref = "file:/run/secrets/ms_imap_secret"
token_endpoint    = "https://login.microsoftonline.com/common/oauth2/v2.0/token"
scope             = "https://outlook.office.com/IMAP.AccessAsUser.All"
`
	parsed, err := Parse([]byte(cfg))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	p, ok := parsed.IMAPImport.OAuth["microsoft"]
	if !ok {
		t.Fatalf("OAuth[\"microsoft\"]: not found")
	}
	if got, want := p.ClientSecretRef, "file:/run/secrets/ms_imap_secret"; got != want {
		t.Errorf("ClientSecretRef: got %q, want %q", got, want)
	}
}

// TestIMAPImport_OAuthProviderNameNormalisedToLowercase verifies that provider
// names are normalised to lowercase at parse time, matching the
// [server.oauth_providers] convention.
func TestIMAPImport_OAuthProviderNameNormalisedToLowercase(t *testing.T) {
	const cfg = bareForIMAP + `
[imap_import.oauth.GOOGLE]
client_id         = "id"
client_secret_ref = "$SECRET"
token_endpoint    = "https://oauth2.googleapis.com/token"
`
	parsed, err := Parse([]byte(cfg))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, ok := parsed.IMAPImport.OAuth["google"]; !ok {
		t.Errorf("OAuth[\"google\"]: not found after lowercase normalisation; keys: %v",
			maps(parsed.IMAPImport.OAuth))
	}
	if _, ok := parsed.IMAPImport.OAuth["GOOGLE"]; ok {
		t.Error("OAuth[\"GOOGLE\"]: still present after normalisation; expected lowercase only")
	}
}

// TestIMAPImport_OAuthMissingClientID verifies that a provider block missing
// client_id is rejected (REQ-IMAP-IMP-03).
func TestIMAPImport_OAuthMissingClientID(t *testing.T) {
	const cfg = bareForIMAP + `
[imap_import.oauth.google]
client_secret_ref = "$HEROLD_GOOGLE_IMAP_CLIENT_SECRET"
token_endpoint    = "https://oauth2.googleapis.com/token"
`
	_, err := Parse([]byte(cfg))
	if err == nil {
		t.Fatal("expected error for missing client_id")
	}
	if !strings.Contains(err.Error(), "client_id") {
		t.Errorf("error %q should mention client_id", err.Error())
	}
}

// TestIMAPImport_OAuthInlineSecretRejected verifies that an inline
// client_secret_ref (not a $VAR or file: reference) is rejected
// (STANDARDS §9 / REQ-OPS-04).
func TestIMAPImport_OAuthInlineSecretRejected(t *testing.T) {
	const cfg = bareForIMAP + `
[imap_import.oauth.google]
client_id         = "id"
client_secret_ref = "not-a-reference"
token_endpoint    = "https://oauth2.googleapis.com/token"
`
	_, err := Parse([]byte(cfg))
	if err == nil {
		t.Fatal("expected error for inline client_secret_ref")
	}
	if !strings.Contains(err.Error(), "client_secret_ref") {
		t.Errorf("error %q should mention client_secret_ref", err.Error())
	}
	if !strings.Contains(err.Error(), "STANDARDS") {
		t.Errorf("error %q should reference STANDARDS §9", err.Error())
	}
}

// TestIMAPImport_OAuthMissingTokenEndpoint verifies that a provider block
// missing token_endpoint is rejected (REQ-IMAP-IMP-03).
func TestIMAPImport_OAuthMissingTokenEndpoint(t *testing.T) {
	const cfg = bareForIMAP + `
[imap_import.oauth.google]
client_id         = "id"
client_secret_ref = "$SECRET"
`
	_, err := Parse([]byte(cfg))
	if err == nil {
		t.Fatal("expected error for missing token_endpoint")
	}
	if !strings.Contains(err.Error(), "token_endpoint") {
		t.Errorf("error %q should mention token_endpoint", err.Error())
	}
}

// TestIMAPImport_OAuthInvalidTokenEndpointURL verifies that a non-absolute
// token_endpoint URL is rejected.
func TestIMAPImport_OAuthInvalidTokenEndpointURL(t *testing.T) {
	const cfg = bareForIMAP + `
[imap_import.oauth.google]
client_id         = "id"
client_secret_ref = "$SECRET"
token_endpoint    = "not-a-url"
`
	_, err := Parse([]byte(cfg))
	if err == nil {
		t.Fatal("expected error for invalid token_endpoint URL")
	}
	if !strings.Contains(err.Error(), "token_endpoint") {
		t.Errorf("error %q should mention token_endpoint", err.Error())
	}
}

// TestIMAPImport_OAuthScopeOptional verifies that a provider block without a
// scope field is valid (scope is optional per the struct comment).
func TestIMAPImport_OAuthScopeOptional(t *testing.T) {
	const cfg = bareForIMAP + `
[imap_import.oauth.custom]
client_id         = "id"
client_secret_ref = "$SECRET"
token_endpoint    = "https://example.com/token"
`
	parsed, err := Parse([]byte(cfg))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	p, ok := parsed.IMAPImport.OAuth["custom"]
	if !ok {
		t.Fatal("OAuth[\"custom\"]: not found")
	}
	if p.Scope != "" {
		t.Errorf("Scope: got %q, want empty (optional field)", p.Scope)
	}
}

// TestIMAPImport_UnknownKeyRejected verifies that the strict TOML parser
// rejects unknown keys in the [imap_import] block.
func TestIMAPImport_UnknownKeyRejected(t *testing.T) {
	const cfg = bareForIMAP + `
[imap_import]
totally_unknown_knob = true
`
	_, err := Parse([]byte(cfg))
	if err == nil {
		t.Fatal("expected error for unknown key in [imap_import]")
	}
	if !strings.Contains(err.Error(), "unknown") {
		t.Errorf("error %q should mention unknown key", err.Error())
	}
}

// TestIMAPImport_UnknownKeyInOAuthProviderRejected verifies that the strict
// TOML parser rejects unknown keys in the [imap_import.oauth.<name>] block.
func TestIMAPImport_UnknownKeyInOAuthProviderRejected(t *testing.T) {
	const cfg = bareForIMAP + `
[imap_import.oauth.google]
client_id         = "id"
client_secret_ref = "$SECRET"
token_endpoint    = "https://oauth2.googleapis.com/token"
totally_unknown   = true
`
	_, err := Parse([]byte(cfg))
	if err == nil {
		t.Fatal("expected error for unknown key in [imap_import.oauth.google]")
	}
	if !strings.Contains(err.Error(), "unknown") {
		t.Errorf("error %q should mention unknown key", err.Error())
	}
}

// TestIMAPImport_MultipleOAuthProviders verifies that multiple OAuth providers
// coexist correctly.
func TestIMAPImport_MultipleOAuthProviders(t *testing.T) {
	const cfg = bareForIMAP + `
[imap_import.oauth.google]
client_id         = "gid"
client_secret_ref = "$GOOGLE_SECRET"
token_endpoint    = "https://oauth2.googleapis.com/token"

[imap_import.oauth.microsoft]
client_id         = "mid"
client_secret_ref = "$MS_SECRET"
token_endpoint    = "https://login.microsoftonline.com/common/oauth2/v2.0/token"
`
	parsed, err := Parse([]byte(cfg))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	ii := &parsed.IMAPImport
	if len(ii.OAuth) != 2 {
		t.Errorf("OAuth: got %d providers, want 2", len(ii.OAuth))
	}
	if _, ok := ii.OAuth["google"]; !ok {
		t.Error("OAuth[\"google\"]: missing")
	}
	if _, ok := ii.OAuth["microsoft"]; !ok {
		t.Error("OAuth[\"microsoft\"]: missing")
	}
}

// maps returns the keys of the given map as a slice for test diagnostics.
func maps[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
