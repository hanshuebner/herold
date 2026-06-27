package admin

// scope_boundary_test.go verifies the ScopeAdmin boundary claim made in
// issue #58: that admin-gated REST routes return 403 for authenticated
// non-admin principals and 200 for admin principals that completed TOTP
// step-up.
//
// Tests:
//  1. Non-admin end-user login → 401/403 on admin-only routes (queue,
//     audit, server status, server config-check, domain list).
//  2. Admin + TOTP login → ScopeAdmin → admin-only routes return 200.
//
// These are pure HTTP-level integration tests against a live server;
// they use no in-process test doubles for the directory or store.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// adminScopeBoundaryOTPCode generates a TOTP code for secret at t using the
// same parameter set the server uses (SHA-1, 6 digits, 30 s period).
func adminScopeBoundaryOTPCode(secret string, t time.Time) (string, error) {
	return totp.GenerateCodeCustom(secret, t, totp.ValidateOpts{
		Period:    30,
		Skew:      1,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	})
}

// adminScopeHelper encapsulates the HTTP interaction helpers used by the
// scope boundary tests.
type adminScopeHelper struct {
	t          *testing.T
	adminAddr  string
	publicAddr string
	client     *http.Client
}

func newAdminScopeHelper(t *testing.T, addrs map[string]string) *adminScopeHelper {
	t.Helper()
	return &adminScopeHelper{
		t:          t,
		adminAddr:  addrs["admin"],
		publicAddr: addrs["public"],
		client: &http.Client{
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// bootstrapAdmin calls POST /api/v1/bootstrap and returns (pid, apiKey, email, password).
func (h *adminScopeHelper) bootstrapAdmin(email string) (pid uint64, apiKey, password string) {
	h.t.Helper()
	b, _ := json.Marshal(map[string]any{
		"email":        email,
		"display_name": "Scope Boundary Admin",
	})
	resp, err := h.client.Post("http://"+h.adminAddr+"/api/v1/bootstrap",
		"application/json", bytes.NewReader(b))
	if err != nil {
		h.t.Fatalf("bootstrap POST: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		h.t.Fatalf("bootstrap: status=%d body=%s", resp.StatusCode, raw)
	}
	var out struct {
		PrincipalID     uint64 `json:"principal_id"`
		InitialPassword string `json:"initial_password"`
		InitialAPIKey   string `json:"initial_api_key"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		h.t.Fatalf("bootstrap unmarshal: %v body=%s", err, raw)
	}
	return out.PrincipalID, out.InitialAPIKey, out.InitialPassword
}

// createEndUser creates a non-admin principal via the admin bearer key and
// returns the email and password.
func (h *adminScopeHelper) createEndUser(apiKey, email, password string) {
	h.t.Helper()
	b, _ := json.Marshal(map[string]any{
		"email":    email,
		"password": password,
	})
	req, _ := http.NewRequest("POST", "http://"+h.adminAddr+"/api/v1/principals",
		bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("create end-user POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		h.t.Fatalf("create end-user: status=%d body=%s", resp.StatusCode, body)
	}
}

// enrollTOTP calls POST /api/v1/principals/{pid}/totp/enroll with the bearer
// key and returns the TOTP secret.
func (h *adminScopeHelper) enrollTOTP(pid uint64, apiKey string) string {
	h.t.Helper()
	req, _ := http.NewRequest("POST",
		fmt.Sprintf("http://"+h.adminAddr+"/api/v1/principals/%d/totp/enroll", pid),
		nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("totp/enroll POST: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("totp/enroll: status=%d body=%s", resp.StatusCode, raw)
	}
	var out struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		h.t.Fatalf("totp/enroll unmarshal: %v body=%s", err, raw)
	}
	if out.Secret == "" {
		h.t.Fatalf("totp/enroll: secret empty in %s", raw)
	}
	return out.Secret
}

// confirmTOTP calls POST /api/v1/principals/{pid}/totp/confirm with the bearer
// key and the current TOTP code derived from secret. The bootstrap key is
// consumed (deleted) on success.
func (h *adminScopeHelper) confirmTOTP(pid uint64, apiKey, secret string) {
	h.t.Helper()
	code, err := adminScopeBoundaryOTPCode(secret, time.Now())
	if err != nil {
		h.t.Fatalf("totp code gen: %v", err)
	}
	b, _ := json.Marshal(map[string]any{"code": code})
	req, _ := http.NewRequest("POST",
		fmt.Sprintf("http://"+h.adminAddr+"/api/v1/principals/%d/totp/confirm", pid),
		bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("totp/confirm POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		h.t.Fatalf("totp/confirm: status=%d body=%s", resp.StatusCode, body)
	}
}

// loginAndGetCookies posts to POST /api/v1/auth/login and returns the
// Set-Cookie header cookies. totpCode may be empty for non-TOTP logins.
func (h *adminScopeHelper) loginAndGetCookies(email, password, totpCode string) []*http.Cookie {
	h.t.Helper()
	body := map[string]any{
		"email":    email,
		"password": password,
	}
	if totpCode != "" {
		body["totp_code"] = totpCode
	}
	b, _ := json.Marshal(body)
	resp, err := h.client.Post("http://"+h.publicAddr+"/api/v1/auth/login",
		"application/json", bytes.NewReader(b))
	if err != nil {
		h.t.Fatalf("login POST: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("login: status=%d body=%s", resp.StatusCode, raw)
	}
	return resp.Cookies()
}

// getWithCookies sends a GET to path with the provided cookies and returns
// the HTTP status code.
func (h *adminScopeHelper) getWithCookies(path string, cookies []*http.Cookie) int {
	h.t.Helper()
	req, _ := http.NewRequest("GET", "http://"+h.publicAddr+path, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// TestScopeBoundary_NonAdminGets403OnAdminRoutes asserts that a logged-in
// non-admin principal receives 403 (insufficient scope) on every admin-gated
// REST route. The route is mounted on the public listener; ScopeAdmin is the
// enforced gate.
func TestScopeBoundary_NonAdminGets403OnAdminRoutes(t *testing.T) {
	_, addrs, done, cancel := startTestServer(t)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("server did not shut down")
		}
	})

	h := newAdminScopeHelper(t, addrs)
	if h.publicAddr == "" {
		t.Fatalf("public listener not bound; addrs=%+v", addrs)
	}
	if h.adminAddr == "" {
		t.Fatalf("admin listener not bound; addrs=%+v", addrs)
	}

	// Bootstrap the admin principal and obtain its one-shot API key.
	adminPID, adminAPIKey, _ := h.bootstrapAdmin("scope-boundary-admin@example.com")

	// Create a non-admin end-user while the bootstrap key is still valid.
	const endUserEmail = "scope-boundary-user@example.com"
	const endUserPassword = "horse-battery-staple-boundary"
	h.createEndUser(adminAPIKey, endUserEmail, endUserPassword)

	// Enroll + confirm TOTP for the admin (consumes the bootstrap key).
	totpSecret := h.enrollTOTP(adminPID, adminAPIKey)
	// Advance time by 2 seconds so the confirm code is in a fresh step
	// relative to the enroll step (avoids the anti-replay window).
	time.Sleep(2 * time.Second)
	h.confirmTOTP(adminPID, adminAPIKey, totpSecret)

	// Log in as the non-admin end-user (no TOTP enrolled, no TOTP code).
	endUserCookies := h.loginAndGetCookies(endUserEmail, endUserPassword, "")

	// Admin-gated routes must return 403 (not 404) for the non-admin session.
	// 403 means the route is mounted and the scope gate fired; 404 would mean
	// the route was unmounted (regression from the pre-#58 config).
	adminOnlyRoutes := []string{
		"/api/v1/queue",
		"/api/v1/audit",
		"/api/v1/server/status",
		"/api/v1/domains",
	}
	for _, route := range adminOnlyRoutes {
		code := h.getWithCookies(route, endUserCookies)
		if code != http.StatusForbidden {
			t.Errorf("non-admin GET %s: status=%d, want 403 (insufficient scope)", route, code)
		}
	}
}

// TestScopeBoundary_AdminWithTOTPGets200OnAdminRoutes is the positive twin:
// an admin principal that completed TOTP step-up obtains ScopeAdmin in the
// session cookie and admin-gated REST routes return 200.
func TestScopeBoundary_AdminWithTOTPGets200OnAdminRoutes(t *testing.T) {
	_, addrs, done, cancel := startTestServer(t)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("server did not shut down")
		}
	})

	h := newAdminScopeHelper(t, addrs)
	if h.publicAddr == "" {
		t.Fatalf("public listener not bound; addrs=%+v", addrs)
	}
	if h.adminAddr == "" {
		t.Fatalf("admin listener not bound; addrs=%+v", addrs)
	}

	// Bootstrap and enroll TOTP.
	const adminEmail = "scope-boundary-admin2@example.com"
	adminPID, adminAPIKey, adminPassword := h.bootstrapAdmin(adminEmail)
	totpSecret := h.enrollTOTP(adminPID, adminAPIKey)
	time.Sleep(2 * time.Second)
	h.confirmTOTP(adminPID, adminAPIKey, totpSecret)

	// Login as admin with TOTP code.
	totpCode, err := adminScopeBoundaryOTPCode(totpSecret, time.Now())
	if err != nil {
		t.Fatalf("generate login TOTP code: %v", err)
	}
	adminCookies := h.loginAndGetCookies(adminEmail, adminPassword, totpCode)

	// Verify the login response carried ScopeAdmin in session_expires_at
	// (we can check via /api/v1/auth/whoami).
	req, _ := http.NewRequest("GET", "http://"+h.publicAddr+"/api/v1/auth/whoami", nil)
	for _, c := range adminCookies {
		req.AddCookie(c)
	}
	whoamiResp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	whoamiBody, _ := io.ReadAll(whoamiResp.Body)
	whoamiResp.Body.Close()
	if whoamiResp.StatusCode != http.StatusOK {
		t.Fatalf("whoami: status=%d body=%s, want 200", whoamiResp.StatusCode, whoamiBody)
	}
	var whoami map[string]any
	if err := json.Unmarshal(whoamiBody, &whoami); err != nil {
		t.Fatalf("whoami unmarshal: %v", err)
	}
	scopes, _ := whoami["scopes"].([]interface{})
	hasAdmin := false
	for _, s := range scopes {
		if s == "admin" {
			hasAdmin = true
		}
	}
	if !hasAdmin {
		t.Fatalf("admin login: scopes %v missing 'admin'; TOTP step-up did not grant ScopeAdmin", scopes)
	}

	// Admin-gated routes must return 200 for the admin session.
	code := h.getWithCookies("/api/v1/server/status", adminCookies)
	if code != http.StatusOK {
		t.Errorf("admin GET /api/v1/server/status: status=%d, want 200", code)
	}

	code = h.getWithCookies("/api/v1/domains", adminCookies)
	if code != http.StatusOK {
		t.Errorf("admin GET /api/v1/domains: status=%d, want 200", code)
	}
}
