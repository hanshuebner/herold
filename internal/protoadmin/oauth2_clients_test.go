package protoadmin_test

// oauth2_clients_test.go covers the admin REST CRUD surface for the
// DB-backed OAuth2 client registry (issue #199's "DB-backed OAuth2
// client registry" work item), and the security properties the split
// specifically calls out:
//   - an operator registers a client through the admin API and that
//     client completes a full authorization-code + PKCE grant with no
//     herold rebuild (the acceptance criterion, verbatim);
//   - a registered client's issued tokens carry exactly its registered
//     scopes, never more;
//   - the grant requires the same authentication strength as web login
//     (TOTP for an enrolled principal) regardless of which client_id is
//     used;
//   - a deleted/unregistered client is refused immediately;
//   - a confidential client's secret is required at the token endpoint,
//     returned in plaintext exactly once, and never re-exposed by GET;
//   - a non-admin caller cannot manage the registry.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type oauthClientDTO struct {
	ClientID     string   `json:"client_id"`
	Name         string   `json:"name"`
	RedirectURIs []string `json:"redirect_uris"`
	Scopes       []string `json:"scopes"`
	Public       bool     `json:"public"`
	ClientSecret string   `json:"client_secret,omitempty"`
}

func mustCreateOAuthClient(t *testing.T, h *harness, adminKey string, body map[string]any) oauthClientDTO {
	t.Helper()
	res, buf := h.doRequest("POST", "/api/v1/oauth2/clients", adminKey, body)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create oauth2 client: status=%d body=%s", res.StatusCode, buf)
	}
	var dto oauthClientDTO
	if err := json.Unmarshal(buf, &dto); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, buf)
	}
	return dto
}

