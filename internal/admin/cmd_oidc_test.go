package admin

import (
	"context"
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
