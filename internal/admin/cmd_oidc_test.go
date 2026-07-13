package admin

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/store"
)

// seedOIDCProviderRow inserts a minimal provider row directly into the
// store so the show / update CLI commands have something to find. We
// bypass directoryoidc.AddProvider because that triggers OIDC discovery
// against the live issuer URL, which is not reachable in unit tests.
func seedOIDCProviderRow(t *testing.T, env *cliTestEnv, name string) {
	t.Helper()
	if err := env.store.Meta().InsertOIDCProvider(context.Background(), store.OIDCProvider{
		Name:            name,
		IssuerURL:       "https://issuer.example.test",
		ClientID:        "test-client",
		ClientSecretRef: "env:OIDC_TEST_SECRET",
		Scopes:          []string{"openid", "email"},
	}); err != nil {
		t.Fatalf("InsertOIDCProvider: %v", err)
	}
}

func TestCLIOIDCProviderShow(t *testing.T) {
	env := newCLITestEnv(t, nil)
	seedOIDCProviderRow(t, env, "stub-show")
	out, _, err := env.run("oidc", "provider", "show", "stub-show", "--json")
	if err != nil {
		t.Fatalf("oidc provider show: %v", err)
	}
	if !strings.Contains(out, "stub-show") {
		t.Fatalf("expected provider name in output: %s", out)
	}
	// Secret material must not surface.
	if strings.Contains(out, "OIDC_TEST_SECRET") {
		t.Fatalf("provider show leaked secret ref: %s", out)
	}
}

func TestCLIOIDCProviderShow_NotFound(t *testing.T) {
	env := newCLITestEnv(t, nil)
	_, _, err := env.run("oidc", "provider", "show", "missing")
	if err == nil {
		t.Fatalf("expected error for missing provider")
	}
	if !strings.Contains(err.Error(), "404") && !strings.Contains(err.Error(), "not_found") {
		t.Fatalf("expected 404; got %v", err)
	}
}

func TestCLIOIDCProviderUpdate_NotImplemented(t *testing.T) {
	env := newCLITestEnv(t, nil)
	seedOIDCProviderRow(t, env, "stub-update")
	_, _, err := env.run("oidc", "provider", "update", "stub-update",
		"--client-secret-env=NEW_ENV")
	if err == nil {
		t.Fatalf("expected 501 not_implemented for provider update")
	}
	if !strings.Contains(err.Error(), "501") && !strings.Contains(err.Error(), "not_implemented") {
		t.Fatalf("expected 501 / not_implemented; got %v", err)
	}
}

func TestCLIOIDCLinkList_ByEmail(t *testing.T) {
	env := newCLITestEnv(t, nil)
	out, _, err := env.run("oidc", "link-list", "admin@test.local", "--json")
	if err != nil {
		t.Fatalf("link-list: %v", err)
	}
	// The seeded admin has no links yet; the API returns an empty page.
	if !strings.Contains(out, `"items"`) {
		t.Fatalf("expected items field; got %s", out)
	}
}

func TestCLIOIDCLinkList_UnknownEmail(t *testing.T) {
	env := newCLITestEnv(t, nil)
	_, _, err := env.run("oidc", "link-list", "ghost@test.local")
	if err == nil {
		t.Fatalf("expected error for unknown principal")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected 'not found' in error: %v", err)
	}
}

// -- External-IdP claim-to-grant mapping CLI surface (epic #188) -----------

// TestCLIOIDCProviderTrust exercises "oidc provider trust" / "--untrust"
// (REQ-AC-66), run as the seeded super-admin.
func TestCLIOIDCProviderTrust(t *testing.T) {
	env := newCLITestEnv(t, nil)
	seedOIDCProviderRow(t, env, "trust-me")

	out, _, err := env.run("oidc", "provider", "trust", "trust-me", "--json")
	if err != nil {
		t.Fatalf("oidc provider trust: %v", err)
	}
	var provider struct {
		AuthzTrusted bool `json:"authz_trusted"`
	}
	if err := json.Unmarshal([]byte(out), &provider); err != nil {
		t.Fatalf("decode: %v: %s", err, out)
	}
	if !provider.AuthzTrusted {
		t.Fatalf("expected authz_trusted=true in output: %s", out)
	}

	out, _, err = env.run("oidc", "provider", "trust", "trust-me", "--untrust", "--json")
	if err != nil {
		t.Fatalf("oidc provider trust --untrust: %v", err)
	}
	if err := json.Unmarshal([]byte(out), &provider); err != nil {
		t.Fatalf("decode: %v: %s", err, out)
	}
	if provider.AuthzTrusted {
		t.Fatalf("expected authz_trusted=false in output: %s", out)
	}
}

