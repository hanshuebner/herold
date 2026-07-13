package protoadmin_test

// oauth2_native_test.go covers the OAuth2 authorization-code + PKCE
// grant for native clients (issue #199, REQ-AND-AUTH-01/02):
// GET/POST /oauth2/authorize (the browser-hosted login page) and
// POST /oauth2/token (the RFC 6749 token endpoint), driven end to end:
// authorize -> code -> token -> use access token on a JMAP-shaped call
// (whoami) -> refresh -> rotated -> replay-old-refresh-is-rejected.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/directory"
)

// mustRegisterHTTPAndroidClient registers the "herold-android" public
// client this file's tests exercise over HTTP. The DB-backed registry
// (issue #199) starts every harness empty -- there is no compiled-in
// client list -- so every test that drives the grant past client_id
// validation registers the client it uses first, going straight
// through the directory (not the admin REST CRUD surface under test
// elsewhere) to keep setup out of the assertions.
func mustRegisterHTTPAndroidClient(t *testing.T, h *harness) {
	t.Helper()
	if _, _, err := h.dir.RegisterOAuthClient(context.Background(), directory.OAuthClientRegistration{
		ClientID: "herold-android",
		Name:     "herold Android client",
		RedirectURIs: []string{
			"net.netzhansa.herold:/oauth2redirect",
			"http://127.0.0.1/oauth2redirect",
			"http://[::1]/oauth2redirect",
		},
	}); err != nil {
		t.Fatalf("RegisterOAuthClient: %v", err)
	}
}

// oauthNoRedirectClient returns an http.Client sharing h's transport but
// never following redirects -- the flow's redirect targets include a
// custom-scheme URI (net.netzhansa.herold:/...) that net/http cannot
// dial, and the test needs the Location header, not a followed request.
func oauthNoRedirectClient(h *harness) *http.Client {
	return &http.Client{
		Transport: h.client.Transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func oauthPKCE(t *testing.T) (verifier, challenge string) {
	t.Helper()
	var b [32]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		t.Fatalf("read random: %v", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(b[:])
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge
}

// oauthAuthorizeGet issues GET /oauth2/authorize for the herold-android
// client with the given redirect_uri/state/PKCE challenge and returns
// the response, its CSRF cookie value, and the "req" hidden-field value
// scraped from the rendered HTML form. Use oauthAuthorizeGetForClient
// to drive the flow for a different registered client_id.
func oauthAuthorizeGet(t *testing.T, client *http.Client, baseURL, redirectURI, state, challenge string) (res *http.Response, csrfCookie, reqField string) {
	t.Helper()
	return oauthAuthorizeGetForClient(t, client, baseURL, "herold-android", redirectURI, state, challenge)
}

// oauthAuthorizeGetForClient is oauthAuthorizeGet parameterised over
// client_id, for tests exercising a client other than herold-android
// (e.g. one registered through the admin CRUD surface under test in
// oauth2_clients_test.go).
func oauthAuthorizeGetForClient(t *testing.T, client *http.Client, baseURL, clientID, redirectURI, state, challenge string) (res *http.Response, csrfCookie, reqField string) {
	t.Helper()
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	req, err := http.NewRequest("GET", baseURL+"/oauth2/authorize?"+q.Encode(), nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	res, err = client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	for _, c := range res.Cookies() {
		if c.Name == "herold_oauth2_csrf" {
			csrfCookie = c.Value
		}
	}
	// Scrape the hidden "req" field value out of the rendered HTML.
	const marker = `name="req" value="`
	if i := strings.Index(string(body), marker); i >= 0 {
		rest := string(body)[i+len(marker):]
		if j := strings.Index(rest, `"`); j >= 0 {
			reqField = rest[:j]
		}
	}
	return res, csrfCookie, reqField
}

func TestOAuth2Authorize_GET_RendersLoginForm(t *testing.T) {
	h := newHarness(t)
	mustRegisterHTTPAndroidClient(t, h)
	client := oauthNoRedirectClient(h)
	_, challenge := oauthPKCE(t)
	res, csrfCookie, reqField := oauthAuthorizeGet(t, client, h.baseURL, "net.netzhansa.herold:/oauth2redirect", "st-1", challenge)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /oauth2/authorize: status=%d", res.StatusCode)
	}
	if csrfCookie == "" {
		t.Fatalf("expected herold_oauth2_csrf cookie to be set")
	}
	if reqField == "" {
		t.Fatalf("expected a non-empty hidden req field in the rendered form")
	}
	ct := res.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", ct)
	}
}

func TestOAuth2Authorize_GET_UnknownClient(t *testing.T) {
	h := newHarness(t)
	client := oauthNoRedirectClient(h)
	q := url.Values{
		"response_type": {"code"}, "client_id": {"not-a-client"},
		"redirect_uri":   {"https://example.test/cb"},
		"code_challenge": {"x"}, "code_challenge_method": {"S256"},
	}
	res, buf := h.doRequestRaw(client, "GET", "/oauth2/authorize?"+q.Encode())
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", res.StatusCode, buf)
	}
}

func TestOAuth2Authorize_GET_InvalidRedirectURI(t *testing.T) {
	h := newHarness(t)
	mustRegisterHTTPAndroidClient(t, h)
	client := oauthNoRedirectClient(h)
	q := url.Values{
		"response_type": {"code"}, "client_id": {"herold-android"},
		"redirect_uri":   {"https://evil.example/steal"},
		"code_challenge": {"x"}, "code_challenge_method": {"S256"},
	}
	res, buf := h.doRequestRaw(client, "GET", "/oauth2/authorize?"+q.Encode())
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400 (no open redirect)", res.StatusCode, buf)
	}
}

