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
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/authsession"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite/sqlitetest"
	"github.com/hanshuebner/herold/internal/testharness"
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
	return newSessionHarnessWithIdleTTL(t, 0)
}

// newSessionHarnessWithIdleTTL is the slice-3 variant that lets a test
// configure the admin-listener idle gate. Pass idleTTL=0 to disable the
// gate (current behaviour for tests that don't exercise it).
func newSessionHarnessWithIdleTTL(t *testing.T, idleTTL time.Duration) *sessionHarness {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs := sqlitetest.Open(t, clk)
	th, _ := testharness.Start(t, testharness.Options{
		Store: fs,
		Clock: clk,
		Listeners: []testharness.ListenerSpec{
			{Name: "admin", Protocol: "http"},
		},
	})
	dir := directory.New(fs.Meta(), nil, clk, nil)
	rp := directoryoidc.New(fs.Meta(), nil, &http.Client{Timeout: 5 * time.Second}, clk)
	srv := protoadmin.NewServer(fs, dir, rp, nil, clk, protoadmin.Options{
		BootstrapPerWindow:      1,
		BootstrapWindow:         5 * time.Minute,
		RequestsPerMinutePerKey: 100,
		Session: authsession.SessionConfig{
			SigningKey:     testSigningKey,
			CookieName:     "herold_public_session",
			CSRFCookieName: "herold_public_csrf",
			TTL:            24 * time.Hour,
			IdleTTL:        idleTTL,
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

// bootstrapAdminAndEnrollTOTP creates the first admin principal via
// bootstrap AND enrolls + confirms TOTP for it, returning the secret
// so callers can mint fresh codes for doLoginWithTOTP. After slice 4
// of issue #12, admin principals MUST have TOTP enrolled before
// password sign-in is permitted, so every test that exercises the
// password-login + cookie path needs this helper. The original
// bootstrapWithPassword is retained for the negative-path tests that
// specifically verify the no-TOTP rejection behaviour.
//
// Returns (email, password, apiKey, totpSecret).
func (sh *sessionHarness) bootstrapAdminAndEnrollTOTP(email string) (string, string, string, string) {
	sh.t.Helper()
	em, pw, key := sh.bootstrapWithPassword(email)
	ctx := context.Background()
	pid, err := sh.dir.Authenticate(ctx, em, pw)
	if err != nil {
		sh.t.Fatalf("Authenticate (for TOTP enrollment): %v", err)
	}
	secret, _, err := sh.dir.EnrollTOTP(ctx, pid)
	if err != nil {
		sh.t.Fatalf("EnrollTOTP: %v", err)
	}
	code, err := otpGenerateCode(secret, sh.clk.Now())
	if err != nil {
		sh.t.Fatalf("otpGenerateCode (enrollment): %v", err)
	}
	if err := sh.dir.ConfirmTOTP(ctx, pid, code); err != nil {
		sh.t.Fatalf("ConfirmTOTP: %v", err)
	}
	// Advance one second so the next code generated for login is a fresh
	// time-step rather than the one just used for enrollment. The
	// directory's TOTP verifier has no anti-replay tracking (issue #228
	// analysis), so this isn't strictly required for correctness today,
	// but it keeps every test independent of that gap.
	sh.clk.Advance(time.Second)
	return em, pw, key, secret
}

// doLoginWithTOTP posts {email, password, totp_code} to /api/v1/auth/login,
// generating totp_code from totpSecret at the harness's current clock
// (REQ-AUTH-JSON-LOGIN, REQ-AUTH-42, issue #228: TOTP is evaluated at login
// for an enrolled principal). extra is merged in after totp_code so a
// caller can override it (e.g. to submit a deliberately wrong code) or add
// unrelated fields.
func (sh *sessionHarness) doLoginWithTOTP(email, password, totpSecret string, extra map[string]any) (int, map[string]any) {
	sh.t.Helper()
	code, err := otpGenerateCode(totpSecret, sh.clk.Now())
	if err != nil {
		sh.t.Fatalf("otpGenerateCode (login): %v", err)
	}
	body := map[string]any{"totp_code": code}
	for k, v := range extra {
		body[k] = v
	}
	return sh.doLogin(email, password, body)
}

// doStepUp posts to POST /api/v1/auth/step-up with a fresh TOTP code and
// returns (statusCode, responseBody). The CSRF token must already be in the
// cookie jar (i.e. the caller must have logged in first). Uses totpSecret at
// the harness's current clock to generate the code.
func (sh *sessionHarness) doStepUp(totpSecret string) (int, map[string]any) {
	sh.t.Helper()
	csrf := sh.csrfToken()
	code, err := otpGenerateCode(totpSecret, sh.clk.Now())
	if err != nil {
		sh.t.Fatalf("otpGenerateCode (step-up): %v", err)
	}
	statusCode, raw := sh.doWithCookie("POST", "/api/v1/auth/step-up", map[string]any{
		"totp_code": code,
	}, csrf)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return statusCode, out
}

// bootstrapWithPassword creates the first admin principal via bootstrap and
// returns (email, password, apiKey). The password is the auto-generated one
// returned by the bootstrap endpoint. After slice 4 of issue #12, the
// principal carries the admin flag but NO TOTP enrollment, so calling
// /api/v1/auth/login on it returns 401 step-up-required. Use
// bootstrapAdminAndEnrollTOTP for the common case; this remains the
// no-TOTP factory for the negative-path tests.
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

// csrfToken reads herold_public_csrf from the cookie jar.
func (sh *sessionHarness) csrfToken() string {
	sh.t.Helper()
	u, _ := url.Parse(sh.baseURL + "/")
	for _, c := range sh.cookieJar.Cookies(u) {
		if c.Name == "herold_public_csrf" {
			return c.Value
		}
	}
	sh.t.Fatal("herold_public_csrf not in cookie jar")
	return ""
}

// sessionCookiePresent reports whether herold_public_session is in the jar.
func (sh *sessionHarness) sessionCookiePresent() bool {
	u, _ := url.Parse(sh.baseURL + "/")
	for _, c := range sh.cookieJar.Cookies(u) {
		if c.Name == "herold_public_session" {
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
// Login issues end-user scopes only (REQ-AUTH-SCOPE-01); admin scope is
// never baked into the cookie.
func TestSessionAuth_Login_SetsCookiesAndReturnsScopes(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("login-scope@example.com")

	code, body := sh.doLoginWithTOTP(email, password, secret, nil)
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
	// Login MUST NOT include admin scope (REQ-AUTH-SCOPE-01, issue #79).
	for _, s := range scopes {
		if s == "admin" {
			t.Fatalf("login scopes must not include admin (REQ-AUTH-SCOPE-01): %v", scopes)
		}
	}
	// Cookies must be present in the jar.
	if !sh.sessionCookiePresent() {
		t.Fatalf("herold_public_session not set after login")
	}
	csrf := sh.csrfToken()
	if csrf == "" {
		t.Fatalf("herold_public_csrf not set after login")
	}
}

// TestSessionAuth_CookieGET_NoCSRF_Succeeds confirms that a GET
// authenticated via cookie needs no X-CSRF-Token (safe method).
func TestSessionAuth_CookieGET_NoCSRF_Succeeds(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("cookie-get@example.com")

	code, _ := sh.doLoginWithTOTP(email, password, secret, nil)
	if code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}

	// Use an auth1-gated endpoint (whoami) so elevation is not required.
	getCode, _ := sh.doWithCookie("GET", "/api/v1/auth/whoami", nil, "")
	if getCode != http.StatusOK {
		t.Fatalf("cookie GET without CSRF: status=%d, want 200", getCode)
	}
}

// TestSessionAuth_CookiePOST_WithoutCSRF_Returns403 asserts POST with
// cookie auth but no X-CSRF-Token gets 403 (REQ-AUTH-CSRF). The CSRF gate
// runs inside requireAuth before requireElevation, so it fires even on
// admin endpoints regardless of whether an elevation record exists.
func TestSessionAuth_CookiePOST_WithoutCSRF_Returns403(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("no-csrf@example.com")

	code, _ := sh.doLoginWithTOTP(email, password, secret, nil)
	if code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}

	// POST to the step-up endpoint (auth1-gated) without CSRF: must get 403.
	postCode, body := sh.doWithCookie("POST", "/api/v1/auth/step-up", map[string]any{
		"totp_code": "000000",
	}, "" /* no CSRF */)
	if postCode != http.StatusForbidden {
		t.Fatalf("POST without CSRF: status=%d body=%s, want 403", postCode, body)
	}
}

// TestSessionAuth_CookiePOST_WithCSRF_Succeeds asserts POST with cookie
// auth + matching X-CSRF-Token + valid elevation passes the gate (REQ-AUTH-CSRF,
// REQ-AUTH-74). Admin endpoints now require step-up in addition to CSRF.
func TestSessionAuth_CookiePOST_WithCSRF_Succeeds(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("with-csrf@example.com")

	code, _ := sh.doLoginWithTOTP(email, password, secret, nil)
	if code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}
	// Step up to get admin elevation before POSTing to an admin endpoint.
	sh.clk.Advance(time.Second) // avoid TOTP re-use window from enrollment
	if sc, _ := sh.doStepUp(secret); sc != http.StatusOK {
		t.Fatalf("step-up: status=%d", sc)
	}
	csrf := sh.csrfToken()

	postCode, body := sh.doWithCookie("POST", "/api/v1/principals", map[string]any{
		"email":    "new-via-cookie@example.com",
		"password": "hunter2hunter2hunter2",
	}, csrf)
	if postCode != http.StatusCreated {
		t.Fatalf("POST with CSRF + elevation: status=%d body=%s, want 201", postCode, body)
	}
}

// TestSessionAuth_CookiePOST_CSRFMismatch_Returns403 asserts POST with
// wrong X-CSRF-Token value gets 403 (constant-time compare, REQ-AUTH-CSRF).
func TestSessionAuth_CookiePOST_CSRFMismatch_Returns403(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("csrf-mismatch@example.com")

	code, _ := sh.doLoginWithTOTP(email, password, secret, nil)
	if code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}

	// POST to step-up (auth1) with a wrong CSRF value; CSRF check fires first.
	postCode, body := sh.doWithCookie("POST", "/api/v1/auth/step-up", map[string]any{
		"totp_code": "000000",
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
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("extra-csrf@example.com")

	code, _ := sh.doLoginWithTOTP(email, password, secret, nil)
	if code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}
	csrf := sh.csrfToken()

	// Both with and without CSRF should return 200 on a GET to an auth1 endpoint.
	for _, tok := range []string{"", csrf, "bogus-value"} {
		getCode, _ := sh.doWithCookie("GET", "/api/v1/auth/whoami", nil, tok)
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
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("logout-user@example.com")

	// Login.
	code, _ := sh.doLoginWithTOTP(email, password, secret, nil)
	if code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}
	if !sh.sessionCookiePresent() {
		t.Fatalf("session cookie not set after login")
	}

	// GET whoami succeeds with the session cookie (auth1, no elevation needed).
	getCode, _ := sh.doWithCookie("GET", "/api/v1/auth/whoami", nil, "")
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

	// Subsequent whoami should be 401.
	getCodeAfter, _ := sh.doWithCookie("GET", "/api/v1/auth/whoami", nil, "")
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

// TestSessionAuth_Login_RequiresTOTPWhenEnrolled asserts the core property of
// issue #228: a TOTP-enrolled principal cannot obtain a session on password
// alone. Missing and wrong codes both refuse the session with the identical
// step_up_required 401 shape (no oracle distinguishing "no code" from "wrong
// code"); the correct code succeeds and elevation_expires_at is populated
// (REQ-AUTH-JSON-LOGIN, REQ-AUTH-42, REQ-AUTH-74(a)).
func TestSessionAuth_Login_RequiresTOTPWhenEnrolled(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _ := sh.bootstrapWithPassword("totp-login-required@example.com")

	// Enroll TOTP.
	pid, err := sh.dir.Authenticate(context.Background(), email, password)
	if err != nil {
		t.Fatalf("Authenticate (get pid): %v", err)
	}
	secret, _, err := sh.dir.EnrollTOTP(context.Background(), pid)
	if err != nil {
		t.Fatalf("EnrollTOTP: %v", err)
	}
	enrollCode, err := otpGenerateCode(secret, sh.clk.Now())
	if err != nil {
		t.Fatalf("otpGenerateCode: %v", err)
	}
	if err := sh.dir.ConfirmTOTP(context.Background(), pid, enrollCode); err != nil {
		t.Fatalf("ConfirmTOTP: %v", err)
	}
	sh.clk.Advance(time.Second)

	// 1. No totp_code field: refused, no session.
	missingCode, missingBody := sh.doLogin(email, password, nil)
	if missingCode != http.StatusUnauthorized {
		t.Fatalf("login without totp_code: status=%d body=%v, want 401", missingCode, missingBody)
	}
	if missingBody["step_up_required"] != true {
		t.Errorf("missing-code response step_up_required=%v, want true: %v", missingBody["step_up_required"], missingBody)
	}
	if sh.sessionCookiePresent() {
		t.Fatal("session cookie set after login without totp_code")
	}

	// 2. Wrong totp_code: refused, no session, identical response shape to (1).
	wrongCode, wrongBody := sh.doLogin(email, password, map[string]any{"totp_code": "000000"})
	if wrongCode != http.StatusUnauthorized {
		t.Fatalf("login with wrong totp_code: status=%d body=%v, want 401", wrongCode, wrongBody)
	}
	if wrongBody["step_up_required"] != true {
		t.Errorf("wrong-code response step_up_required=%v, want true: %v", wrongBody["step_up_required"], wrongBody)
	}
	if wrongBody["type"] != missingBody["type"] {
		t.Errorf("wrong-code type=%v differs from missing-code type=%v; both must be indistinguishable", wrongBody["type"], missingBody["type"])
	}
	if sh.sessionCookiePresent() {
		t.Fatal("session cookie set after login with wrong totp_code")
	}

	// 3. Correct totp_code: succeeds, session issued, elevation created
	// immediately (REQ-AUTH-74(a)).
	code, err := otpGenerateCode(secret, sh.clk.Now())
	if err != nil {
		t.Fatalf("otpGenerateCode (login): %v", err)
	}
	okCode, okBody := sh.doLogin(email, password, map[string]any{"totp_code": code})
	if okCode != http.StatusOK {
		t.Fatalf("login with correct totp_code: status=%d body=%v, want 200", okCode, okBody)
	}
	if !sh.sessionCookiePresent() {
		t.Fatal("session cookie not set after login with correct totp_code")
	}
	if okBody["elevation_expires_at"] == nil {
		t.Errorf("elevation_expires_at missing/nil after TOTP-gated login: %v", okBody)
	}
}

// TestSessionAuth_Login_TOTPReplay_Gap documents the current, deliberate gap
// (issue #228 acceptance): directory.VerifyTOTP has no anti-replay tracking,
// so the same code accepted once is still accepted again within its validity
// window. This test pins that behaviour rather than silently relying on it;
// if replay prevention is added later, this test starts failing and must be
// updated to assert the second attempt is refused.
func TestSessionAuth_Login_TOTPReplay_Gap(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("totp-login-replay@example.com")

	code, err := otpGenerateCode(secret, sh.clk.Now())
	if err != nil {
		t.Fatalf("otpGenerateCode: %v", err)
	}

	first, firstBody := sh.doLogin(email, password, map[string]any{"totp_code": code})
	if first != http.StatusOK {
		t.Fatalf("first login: status=%d body=%v, want 200", first, firstBody)
	}

	// Fresh cookie jar simulating a second, independent login attempt
	// replaying the exact same code within its validity window.
	sh.cookieJar, _ = cookiejar.New(nil)
	sh.cookieJarClient.Jar = sh.cookieJar
	second, secondBody := sh.doLogin(email, password, map[string]any{"totp_code": code})
	if second != http.StatusOK {
		t.Fatalf("replayed login: status=%d body=%v -- if this now fails with 401, "+
			"anti-replay protection was added; update this test to assert refusal", second, secondBody)
	}
}

// TestSessionAuth_Login_TOTPBruteForce_RateLimited asserts the brute-force
// bound on TOTP guesses at login: directory.VerifyTOTP rate-limits by
// (principal, source) -- the same bucket used by step-up and the
// device-token/OAuth2 paths -- so repeated wrong codes against a login whose
// password is already known lock out after rlMaxFailures (5) within
// rlWindow (1 minute), returning 429 rather than continuing to accept
// guesses (issue #228 acceptance: "brute-force bound on code attempts").
func TestSessionAuth_Login_TOTPBruteForce_RateLimited(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _, _ := sh.bootstrapAdminAndEnrollTOTP("totp-login-bruteforce@example.com")

	// 5 wrong codes within the rate limiter's window.
	for i := 0; i < 5; i++ {
		code, body := sh.doLogin(email, password, map[string]any{"totp_code": "000000"})
		if code != http.StatusUnauthorized {
			t.Fatalf("wrong-code attempt %d: status=%d body=%v, want 401", i, code, body)
		}
	}

	// The 6th attempt, even with a correct password, is locked out.
	code, body := sh.doLogin(email, password, map[string]any{"totp_code": "000000"})
	if code != http.StatusTooManyRequests {
		t.Fatalf("6th attempt: status=%d body=%v, want 429", code, body)
	}
	if sh.sessionCookiePresent() {
		t.Fatal("session cookie set despite TOTP rate-limit lockout")
	}
}

// TestSessionAuth_Login_Succeeds asserts that login with correct email+password
// succeeds regardless of TOTP enrollment state (REQ-AUTH-JSON-LOGIN, issue #79).
func TestSessionAuth_Login_Succeeds(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _ := sh.bootstrapWithPassword("login-ok@example.com")

	statusCode, body := sh.doLogin(email, password, nil)
	if statusCode != http.StatusOK {
		t.Fatalf("login: status=%d body=%v", statusCode, body)
	}
	if !sh.sessionCookiePresent() {
		t.Fatalf("session cookie not set after login")
	}
}

// TestSessionAuth_Login_BadCredentials_AuditsFailure asserts that a wrong
// password lands a structured failure record in the audit log
// (REQ-ADM-300, REQ-ADM-303). Without this, brute-force attempts are
// invisible to operators reading GET /api/v1/audit.
func TestSessionAuth_Login_BadCredentials_AuditsFailure(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, _, _ := sh.bootstrapWithPassword("auditfail@example.com")

	if code, _ := sh.doLogin(email, "wrong-password", nil); code != http.StatusUnauthorized {
		t.Fatalf("bad-creds login: status=%d, want 401", code)
	}

	entries, err := sh.h.Store.Meta().ListAuditLog(context.Background(),
		store.AuditLogFilter{Action: "auth.login"})
	if err != nil {
		t.Fatalf("ListAuditLog: %v", err)
	}
	var found *store.AuditLogEntry
	for i := range entries {
		if entries[i].Outcome == store.OutcomeFailure &&
			entries[i].Subject == "email:"+email {
			found = &entries[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("no failure audit record for bad-creds login; entries=%+v", entries)
	}
	if found.Metadata["attempted_email"] != email {
		t.Errorf("metadata.attempted_email=%q, want %q",
			found.Metadata["attempted_email"], email)
	}
}

// TestSessionAuth_Logout_AuditRecordCarriesPrincipal asserts the logout
// audit record's subject identifies the calling principal, not the empty
// string -- REQ-ADM-300 requires {actor, action, resource} per record.
func TestSessionAuth_Logout_AuditRecordCarriesPrincipal(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("auditlogout@example.com")

	if code, _ := sh.doLoginWithTOTP(email, password, secret, nil); code != http.StatusOK {
		t.Fatalf("login: status=%d", code)
	}
	if code, _ := sh.doWithCookie("POST", "/api/v1/auth/logout", nil, sh.csrfToken()); code != http.StatusNoContent {
		t.Fatalf("logout: status=%d", code)
	}

	entries, err := sh.h.Store.Meta().ListAuditLog(context.Background(),
		store.AuditLogFilter{Action: "auth.logout"})
	if err != nil {
		t.Fatalf("ListAuditLog: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("expected one auth.logout entry, got 0")
	}
	last := entries[len(entries)-1]
	want := "principal:" + email
	if last.Subject != want {
		t.Errorf("logout audit subject=%q, want %q", last.Subject, want)
	}
	if last.ActorKind != store.ActorPrincipal {
		t.Errorf("logout audit actor_kind=%q, want %q", last.ActorKind, store.ActorPrincipal)
	}
}

// TestWhoAmI_WithValidSession returns 200 + principal info after login.
func TestWhoAmI_WithValidSession(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("whoami-ok@example.com")

	if code, _ := sh.doLoginWithTOTP(email, password, secret, nil); code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}

	code, raw := sh.doWithCookie("GET", "/api/v1/auth/whoami", nil, "")
	if code != http.StatusOK {
		t.Fatalf("whoami: status=%d body=%s, want 200", code, raw)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("whoami unmarshal: %v", err)
	}
	if out["principal_id"] == nil {
		t.Fatalf("whoami missing principal_id: %v", out)
	}
	if out["email"] != email {
		t.Fatalf("whoami email=%v, want %q", out["email"], email)
	}
	scopes, _ := out["scopes"].([]interface{})
	if len(scopes) == 0 {
		t.Fatalf("whoami scopes empty: %v", out)
	}
	// Login issues end-user scopes only; admin scope must NOT appear in a
	// cookie session's whoami response without a step-up (REQ-AUTH-SCOPE-01).
	for _, s := range scopes {
		if s == "admin" {
			t.Fatalf("whoami scopes must not include admin without elevation: %v", scopes)
		}
	}
	// elevation_expires_at must be present in the response (null when no elevation).
	if _, ok := out["elevation_expires_at"]; !ok {
		t.Fatalf("whoami response missing elevation_expires_at key: %v", out)
	}
}

// TestWhoAmI_WithoutCredentials returns 401.
func TestWhoAmI_WithoutCredentials(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	// No bootstrap, no login, no credentials.
	code, _ := sh.doWithCookie("GET", "/api/v1/auth/whoami", nil, "")
	if code != http.StatusUnauthorized {
		t.Fatalf("whoami without creds: status=%d, want 401", code)
	}
}

// TestWhoAmI_AfterLogout returns 401 because the cookie jar is cleared.
func TestWhoAmI_AfterLogout(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("whoami-logout@example.com")

	if code, _ := sh.doLoginWithTOTP(email, password, secret, nil); code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}
	// whoami OK while session is live.
	if code, _ := sh.doWithCookie("GET", "/api/v1/auth/whoami", nil, ""); code != http.StatusOK {
		t.Fatalf("whoami before logout: %d, want 200", code)
	}
	// Logout.
	csrf := sh.csrfToken()
	if code, _ := sh.doWithCookie("POST", "/api/v1/auth/logout", nil, csrf); code != http.StatusNoContent {
		t.Fatalf("logout: %d", code)
	}
	// whoami after logout must be 401.
	if code, _ := sh.doWithCookie("GET", "/api/v1/auth/whoami", nil, ""); code != http.StatusUnauthorized {
		t.Fatalf("whoami after logout: status=%d, want 401", code)
	}
}

// TestWhoAmI_BearerAuth returns 200 + principal info when authenticated
// with a Bearer API key (not a session cookie).
func TestWhoAmI_BearerAuth(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	_, _, apiKey := sh.bootstrapWithPassword("whoami-bearer@example.com")

	// Use the standard doRequest helper (Bearer token, no cookie jar).
	res, buf := sh.doRequest("GET", "/api/v1/auth/whoami", apiKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("whoami bearer: status=%d body=%s, want 200", res.StatusCode, buf)
	}
	var out map[string]any
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatalf("whoami unmarshal: %v", err)
	}
	if out["principal_id"] == nil {
		t.Fatalf("whoami bearer missing principal_id: %v", out)
	}
}

// TestServerStatus_IncludesPrincipalInfo asserts that GET /api/v1/server/status
// returns principal_id, email, and scopes so the admin SPA bootstrap()
// can populate its auth state without a second whoami call. The endpoint is
// admin-gated so step-up is required (REQ-AUTH-74, issue #79).
func TestServerStatus_IncludesPrincipalInfo(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("status-principal@example.com")

	// A TOTP-gated login already elevates the session (REQ-AUTH-74(a),
	// issue #228), so no separate step-up is needed before the admin-gated
	// server/status endpoint.
	if code, _ := sh.doLoginWithTOTP(email, password, secret, nil); code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}

	code, raw := sh.doWithCookie("GET", "/api/v1/server/status", nil, "")
	if code != http.StatusOK {
		t.Fatalf("server/status: status=%d body=%s, want 200", code, raw)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["principal_id"] == nil {
		t.Fatalf("server/status missing principal_id: %v", out)
	}
	if out["email"] != email {
		t.Fatalf("server/status email=%v, want %q", out["email"], email)
	}
	scopes, _ := out["scopes"].([]interface{})
	if len(scopes) == 0 {
		t.Fatalf("server/status scopes empty: %v", out)
	}
}

// TestSessionAuth_AdminWithoutTOTP_LoginSucceedsStepUpRefused asserts the new
// step-up model (issue #79): an admin principal without TOTP enrolled CAN log in
// (login no longer gates on TOTP), but attempting step-up returns 400 with
// enroll_required=true so the SPA can redirect to enrollment.
func TestSessionAuth_AdminWithoutTOTP_LoginSucceedsStepUpRefused(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _ := sh.bootstrapWithPassword("admin-no-totp@example.com")

	// Login must succeed even without TOTP enrollment.
	code, body := sh.doLogin(email, password, nil)
	if code != http.StatusOK {
		t.Fatalf("admin login without TOTP: status=%d body=%v, want 200", code, body)
	}
	if !sh.sessionCookiePresent() {
		t.Fatal("session cookie should be set after login regardless of TOTP state")
	}

	// Attempting step-up without TOTP enrolled must return 400 + enroll_required.
	csrf := sh.csrfToken()
	stepUpCode, stepUpRaw := sh.doWithCookie("POST", "/api/v1/auth/step-up", map[string]any{
		"totp_code": "000000",
	}, csrf)
	var stepUpBody map[string]any
	_ = json.Unmarshal(stepUpRaw, &stepUpBody)
	if stepUpCode != http.StatusBadRequest {
		t.Errorf("step-up without TOTP enrolled: status=%d body=%s, want 400", stepUpCode, stepUpRaw)
	}
	if stepUpBody["enroll_required"] != true {
		t.Errorf("enroll_required not true: %v", stepUpBody)
	}
}

// TestSessionAuth_AdminWithTOTP_LoginElevatesImmediately asserts that an
// admin principal with TOTP enrolled must supply a valid totp_code at login
// (REQ-AUTH-JSON-LOGIN, REQ-AUTH-42, issue #228), and that a successful
// TOTP-gated login immediately elevates the session (REQ-AUTH-74(a)) so the
// admin-gated endpoint is reachable without a further step-up prompt.
func TestSessionAuth_AdminWithTOTP_LoginElevatesImmediately(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("admin-with-totp@example.com")

	// Login without totp_code: refused (no session at all).
	missingCode, _ := sh.doLogin(email, password, nil)
	if missingCode != http.StatusUnauthorized {
		t.Fatalf("login without totp_code: status=%d, want 401", missingCode)
	}
	if sh.sessionCookiePresent() {
		t.Fatal("session cookie set despite missing totp_code")
	}

	// Login with a valid totp_code: succeeds.
	code, body := sh.doLoginWithTOTP(email, password, secret, nil)
	if code != http.StatusOK {
		t.Fatalf("admin login with totp_code: status=%d body=%v, want 200", code, body)
	}
	if !sh.sessionCookiePresent() {
		t.Errorf("session cookie missing after successful login")
	}
	if body["elevation_expires_at"] == nil {
		t.Errorf("elevation_expires_at missing/nil after TOTP-gated login: %v", body)
	}

	// Admin endpoint is reachable immediately -- no separate step-up needed.
	getCode, _ := sh.doWithCookie("GET", "/api/v1/principals", nil, "")
	if getCode != http.StatusOK {
		t.Errorf("admin endpoint immediately after TOTP-gated login: status=%d, want 200", getCode)
	}
}

// TestBootstrapSuperadmin_TOTPEnrollmentViaAPIKey is the end-to-end
// integration test for the bootstrap-to-first-login flow (issue #12 slice 6).
// The bootstrap API key is the Bearer credential for TOTP enrollment. The
// freshly bootstrapped superadmin has no TOTP enrolled yet, so the TOTP
// gate at login (REQ-AUTH-JSON-LOGIN, REQ-AUTH-42, issue #228) does not
// apply to it: password login succeeds immediately, same as any
// non-enrolled principal. Once TOTP is enrolled, admin operations require
// an elevation, obtained here via an explicit step-up (the pre-enrollment
// session was never TOTP-gated, so it was not auto-elevated at login).
//
// Flow under test:
//  1. Bootstrap → (pid, password, apiKey).
//  2. Password login → 200 (no TOTP enrolled yet).
//  3. POST /api/v1/principals/{pid}/totp/enroll  with Bearer apiKey → 200 + secret.
//  4. POST /api/v1/principals/{pid}/totp/confirm with first code and Bearer → 204.
//  5. POST /api/v1/auth/step-up with valid TOTP code → 200 + elevation_expires_at.
//  6. Admin endpoint → 200.
//  7. Bootstrap API key is one-shot: rejected after confirm (re #21).
func TestBootstrapSuperadmin_TOTPEnrollmentViaAPIKey(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)

	// 1. Bootstrap.
	const email = "bootstrap-superadmin@example.com"
	bootBody, _ := json.Marshal(map[string]any{
		"email":        email,
		"display_name": "Bootstrap Superadmin",
	})
	req, _ := http.NewRequest("POST", sh.baseURL+"/api/v1/bootstrap", bytes.NewReader(bootBody))
	req.Header.Set("Content-Type", "application/json")
	bootRes, err := sh.cookieJarClient.Do(req)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	defer bootRes.Body.Close()
	bootRaw, _ := io.ReadAll(bootRes.Body)
	if bootRes.StatusCode != http.StatusCreated {
		t.Fatalf("bootstrap: status=%d body=%s, want 201", bootRes.StatusCode, bootRaw)
	}
	var bootResp struct {
		PrincipalID     uint64 `json:"principal_id"`
		InitialPassword string `json:"initial_password"`
		InitialAPIKey   string `json:"initial_api_key"`
	}
	if err := json.Unmarshal(bootRaw, &bootResp); err != nil {
		t.Fatalf("bootstrap unmarshal: %v body=%s", err, bootRaw)
	}
	if bootResp.PrincipalID == 0 || bootResp.InitialPassword == "" || bootResp.InitialAPIKey == "" {
		t.Fatalf("bootstrap response missing fields: %+v", bootResp)
	}

	// 2. Password login succeeds immediately (no TOTP gate at login, issue #79).
	loginCode, _ := sh.doLogin(email, bootResp.InitialPassword, nil)
	if loginCode != http.StatusOK {
		t.Fatalf("pre-enrollment password login: status=%d, want 200", loginCode)
	}
	if !sh.sessionCookiePresent() {
		t.Fatal("session cookie not set after login")
	}

	// 3. Use the Bearer API key to enroll TOTP.
	enrollRes, enrollRaw := sh.doRequest("POST",
		fmt.Sprintf("/api/v1/principals/%d/totp/enroll", bootResp.PrincipalID),
		bootResp.InitialAPIKey, nil)
	if enrollRes.StatusCode != http.StatusOK {
		t.Fatalf("totp/enroll via Bearer apiKey: status=%d body=%s, want 200",
			enrollRes.StatusCode, enrollRaw)
	}
	var enrollResp struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(enrollRaw, &enrollResp); err != nil {
		t.Fatalf("enroll unmarshal: %v body=%s", err, enrollRaw)
	}
	if enrollResp.Secret == "" {
		t.Fatalf("enroll response missing secret: %s", enrollRaw)
	}

	// 4. Confirm with the first valid TOTP code via Bearer.
	firstCode, err := otpGenerateCode(enrollResp.Secret, sh.clk.Now())
	if err != nil {
		t.Fatalf("otpGenerateCode (first): %v", err)
	}
	confirmRes, confirmRaw := sh.doRequest("POST",
		fmt.Sprintf("/api/v1/principals/%d/totp/confirm", bootResp.PrincipalID),
		bootResp.InitialAPIKey, map[string]any{"code": firstCode})
	if confirmRes.StatusCode != http.StatusNoContent {
		t.Fatalf("totp/confirm via Bearer apiKey: status=%d body=%s, want 204",
			confirmRes.StatusCode, confirmRaw)
	}

	// Advance one second so the next generated code lands in a fresh
	// time-step (anti-replay window).
	sh.clk.Advance(time.Second)

	// 5. Step up with a valid TOTP code from the cookie session.
	if sc, stepBody := sh.doStepUp(enrollResp.Secret); sc != http.StatusOK {
		t.Fatalf("step-up after enrollment: status=%d body=%v, want 200", sc, stepBody)
	}

	// 6. Admin endpoint accessible after elevation.
	getCode, _ := sh.doWithCookie("GET", "/api/v1/principals", nil, "")
	if getCode != http.StatusOK {
		t.Fatalf("admin GET after elevation: status=%d, want 200", getCode)
	}

	// 7. The bootstrap API key is one-shot: rejected after confirm (re #21).
	postConfirmRes, _ := sh.doRequest("GET",
		fmt.Sprintf("/api/v1/principals/%d", bootResp.PrincipalID),
		bootResp.InitialAPIKey, nil)
	if postConfirmRes.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bootstrap key reuse after confirm: status=%d, want 401",
			postConfirmRes.StatusCode)
	}
}

// TestSessionAuth_IdleGate_RejectsStaleCookie asserts the slice 3 idle
// gate: when SessionConfig.IdleTTL is set, an authenticated request
// that arrives more than IdleTTL after the last-seen tick is rejected
// and the session row is deleted so the same cookie cannot resurrect
// the session on a later (also-late) request.
func TestSessionAuth_IdleGate_RejectsStaleCookie(t *testing.T) {
	t.Parallel()
	const idleTTL = 30 * time.Minute
	sh := newSessionHarnessWithIdleTTL(t, idleTTL)
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("idle-gate@example.com")

	// Login mints a fresh session row with LastSeenAt = now.
	loginCode, _ := sh.doLoginWithTOTP(email, password, secret, nil)
	if loginCode != http.StatusOK {
		t.Fatalf("login: status=%d", loginCode)
	}

	// Inside the idle window, whoami still works and slides the deadline forward.
	sh.clk.Advance(idleTTL - time.Minute)
	code, _ := sh.doWithCookie("GET", "/api/v1/auth/whoami", nil, "")
	if code != http.StatusOK {
		t.Fatalf("GET inside idle window: status=%d, want 200", code)
	}

	// Past the idle window relative to the LAST touch above, the gate trips.
	sh.clk.Advance(idleTTL + time.Minute)
	staleCode, _ := sh.doWithCookie("GET", "/api/v1/auth/whoami", nil, "")
	if staleCode != http.StatusUnauthorized {
		t.Fatalf("GET past idle window: status=%d, want 401", staleCode)
	}
	// A second request with the same cookie still fails — the session
	// row was deleted, so even rolling the clock back wouldn't help.
	staleCode2, _ := sh.doWithCookie("GET", "/api/v1/auth/whoami", nil, "")
	if staleCode2 != http.StatusUnauthorized {
		t.Fatalf("GET after idle trip + retry: status=%d, want 401", staleCode2)
	}
}

// TestSessionAuth_IdleGate_TouchSlidesDeadline confirms the sliding-
// renewal behaviour: a sequence of requests each spaced inside the
// idle window keeps the session live indefinitely (up to the absolute
// TTL, which this test doesn't exercise).
func TestSessionAuth_IdleGate_TouchSlidesDeadline(t *testing.T) {
	t.Parallel()
	const idleTTL = 30 * time.Minute
	sh := newSessionHarnessWithIdleTTL(t, idleTTL)
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("idle-slide@example.com")

	loginCode, _ := sh.doLoginWithTOTP(email, password, secret, nil)
	if loginCode != http.StatusOK {
		t.Fatalf("login: status=%d", loginCode)
	}

	// Five hops using whoami (auth1, no elevation needed), each at
	// idleTTL - 1 minute past the previous one. The cumulative advance
	// is well past one bare idleTTL; sliding deadline keeps it live.
	for i := 0; i < 5; i++ {
		sh.clk.Advance(idleTTL - time.Minute)
		code, _ := sh.doWithCookie("GET", "/api/v1/auth/whoami", nil, "")
		if code != http.StatusOK {
			t.Fatalf("hop %d: status=%d, want 200", i, code)
		}
	}
}

// TestSessionAuth_NoIdleGate_WhenIdleTTLZero asserts the public-
// listener compatibility shape: when SessionConfig.IdleTTL is zero,
// the resolver does NOT touch the session row, so cookies remain
// usable for as long as the cookie's absolute expiry allows even
// after the user has been silent for hours.
func TestSessionAuth_NoIdleGate_WhenIdleTTLZero(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t) // IdleTTL=0
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("no-idle@example.com")

	loginCode, _ := sh.doLoginWithTOTP(email, password, secret, nil)
	if loginCode != http.StatusOK {
		t.Fatalf("login: status=%d", loginCode)
	}

	// Advance way past any reasonable idle window. The cookie remains
	// valid because cfg.IdleTTL = 0 skips the row lookup + touch.
	sh.clk.Advance(6 * time.Hour)
	// Use whoami (auth1) rather than an admin endpoint (requires elevation).
	code, _ := sh.doWithCookie("GET", "/api/v1/auth/whoami", nil, "")
	if code != http.StatusOK {
		t.Errorf("GET after long idle with IdleTTL=0: status=%d, want 200", code)
	}
}

// TestSessionAuth_AdminSession_UsesSessionTTL asserts that an admin login
// uses Session.TTL for the cookie expiry, not a shorter admin-specific TTL.
// In idle-only mode (Session.TTL = 0), the session_expires_at in the login
// response must be approximately 365 days from now (REQ-AUTH-72, issue #78).
func TestSessionAuth_AdminSession_UsesSessionTTL(t *testing.T) {
	t.Parallel()
	// Build a harness with TTL=0 (idle-only mode) so we can verify the 365d
	// fallback applies to admin sessions as well as end-user sessions.
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs := sqlitetest.Open(t, clk)
	th, _ := testharness.Start(t, testharness.Options{
		Store: fs, Clock: clk,
		Listeners: []testharness.ListenerSpec{{Name: "admin", Protocol: "http"}},
	})
	dir := directory.New(fs.Meta(), nil, clk, nil)
	rp := directoryoidc.New(fs.Meta(), nil, &http.Client{Timeout: 5 * time.Second}, clk)
	srv := protoadmin.NewServer(fs, dir, rp, nil, clk, protoadmin.Options{
		BootstrapPerWindow:      1,
		BootstrapWindow:         5 * time.Minute,
		RequestsPerMinutePerKey: 100,
		Session: authsession.SessionConfig{
			SigningKey:     testSigningKey,
			CookieName:     "herold_public_session",
			CSRFCookieName: "herold_public_csrf",
			TTL:            0, // idle-only mode: 365d fallback
			IdleTTL:        7 * 24 * time.Hour,
			SecureCookies:  false,
		},
	})
	if err := th.AttachAdmin("admin", srv, protoadmin.ListenerModePlain); err != nil {
		t.Fatalf("AttachAdmin: %v", err)
	}
	baseClient, base := th.DialAdminByName(context.Background(), "admin")
	jar, _ := cookiejar.New(nil)
	cookieClient := &http.Client{Transport: baseClient.Transport, Jar: jar,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		}}
	sh := &sessionHarness{
		harness:         &harness{t: t, h: th, srv: srv, client: baseClient, baseURL: base, clk: clk, dir: dir, rp: rp},
		cookieJar:       jar,
		cookieJarClient: cookieClient,
	}

	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("admin-ttl-unified@example.com")
	code, body := sh.doLoginWithTOTP(email, password, secret, nil)
	if code != http.StatusOK {
		t.Fatalf("admin login: status=%d body=%v", code, body)
	}

	expiresAtStr, _ := body["session_expires_at"].(string)
	if expiresAtStr == "" {
		t.Fatalf("session_expires_at missing from login response: %v", body)
	}
	expiresAt, err := time.Parse(time.RFC3339, expiresAtStr)
	if err != nil {
		t.Fatalf("session_expires_at parse: %v", err)
	}
	ttl := expiresAt.Sub(clk.Now())

	// With idle-only mode (Session.TTL=0), the session's ExpiresAt must be
	// the 365d fallback — the idle gate is the real expiry, not this value.
	const want = 365 * 24 * time.Hour
	if ttl < want-time.Hour || ttl > want+time.Hour {
		t.Errorf("admin session TTL with idle-only mode = %s; want ~365d", ttl)
	}
}

