package protoadmin_test

// oidc_privilege_test.go: who may register an OIDC provider, who may start
// a link flow, and what the refusals reveal. A provider is
// deployment-wide and its client_secret_ref makes the server read a file
// or environment variable, so registration is superadmin-only; a link
// flow attaches the signing-in identity to its principal, so only the
// principal itself may start one.

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testfakes/fakeoidc"
)

func TestOIDCProviders_RegistrationRequiresSuperAdmin(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("sa@example.com")
	promoteSuperAdmin(t, h, "sa@example.com")
	_, opKey := createDomainOperator(t, h, adminKey, "op@example.com")
	createOIDCProviderForTest(t, h, "existing")
	idp := fakeoidc.New(t, fakeoidc.Options{ClientID: "herold", ClientSecret: "s3cret"})

	secretFile := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secretFile, []byte("s3cret"), 0o600); err != nil {
		t.Fatal(err)
	}

	bodies := map[string]map[string]any{
		"secret ref (file)": {"name": "a", "issuer": idp.IssuerURL(), "client_id": "herold", "client_secret_ref": "file:" + secretFile},
		"secret ref (env)":  {"name": "b", "issuer": idp.IssuerURL(), "client_id": "herold", "client_secret_ref": "$HOME"},
		"inline secret":     {"name": "c", "issuer": idp.IssuerURL(), "client_id": "herold", "client_secret": "s3cret"},
	}
	for name, body := range bodies {
		res, buf := h.doRequest("POST", "/api/v1/oidc/providers", opKey, body)
		if res.StatusCode != http.StatusForbidden {
			t.Errorf("operator POST provider (%s) = %d, want 403: %s", name, res.StatusCode, buf)
		}
	}
	if res, buf := h.doRequest("PATCH", "/api/v1/oidc/providers/existing", opKey,
		map[string]any{"auto_provision": false}); res.StatusCode != http.StatusForbidden {
		t.Errorf("operator PATCH provider = %d, want 403: %s", res.StatusCode, buf)
	}
	if res, buf := h.doRequest("DELETE", "/api/v1/oidc/providers/existing", opKey, nil); res.StatusCode != http.StatusForbidden {
		t.Errorf("operator DELETE provider = %d, want 403: %s", res.StatusCode, buf)
	}
	if _, err := h.h.Store.Meta().GetOIDCProvider(t.Context(), "existing"); err != nil {
		t.Fatalf("provider vanished after a refused DELETE: %v", err)
	}

	// Control: a superadmin registers the same provider.
	res, buf := h.doRequest("POST", "/api/v1/oidc/providers", adminKey, bodies["secret ref (file)"])
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("superadmin POST provider = %d, want 201: %s", res.StatusCode, buf)
	}
	if res, buf := h.doRequest("DELETE", "/api/v1/oidc/providers/a", adminKey, nil); res.StatusCode != http.StatusNoContent {
		t.Fatalf("superadmin DELETE provider = %d, want 204: %s", res.StatusCode, buf)
	}
}

// TestOIDCProviders_UnresolvableSecretRefRevealsNothing: the 400 for a
// reference that cannot be read must not echo the path, the variable
// name or the OS error, so it is no oracle for the host's files.
func TestOIDCProviders_UnresolvableSecretRefRevealsNothing(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("sa@example.com")
	promoteSuperAdmin(t, h, "sa@example.com")
	idp := fakeoidc.New(t, fakeoidc.Options{})

	missing := filepath.Join(t.TempDir(), "no-such-secret-file")
	for _, ref := range []string{"file:" + missing, "$HEROLD_TEST_UNSET_SECRET_VAR"} {
		res, buf := h.doRequest("POST", "/api/v1/oidc/providers", adminKey, map[string]any{
			"name": "p", "issuer": idp.IssuerURL(), "client_id": "c", "client_secret_ref": ref,
		})
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("ref %q: status = %d, want 400: %s", ref, res.StatusCode, buf)
		}
		body := string(buf)
		if !strings.Contains(body, "invalid_client_secret_ref") {
			t.Errorf("ref %q: body lacks the problem code: %s", ref, body)
		}
		for _, leak := range []string{"no-such-secret-file", missing, "HEROLD_TEST_UNSET_SECRET_VAR", "no such file", "read secret"} {
			if strings.Contains(body, leak) {
				t.Errorf("ref %q: body leaks %q: %s", ref, leak, body)
			}
		}
	}
}

// TestOIDCLink_BeginIsSelfOnly: an admin cannot start a link flow for
// another principal (they would attach their own provider identity to the
// victim and then sign in as the victim); an unauthenticated caller gets
// 401. Unlinking stays open to an admin and is audited.
func TestOIDCLink_BeginIsSelfOnly(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("sa@example.com")
	promoteSuperAdmin(t, h, "sa@example.com")
	_, opKey := createDomainOperator(t, h, adminKey, "op@example.com")
	victim := h.createPrincipal(adminKey, "victim@example.com")
	createOIDCProviderForTest(t, h, "idp")

	path := "/api/v1/principals/" + pidStr(victim) + "/oidc-links/begin"
	for name, key := range map[string]string{"operator": opKey, "superadmin": adminKey} {
		res, buf := h.doRequest("POST", path, key, map[string]any{"provider_id": "idp"})
		if res.StatusCode != http.StatusForbidden {
			t.Errorf("%s begin link for another principal = %d, want 403: %s", name, res.StatusCode, buf)
		}
	}
	if res, buf := h.doRequest("POST", path, "", map[string]any{"provider_id": "idp"}); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated begin link = %d, want 401: %s", res.StatusCode, buf)
	}

	if err := h.h.Store.Meta().LinkOIDC(t.Context(), store.OIDCLink{
		PrincipalID: store.PrincipalID(victim), ProviderName: "idp", Subject: "victim-sub",
	}); err != nil {
		t.Fatal(err)
	}
	res, buf := h.doRequest("DELETE", "/api/v1/principals/"+pidStr(victim)+"/oidc-links/idp", opKey, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("operator unlink = %d, want 204: %s", res.StatusCode, buf)
	}
	entries, err := h.h.Store.Meta().ListAuditLog(t.Context(), store.AuditLogFilter{Action: "oidc.link.delete"})
	if err != nil || len(entries) != 1 {
		t.Fatalf("audit entries for oidc.link.delete = %d (%v), want 1", len(entries), err)
	}
}
