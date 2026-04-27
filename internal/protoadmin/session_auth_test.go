package protoadmin_test

// session_auth_test.go covers the new cookie-auth + JSON login/logout
// endpoints (REQ-AUTH-SESSION-REST, REQ-AUTH-CSRF).
//
// Test matrix:
//   - bearer-only auth works as before (no cookie jar needed)
//   - cookie-only GET: succeeds without CSRF header (safe method)
//   - cookie POST without CSRF: 403
//   - cookie POST + matching X-CSRF-Token: 201
//   - cookie POST + wrong X-CSRF-Token: 403
//   - GET with extraneous X-CSRF-Token: 200 (header is fine, just ignored)
//   - end-to-end: login -> cookie -> GET 200 -> logout -> GET 401
//   - TOTP: missing totp_code -> 401 step_up_required=true
//   - TOTP: correct totp_code -> 200

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/protoui"
	"github.com/hanshuebner/herold/internal/testharness"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
)

// testSigningKey is a 32-byte signing key shared between session-auth tests.
// It must be exactly 32 bytes for the length check in protoadmin.
var testSigningKey = []byte("session-auth-test-key-32bytes-xx")

// sessionHarness wraps the existing harness with a cookie-jar-equipped client.
type sessionHarness struct {
	*harness
	cookieJar       *cookiejar.Jar
	cookieJarClient *http.Client
}