// TestSessionAuth_IdleGate_CookieSession asserts the idle gate applies to
// cookie sessions via Session.IdleTTL (REQ-AUTH-72, issue #78).
func TestSessionAuth_IdleGate_CookieSession(t *testing.T) {
	t.Parallel()
	const idleTTL = 30 * time.Minute
	sh := newSessionHarnessWithIdleTTL(t, idleTTL)
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("admin-idle-unified@example.com")

	loginCode, _ := sh.doLoginWithTOTP(email, password, secret, nil)
	if loginCode != http.StatusOK {
		t.Fatalf("login: status=%d", loginCode)
	}

	// Inside the idle window, whoami still works.
	sh.clk.Advance(idleTTL - time.Minute)
	code, _ := sh.doWithCookie("GET", "/api/v1/auth/whoami", nil, "")
	if code != http.StatusOK {
		t.Fatalf("GET inside idle window: status=%d, want 200", code)
	}

	// Past the idle window, the gate trips.
	sh.clk.Advance(idleTTL + time.Minute)
	staleCode, _ := sh.doWithCookie("GET", "/api/v1/auth/whoami", nil, "")
	if staleCode != http.StatusUnauthorized {
		t.Fatalf("GET past idle window: status=%d, want 401", staleCode)
	}
}

// TestSessionAuth_SessionRecord_DeviceContext asserts that a session row is
// persisted at login with UserAgent and LastSeenIP populated, and that
// UpdateSessionLastSeen slides LastSeenIP forward on subsequent requests
// (REQ-AUTH-72, issue #78, issue #80).
func TestSessionAuth_SessionRecord_DeviceContext(t *testing.T) {
	t.Parallel()
	const idleTTL = 7 * 24 * time.Hour
	sh := newSessionHarnessWithIdleTTL(t, idleTTL)
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("devctx@example.com")
	totpCode, err := otpGenerateCode(secret, sh.clk.Now())
	if err != nil {
		t.Fatalf("otpGenerateCode: %v", err)
	}

	// Set a recognisable UA on the login request via a custom request.
	const wantUA = "TestBrowser/1.0"
	loginBody, _ := json.Marshal(map[string]any{"email": email, "password": password, "totp_code": totpCode})
	loginReq, _ := http.NewRequest("POST", sh.baseURL+"/api/v1/auth/login", bytes.NewReader(loginBody))
	loginReq.Header.Set("Content-Type", "application/json")
	loginReq.Header.Set("User-Agent", wantUA)
	loginRes, err := sh.cookieJarClient.Do(loginReq)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer loginRes.Body.Close()
	if loginRes.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(loginRes.Body)
		t.Fatalf("login: status=%d body=%s", loginRes.StatusCode, raw)
	}

	// The CSRF cookie value is the session ID in the sessions table.
	sessID := sh.csrfToken()
	if sessID == "" {
		t.Fatal("CSRF token not set after login")
	}

	row, err := sh.h.Store.Meta().GetSession(context.Background(), sessID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if row.UserAgent != wantUA {
		t.Errorf("UserAgent = %q; want %q", row.UserAgent, wantUA)
	}
	if row.LastSeenIP == "" {
		t.Error("LastSeenIP is empty after login")
	}
}