func TestOAuth2Authorize_GET_MissingPKCE_RedirectsWithError(t *testing.T) {
	h := newHarness(t)
	mustRegisterHTTPAndroidClient(t, h)
	client := oauthNoRedirectClient(h)
	q := url.Values{
		"response_type": {"code"}, "client_id": {"herold-android"},
		"redirect_uri": {"net.netzhansa.herold:/oauth2redirect"},
		"state":        {"st-2"},
	}
	req, _ := http.NewRequest("GET", h.baseURL+"/oauth2/authorize?"+q.Encode(), nil)
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusFound {
		t.Fatalf("status=%d, want 302 (redirect back with error)", res.StatusCode)
	}
	loc, err := url.Parse(res.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	if loc.Query().Get("error") == "" {
		t.Fatalf("Location = %q, want an error query parameter", loc.String())
	}
	if loc.Query().Get("state") != "st-2" {
		t.Fatalf("Location state = %q, want st-2", loc.Query().Get("state"))
	}
}

// TestOAuth2_FullFlow_ViaHTTP drives the entire grant through the HTTP
// surface: GET authorize (login form) -> POST authorize (credentials) ->
// redirect carrying code -> POST /oauth2/token (authorization_code) ->
// access token authenticates GET /api/v1/auth/whoami -> POST /oauth2/token
// (refresh_token) rotates -> replaying the old refresh token is rejected.
func TestOAuth2_FullFlow_ViaHTTP(t *testing.T) {
	h := newHarness(t)
	mustRegisterHTTPAndroidClient(t, h)
	_, adminKey := h.bootstrap("oauth2http-admin@example.com")
	const email = "oauth2http-user@example.com"
	const password = "correct-horse-battery-staple"
	pid := h.createPrincipal(adminKey, email)

	client := oauthNoRedirectClient(h)
	verifier, challenge := oauthPKCE(t)
	redirectURI := "net.netzhansa.herold:/oauth2redirect"

	getRes, csrfCookie, reqField := oauthAuthorizeGet(t, client, h.baseURL, redirectURI, "state-abc", challenge)
	if getRes.StatusCode != http.StatusOK {
		t.Fatalf("GET authorize: status=%d", getRes.StatusCode)
	}

	form := url.Values{
		"req": {reqField}, "csrf": {csrfCookie},
		"email": {email}, "password": {password},
	}
	postReq, _ := http.NewRequest("POST", h.baseURL+"/oauth2/authorize", strings.NewReader(form.Encode()))
	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postReq.AddCookie(&http.Cookie{Name: "herold_oauth2_csrf", Value: csrfCookie})
	postRes, err := client.Do(postReq)
	if err != nil {
		t.Fatalf("POST authorize: %v", err)
	}
	postBody, _ := io.ReadAll(postRes.Body)
	postRes.Body.Close()
	if postRes.StatusCode != http.StatusFound {
		t.Fatalf("POST authorize: status=%d body=%s, want 302", postRes.StatusCode, postBody)
	}
	loc, err := url.Parse(postRes.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	if !strings.HasPrefix(loc.String(), redirectURI) {
		t.Fatalf("Location = %q, want prefix %q", loc.String(), redirectURI)
	}
	code := loc.Query().Get("code")
	if code == "" {
		t.Fatalf("Location = %q, missing code", loc.String())
	}
	if loc.Query().Get("state") != "state-abc" {
		t.Fatalf("Location state = %q, want state-abc", loc.Query().Get("state"))
	}

	// Exchange the code.
	tokenForm := url.Values{
		"grant_type": {"authorization_code"}, "code": {code},
		"redirect_uri": {redirectURI}, "client_id": {"herold-android"},
		"code_verifier": {verifier},
	}
	tokenRes, tokenBody := h.doRequestForm("POST", "/oauth2/token", tokenForm)
	if tokenRes.StatusCode != http.StatusOK {
		t.Fatalf("token exchange: status=%d body=%s", tokenRes.StatusCode, tokenBody)
	}
	var tok struct {
		AccessToken  string `json:"access_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"`
		RefreshToken string `json:"refresh_token"`
		Scope        string `json:"scope"`
	}
	if err := json.Unmarshal(tokenBody, &tok); err != nil {
		t.Fatalf("unmarshal token response: %v: %s", err, tokenBody)
	}
	if tok.AccessToken == "" || tok.RefreshToken == "" || tok.TokenType != "Bearer" || tok.ExpiresIn <= 0 {
		t.Fatalf("incomplete token response: %+v", tok)
	}

	// Use the access token exactly as a native client would for any
	// bearer-authenticated call (JMAP coverage lives in
	// internal/protojmap/oauth2_native_grant_test.go; whoami here proves
	// the same protoadmin Bearer path unmodified).
	whoRes, whoBody := h.doRequest("GET", "/api/v1/auth/whoami", tok.AccessToken, nil)
	if whoRes.StatusCode != http.StatusOK {
		t.Fatalf("whoami with oauth2 access token: status=%d body=%s", whoRes.StatusCode, whoBody)
	}
	var who struct {
		PrincipalID uint64 `json:"principal_id"`
	}
	_ = json.Unmarshal(whoBody, &who)
	if who.PrincipalID != pid {
		t.Fatalf("whoami principal_id = %d, want %d", who.PrincipalID, pid)
	}

	// Refresh: rotates.
	refreshForm := url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {tok.RefreshToken},
		"client_id": {"herold-android"},
	}
	refreshRes, refreshBody := h.doRequestForm("POST", "/oauth2/token", refreshForm)
	if refreshRes.StatusCode != http.StatusOK {
		t.Fatalf("refresh: status=%d body=%s", refreshRes.StatusCode, refreshBody)
	}
	var refreshed struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(refreshBody, &refreshed); err != nil {
		t.Fatalf("unmarshal refresh response: %v: %s", err, refreshBody)
	}
	if refreshed.RefreshToken == tok.RefreshToken || refreshed.AccessToken == tok.AccessToken {
		t.Fatalf("refresh did not rotate: %+v", refreshed)
	}
	// The rotated access token still authenticates.
	who2Res, who2Body := h.doRequest("GET", "/api/v1/auth/whoami", refreshed.AccessToken, nil)
	if who2Res.StatusCode != http.StatusOK {
		t.Fatalf("whoami with rotated access token: status=%d body=%s", who2Res.StatusCode, who2Body)
	}

	// Replay of the OLD refresh token is rejected (invalid_grant) --
	// reuse detection has revoked the whole chain.
	replayRes, replayBody := h.doRequestForm("POST", "/oauth2/token", refreshForm)
	if replayRes.StatusCode != http.StatusBadRequest {
		t.Fatalf("replayed refresh: status=%d body=%s, want 400", replayRes.StatusCode, replayBody)
	}
	var errBody struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(replayBody, &errBody)
	if errBody.Error != "invalid_grant" {
		t.Fatalf("replayed refresh error = %q, want invalid_grant: %s", errBody.Error, replayBody)
	}

	// The reuse-detected revocation also kills the rotated (newest)
	// access token immediately.
	who3Res, _ := h.doRequest("GET", "/api/v1/auth/whoami", refreshed.AccessToken, nil)
	if who3Res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("whoami with revoked-by-reuse access token: status=%d, want 401", who3Res.StatusCode)
	}
}