func newSessionHarness(t *testing.T) *sessionHarness {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := fakestore.New(fakestore.Options{Clock: clk, BlobDir: t.TempDir()})
	if err != nil {
		t.Fatalf("fakestore.New: %v", err)
	}
	th, _ := testharness.Start(t, testharness.Options{
		Store: fs,
		Clock: clk,
		Listeners: []testharness.ListenerSpec{
			{Name: "admin", Protocol: "admin"},
		},
	})
	dir := directory.New(fs.Meta(), nil, clk, nil)
	rp := directoryoidc.New(fs.Meta(), nil, &http.Client{Timeout: 5 * time.Second}, clk)
	srv := protoadmin.NewServer(fs, dir, rp, nil, clk, protoadmin.Options{
		BootstrapPerWindow:      1,
		BootstrapWindow:         5 * time.Minute,
		RequestsPerMinutePerKey: 100,
		Session: protoui.SessionConfig{
			SigningKey:      testSigningKey,
			CookieName:     "herold_admin_session",
			CSRFCookieName: "herold_admin_csrf",
			TTL:            24 * time.Hour,
			SecureCookies:  false,
		},
	})
	if err := th.AttachAdmin("admin", srv, protoadmin.ListenerModePlain); err != nil {
		t.Fatalf("AttachAdmin: %v", err)
	}
	baseClient, base := th.DialAdminByName(context.Background(), "admin")

	jar, _ := cookiejar.New(nil)
	cookieClient := &http.Client{
		Transport: baseClient.Transport,
		Jar:       jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	h := &harness{
		t: t, h: th, srv: srv, client: baseClient, baseURL: base,
		clk: clk, dir: dir, rp: rp,
	}
	return &sessionHarness{harness: h, cookieJar: jar, cookieJarClient: cookieClient}
}

// bootstrapWithPassword creates the first admin principal via bootstrap and
// returns (email, password, apiKey). The password is the auto-generated one
// returned by the bootstrap endpoint.
func (sh *sessionHarness) bootstrapWithPassword(email string) (string, string, string) {
	sh.t.Helper()
	b, _ := json.Marshal(map[string]any{
		"email":        email,
		"display_name": "Test Admin",
	})
	req, _ := http.NewRequest("POST", sh.baseURL+"/api/v1/bootstrap", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	res, err := sh.cookieJarClient.Do(req)
	if err != nil {
		sh.t.Fatalf("bootstrap: %v", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusCreated {
		sh.t.Fatalf("bootstrap: status=%d body=%s", res.StatusCode, raw)
	}
	var out struct {
		InitialPassword string `json:"initial_password"`
		InitialAPIKey   string `json:"initial_api_key"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		sh.t.Fatalf("bootstrap unmarshal: %v body=%s", err, raw)
	}
	return email, out.InitialPassword, out.InitialAPIKey
}

// doLogin posts to /api/v1/auth/login and returns (statusCode, responseBody).
func (sh *sessionHarness) doLogin(email, password string, extra map[string]any) (int, map[string]any) {
	sh.t.Helper()
	body := map[string]any{
		"email":    email,
		"password": password,
	}
	for k, v := range extra {
		body[k] = v
	}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", sh.baseURL+"/api/v1/auth/login", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	res, err := sh.cookieJarClient.Do(req)
	if err != nil {
		sh.t.Fatalf("login: %v", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return res.StatusCode, out
}

// csrfToken reads herold_admin_csrf from the cookie jar.
func (sh *sessionHarness) csrfToken() string {
	sh.t.Helper()
	u, _ := url.Parse(sh.baseURL + "/")
	for _, c := range sh.cookieJar.Cookies(u) {
		if c.Name == "herold_admin_csrf" {
			return c.Value
		}
	}
	sh.t.Fatal("herold_admin_csrf not in cookie jar")
	return ""
}

// sessionCookiePresent reports whether herold_admin_session is in the jar.
func (sh *sessionHarness) sessionCookiePresent() bool {
	u, _ := url.Parse(sh.baseURL + "/")
	for _, c := range sh.cookieJar.Cookies(u) {
		if c.Name == "herold_admin_session" {
			return true
		}
	}
	return false
}

// doWithCookie executes a request through the cookie-jar client, optionally
// adding X-CSRF-Token when csrfTok is non-empty.
func (sh *sessionHarness) doWithCookie(method, path string, body any, csrfTok string) (int, []byte) {
	sh.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, sh.baseURL+path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if csrfTok != "" {
		req.Header.Set("X-CSRF-Token", csrfTok)
	}
	res, err := sh.cookieJarClient.Do(req)
	if err != nil {
		sh.t.Fatalf("doWithCookie %s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, raw
}

// -----------------------------------------------------------------------
// Tests
// -----------------------------------------------------------------------

// TestSessionAuth_BearerOnlyStillWorks confirms Bearer auth is unaffected
// by the new session-cookie path.
func TestSessionAuth_BearerOnlyStillWorks(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	_, _, key := sh.bootstrapWithPassword("bearer@example.com")

	res, _ := sh.doRequest("GET", "/api/v1/principals", key, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("bearer GET: status=%d, want 200", res.StatusCode)
	}
}

// TestSessionAuth_Login_SetsCookiesAndReturnsScopes drives the full
// JSON login flow and asserts the response shape + cookie attributes.
func TestSessionAuth_Login_SetsCookiesAndReturnsScopes(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _ := sh.bootstrapWithPassword("login-scope@example.com")

	code, body := sh.doLogin(email, password, nil)
	if code != http.StatusOK {
		t.Fatalf("login: status=%d body=%v", code, body)
	}
	// Response must carry principal_id, email, scopes.
	if body["principal_id"] == nil {
		t.Fatalf("login response missing principal_id: %v", body)
	}
	if body["email"] == nil {
		t.Fatalf("login response missing email: %v", body)
	}
	scopes, _ := body["scopes"].([]interface{})
	if len(scopes) == 0 {
		t.Fatalf("login response scopes empty: %v", body)
	}
	found := false
	for _, s := range scopes {
		if s == "admin" {
			found = true
		}
	}
	if !found {
		t.Fatalf("login scopes %v missing admin", scopes)
	}
	// Cookies must be present in the jar.
	if !sh.sessionCookiePresent() {
		t.Fatalf("herold_admin_session not set after login")
	}
	csrf := sh.csrfToken()
	if csrf == "" {
		t.Fatalf("herold_admin_csrf not set after login")
	}
}

// TestSessionAuth_CookieGET_NoCSRF_Succeeds confirms that a GET
// authenticated via cookie needs no X-CSRF-Token (safe method).
func TestSessionAuth_CookieGET_NoCSRF_Succeeds(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _ := sh.bootstrapWithPassword("cookie-get@example.com")

	code, _ := sh.doLogin(email, password, nil)
	if code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}

	getCode, _ := sh.doWithCookie("GET", "/api/v1/principals", nil, "")
	if getCode != http.StatusOK {
		t.Fatalf("cookie GET without CSRF: status=%d, want 200", getCode)
	}
}

// TestSessionAuth_CookiePOST_WithoutCSRF_Returns403 asserts POST with
// cookie auth but no X-CSRF-Token gets 403 (REQ-AUTH-CSRF).
func TestSessionAuth_CookiePOST_WithoutCSRF_Returns403(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _ := sh.bootstrapWithPassword("no-csrf@example.com")

	code, _ := sh.doLogin(email, password, nil)
	if code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}

	postCode, body := sh.doWithCookie("POST", "/api/v1/principals", map[string]any{
		"email":    "new@example.com",
		"password": "hunter2hunter2hunter2",
	}, "" /* no CSRF */)
	if postCode != http.StatusForbidden {
		t.Fatalf("POST without CSRF: status=%d body=%s, want 403", postCode, body)
	}
}

// TestSessionAuth_CookiePOST_WithCSRF_Succeeds asserts POST with cookie
// auth + matching X-CSRF-Token passes the gate (REQ-AUTH-CSRF).
func TestSessionAuth_CookiePOST_WithCSRF_Succeeds(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _ := sh.bootstrapWithPassword("with-csrf@example.com")

	code, _ := sh.doLogin(email, password, nil)
	if code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}
	csrf := sh.csrfToken()

	postCode, body := sh.doWithCookie("POST", "/api/v1/principals", map[string]any{
		"email":    "new-via-cookie@example.com",
		"password": "hunter2hunter2hunter2",
	}, csrf)
	if postCode != http.StatusCreated {
		t.Fatalf("POST with CSRF: status=%d body=%s, want 201", postCode, body)
	}
}

// TestSessionAuth_CookiePOST_CSRFMismatch_Returns403 asserts POST with
// wrong X-CSRF-Token value gets 403 (constant-time compare, REQ-AUTH-CSRF).
func TestSessionAuth_CookiePOST_CSRFMismatch_Returns403(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _ := sh.bootstrapWithPassword("csrf-mismatch@example.com")

	code, _ := sh.doLogin(email, password, nil)
	if code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}

	postCode, body := sh.doWithCookie("POST", "/api/v1/principals", map[string]any{
		"email":    "new@example.com",
		"password": "hunter2hunter2hunter2",
	}, "totally-wrong-csrf-value")
	if postCode != http.StatusForbidden {
		t.Fatalf("POST with wrong CSRF: status=%d body=%s, want 403", postCode, body)
	}
}

// TestSessionAuth_GetWithExtraCSRF_OK asserts that passing X-CSRF-Token on a
// GET is harmless (the CSRF check is skipped for safe methods per REQ-AUTH-CSRF).
func TestSessionAuth_GetWithExtraCSRF_OK(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _ := sh.bootstrapWithPassword("extra-csrf@example.com")

	code, _ := sh.doLogin(email, password, nil)
	if code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}
	csrf := sh.csrfToken()

	// Both with and without CSRF should return 200 on a GET.
	for _, tok := range []string{"", csrf, "bogus-value"} {
		getCode, _ := sh.doWithCookie("GET", "/api/v1/principals", nil, tok)
		if getCode != http.StatusOK {
			t.Fatalf("GET with csrfTok=%q: status=%d, want 200", tok, getCode)
		}
	}
}

// TestSessionAuth_Logout_ClearsCookiesThenReturns401 is the full lifecycle:
// login -> cookie in jar -> GET 200 -> logout -> GET 401.
func TestSessionAuth_Logout_ClearsCookiesThenReturns401(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _ := sh.bootstrapWithPassword("logout-user@example.com")

	// Login.
	code, _ := sh.doLogin(email, password, nil)
	if code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}
	if !sh.sessionCookiePresent() {
		t.Fatalf("session cookie not set after login")
	}

	// GET succeeds with the session cookie.
	getCode, _ := sh.doWithCookie("GET", "/api/v1/principals", nil, "")
	if getCode != http.StatusOK {
		t.Fatalf("GET after login: %d, want 200", getCode)
	}

	// Logout (cookie-authenticated, mutating -- provide CSRF).
	csrf := sh.csrfToken()
	logoutCode, _ := sh.doWithCookie("POST", "/api/v1/auth/logout", nil, csrf)
	if logoutCode != http.StatusNoContent {
		t.Fatalf("logout: status=%d, want 204", logoutCode)
	}

	// Cookie jar should no longer carry a valid session.
	if sh.sessionCookiePresent() {
		t.Fatalf("session cookie still present after logout")
	}

	// Subsequent GET should be 401.
	getCodeAfter, _ := sh.doWithCookie("GET", "/api/v1/principals", nil, "")
	if getCodeAfter != http.StatusUnauthorized {
		t.Fatalf("GET after logout: status=%d, want 401", getCodeAfter)
	}
}

// TestSessionAuth_Login_BadCredentials_Returns401 asserts wrong password
// gives 401. We don't differentiate wrong email from wrong password.
func TestSessionAuth_Login_BadCredentials_Returns401(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	// Don't bootstrap -- use a non-existent email.
	code, _ := sh.doLogin("nobody@example.com", "wrongpassword", nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("bad creds: status=%d, want 401", code)
	}
}

// TestSessionAuth_TOTP_StepUpRequired asserts that a TOTP-enabled principal
// without totp_code in the request gets 401 with step_up_required=true.
func TestSessionAuth_TOTP_StepUpRequired(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _ := sh.bootstrapWithPassword("totp-step-up@example.com")

	// Enable TOTP on the principal.
	pid, err := sh.dir.Authenticate(context.Background(), email, password)
	if err != nil {
		t.Fatalf("Authenticate (get pid): %v", err)
	}
	secret, _, err := sh.dir.EnrollTOTP(context.Background(), pid)
	if err != nil {
		t.Fatalf("EnrollTOTP: %v", err)
	}
	code, err := otpGenerateCode(secret, sh.clk.Now())
	if err != nil {
		t.Fatalf("otpGenerateCode: %v", err)
	}
	if err := sh.dir.ConfirmTOTP(context.Background(), pid, code); err != nil {
		t.Fatalf("ConfirmTOTP: %v", err)
	}

	// Login without totp_code.
	loginCode, loginBody := sh.doLogin(email, password, nil)
	if loginCode != http.StatusUnauthorized {
		t.Fatalf("login without TOTP: status=%d, want 401", loginCode)
	}
	if loginBody["step_up_required"] != true {
		t.Fatalf("step_up_required not true: %v", loginBody)
	}
}

// TestSessionAuth_TOTP_WithCodeSucceeds asserts that a TOTP-enabled principal
// supplying a valid totp_code gets 200.
func TestSessionAuth_TOTP_WithCodeSucceeds(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _ := sh.bootstrapWithPassword("totp-ok@example.com")

	pid, err := sh.dir.Authenticate(context.Background(), email, password)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	secret, _, err := sh.dir.EnrollTOTP(context.Background(), pid)
	if err != nil {
		t.Fatalf("EnrollTOTP: %v", err)
	}
	enrollCode, err := otpGenerateCode(secret, sh.clk.Now())
	if err != nil {
		t.Fatalf("enrollCode: %v", err)
	}
	if err := sh.dir.ConfirmTOTP(context.Background(), pid, enrollCode); err != nil {
		t.Fatalf("ConfirmTOTP: %v", err)
	}

	// Advance by 1 second to avoid TOTP re-use window.
	sh.clk.Advance(time.Second)
	loginCode, err := otpGenerateCode(secret, sh.clk.Now())
	if err != nil {
		t.Fatalf("loginCode: %v", err)
	}

	statusCode, body := sh.doLogin(email, password, map[string]any{
		"totp_code": loginCode,
	})
	if statusCode != http.StatusOK {
		t.Fatalf("login with TOTP code: status=%d body=%v", statusCode, body)
	}
	if !sh.sessionCookiePresent() {
		t.Fatalf("session cookie not set after TOTP login")
	}
}