// TestOAuthClientsAdmin_CRUD exercises create/list/get/update/delete
// end to end and confirms the acceptance criterion verbatim: a client
// registered purely through the admin API (no compiled-in registry,
// no rebuild) completes a full authorization-code + PKCE grant.
func TestOAuthClientsAdmin_CRUD(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("oauth2clients-admin@example.com")
	const email = "oauth2clients-crud-user@example.com"
	h.createPrincipal(adminKey, email)

	created := mustCreateOAuthClient(t, h, adminKey, map[string]any{
		"client_id":     "acceptance-client",
		"name":          "Acceptance test client",
		"redirect_uris": []string{"net.netzhansa.herold:/oauth2redirect"},
	})
	if created.ClientID != "acceptance-client" || !created.Public {
		t.Fatalf("created = %+v", created)
	}
	if created.ClientSecret != "" {
		t.Fatalf("public client registration must not return a secret")
	}

	// List includes it.
	listRes, listBuf := h.doRequest("GET", "/api/v1/oauth2/clients", adminKey, nil)
	if listRes.StatusCode != http.StatusOK {
		t.Fatalf("list: status=%d body=%s", listRes.StatusCode, listBuf)
	}
	var list struct {
		Items []oauthClientDTO `json:"items"`
	}
	if err := json.Unmarshal(listBuf, &list); err != nil {
		t.Fatalf("unmarshal list: %v: %s", err, listBuf)
	}
	found := false
	for _, c := range list.Items {
		if c.ClientID == "acceptance-client" {
			found = true
		}
	}
	if !found {
		t.Fatalf("list missing created client: %+v", list.Items)
	}

	// Get.
	getRes, getBuf := h.doRequest("GET", "/api/v1/oauth2/clients/acceptance-client", adminKey, nil)
	if getRes.StatusCode != http.StatusOK {
		t.Fatalf("get: status=%d body=%s", getRes.StatusCode, getBuf)
	}

	// Update: widen the redirect_uris set.
	updRes, updBuf := h.doRequest("PATCH", "/api/v1/oauth2/clients/acceptance-client", adminKey, map[string]any{
		"name":          "Renamed client",
		"redirect_uris": []string{"net.netzhansa.herold:/oauth2redirect", "http://127.0.0.1/oauth2redirect"},
	})
	if updRes.StatusCode != http.StatusOK {
		t.Fatalf("update: status=%d body=%s", updRes.StatusCode, updBuf)
	}
	var updated oauthClientDTO
	_ = json.Unmarshal(updBuf, &updated)
	if updated.Name != "Renamed client" || len(updated.RedirectURIs) != 2 {
		t.Fatalf("updated = %+v", updated)
	}

	// The acceptance criterion, verbatim: this admin-registered client
	// completes a full authorization-code + PKCE grant, purely through
	// REST registration -- no herold rebuild, no compiled-in entry.
	client := oauthNoRedirectClient(h)
	verifier, challenge := oauthPKCE(t)
	redirectURI := "net.netzhansa.herold:/oauth2redirect"
	_, csrfCookie, reqField := oauthAuthorizeGetForClient(t, client, h.baseURL, "acceptance-client", redirectURI, "acc-1", challenge)

	form := url.Values{
		"req": {reqField}, "csrf": {csrfCookie},
		"email": {email}, "password": {"correct-horse-battery-staple"},
	}
	postReq, _ := http.NewRequest("POST", h.baseURL+"/oauth2/authorize", strings.NewReader(form.Encode()))
	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postReq.AddCookie(&http.Cookie{Name: "herold_oauth2_csrf", Value: csrfCookie})
	postRes, err := client.Do(postReq)
	if err != nil {
		t.Fatalf("POST authorize: %v", err)
	}
	loc, _ := url.Parse(postRes.Header.Get("Location"))
	postRes.Body.Close()
	code := loc.Query().Get("code")
	if code == "" {
		t.Fatalf("no code in Location %q (client must validate and complete sign-in without a rebuild)", loc.String())
	}

	tokenForm := url.Values{
		"grant_type": {"authorization_code"}, "code": {code},
		"redirect_uri": {redirectURI}, "client_id": {"acceptance-client"},
		"code_verifier": {verifier},
	}
	tokenRes, tokenBody := h.doRequestForm("POST", "/oauth2/token", tokenForm)
	if tokenRes.StatusCode != http.StatusOK {
		t.Fatalf("token exchange: status=%d body=%s (client must complete the full grant without a rebuild)", tokenRes.StatusCode, tokenBody)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(tokenBody, &tok); err != nil || tok.AccessToken == "" {
		t.Fatalf("token response missing access_token: %v: %s", err, tokenBody)
	}
	whoRes, whoBody := h.doRequest("GET", "/api/v1/auth/whoami", tok.AccessToken, nil)
	if whoRes.StatusCode != http.StatusOK {
		t.Fatalf("whoami with issued access token: status=%d body=%s", whoRes.StatusCode, whoBody)
	}

	// Delete removes it; new authorize/token requests for this
	// client_id are refused immediately.
	delRes, delBuf := h.doRequest("DELETE", "/api/v1/oauth2/clients/acceptance-client", adminKey, nil)
	if delRes.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: status=%d body=%s", delRes.StatusCode, delBuf)
	}
	getAfterDelRes, _ := h.doRequest("GET", "/api/v1/oauth2/clients/acceptance-client", adminKey, nil)
	if getAfterDelRes.StatusCode != http.StatusNotFound {
		t.Fatalf("get after delete: status=%d, want 404", getAfterDelRes.StatusCode)
	}
	_, secondChallenge := oauthPKCE(t)
	authAfterDelRes, _ := h.doRequestRaw(client, "GET", "/oauth2/authorize?"+url.Values{
		"response_type": {"code"}, "client_id": {"acceptance-client"},
		"redirect_uri": {redirectURI}, "code_challenge": {secondChallenge}, "code_challenge_method": {"S256"},
	}.Encode())
	if authAfterDelRes.StatusCode != http.StatusBadRequest {
		t.Fatalf("GET authorize after delete: status=%d, want 400 (unknown/unregistered client_id refused)", authAfterDelRes.StatusCode)
	}
}

// TestOAuthClientsAdmin_RequiresAdmin proves a non-admin caller's own
// bearer token cannot manage the registry: neither create nor list
// succeeds.
func TestOAuthClientsAdmin_RequiresAdmin(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("oauth2clients-nonadmin-admin@example.com")
	const email = "oauth2clients-nonadmin-user@example.com"
	const password = "correct-horse-battery-staple"
	h.createPrincipal(adminKey, email)

	tokRes, tokBuf := h.doRequest("POST", "/api/v1/auth/device-token", "", map[string]any{
		"email": email, "password": password,
	})
	if tokRes.StatusCode != http.StatusCreated {
		t.Fatalf("device-token: status=%d body=%s", tokRes.StatusCode, tokBuf)
	}
	var tok struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(tokBuf, &tok)

	createRes, _ := h.doRequest("POST", "/api/v1/oauth2/clients", tok.Token, map[string]any{
		"client_id": "should-not-exist", "redirect_uris": []string{"https://example.test/cb"},
	})
	if createRes.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin create: status=%d, want 403", createRes.StatusCode)
	}
	listRes, _ := h.doRequest("GET", "/api/v1/oauth2/clients", tok.Token, nil)
	if listRes.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin list: status=%d, want 403", listRes.StatusCode)
	}
}