func TestOAuth2Token_MissingParams(t *testing.T) {
	h := newHarness(t)
	res, buf := h.doRequestForm("POST", "/oauth2/token", url.Values{"grant_type": {"authorization_code"}, "client_id": {"herold-android"}})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", res.StatusCode, buf)
	}
	var errBody struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(buf, &errBody)
	if errBody.Error != "invalid_request" {
		t.Fatalf("error = %q, want invalid_request", errBody.Error)
	}
}

func TestOAuth2Token_UnsupportedGrantType(t *testing.T) {
	h := newHarness(t)
	res, buf := h.doRequestForm("POST", "/oauth2/token", url.Values{"grant_type": {"password"}, "client_id": {"herold-android"}})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", res.StatusCode, buf)
	}
	var errBody struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(buf, &errBody)
	if errBody.Error != "unsupported_grant_type" {
		t.Fatalf("error = %q, want unsupported_grant_type", errBody.Error)
	}
}

func TestOAuth2Token_PKCEMismatchRejected(t *testing.T) {
	h := newHarness(t)
	mustRegisterHTTPAndroidClient(t, h)
	_, adminKey := h.bootstrap("oauth2pkce-admin@example.com")
	const email = "oauth2pkce-user@example.com"
	h.createPrincipal(adminKey, email)

	client := oauthNoRedirectClient(h)
	_, challenge := oauthPKCE(t)
	redirectURI := "net.netzhansa.herold:/oauth2redirect"
	_, csrfCookie, reqField := oauthAuthorizeGet(t, client, h.baseURL, redirectURI, "st", challenge)

	form := url.Values{
		"req": {reqField}, "csrf": {csrfCookie},
		"email": {email}, "password": {"correct-horse-battery-staple"},
	}
	postReq, _ := http.NewRequest("POST", h.baseURL+"/oauth2/authorize", strings.NewReader(form.Encode()))
	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postReq.AddCookie(&http.Cookie{Name: "herold_oauth2_csrf", Value: csrfCookie})
	postRes, _ := client.Do(postReq)
	loc, _ := url.Parse(postRes.Header.Get("Location"))
	postRes.Body.Close()
	code := loc.Query().Get("code")

	wrongVerifier, _ := oauthPKCE(t)
	tokenForm := url.Values{
		"grant_type": {"authorization_code"}, "code": {code},
		"redirect_uri": {redirectURI}, "client_id": {"herold-android"},
		"code_verifier": {wrongVerifier},
	}
	tokenRes, tokenBody := h.doRequestForm("POST", "/oauth2/token", tokenForm)
	if tokenRes.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", tokenRes.StatusCode, tokenBody)
	}
	var errBody struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(tokenBody, &errBody)
	if errBody.Error != "invalid_grant" {
		t.Fatalf("error = %q, want invalid_grant", errBody.Error)
	}
}

