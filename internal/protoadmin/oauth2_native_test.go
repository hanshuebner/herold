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
	"time"

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

// mustEnrollTOTP enrolls and confirms TOTP for pid, returning the shared
// secret so callers can generate valid codes with otpGenerateCode.
func mustEnrollTOTP(t *testing.T, h *harness, pid uint64) string {
	t.Helper()
	ctx := context.Background()
	secret, _, err := h.dir.EnrollTOTP(ctx, directory.PrincipalID(pid))
	if err != nil {
		t.Fatalf("EnrollTOTP: %v", err)
	}
	code, err := otpGenerateCode(secret, h.clk.Now())
	if err != nil {
		t.Fatalf("otpGenerateCode: %v", err)
	}
	if err := h.dir.ConfirmTOTP(ctx, directory.PrincipalID(pid), code); err != nil {
		t.Fatalf("ConfirmTOTP: %v", err)
	}
	return secret
}

// scrapeReqField extracts the hidden "req" field value out of a rendered
// login-form HTML body, or "" if absent.
func scrapeReqField(body []byte) string {
	const marker = `name="req" value="`
	s := string(body)
	i := strings.Index(s, marker)
	if i < 0 {
		return ""
	}
	rest := s[i+len(marker):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// postOAuthAuthorize submits form to POST /oauth2/authorize with the CSRF
// cookie attached, returning the response and its body (never following
// a redirect).
func postOAuthAuthorize(t *testing.T, client *http.Client, baseURL, csrfCookie string, form url.Values) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest("POST", baseURL+"/oauth2/authorize", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "herold_oauth2_csrf", Value: csrfCookie})
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	res.Body.Close()
	return res, body
}