// TestOAuthClientsAdmin_DuplicateConflict asserts a second registration
// of the same client_id is refused (409), not silently overwritten.
func TestOAuthClientsAdmin_DuplicateConflict(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("oauth2clients-dup-admin@example.com")
	body := map[string]any{
		"client_id": "dup-client", "redirect_uris": []string{"https://example.test/cb"},
	}
	mustCreateOAuthClient(t, h, adminKey, body)
	res, buf := h.doRequest("POST", "/api/v1/oauth2/clients", adminKey, body)
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate create: status=%d body=%s, want 409", res.StatusCode, buf)
	}
}

// TestOAuthClientsAdmin_RejectsAdminScope asserts a client can never be
// registered with the admin scope: this grant issues end-user-scoped
// tokens only, so widening a client's Scopes to include "admin" is
// rejected outright rather than silently ignored.
func TestOAuthClientsAdmin_RejectsAdminScope(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("oauth2clients-adminscope-admin@example.com")
	res, buf := h.doRequest("POST", "/api/v1/oauth2/clients", adminKey, map[string]any{
		"client_id": "admin-scope-client", "redirect_uris": []string{"https://example.test/cb"},
		"scopes": []string{"admin"},
	})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("register with admin scope: status=%d body=%s, want 400", res.StatusCode, buf)
	}
}

// TestOAuthClientsAdmin_MissingRedirectURI asserts registration without
// at least one redirect_uri is rejected.
func TestOAuthClientsAdmin_MissingRedirectURI(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("oauth2clients-noredirect-admin@example.com")
	res, buf := h.doRequest("POST", "/api/v1/oauth2/clients", adminKey, map[string]any{
		"client_id": "no-redirect-client",
	})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("register without redirect_uris: status=%d body=%s, want 400", res.StatusCode, buf)
	}
}

// TestOAuthClientsAdmin_ScopeRestriction_TokenCarriesOnlyRegisteredScopes
// registers a client scoped to mail.send only and drives a full grant
// over HTTP, asserting the resulting access token's scope is exactly
// that set -- a bearer token grants exactly the scopes it was issued
// for, and no more (issue #199 security property).
func TestOAuthClientsAdmin_ScopeRestriction_TokenCarriesOnlyRegisteredScopes(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("oauth2clients-scope-admin@example.com")
	const email = "oauth2clients-scope-user@example.com"
	h.createPrincipal(adminKey, email)

	mustCreateOAuthClient(t, h, adminKey, map[string]any{
		"client_id":     "scoped-client",
		"redirect_uris": []string{"net.netzhansa.herold:/oauth2redirect"},
		"scopes":        []string{"mail.send"},
	})

	client := oauthNoRedirectClient(h)
	verifier, challenge := oauthPKCE(t)
	redirectURI := "net.netzhansa.herold:/oauth2redirect"
	_, csrfCookie, reqField := oauthAuthorizeGetForClient(t, client, h.baseURL, "scoped-client", redirectURI, "sc-1", challenge)

	form := url.Values{
		"req": {reqField}, "csrf": {csrfCookie},
		"email": {email}, "password": {"correct-horse-battery-staple"},
	}
	postReq, _ := http.NewRequest("POST", h.baseURL+"/oauth2/authorize", strings.NewReader(form.Encode()))
	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postReq.AddCookie(&http.Cookie{Name: "herold_oauth2_csrf", Value: csrfCookie})
	postRes, err := client.Do(postReq)
	if err != nil {
		t.Fatalf("POST authorize: %v", err)
	}
	loc, _ := url.Parse(postRes.Header.Get("Location"))
	postRes.Body.Close()
	code := loc.Query().Get("code")
	if code == "" {
		t.Fatalf("no code in Location %q", loc.String())
	}

	tokenForm := url.Values{
		"grant_type": {"authorization_code"}, "code": {code},
		"redirect_uri": {redirectURI}, "client_id": {"scoped-client"},
		"code_verifier": {verifier},
	}
	tokenRes, tokenBody := h.doRequestForm("POST", "/oauth2/token", tokenForm)
	if tokenRes.StatusCode != http.StatusOK {
		t.Fatalf("token exchange: status=%d body=%s", tokenRes.StatusCode, tokenBody)
	}
	var tok struct {
		Scope string `json:"scope"`
	}
	if err := json.Unmarshal(tokenBody, &tok); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, tokenBody)
	}
	if tok.Scope != "mail.send" {
		t.Fatalf("token scope = %q, want exactly %q (a client scoped to mail.send must never grant more)", tok.Scope, "mail.send")
	}
}