// TestSessionAuth_Logout_DeletesSessionRow asserts that handleLogout deletes
// the server-side session row so the idle gate rejects any future request
// carrying the same cookie (REQ-AUTH-72, issue #78).
func TestSessionAuth_Logout_DeletesSessionRow(t *testing.T) {
	t.Parallel()
	const idleTTL = 7 * 24 * time.Hour
	sh := newSessionHarnessWithIdleTTL(t, idleTTL)
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("logout-row@example.com")

	loginCode, _ := sh.doLoginWithTOTP(email, password, secret, nil)
	if loginCode != http.StatusOK {
		t.Fatalf("login: status=%d", loginCode)
	}
	sessID := sh.csrfToken()

	// Session row must exist immediately after login.
	if _, err := sh.h.Store.Meta().GetSession(context.Background(), sessID); err != nil {
		t.Fatalf("GetSession before logout: %v", err)
	}

	// Logout.
	csrf := sh.csrfToken()
	if code, _ := sh.doWithCookie("POST", "/api/v1/auth/logout", nil, csrf); code != http.StatusNoContent {
		t.Fatalf("logout: status=%d, want 204", code)
	}

	// Session row must be gone after logout.
	_, err := sh.h.Store.Meta().GetSession(context.Background(), sessID)
	if err == nil {
		t.Error("GetSession after logout: expected ErrNotFound, got nil")
	}
}

