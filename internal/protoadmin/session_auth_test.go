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
	// Advance one second so the next code generated for login is a
	// different time-step than the enrollment code (avoids the
	// directory's anti-replay window).
	sh.clk.Advance(time.Second)
	return em, pw, key, secret
}

// doLoginWithTOTP wraps doLogin with a fresh TOTP code generated from
// the secret at the harness's current clock. Use this in tests that
// have an admin principal with TOTP enrolled (the common case after
// slice 4 of issue #12). The optional extra map is merged with the
// totp_code so callers can still pass redirect targets or other
// per-test login-body fields.
func (sh *sessionHarness) doLoginWithTOTP(email, password, totpSecret string, extra map[string]any) (int, map[string]any) {
	sh.t.Helper()
	code, err := otpGenerateCode(totpSecret, sh.clk.Now())
	if err != nil {
		sh.t.Fatalf("otpGenerateCode (login): %v", err)
	}
	merged := map[string]any{"totp_code": code}
	for k, v := range extra {
		merged[k] = v
	}
	return sh.doLogin(email, password, merged)
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
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("cookie-get@example.com")

	code, _ := sh.doLoginWithTOTP(email, password, secret, nil)
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
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("no-csrf@example.com")

	code, _ := sh.doLoginWithTOTP(email, password, secret, nil)
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
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("with-csrf@example.com")

	code, _ := sh.doLoginWithTOTP(email, password, secret, nil)
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
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("csrf-mismatch@example.com")

	code, _ := sh.doLoginWithTOTP(email, password, secret, nil)
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
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("extra-csrf@example.com")

	code, _ := sh.doLoginWithTOTP(email, password, secret, nil)
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
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("logout-user@example.com")

	// Login.
	code, _ := sh.doLoginWithTOTP(email, password, secret, nil)
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
// can populate its auth state without a second whoami call.
func TestServerStatus_IncludesPrincipalInfo(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("status-principal@example.com")

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

// TestSessionAuth_AdminWithoutTOTP_RefusedAtLogin asserts the
// REQ-AUTH-44 gate (issue #12 slice 4): an admin principal that has
// authenticated correctly but does NOT have TOTP enrolled is refused
// the admin-scoped session and steered toward enrollment. The bootstrap
// path leaves the superadmin in this exact state until they enroll.
func TestSessionAuth_AdminWithoutTOTP_RefusedAtLogin(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _ := sh.bootstrapWithPassword("admin-no-totp@example.com")

	code, body := sh.doLogin(email, password, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("admin login without TOTP: status=%d, want 401", code)
	}
	if body["step_up_required"] != true {
		t.Errorf("step_up_required not true: %v", body)
	}
	if body["totp_enrollment_required"] != true {
		t.Errorf("totp_enrollment_required not true: %v", body)
	}
	if body["enroll_url"] != "/api/v1/totp/enroll" {
		t.Errorf("enroll_url=%v, want /api/v1/totp/enroll", body["enroll_url"])
	}
	if sh.sessionCookiePresent() {
		t.Errorf("herold_public_session should not be set when TOTP enrollment is required")
	}
}

// TestSessionAuth_AdminWithTOTP_LoginSucceeds is the positive twin of
// the gate test above: once the admin principal completes TOTP
// enrollment + confirmation, the password+TOTP login mints the
// expected admin-scoped session.
func TestSessionAuth_AdminWithTOTP_LoginSucceeds(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("admin-with-totp@example.com")

	code, body := sh.doLoginWithTOTP(email, password, secret, nil)
	if code != http.StatusOK {
		t.Fatalf("admin login after enrollment: status=%d body=%v, want 200", code, body)
	}
	if !sh.sessionCookiePresent() {
		t.Errorf("session cookie missing after successful TOTP login")
	}
}

// TestBootstrapSuperadmin_TOTPEnrollmentViaAPIKey is the end-to-end
// integration test for the bootstrap-to-first-login flow under issue
// #12 slice 6 (interpretation 1): the bootstrap API key is the
// Bearer credential that lets the first-time superadmin reach the
// per-principal TOTP enrollment endpoints WITHOUT a TOTP code,
// because Bearer auth on /api/v1/principals/{pid}/totp/enroll and
// /totp/confirm bypasses the password-login gate (slice 4) that
// would otherwise block any sign-in attempt.
//
// Flow under test:
//  1. Bootstrap → (pid, password, apiKey).
//  2. Password-login without TOTP code → 401 step_up_required +
//     totp_enrollment_required (slice 4 gate, verified explicitly).
//  3. POST /api/v1/principals/{pid}/totp/enroll  with Bearer apiKey
//     → 200 + secret.
//  4. POST /api/v1/principals/{pid}/totp/confirm with first code
//     and Bearer apiKey → 204.
//  5. Password+TOTP login → 200, admin-scoped session cookie set.
//
// Step 6 asserts interpretation 2 (re #21): the bootstrap API key is
// one-shot and is consumed on the first successful TOTP confirm so it
// cannot be reused for any subsequent request.
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

	// 2. Password login without TOTP — slice 4 gate refuses.
	gateCode, gateBody := sh.doLogin(email, bootResp.InitialPassword, nil)
	if gateCode != http.StatusUnauthorized {
		t.Fatalf("pre-enrollment password login: status=%d, want 401", gateCode)
	}
	if gateBody["totp_enrollment_required"] != true {
		t.Fatalf("pre-enrollment gate: totp_enrollment_required not true: %v", gateBody)
	}

	// 3. Use the Bearer API key to enroll TOTP. The Bearer path does
	// not go through the password-login gate at all — Bearer is the
	// "ticket" interpretation 1 leans on.
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

	// 4. Confirm with the first valid TOTP code, again via Bearer.
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

	// 5. Password + TOTP login now succeeds — slice 4 gate passes
	// because the FlagTOTPEnabled bit is set by step 4.
	loginCode, _ := sh.doLoginWithTOTP(email, bootResp.InitialPassword, enrollResp.Secret, nil)
	if loginCode != http.StatusOK {
		t.Fatalf("post-enrollment login: status=%d, want 200", loginCode)
	}
	if !sh.sessionCookiePresent() {
		t.Errorf("session cookie missing after bootstrap-superadmin enrollment + login")
	}

	// 6. The bootstrap API key is one-shot: it must be rejected on any
	// subsequent use after the successful confirm in step 4 (re #21,
	// REQ-AUTH-44 interpretation 2). Assert that Bearer with the initial
	// key now returns 401.
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

	// Inside the idle window, the cookie still works and slides the
	// deadline forward.
	sh.clk.Advance(idleTTL - time.Minute)
	code, _ := sh.doWithCookie("GET", "/api/v1/principals", nil, "")
	if code != http.StatusOK {
		t.Fatalf("GET inside idle window: status=%d, want 200", code)
	}

	// Past the idle window relative to the LAST touch above, the gate
	// trips.
	sh.clk.Advance(idleTTL + time.Minute)
	staleCode, _ := sh.doWithCookie("GET", "/api/v1/principals", nil, "")
	if staleCode != http.StatusUnauthorized {
		t.Fatalf("GET past idle window: status=%d, want 401", staleCode)
	}
	// A second request with the same cookie still fails — the session
	// row was deleted, so even rolling the clock back wouldn't help.
	staleCode2, _ := sh.doWithCookie("GET", "/api/v1/principals", nil, "")
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

	// Five hops, each at idleTTL - 1 minute past the previous one, so
	// the cumulative wall-clock advance is well past one bare idleTTL.
	for i := 0; i < 5; i++ {
		sh.clk.Advance(idleTTL - time.Minute)
		code, _ := sh.doWithCookie("GET", "/api/v1/principals", nil, "")
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
	code, _ := sh.doWithCookie("GET", "/api/v1/principals", nil, "")
	if code != http.StatusOK {
		t.Errorf("GET after long idle with IdleTTL=0: status=%d, want 200", code)
	}
}

// TestSessionAuth_AdminTTL_ShorterThanSessionTTL asserts that an admin
// login with ScopeAdmin uses AdminTTL (not Session.TTL) for the cookie
// expiry (B-1 regression guard, re #58).
//
// The test harness sets Session.TTL = 24 h and leaves AdminTTL = 0 (which
// defaults to 8 h in handleLogin). The session_expires_at in the login
// response MUST be ~8 h from now, not ~24 h.
func TestSessionAuth_AdminTTL_ShorterThanSessionTTL(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t) // Session.TTL = 24 h; AdminTTL = 0 → default 8 h
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("admin-ttl@example.com")

	code, body := sh.doLoginWithTOTP(email, password, secret, nil)
	if code != http.StatusOK {
		t.Fatalf("admin login: status=%d body=%v", code, body)
	}

	// The response carries session_expires_at.
	expiresAtStr, _ := body["session_expires_at"].(string)
	if expiresAtStr == "" {
		t.Fatalf("session_expires_at missing from login response: %v", body)
	}
	expiresAt, err := time.Parse(time.RFC3339, expiresAtStr)
	if err != nil {
		t.Fatalf("session_expires_at parse: %v", err)
	}

	ttl := expiresAt.Sub(sh.clk.Now())

	// Admin sessions must use AdminTTL (default 8 h when zero), NOT
	// Session.TTL (24 h in the harness). Allow a small margin for code
	// execution time inside the server.
	const adminDefault = 8 * time.Hour
	const sessionTTL = 24 * time.Hour
	if ttl >= sessionTTL {
		t.Errorf("admin session TTL %s >= Session.TTL %s; expected ~AdminTTL (%s)",
			ttl, sessionTTL, adminDefault)
	}
	// Lower bound: must be at least 7 hours (allows ~1 h of test overhead).
	if ttl < 7*time.Hour {
		t.Errorf("admin session TTL %s < 7h; expected ~AdminTTL (%s)", ttl, adminDefault)
	}
}

// TestSessionAuth_AdminIdleTTL_EnforcedViaAdminIdleTTLOption asserts that
// Options.AdminIdleTTL is honoured for admin-scoped sessions when
// Session.IdleTTL is zero. This is the per-scope idle gate (B-1, re #58).
func TestSessionAuth_AdminIdleTTL_EnforcedViaAdminIdleTTLOption(t *testing.T) {
	t.Parallel()
	const adminIdleTTL = 30 * time.Minute

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
			IdleTTL:        0, // not set — non-admin sessions have no idle gate
			SecureCookies:  false,
		},
		AdminIdleTTL: adminIdleTTL, // admin sessions: 30-minute idle gate
	})
	if err := th.AttachAdmin("admin", srv, protoadmin.ListenerModePlain); err != nil {
		t.Fatalf("AttachAdmin: %v", err)
	}
	_, base := th.DialAdminByName(context.Background(), "admin")

	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Transport: &http.Transport{},
		Jar:       jar,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	// Re-use the base transport from the test harness.
	baseClient, _ := th.DialAdminByName(context.Background(), "admin")
	client.Transport = baseClient.Transport

	sh := &sessionHarness{
		harness:         &harness{t: t, h: th, srv: srv, client: baseClient, baseURL: base, clk: clk, dir: dir, rp: rp},
		cookieJar:       jar,
		cookieJarClient: client,
	}

	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("admin-idle@example.com")

	loginCode, _ := sh.doLoginWithTOTP(email, password, secret, nil)
	if loginCode != http.StatusOK {
		t.Fatalf("admin login: status=%d", loginCode)
	}

	// Inside the idle window the admin cookie works.
	clk.Advance(adminIdleTTL - time.Minute)
	code, _ := sh.doWithCookie("GET", "/api/v1/principals", nil, "")
	if code != http.StatusOK {
		t.Fatalf("admin GET inside idle window: status=%d, want 200", code)
	}

	// Past the idle window the gate trips.
	clk.Advance(adminIdleTTL + time.Minute)
	staleCode, _ := sh.doWithCookie("GET", "/api/v1/principals", nil, "")
	if staleCode != http.StatusUnauthorized {
		t.Fatalf("admin GET past idle window: status=%d, want 401", staleCode)
	}
}