// TestOAuth2Authorize_TOTPStepUp_CodeOnlyForm covers issue #372: once the
// password step succeeds for a TOTP-enrolled principal, the second POST
// needs only totp_code -- the re-rendered form carries no email/password
// fields, and a correct code redeems straight into an authorization code
// without the human retyping anything already proven.
func TestOAuth2Authorize_TOTPStepUp_CodeOnlyForm(t *testing.T) {
	h := newHarness(t)
	mustRegisterHTTPAndroidClient(t, h)
	_, adminKey := h.bootstrap("oauth2totp-admin@example.com")
	const email = "oauth2totp-user@example.com"
	const password = "correct-horse-battery-staple"
	pid := h.createPrincipal(adminKey, email)
	secret := mustEnrollTOTP(t, h, pid)

	client := oauthNoRedirectClient(h)
	_, challenge := oauthPKCE(t)
	redirectURI := "net.netzhansa.herold:/oauth2redirect"

	_, csrfCookie, reqField := oauthAuthorizeGet(t, client, h.baseURL, redirectURI, "state-totp", challenge)

	// Step 1: email + password, no code yet -- the password checks out
	// and TOTP is required, so the response must be the code-only form,
	// not a redirect and not the full form again.
	passRes, passBody := postOAuthAuthorize(t, client, h.baseURL, csrfCookie, url.Values{
		"req": {reqField}, "csrf": {csrfCookie},
		"email": {email}, "password": {password},
	})
	if passRes.StatusCode != http.StatusOK {
		t.Fatalf("password step: status=%d body=%s, want 200 (code-only form)", passRes.StatusCode, passBody)
	}
	if strings.Contains(string(passBody), `name="email"`) || strings.Contains(string(passBody), `name="password"`) {
		t.Fatalf("code-only form still asks for email/password: %s", passBody)
	}
	if !strings.Contains(string(passBody), `name="totp_code"`) {
		t.Fatalf("code-only form missing totp_code field: %s", passBody)
	}
	reqField2 := scrapeReqField(passBody)
	if reqField2 == "" || reqField2 == reqField {
		t.Fatalf("expected a fresh, distinct req field for the code-only step")
	}

	// Step 2: totp_code only -- no email/password submitted at all.
	code, err := otpGenerateCode(secret, h.clk.Now())
	if err != nil {
		t.Fatalf("otpGenerateCode: %v", err)
	}
	codeRes, codeBody := postOAuthAuthorize(t, client, h.baseURL, csrfCookie, url.Values{
		"req": {reqField2}, "csrf": {csrfCookie}, "totp_code": {code},
	})
	if codeRes.StatusCode != http.StatusFound {
		t.Fatalf("code step: status=%d body=%s, want 302", codeRes.StatusCode, codeBody)
	}
	loc, err := url.Parse(codeRes.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	if !strings.HasPrefix(loc.String(), redirectURI) {
		t.Fatalf("Location = %q, want prefix %q", loc.String(), redirectURI)
	}
	if loc.Query().Get("code") == "" {
		t.Fatalf("Location = %q, missing code", loc.String())
	}
	if loc.Query().Get("state") != "state-totp" {
		t.Fatalf("Location state = %q, want state-totp", loc.Query().Get("state"))
	}
}

// TestOAuth2Authorize_TOTPStepUp_WrongCodeRerendersCodeOnlyForm asserts a
// wrong code on the second step re-renders the code-only form -- not the
// full email+password form -- and that the rebound token still accepts
// the correct code afterwards.
func TestOAuth2Authorize_TOTPStepUp_WrongCodeRerendersCodeOnlyForm(t *testing.T) {
	h := newHarness(t)
	mustRegisterHTTPAndroidClient(t, h)
	_, adminKey := h.bootstrap("oauth2totp-wrong-admin@example.com")
	const email = "oauth2totp-wrong-user@example.com"
	const password = "correct-horse-battery-staple"
	pid := h.createPrincipal(adminKey, email)
	secret := mustEnrollTOTP(t, h, pid)

	client := oauthNoRedirectClient(h)
	_, challenge := oauthPKCE(t)
	redirectURI := "net.netzhansa.herold:/oauth2redirect"
	_, csrfCookie, reqField := oauthAuthorizeGet(t, client, h.baseURL, redirectURI, "state-wrong", challenge)

	_, passBody := postOAuthAuthorize(t, client, h.baseURL, csrfCookie, url.Values{
		"req": {reqField}, "csrf": {csrfCookie},
		"email": {email}, "password": {password},
	})
	reqField2 := scrapeReqField(passBody)
	if reqField2 == "" {
		t.Fatalf("missing req field in code-only form: %s", passBody)
	}

	correct, err := otpGenerateCode(secret, h.clk.Now())
	if err != nil {
		t.Fatalf("otpGenerateCode: %v", err)
	}
	wrong := "000000"
	if wrong == correct {
		wrong = "111111"
	}

	wrongRes, wrongBody := postOAuthAuthorize(t, client, h.baseURL, csrfCookie, url.Values{
		"req": {reqField2}, "csrf": {csrfCookie}, "totp_code": {wrong},
	})
	if wrongRes.StatusCode != http.StatusOK {
		t.Fatalf("wrong code: status=%d body=%s, want 200 (code-only form)", wrongRes.StatusCode, wrongBody)
	}
	if strings.Contains(string(wrongBody), `name="email"`) || strings.Contains(string(wrongBody), `name="password"`) {
		t.Fatalf("wrong-code response should stay code-only, not re-ask for email/password: %s", wrongBody)
	}
	if !strings.Contains(string(wrongBody), `name="totp_code"`) {
		t.Fatalf("wrong-code response missing totp_code field: %s", wrongBody)
	}

	// The rebound token still redeems a code with the correct value.
	reqField3 := scrapeReqField(wrongBody)
	if reqField3 == "" {
		t.Fatalf("missing req field after wrong code: %s", wrongBody)
	}
	rightRes, rightBody := postOAuthAuthorize(t, client, h.baseURL, csrfCookie, url.Values{
		"req": {reqField3}, "csrf": {csrfCookie}, "totp_code": {correct},
	})
	if rightRes.StatusCode != http.StatusFound {
		t.Fatalf("correct code after wrong: status=%d body=%s, want 302", rightRes.StatusCode, rightBody)
	}
}

// TestOAuth2Authorize_TOTPStepUp_ExpiredBindingFallsBackToFullForm
// asserts that once the password-verified binding's own (short) TTL has
// elapsed, submitting the code-only form falls back to the full
// email+password form rather than running an authentication attempt
// against the blank credential fields the code-only form never carried.
func TestOAuth2Authorize_TOTPStepUp_ExpiredBindingFallsBackToFullForm(t *testing.T) {
	h := newHarness(t)
	mustRegisterHTTPAndroidClient(t, h)
	_, adminKey := h.bootstrap("oauth2totp-exp-admin@example.com")
	const email = "oauth2totp-exp-user@example.com"
	const password = "correct-horse-battery-staple"
	pid := h.createPrincipal(adminKey, email)
	mustEnrollTOTP(t, h, pid)

	client := oauthNoRedirectClient(h)
	_, challenge := oauthPKCE(t)
	redirectURI := "net.netzhansa.herold:/oauth2redirect"
	_, csrfCookie, reqField := oauthAuthorizeGet(t, client, h.baseURL, redirectURI, "state-exp", challenge)

	_, passBody := postOAuthAuthorize(t, client, h.baseURL, csrfCookie, url.Values{
		"req": {reqField}, "csrf": {csrfCookie},
		"email": {email}, "password": {password},
	})
	reqField2 := scrapeReqField(passBody)
	if reqField2 == "" {
		t.Fatalf("missing req field in code-only form: %s", passBody)
	}

	// directory.PasswordStepUpTTL is 5 minutes; 6 minutes elapses the
	// binding while staying comfortably inside the outer
	// AuthorizeRequestTTL (10 minutes) so the signed token itself still
	// decodes.
	h.clk.Advance(6 * time.Minute)

	expRes, expBody := postOAuthAuthorize(t, client, h.baseURL, csrfCookie, url.Values{
		"req": {reqField2}, "csrf": {csrfCookie}, "totp_code": {"000000"},
	})
	if expRes.StatusCode != http.StatusOK {
		t.Fatalf("expired step-up: status=%d body=%s, want 200 (full form)", expRes.StatusCode, expBody)
	}
	if !strings.Contains(string(expBody), `name="email"`) || !strings.Contains(string(expBody), `name="password"`) {
		t.Fatalf("expired step-up should fall back to the full email+password form: %s", expBody)
	}
	if strings.Contains(string(expBody), `name="totp_code"`) {
		t.Fatalf("expired step-up should not still show the code-only field: %s", expBody)
	}
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