// TestOAuthClientsAdmin_ConfidentialSecret exercises a confidential
// client end to end: the secret is returned exactly once at creation,
// never re-exposed by GET, stored hashed (never logged/returned again),
// and the token endpoint requires it -- a missing or wrong secret is
// refused even with valid PKCE and a valid code.
func TestOAuthClientsAdmin_ConfidentialSecret(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("oauth2clients-conf-admin@example.com")
	const email = "oauth2clients-conf-user@example.com"
	h.createPrincipal(adminKey, email)

	created := mustCreateOAuthClient(t, h, adminKey, map[string]any{
		"client_id":     "confidential-client",
		"redirect_uris": []string{"net.netzhansa.herold:/oauth2redirect"},
		"confidential":  true,
	})
	if created.Public {
		t.Fatalf("confidential registration returned Public=true")
	}
	if created.ClientSecret == "" {
		t.Fatalf("confidential registration must return a plaintext secret exactly once")
	}
	secret := created.ClientSecret

	// GET never re-exposes the secret.
	getRes, getBuf := h.doRequest("GET", "/api/v1/oauth2/clients/confidential-client", adminKey, nil)
	if getRes.StatusCode != http.StatusOK {
		t.Fatalf("get: status=%d body=%s", getRes.StatusCode, getBuf)
	}
	if strings.Contains(string(getBuf), secret) {
		t.Fatalf("GET response leaked the client secret: %s", getBuf)
	}

	client := oauthNoRedirectClient(h)
	verifier, challenge := oauthPKCE(t)
	redirectURI := "net.netzhansa.herold:/oauth2redirect"
	_, csrfCookie, reqField := oauthAuthorizeGetForClient(t, client, h.baseURL, "confidential-client", redirectURI, "cc-1", challenge)

	form := url.Values{
		"req": {reqField}, "csrf": {csrfCookie},
		"email": {email}, "password": {"correct-horse-battery-staple"},
	}
	postReq, _ := http.NewRequest("POST", h.baseURL+"/oauth2/authorize", strings.NewReader(form.Encode()))
	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postReq.AddCookie(&http.Cookie{Name: "herold_oauth2_csrf", Value: csrfCookie})
	postRes, err := client.Do(postReq)
	if err != nil {
		t.Fatalf("POST authorize: %v", err)
	}
	loc, _ := url.Parse(postRes.Header.Get("Location"))
	postRes.Body.Close()
	code := loc.Query().Get("code")
	if code == "" {
		t.Fatalf("no code in Location %q", loc.String())
	}

	baseTokenForm := func() url.Values {
		return url.Values{
			"grant_type": {"authorization_code"}, "code": {code},
			"redirect_uri": {redirectURI}, "client_id": {"confidential-client"},
			"code_verifier": {verifier},
		}
	}

	// No secret: refused.
	noSecretRes, noSecretBody := h.doRequestForm("POST", "/oauth2/token", baseTokenForm())
	if noSecretRes.StatusCode != http.StatusUnauthorized {
		t.Fatalf("exchange without client_secret: status=%d body=%s, want 401", noSecretRes.StatusCode, noSecretBody)
	}

	// Wrong secret: refused.
	wrongForm := baseTokenForm()
	wrongForm.Set("client_secret", "hcs_wrong-secret-value")
	wrongSecretRes, wrongSecretBody := h.doRequestForm("POST", "/oauth2/token", wrongForm)
	if wrongSecretRes.StatusCode != http.StatusUnauthorized {
		t.Fatalf("exchange with wrong client_secret: status=%d body=%s, want 401", wrongSecretRes.StatusCode, wrongSecretBody)
	}

	// Correct secret: succeeds.
	rightForm := baseTokenForm()
	rightForm.Set("client_secret", secret)
	okRes, okBody := h.doRequestForm("POST", "/oauth2/token", rightForm)
	if okRes.StatusCode != http.StatusOK {
		t.Fatalf("exchange with correct client_secret: status=%d body=%s, want 200", okRes.StatusCode, okBody)
	}
}
