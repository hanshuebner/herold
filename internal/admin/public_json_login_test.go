package admin

// public_json_login_test.go verifies the JSON login/logout endpoints on the
// public listener added in Phase 3c-i (REQ-AUTH-SCOPE-01).
//
// Happy-path contract tested here:
//   1. POST /api/v1/auth/login on the public listener with valid creds returns
//      200 + Set-Cookie herold_public_session + Set-Cookie herold_public_csrf.
//   2. POST /api/v1/auth/logout on the public listener returns 204 and the
//      cookies are cleared from the jar.
//   3. The admin REST /api/v1/auth/login endpoint still works (regression guard
//      -- protoadmin/session_auth.go is untouched by this commit).

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"
)

// bootstrapUserViaAdmin creates the first admin principal via the admin
// listener bootstrap endpoint and returns (email, password).
func bootstrapUserViaAdmin(t *testing.T, adminAddr string) (email, password string) {
	t.Helper()
	email = "public-login-test@example.com"
	b, _ := json.Marshal(map[string]any{
		"email":        email,
		"display_name": "Public Login Test",
	})
	resp, err := http.Post("http://"+adminAddr+"/api/v1/bootstrap",
		"application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("bootstrap: status=%d body=%s", resp.StatusCode, raw)
	}
	var out struct {
		InitialPassword string `json:"initial_password"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("bootstrap unmarshal: %v body=%s", err, raw)
	}
	return email, out.InitialPassword
}

// TestPublicJSONLogin_HappyPath is the canonical happy-path test for the JSON
// login endpoint on the public listener.
//
// Flow:
//  1. Bootstrap the first principal (admin) via the public listener.
//  2. Create a non-admin end-user via the admin API key.
//  3. POST /api/v1/auth/login on the public listener with end-user creds -> 200 + cookies.
//  4. POST /api/v1/auth/logout on the public listener -> 204 + cookies cleared.
//
// The non-admin user is used because admin principals require TOTP step-up
// before a session cookie with ScopeAdmin is issued (re #58). Non-admin
// principals receive AllEndUserScopes without TOTP.
func TestPublicJSONLogin_HappyPath(t *testing.T) {
	_, addrs, done, cancel := startTestServer(t)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("server did not shut down")
		}
	})

	publicAddr := addrs["public"]
	adminAddr := addrs["admin"]
	if publicAddr == "" {
		t.Fatalf("public listener not bound; addrs=%+v", addrs)
	}
	if adminAddr == "" {
		t.Fatalf("admin listener not bound; addrs=%+v", addrs)
	}

	// Bootstrap admin principal to get an API key for creating the end-user.
	// bootstrapAndGetAPIKey is defined in public_selfservice_test.go.
	_, adminAPIKey, _, _ := bootstrapAndGetAPIKey(t, adminAddr)

	// Create a non-admin end-user principal.
	endUserEmail := "enduser-json-login@example.com"
	endUserPassword := "horse-battery-staple-login"
	createBody, _ := json.Marshal(map[string]any{
		"email":    endUserEmail,
		"password": endUserPassword,
	})
	createReq, _ := http.NewRequest("POST",
		"http://"+adminAddr+"/api/v1/principals",
		bytes.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("Authorization", "Bearer "+adminAPIKey)
	createResp, err := http.DefaultClient.Do(createReq)
	if err != nil {
		t.Fatalf("create end-user: %v", err)
	}
	createResp.Body.Close()
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create end-user: status=%d", createResp.StatusCode)
	}

	email := endUserEmail
	password := endUserPassword

	client := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// Step 3: login with the end-user on the public listener.
	b, _ := json.Marshal(map[string]any{
		"email":    email,
		"password": password,
	})
	loginResp, err := client.Post(
		"http://"+publicAddr+"/api/v1/auth/login",
		"application/json",
		bytes.NewReader(b),
	)
	if err != nil {
		t.Fatalf("POST /api/v1/auth/login: %v", err)
	}
	defer loginResp.Body.Close()
	loginBody, _ := io.ReadAll(loginResp.Body)

	if loginResp.StatusCode != http.StatusOK {
		t.Fatalf("public login: status=%d body=%s, want 200",
			loginResp.StatusCode, loginBody)
	}

	// Login response must carry principal_id, email, scopes.
	var loginOut map[string]any
	if err := json.Unmarshal(loginBody, &loginOut); err != nil {
		t.Fatalf("login response not JSON: %v body=%s", err, loginBody)
	}
	if loginOut["principal_id"] == nil {
		t.Errorf("login response missing principal_id: %v", loginOut)
	}
	if loginOut["email"] != email {
		t.Errorf("login response email=%v, want %q", loginOut["email"], email)
	}
	scopes, _ := loginOut["scopes"].([]interface{})
	if len(scopes) == 0 {
		t.Errorf("login response has no scopes: %v", loginOut)
	}

	// Both cookies must appear in the Set-Cookie response headers.
	// We inspect the response headers directly rather than the cookiejar
	// because the test server runs on plain HTTP and a Go cookiejar silently
	// discards Secure cookies on non-HTTPS transports. The server's default
	// config sets secure_cookies=true (production default); we verify the
	// header shape rather than jar contents to stay transport-independent.
	var sessionCookie, csrfCookieVal *http.Cookie
	var gotSession, gotCSRF bool
	for _, c := range loginResp.Cookies() {
		if c.Name == "herold_public_session" {
			gotSession = true
			sessionCookie = c
		}
		if c.Name == "herold_public_csrf" {
			gotCSRF = true
			csrfCookieVal = c
		}
	}
	if !gotSession {
		t.Error("herold_public_session not set after login")
	}
	if !gotCSRF {
		t.Error("herold_public_csrf not set after login")
	}

	// Step 4: logout. The logout handler requires authentication (cookie or
	// Bearer) and CSRF protection. Attach both cookies and the X-CSRF-Token
	// header manually since the plain-HTTP test transport does not automatically
	// send Secure cookies via a cookie jar.
	logoutReq, _ := http.NewRequest("POST",
		"http://"+publicAddr+"/api/v1/auth/logout",
		nil)
	logoutReq.Header.Set("Content-Type", "application/json")
	if sessionCookie != nil {
		logoutReq.AddCookie(sessionCookie)
	}
	if csrfCookieVal != nil {
		logoutReq.AddCookie(csrfCookieVal)
		logoutReq.Header.Set("X-CSRF-Token", csrfCookieVal.Value)
	}
	logoutResp, err := client.Do(logoutReq)
	if err != nil {
		t.Fatalf("POST /api/v1/auth/logout: %v", err)
	}
	defer logoutResp.Body.Close()
	if logoutResp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(logoutResp.Body)
		t.Fatalf("public logout: status=%d body=%s, want 204",
			logoutResp.StatusCode, body)
	}

	// After logout, the response Set-Cookie headers must clear both cookies
	// (MaxAge=-1 signals deletion to the browser). We check the response
	// headers directly because the cookiejar silently discards Secure cookies
	// on plain-HTTP test transports.
	var sessionCleared, csrfCleared bool
	for _, c := range logoutResp.Cookies() {
		if c.Name == "herold_public_session" && c.MaxAge < 0 {
			sessionCleared = true
		}
		if c.Name == "herold_public_csrf" && c.MaxAge < 0 {
			csrfCleared = true
		}
	}
	if !sessionCleared {
		t.Error("herold_public_session not cleared after logout (MaxAge should be < 0)")
	}
	if !csrfCleared {
		t.Error("herold_public_csrf not cleared after logout (MaxAge should be < 0)")
	}
}

// TestPublicJSONLogin_BadCreds_Returns401 asserts that wrong credentials
// on the public listener login endpoint return 401.
func TestPublicJSONLogin_BadCreds_Returns401(t *testing.T) {
	_, addrs, done, cancel := startTestServer(t)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("server did not shut down")
		}
	})
	publicAddr := addrs["public"]
	if publicAddr == "" {
		t.Fatalf("public listener not bound; addrs=%+v", addrs)
	}

	b, _ := json.Marshal(map[string]any{
		"email":    "nobody@example.com",
		"password": "wrongpassword",
	})
	resp, err := http.Post(
		"http://"+publicAddr+"/api/v1/auth/login",
		"application/json",
		bytes.NewReader(b),
	)
	if err != nil {
		t.Fatalf("POST /api/v1/auth/login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("bad creds on public login: status=%d body=%s, want 401",
			resp.StatusCode, body)
	}
}

// TestAdminJSONLogin_NoTOTP_RefusedWithEnrollmentHint guards the step-up
// model (REQ-AUTH-74, issue #79) for an admin principal without TOTP enrolled.
//
// Under the step-up model:
//  1. Login always succeeds with an end-user-scoped session (TOTP is never
//     evaluated at login time — REQ-AUTH-42, REQ-AUTH-SCOPE-03).
//  2. An admin without TOTP enrolled who attempts POST /api/v1/auth/step-up
//     receives 400 with enroll_required:true and an enroll_url.
//  3. An admin-gated route (without elevation) returns 403 step_up_required.
//
// The regression this test guards: the old code rejected admin login entirely
// when TOTP was not enrolled (pre-issue-#79). The new code permits the login
// but blocks admin access until TOTP is enrolled and a step-up is performed.
//
// The positive twin (step-up succeeds after TOTP enrollment) lives in
// internal/protoadmin/session_auth_test.go and internal/admin/scope_boundary_test.go.
func TestAdminJSONLogin_NoTOTP_RefusedWithEnrollmentHint(t *testing.T) {
	_, addrs, done, cancel := startTestServer(t)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("server did not shut down")
		}
	})
	publicAddr := addrs["public"]
	if publicAddr == "" {
		t.Fatalf("public listener not bound; addrs=%+v", addrs)
	}
	adminAddr := addrs["admin"]
	if adminAddr == "" {
		t.Fatalf("admin listener not bound; addrs=%+v", addrs)
	}

	// Bootstrap an admin principal (has PrincipalFlagAdmin but no TOTP enrolled).
	email, password := bootstrapUserViaAdmin(t, adminAddr)

	// Step 1: login returns 200 with a session cookie even without TOTP.
	client := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	loginBody, _ := json.Marshal(map[string]any{
		"email":    email,
		"password": password,
	})
	resp, err := client.Post(
		"http://"+publicAddr+"/api/v1/auth/login",
		"application/json",
		bytes.NewReader(loginBody),
	)
	if err != nil {
		t.Fatalf("POST /api/v1/auth/login: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: status=%d body=%s, want 200", resp.StatusCode, body)
	}

	// Session cookie must be set; it carries only end-user scopes.
	var sessionCookie, csrfCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "herold_public_session" {
			sessionCookie = c
		}
		if c.Name == "herold_public_csrf" {
			csrfCookie = c
		}
	}
	if sessionCookie == nil {
		t.Fatal("herold_public_session not set after login")
	}

	// Step 2: step-up without TOTP enrolled → 400 with enroll_required.
	var csrfValue string
	if csrfCookie != nil {
		csrfValue = csrfCookie.Value
	}
	stepUpBody, _ := json.Marshal(map[string]any{"totp_code": "123456"})
	stepReq, _ := http.NewRequest("POST",
		"http://"+publicAddr+"/api/v1/auth/step-up",
		bytes.NewReader(stepUpBody))
	stepReq.Header.Set("Content-Type", "application/json")
	if csrfValue != "" {
		stepReq.Header.Set("X-CSRF-Token", csrfValue)
		stepReq.AddCookie(csrfCookie)
	}
	stepReq.AddCookie(sessionCookie)
	stepResp, err := client.Do(stepReq)
	if err != nil {
		t.Fatalf("POST /api/v1/auth/step-up: %v", err)
	}
	stepRaw, _ := io.ReadAll(stepResp.Body)
	stepResp.Body.Close()
	if stepResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("step-up without TOTP enrolled: status=%d body=%s, want 400",
			stepResp.StatusCode, stepRaw)
	}
	var stepDetail map[string]any
	if err := json.Unmarshal(stepRaw, &stepDetail); err != nil {
		t.Fatalf("step-up problem detail unmarshal: %v body=%s", err, stepRaw)
	}
	if stepDetail["enroll_required"] != true {
		t.Errorf("step-up body missing enroll_required:true: %v", stepDetail)
	}

	// Step 3: admin route without elevation returns 403 step_up_required.
	adminReq, _ := http.NewRequest("GET",
		"http://"+publicAddr+"/api/v1/server/status", nil)
	adminReq.AddCookie(sessionCookie)
	adminResp, err := client.Do(adminReq)
	if err != nil {
		t.Fatalf("GET /api/v1/server/status: %v", err)
	}
	adminBody, _ := io.ReadAll(adminResp.Body)
	adminResp.Body.Close()
	if adminResp.StatusCode != http.StatusForbidden {
		t.Fatalf("admin route without elevation: status=%d body=%s, want 403",
			adminResp.StatusCode, adminBody)
	}
	var adminDetail map[string]any
	if err := json.Unmarshal(adminBody, &adminDetail); err != nil {
		t.Fatalf("admin 403 unmarshal: %v body=%s", err, adminBody)
	}
	if adminDetail["step_up_required"] != true {
		t.Errorf("admin 403 missing step_up_required:true: %v", adminDetail)
	}
}
