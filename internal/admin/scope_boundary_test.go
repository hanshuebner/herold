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

// stepUp posts a TOTP code to POST /api/v1/auth/step-up using the session
// and CSRF cookies from a prior login response. It returns the HTTP status
// code and the decoded JSON body.
func (h *adminScopeHelper) stepUp(cookies []*http.Cookie, totpCode string) (int, map[string]any) {
	h.t.Helper()
	// Locate the CSRF cookie so we can echo it back in X-CSRF-Token.
	var csrfValue string
	for _, c := range cookies {
		if c.Name == "herold_public_csrf" {
			csrfValue = c.Value
		}
	}
	b, _ := json.Marshal(map[string]any{"totp_code": totpCode})
	req, _ := http.NewRequest("POST", "http://"+h.publicAddr+"/api/v1/auth/step-up",
		bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if csrfValue != "" {
		req.Header.Set("X-CSRF-Token", csrfValue)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("POST /api/v1/auth/step-up: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
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

// TestScopeBoundary_AdminWithTOTPGets200OnAdminRoutes verifies the positive
// path: an admin principal with TOTP enrolled can login and reach
// admin-gated REST routes immediately. TOTP is required at login
// (REQ-AUTH-JSON-LOGIN, REQ-AUTH-42, issue #228):
//   - Login issues an end-user-scoped session (no admin scope in the cookie)
//     only after a valid totp_code is presented.
//   - A successful TOTP-gated login also creates an initial elevation record
//     (REQ-AUTH-74(a)), so admin-gated routes are reachable right away, with
//     no separate step-up prompt needed for the freshly authenticated session.
//   - POST /api/v1/auth/step-up still works to refresh/extend that elevation.
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

	// Login requires a valid totp_code for this TOTP-enrolled principal
	// (REQ-AUTH-JSON-LOGIN, REQ-AUTH-42, issue #228). Login always returns
	// end-user scope only in the cookie (REQ-AUTH-SCOPE-01); admin
	// authorization comes from the separate elevation record created below.
	loginCode, err := adminScopeBoundaryOTPCode(totpSecret, time.Now())
	if err != nil {
		t.Fatalf("generate login TOTP code: %v", err)
	}
	adminCookies := h.loginAndGetCookies(adminEmail, adminPassword, loginCode)

	// Verify that whoami reports end-user scopes only (no 'admin').
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
	for _, s := range scopes {
		if s == "admin" {
			t.Fatalf("login response must not contain 'admin' scope (REQ-AUTH-SCOPE-01): scopes=%v", scopes)
		}
	}

	// The TOTP-gated login already created an elevation record
	// (REQ-AUTH-74(a)), so admin-gated routes are reachable immediately.
	code := h.getWithCookies("/api/v1/server/status", adminCookies)
	if code != http.StatusOK {
		t.Errorf("admin GET /api/v1/server/status immediately after TOTP-gated login: status=%d, want 200", code)
	}

	// Step-up still works to (re-)elevate: POST /api/v1/auth/step-up with a
	// fresh TOTP code.
	time.Sleep(2 * time.Second)
	totpCode, err := adminScopeBoundaryOTPCode(totpSecret, time.Now())
	if err != nil {
		t.Fatalf("generate step-up TOTP code: %v", err)
	}
	stepStatus, stepBody := h.stepUp(adminCookies, totpCode)
	if stepStatus != http.StatusOK {
		t.Fatalf("step-up: status=%d body=%v, want 200", stepStatus, stepBody)
	}
	if _, ok := stepBody["elevation_expires_at"]; !ok {
		t.Errorf("step-up response missing elevation_expires_at: %v", stepBody)
	}

	// After step-up, whoami must carry elevation_expires_at.
	req, _ = http.NewRequest("GET", "http://"+h.publicAddr+"/api/v1/auth/whoami", nil)
	for _, c := range adminCookies {
		req.AddCookie(c)
	}
	whoamiResp2, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("whoami after step-up: %v", err)
	}
	whoamiBody2, _ := io.ReadAll(whoamiResp2.Body)
	whoamiResp2.Body.Close()
	if whoamiResp2.StatusCode != http.StatusOK {
		t.Fatalf("whoami after step-up: status=%d body=%s, want 200", whoamiResp2.StatusCode, whoamiBody2)
	}
	var whoami2 map[string]any
	_ = json.Unmarshal(whoamiBody2, &whoami2)
	if whoami2["elevation_expires_at"] == nil {
		t.Errorf("whoami after step-up: elevation_expires_at should be non-null: %v", whoami2)
	}

	// Admin-gated routes must now return 200 with an active elevation record.
	code = h.getWithCookies("/api/v1/server/status", adminCookies)
	if code != http.StatusOK {
		t.Errorf("admin GET /api/v1/server/status after step-up: status=%d, want 200", code)
	}

	code = h.getWithCookies("/api/v1/domains", adminCookies)
	if code != http.StatusOK {
		t.Errorf("admin GET /api/v1/domains after step-up: status=%d, want 200", code)
	}
}
