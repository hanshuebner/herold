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

// TestPublicJSONLogin_HappyPath is the canonical happy-path test for the new
// JSON login endpoint on the public listener.
//
// Flow:
//  1. Bootstrap a principal via the admin listener.
//  2. POST /api/v1/auth/login on the public listener -> 200 + cookies.
//  3. POST /api/v1/auth/logout on the public listener -> 204 + cookies cleared.
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

	email, password := bootstrapUserViaAdmin(t, adminAddr)

	client := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// Step 1: login.
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
	var gotSession, gotCSRF bool
	for _, c := range loginResp.Cookies() {
		if c.Name == "herold_public_session" {
			gotSession = true
		}
		if c.Name == "herold_public_csrf" {
			gotCSRF = true
		}
	}
	if !gotSession {
		t.Error("herold_public_session not set after login")
	}
	if !gotCSRF {
		t.Error("herold_public_csrf not set after login")
	}

	// Step 2: logout.
	logoutResp, err := client.Post(
		"http://"+publicAddr+"/api/v1/auth/logout",
		"application/json",
		nil,
	)
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

// TestAdminJSONLogin_StillWorks_Regression guards that the admin listener's
// JSON login endpoint (internal/protoadmin/session_auth.go) is unaffected
// by the Phase 3c-i changes.
func TestAdminJSONLogin_StillWorks_Regression(t *testing.T) {
	_, addrs, done, cancel := startTestServer(t)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("server did not shut down")
		}
	})
	adminAddr := addrs["admin"]
	if adminAddr == "" {
		t.Fatalf("admin listener not bound; addrs=%+v", addrs)
	}

	email, password := bootstrapUserViaAdmin(t, adminAddr)

	b, _ := json.Marshal(map[string]any{
		"email":    email,
		"password": password,
	})
	resp, err := http.Post(
		"http://"+adminAddr+"/api/v1/auth/login",
		"application/json",
		bytes.NewReader(b),
	)
	if err != nil {
		t.Fatalf("admin POST /api/v1/auth/login: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin login regression: status=%d body=%s, want 200",
			resp.StatusCode, body)
	}

	// Admin session cookie must be present in Set-Cookie headers.
	// Checking headers directly because the test transport is plain HTTP
	// and the cookie jar silently discards Secure cookies.
	var gotAdminSession bool
	for _, c := range resp.Cookies() {
		if c.Name == "herold_admin_session" {
			gotAdminSession = true
		}
	}
	if !gotAdminSession {
		t.Error("herold_admin_session not set after admin login")
	}
}
