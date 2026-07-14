package protoadmin_test

// oauth2_federated_test.go covers the external-OIDC sign-in leg of the
// /oauth2/authorize native-client login page (issue #238, split from
// #199): the login page lists configured providers, POST
// /oauth2/authorize/federated starts an OIDC sign-in against the
// in-tree fake IdP (internal/testfakes/fakeoidc, "fakes before fixes"
// per CLAUDE.md -- this codebase's only deterministic IdP double), and
// GET /oauth2/authorize/federated/callback completes it and mints the
// same authorization code the password+TOTP leg mints, driving all the
// way to a working access token exactly as
// TestOAuth2_FullFlow_ViaHTTP does for the password+TOTP leg.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testfakes/fakeoidc"
)

// TestOAuth2Authorize_Federated_FullFlow_ViaHTTP drives, on one shared
// harness: (1) a password login for a regular principal, to pin the
// acceptance criterion that the federated leg leaves that path
// unaffected, and (2) the full federated leg -- GET authorize (login
// form lists the configured provider) -> POST /oauth2/authorize/federated
// (begins OIDC sign-in) -> the fake IdP's /authorize redirect ->
// GET /oauth2/authorize/federated/callback (completes sign-in, mints the
// authorization code) -> redirect carrying the code -> POST /oauth2/token
// -> the access token authenticates GET /api/v1/auth/whoami as the
// principal the OIDC identity is linked to.
func TestOAuth2Authorize_Federated_FullFlow_ViaHTTP(t *testing.T) {
	h := newHarness(t)
	mustRegisterHTTPAndroidClient(t, h)
	ctx := context.Background()
	redirectURI := "net.netzhansa.herold:/oauth2redirect"
	client := oauthNoRedirectClient(h)

	// -- (1) password leg, unaffected -----------------------------------
	_, adminKey := h.bootstrap("oauth2fed-admin@example.com")
	const pwEmail = "oauth2fed-pw-user@example.com"
	const pwPassword = "correct-horse-battery-staple"
	h.createPrincipal(adminKey, pwEmail)

	_, pwChallenge := oauthPKCE(t)
	pwGetRes, pwCSRF, pwReq := oauthAuthorizeGet(t, client, h.baseURL, redirectURI, "state-pw", pwChallenge)
	if pwGetRes.StatusCode != http.StatusOK {
		t.Fatalf("GET authorize (password leg): status=%d", pwGetRes.StatusCode)
	}
	pwForm := url.Values{"req": {pwReq}, "csrf": {pwCSRF}, "email": {pwEmail}, "password": {pwPassword}}
	pwPostReq, _ := http.NewRequest("POST", h.baseURL+"/oauth2/authorize", strings.NewReader(pwForm.Encode()))
	pwPostReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	pwPostReq.AddCookie(&http.Cookie{Name: "herold_oauth2_csrf", Value: pwCSRF})
	pwPostRes, err := client.Do(pwPostReq)
	if err != nil {
		t.Fatalf("POST authorize (password leg): %v", err)
	}
	pwPostRes.Body.Close()
	if pwPostRes.StatusCode != http.StatusFound {
		t.Fatalf("POST authorize (password leg): status=%d, want 302", pwPostRes.StatusCode)
	}
	if loc := pwPostRes.Header.Get("Location"); !strings.HasPrefix(loc, redirectURI) {
		t.Fatalf("password leg Location = %q, want prefix %q", loc, redirectURI)
	}

	// -- (2) federated leg ------------------------------------------------
	stub := fakeoidc.New(t, fakeoidc.Options{ClientID: "fed-client", ClientSecret: "fed-secret"})
	stub.SetIdentity(fakeoidc.Identity{
		Subject:       "fed-sub-1",
		Email:         "fed-user@idp.test",
		EmailVerified: true,
		Name:          "Fed User",
	})
	providerID, err := h.rp.AddProvider(ctx, directoryoidc.ProviderConfig{
		Name:         "fedprov",
		IssuerURL:    stub.IssuerURL(),
		ClientID:     "fed-client",
		ClientSecret: "fed-secret",
		RedirectURL:  "http://placeholder.invalid/unused", // overridden per-flow by BeginSignInAt
	})
	if err != nil {
		t.Fatalf("AddProvider: %v", err)
	}

	// Seed the local principal this OIDC identity is linked to.
	fedPid, err := h.h.Store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "fed-local@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	if err := h.h.Store.Meta().LinkOIDC(ctx, store.OIDCLink{
		PrincipalID:  fedPid.ID,
		ProviderName: string(providerID),
		Subject:      "fed-sub-1",
	}); err != nil {
		t.Fatalf("LinkOIDC: %v", err)
	}

	fedVerifier, fedChallenge := oauthPKCE(t)
	getQ := url.Values{
		"response_type": {"code"}, "client_id": {"herold-android"},
		"redirect_uri": {redirectURI}, "state": {"state-fed"},
		"code_challenge": {fedChallenge}, "code_challenge_method": {"S256"},
	}
	getReq, _ := http.NewRequest("GET", h.baseURL+"/oauth2/authorize?"+getQ.Encode(), nil)
	getRes, err := client.Do(getReq)
	if err != nil {
		t.Fatalf("GET authorize (federated leg): %v", err)
	}
	getBody, _ := io.ReadAll(getRes.Body)
	getRes.Body.Close()
	if getRes.StatusCode != http.StatusOK {
		t.Fatalf("GET authorize (federated leg): status=%d body=%s", getRes.StatusCode, getBody)
	}
	if !strings.Contains(string(getBody), "fedprov") {
		t.Fatalf("login form does not list the configured provider %q: %s", "fedprov", getBody)
	}
	var csrfCookie string
	for _, c := range getRes.Cookies() {
		if c.Name == "herold_oauth2_csrf" {
			csrfCookie = c.Value
		}
	}
	if csrfCookie == "" {
		t.Fatalf("expected herold_oauth2_csrf cookie to be set")
	}
	const marker = `name="req" value="`
	reqField := ""
	if i := strings.Index(string(getBody), marker); i >= 0 {
		rest := string(getBody)[i+len(marker):]
		if j := strings.Index(rest, `"`); j >= 0 {
			reqField = rest[:j]
		}
	}
	if reqField == "" {
		t.Fatalf("expected a non-empty hidden req field in the rendered form")
	}

	beginForm := url.Values{"req": {reqField}, "csrf": {csrfCookie}, "provider": {"fedprov"}}
	beginReq, _ := http.NewRequest("POST", h.baseURL+"/oauth2/authorize/federated", strings.NewReader(beginForm.Encode()))
	beginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	beginReq.AddCookie(&http.Cookie{Name: "herold_oauth2_csrf", Value: csrfCookie})
	beginRes, err := client.Do(beginReq)
	if err != nil {
		t.Fatalf("POST /oauth2/authorize/federated: %v", err)
	}
	beginRes.Body.Close()
	if beginRes.StatusCode != http.StatusFound {
		t.Fatalf("POST /oauth2/authorize/federated: status=%d, want 302", beginRes.StatusCode)
	}
	idpAuthURL := beginRes.Header.Get("Location")
	if idpAuthURL == "" || !strings.HasPrefix(idpAuthURL, stub.IssuerURL()) {
		t.Fatalf("Location = %q, want the fake IdP's authorize URL (prefix %q)", idpAuthURL, stub.IssuerURL())
	}

	// Simulate the fake IdP's own redirect back to our federated
	// callback, capturing the code + our own OIDC state.
	idpCode, oidcState := fakeoidc.FollowAuthorize(t, idpAuthURL, "")

	callbackURL := h.baseURL + "/oauth2/authorize/federated/callback?" + url.Values{
		"code": {idpCode}, "state": {oidcState},
	}.Encode()
	callbackReq, _ := http.NewRequest("GET", callbackURL, nil)
	callbackRes, err := client.Do(callbackReq)
	if err != nil {
		t.Fatalf("GET /oauth2/authorize/federated/callback: %v", err)
	}
	callbackBody, _ := io.ReadAll(callbackRes.Body)
	callbackRes.Body.Close()
	if callbackRes.StatusCode != http.StatusFound {
		t.Fatalf("federated callback: status=%d body=%s, want 302", callbackRes.StatusCode, callbackBody)
	}
	loc, err := url.Parse(callbackRes.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	if !strings.HasPrefix(loc.String(), redirectURI) {
		t.Fatalf("federated callback Location = %q, want prefix %q", loc.String(), redirectURI)
	}
	authCode := loc.Query().Get("code")
	if authCode == "" {
		t.Fatalf("federated callback Location = %q, missing code", loc.String())
	}
	if loc.Query().Get("state") != "state-fed" {
		t.Fatalf("federated callback Location state = %q, want state-fed", loc.Query().Get("state"))
	}

	// Exchange the code exactly like the password+TOTP leg's client does.
	tokenForm := url.Values{
		"grant_type": {"authorization_code"}, "code": {authCode},
		"redirect_uri": {redirectURI}, "client_id": {"herold-android"},
		"code_verifier": {fedVerifier},
	}
	tokenRes, tokenBody := h.doRequestForm("POST", "/oauth2/token", tokenForm)
	if tokenRes.StatusCode != http.StatusOK {
		t.Fatalf("token exchange: status=%d body=%s", tokenRes.StatusCode, tokenBody)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(tokenBody, &tok); err != nil {
		t.Fatalf("unmarshal token response: %v: %s", err, tokenBody)
	}
	if tok.AccessToken == "" || tok.TokenType != "Bearer" || tok.ExpiresIn <= 0 {
		t.Fatalf("incomplete token response: %+v", tok)
	}

	// The access token authenticates as the principal the OIDC identity
	// is linked to, proving the federated leg reached the same
	// code/token issuance path as the password+TOTP leg.
	whoRes, whoBody := h.doRequest("GET", "/api/v1/auth/whoami", tok.AccessToken, nil)
	if whoRes.StatusCode != http.StatusOK {
		t.Fatalf("whoami with federated-leg access token: status=%d body=%s", whoRes.StatusCode, whoBody)
	}
	var who struct {
		PrincipalID uint64 `json:"principal_id"`
	}
	if err := json.Unmarshal(whoBody, &who); err != nil {
		t.Fatalf("unmarshal whoami: %v: %s", err, whoBody)
	}
	if who.PrincipalID != uint64(fedPid.ID) {
		t.Fatalf("whoami principal_id = %d, want %d (the federated principal)", who.PrincipalID, fedPid.ID)
	}
}

// TestOAuth2Authorize_Federated_UnlinkedSubject_RerendersLoginForm
// asserts a sign-in for a subject with no local link (REQ-AUTH-56, auto-
// provisioning off by default here) re-renders the login form with an
// error rather than erroring the whole authorization request or leaking
// account-existence information -- the user can retry with password or
// a different provider without restarting the native client's flow.
func TestOAuth2Authorize_Federated_UnlinkedSubject_RerendersLoginForm(t *testing.T) {
	h := newHarness(t)
	mustRegisterHTTPAndroidClient(t, h)
	ctx := context.Background()
	redirectURI := "net.netzhansa.herold:/oauth2redirect"
	client := oauthNoRedirectClient(h)

	stub := fakeoidc.New(t, fakeoidc.Options{ClientID: "fed-client-2", ClientSecret: "fed-secret-2"})
	stub.SetIdentity(fakeoidc.Identity{
		Subject:       "never-linked-sub",
		Email:         "nobody@idp.test",
		EmailVerified: true,
	})
	if _, err := h.rp.AddProvider(ctx, directoryoidc.ProviderConfig{
		Name:         "fedprov2",
		IssuerURL:    stub.IssuerURL(),
		ClientID:     "fed-client-2",
		ClientSecret: "fed-secret-2",
		RedirectURL:  "http://placeholder.invalid/unused",
		// AutoProvision intentionally left off (REQ-AUTH-56 default).
	}); err != nil {
		t.Fatalf("AddProvider: %v", err)
	}

	_, challenge := oauthPKCE(t)
	_, csrfCookie, reqField := oauthAuthorizeGet(t, client, h.baseURL, redirectURI, "state-unlinked", challenge)

	beginForm := url.Values{"req": {reqField}, "csrf": {csrfCookie}, "provider": {"fedprov2"}}
	beginReq, _ := http.NewRequest("POST", h.baseURL+"/oauth2/authorize/federated", strings.NewReader(beginForm.Encode()))
	beginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	beginReq.AddCookie(&http.Cookie{Name: "herold_oauth2_csrf", Value: csrfCookie})
	beginRes, err := client.Do(beginReq)
	if err != nil {
		t.Fatalf("POST /oauth2/authorize/federated: %v", err)
	}
	beginRes.Body.Close()
	idpAuthURL := beginRes.Header.Get("Location")

	idpCode, oidcState := fakeoidc.FollowAuthorize(t, idpAuthURL, "")

	callbackURL := h.baseURL + "/oauth2/authorize/federated/callback?" + url.Values{
		"code": {idpCode}, "state": {oidcState},
	}.Encode()
	callbackReq, _ := http.NewRequest("GET", callbackURL, nil)
	callbackRes, err := client.Do(callbackReq)
	if err != nil {
		t.Fatalf("GET /oauth2/authorize/federated/callback: %v", err)
	}
	body, _ := io.ReadAll(callbackRes.Body)
	callbackRes.Body.Close()
	if callbackRes.StatusCode != http.StatusOK {
		t.Fatalf("callback for unlinked subject: status=%d, want 200 (re-rendered login form): %s", callbackRes.StatusCode, body)
	}
	if !strings.Contains(string(body), "not linked") {
		t.Fatalf("re-rendered login form missing the expected error text: %s", body)
	}
	// A reused (already-consumed) OIDC state must not work a second time.
	callbackRes2, err := client.Do(callbackReq)
	if err != nil {
		t.Fatalf("GET /oauth2/authorize/federated/callback (replay): %v", err)
	}
	callbackRes2.Body.Close()
	if callbackRes2.StatusCode != http.StatusBadRequest {
		t.Fatalf("replayed federated callback: status=%d, want 400", callbackRes2.StatusCode)
	}
}