// TestSessionAuth_IdleExpired_TypedProblem asserts that when the idle gate
// trips, the 401 response carries a typed RFC 7807 body with
// type="https://netzhansa.com/problems/session_expired" (REQ-AUTH-76, re #77).
// The client uses this type to distinguish "session idle-expired" from "not
// authenticated at all" so it can present the correct forced-login copy.
func TestSessionAuth_IdleExpired_TypedProblem(t *testing.T) {
	t.Parallel()
	const idleTTL = 30 * time.Minute
	sh := newSessionHarnessWithIdleTTL(t, idleTTL)
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("idle-typed@example.com")

	loginCode, _ := sh.doLoginWithTOTP(email, password, secret, nil)
	if loginCode != http.StatusOK {
		t.Fatalf("login: status=%d", loginCode)
	}

	// Advance past the idle window so the gate trips on the next request.
	sh.clk.Advance(idleTTL + time.Minute)

	code, raw := sh.doWithCookie("GET", "/api/v1/principals", nil, "")
	if code != http.StatusUnauthorized {
		t.Fatalf("idle-expired GET: status=%d, want 401", code)
	}

	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal 401 body: %v body=%s", err, raw)
	}
	wantType := "https://netzhansa.com/problems/session_expired"
	if body["type"] != wantType {
		t.Errorf("401 type=%v, want %q", body["type"], wantType)
	}
}