// TestCLIOIDCClaimAllowlist_CRUD exercises "oidc claim-allowlist"
// add/list/remove (REQ-AC-67).
func TestCLIOIDCClaimAllowlist_CRUD(t *testing.T) {
	env := newCLITestEnv(t, nil)
	seedOIDCProviderRow(t, env, "allow-me")

	if _, _, err := env.run("oidc", "claim-allowlist", "add", "allow-me", "groups"); err != nil {
		t.Fatalf("claim-allowlist add: %v", err)
	}
	out, _, err := env.run("oidc", "claim-allowlist", "list", "allow-me", "--json")
	if err != nil {
		t.Fatalf("claim-allowlist list: %v", err)
	}
	if !strings.Contains(out, "groups") {
		t.Fatalf("expected groups claim in output: %s", out)
	}
	if _, _, err := env.run("oidc", "claim-allowlist", "remove", "allow-me", "groups"); err != nil {
		t.Fatalf("claim-allowlist remove: %v", err)
	}
	if _, _, err := env.run("oidc", "claim-allowlist", "remove", "allow-me", "groups"); err == nil {
		t.Fatalf("expected error removing an already-absent claim")
	}
}

// TestCLIOIDCClaimMappingRule_CRUD exercises "oidc claim-mapping-rule"
// add/list/remove (REQ-AC-60), run as the seeded super-admin (who holds
// delegable authority -- server:superadmin -- over every resource).
func TestCLIOIDCClaimMappingRule_CRUD(t *testing.T) {
	env := newCLITestEnv(t, nil)
	seedOIDCProviderRow(t, env, "rule-me")

	out, _, err := env.run("oidc", "claim-mapping-rule", "add", "rule-me",
		"--claim=groups", "--match-value=list-x-admins",
		"--resource-kind=domain", "--resource-id=example.test", "--level=operator",
		"--json")
	if err != nil {
		t.Fatalf("claim-mapping-rule add: %v", err)
	}
	var created struct {
		ID         uint64 `json:"id"`
		ResourceID string `json:"resource_id"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("decode created rule: %v: %s", err, out)
	}
	if created.ResourceID != "example.test" {
		t.Fatalf("expected resource_id=example.test in output: %s", out)
	}

	out, _, err = env.run("oidc", "claim-mapping-rule", "list", "rule-me", "--json")
	if err != nil {
		t.Fatalf("claim-mapping-rule list: %v", err)
	}
	if !strings.Contains(out, "list-x-admins") {
		t.Fatalf("expected rule in listing: %s", out)
	}

	if _, _, err := env.run("oidc", "claim-mapping-rule", "remove", "rule-me",
		strconv.FormatUint(created.ID, 10)); err != nil {
		t.Fatalf("claim-mapping-rule remove: %v", err)
	}
}

// TestCLIOIDCClaimMappingRule_RejectsServerKind covers REQ-AC-64 at the
// CLI boundary: resource-kind "server" is refused before a row is ever
// written.
func TestCLIOIDCClaimMappingRule_RejectsServerKind(t *testing.T) {
	env := newCLITestEnv(t, nil)
	seedOIDCProviderRow(t, env, "no-server")

	_, _, err := env.run("oidc", "claim-mapping-rule", "add", "no-server",
		"--claim=groups", "--match-value=root", "--resource-kind=server",
		"--resource-id=", "--level=superadmin")
	if err == nil {
		t.Fatalf("expected error creating a server-kind rule")
	}
	if !strings.Contains(err.Error(), "400") && !strings.Contains(err.Error(), "validation_failed") {
		t.Fatalf("expected 400/validation_failed; got %v", err)
	}
}