// TestOAuth2Authorize_POST_CSRFMismatch asserts a form submission whose
// csrf field does not match the cookie is rejected before any credential
// is checked.
func TestOAuth2Authorize_POST_CSRFMismatch(t *testing.T) {
	h := newHarness(t)
	mustRegisterHTTPAndroidClient(t, h)
	client := oauthNoRedirectClient(h)
	_, challenge := oauthPKCE(t)
	redirectURI := "net.netzhansa.herold:/oauth2redirect"
	_, csrfCookie, reqField := oauthAuthorizeGet(t, client, h.baseURL, redirectURI, "st", challenge)

	form := url.Values{
		"req": {reqField}, "csrf": {"wrong-csrf-value"},
		"email": {"nobody@example.com"}, "password": {"whatever12345"},
	}
	postReq, _ := http.NewRequest("POST", h.baseURL+"/oauth2/authorize", strings.NewReader(form.Encode()))
	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postReq.AddCookie(&http.Cookie{Name: "herold_oauth2_csrf", Value: csrfCookie})
	postRes, err := client.Do(postReq)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer postRes.Body.Close()
	if postRes.StatusCode != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", postRes.StatusCode)
	}
}

// doRequestRaw issues a request with no body via an explicit client
// (needed when the harness's default following-redirects client would
// choke on a custom-scheme Location).
func (h *harness) doRequestRaw(client *http.Client, method, path string) (*http.Response, []byte) {
	h.t.Helper()
	req, err := http.NewRequest(method, h.baseURL+path, nil)
	if err != nil {
		h.t.Fatalf("new request: %v", err)
	}
	res, err := client.Do(req)
	if err != nil {
		h.t.Fatalf("do: %v", err)
	}
	defer res.Body.Close()
	buf, err := io.ReadAll(res.Body)
	if err != nil {
		h.t.Fatalf("read: %v", err)
	}
	return res, buf
}

// doRequestForm issues a form-encoded POST (the OAuth2 token endpoint's
// wire format, distinct from doRequest's JSON body).
func (h *harness) doRequestForm(method, path string, form url.Values) (*http.Response, []byte) {
	h.t.Helper()
	req, err := http.NewRequest(method, h.baseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		h.t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("do: %v", err)
	}
	defer res.Body.Close()
	buf, err := io.ReadAll(res.Body)
	if err != nil {
		h.t.Fatalf("read: %v", err)
	}
	return res, buf
}