// TestSessionAuth_LoginResponse_SessionIdleDeadline asserts that a
// successful login response includes session_idle_deadline when
// Session.IdleTTL is configured (REQ-AUTH-75, re #77).
func TestSessionAuth_LoginResponse_SessionIdleDeadline(t *testing.T) {
	t.Parallel()
	const idleTTL = 7 * 24 * time.Hour
	sh := newSessionHarnessWithIdleTTL(t, idleTTL)
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("idle-deadline-login@example.com")

	code, body := sh.doLoginWithTOTP(email, password, secret, nil)
	if code != http.StatusOK {
		t.Fatalf("login: status=%d body=%v", code, body)
	}

	deadlineStr, _ := body["session_idle_deadline"].(string)
	if deadlineStr == "" {
		t.Fatalf("session_idle_deadline missing from login response: %v", body)
	}
	deadline, err := time.Parse(time.RFC3339, deadlineStr)
	if err != nil {
		t.Fatalf("session_idle_deadline parse: %v", err)
	}
	// The deadline should be approximately now + idleTTL.
	want := sh.clk.Now().Add(idleTTL)
	diff := deadline.Sub(want)
	if diff < -time.Minute || diff > time.Minute {
		t.Errorf("session_idle_deadline=%s; want ~%s (diff=%s)", deadline, want, diff)
	}
}

// TestAuthMe_SessionIdleDeadline asserts that GET /api/v1/auth/me
// includes session_idle_deadline when Session.IdleTTL is configured
// (REQ-AUTH-75, re #77).
func TestAuthMe_SessionIdleDeadline(t *testing.T) {
	t.Parallel()
	const idleTTL = 7 * 24 * time.Hour
	sh := newSessionHarnessWithIdleTTL(t, idleTTL)
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("idle-deadline-me@example.com")

	if code, _ := sh.doLoginWithTOTP(email, password, secret, nil); code != http.StatusOK {
		t.Fatalf("login: status=%d", code)
	}

	code, raw := sh.doWithCookie("GET", "/api/v1/auth/me", nil, "")
	if code != http.StatusOK {
		t.Fatalf("auth/me: status=%d body=%s", code, raw)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	deadlineStr, _ := out["session_idle_deadline"].(string)
	if deadlineStr == "" {
		t.Fatalf("session_idle_deadline missing from /auth/me response: %v", out)
	}
	deadline, err := time.Parse(time.RFC3339, deadlineStr)
	if err != nil {
		t.Fatalf("session_idle_deadline parse: %v", err)
	}
	want := sh.clk.Now().Add(idleTTL)
	diff := deadline.Sub(want)
	if diff < -time.Minute || diff > time.Minute {
		t.Errorf("session_idle_deadline=%s; want ~%s (diff=%s)", deadline, want, diff)
	}
}
