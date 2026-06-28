package protoadmin_test

// step_up_test.go covers POST /api/v1/auth/step-up (REQ-AUTH-74, issue #79).
//
// Test matrix:
//   - valid TOTP code -> 200 + elevation_expires_at
//   - invalid TOTP code -> 401
//   - not enrolled -> 400 enroll_required
//   - non-admin principal -> 403
//   - no CSRF header -> 403
//   - admin endpoint without elevation -> 403 step_up_required
//   - admin endpoint after elevation -> 200
//   - elevation expiry gates access after TTL
//   - whoami shows elevation_expires_at after step-up
//   - whoami shows null elevation_expires_at before step-up
//   - Bearer API-key admin bypasses elevation check

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"testing"
	"time"
)

// TestStepUp_ValidTOTP_CreatesElevation asserts that a correct TOTP code
// returns 200 with elevation_expires_at, and the session can then reach
// admin-gated endpoints (REQ-AUTH-74, issue #79).
func TestStepUp_ValidTOTP_CreatesElevation(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("stepup-valid@example.com")

	// Login (password only).
	if code, _ := sh.doLogin(email, password, nil); code != http.StatusOK {
		t.Fatalf("login: status=%d", code)
	}

	// Step up.
	sh.clk.Advance(time.Second)
	sc, body := sh.doStepUp(secret)
	if sc != http.StatusOK {
		t.Fatalf("step-up: status=%d body=%v, want 200", sc, body)
	}
	expiresAt, _ := body["elevation_expires_at"].(string)
	if expiresAt == "" {
		t.Fatalf("elevation_expires_at missing from step-up response: %v", body)
	}
	exp, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		t.Fatalf("parse elevation_expires_at: %v", err)
	}
	// Default TTL is 15 minutes.
	want := sh.clk.Now().Add(15 * time.Minute)
	if diff := exp.Sub(want); diff < -time.Minute || diff > time.Minute {
		t.Errorf("elevation_expires_at=%s; want ~%s (diff=%s)", exp, want, diff)
	}

	// Admin endpoint must be accessible now.
	code, _ := sh.doWithCookie("GET", "/api/v1/principals", nil, "")
	if code != http.StatusOK {
		t.Errorf("admin GET after elevation: status=%d, want 200", code)
	}
}

// TestStepUp_InvalidCode_Returns401 asserts that a wrong TOTP code returns 401.
func TestStepUp_InvalidCode_Returns401(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _, _ := sh.bootstrapAdminAndEnrollTOTP("stepup-bad-code@example.com")

	if code, _ := sh.doLogin(email, password, nil); code != http.StatusOK {
		t.Fatalf("login: status=%d", code)
	}

	csrf := sh.csrfToken()
	sc, raw := sh.doWithCookie("POST", "/api/v1/auth/step-up", map[string]any{
		"totp_code": "000000", // deliberately wrong
	}, csrf)
	if sc != http.StatusUnauthorized {
		t.Errorf("step-up with wrong code: status=%d body=%s, want 401", sc, raw)
	}
}

// TestStepUp_NotEnrolled_Returns400EnrollRequired asserts that attempting
// step-up when TOTP is not enrolled returns 400 with enroll_required=true.
func TestStepUp_NotEnrolled_Returns400EnrollRequired(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _ := sh.bootstrapWithPassword("stepup-noenroll@example.com")

	if code, _ := sh.doLogin(email, password, nil); code != http.StatusOK {
		t.Fatalf("login: status=%d", code)
	}

	csrf := sh.csrfToken()
	sc, raw := sh.doWithCookie("POST", "/api/v1/auth/step-up", map[string]any{
		"totp_code": "123456",
	}, csrf)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	if sc != http.StatusBadRequest {
		t.Errorf("step-up without enrollment: status=%d body=%s, want 400", sc, raw)
	}
	if body["enroll_required"] != true {
		t.Errorf("enroll_required not true: %v", body)
	}
	if body["enroll_url"] == nil {
		t.Errorf("enroll_url missing from response: %v", body)
	}
}

// TestStepUp_NonAdmin_Returns403 asserts that a non-admin principal attempting
// step-up gets 403 (elevation is only meaningful for admin principals).
func TestStepUp_NonAdmin_Returns403(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)

	// Bootstrap admin to get an API key for creating the non-admin principal.
	_, _, adminKey := sh.bootstrapWithPassword("stepup-admin2@example.com")

	// Create a non-admin principal.
	const nonAdminEmail = "stepup-nonadmin@example.com"
	const nonAdminPass = "hunter2hunter2hunter2"
	res, raw := sh.doRequest("POST", "/api/v1/principals", adminKey, map[string]any{
		"email":    nonAdminEmail,
		"password": nonAdminPass,
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create non-admin principal: status=%d body=%s", res.StatusCode, raw)
	}

	// Login as the non-admin principal with a fresh cookie jar.
	jar, _ := cookiejar.New(nil)
	sh.cookieJar = jar
	sh.cookieJarClient.Jar = jar
	if code, _ := sh.doLogin(nonAdminEmail, nonAdminPass, nil); code != http.StatusOK {
		t.Fatalf("non-admin login: status=%d", code)
	}

	csrf := sh.csrfToken()
	sc, raw2 := sh.doWithCookie("POST", "/api/v1/auth/step-up", map[string]any{
		"totp_code": "123456",
	}, csrf)
	if sc != http.StatusForbidden {
		t.Errorf("step-up as non-admin: status=%d body=%s, want 403", sc, raw2)
	}
}

// TestStepUp_NoCSRF_Returns403 asserts that step-up without an X-CSRF-Token
// returns 403 (CSRF gate fires inside requireAuth, REQ-AUTH-CSRF).
func TestStepUp_NoCSRF_Returns403(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _, _ := sh.bootstrapAdminAndEnrollTOTP("stepup-nocsrf@example.com")

	if code, _ := sh.doLogin(email, password, nil); code != http.StatusOK {
		t.Fatalf("login: status=%d", code)
	}

	sc, raw := sh.doWithCookie("POST", "/api/v1/auth/step-up", map[string]any{
		"totp_code": "123456",
	}, "" /* no CSRF */)
	if sc != http.StatusForbidden {
		t.Errorf("step-up without CSRF: status=%d body=%s, want 403", sc, raw)
	}
}

// TestStepUp_AdminEndpoint_BlockedWithoutElevation asserts that admin-gated
// endpoints return 403 with step_up_required when no elevation exists.
func TestStepUp_AdminEndpoint_BlockedWithoutElevation(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _, _ := sh.bootstrapAdminAndEnrollTOTP("stepup-blocked@example.com")

	if code, _ := sh.doLogin(email, password, nil); code != http.StatusOK {
		t.Fatalf("login: status=%d", code)
	}

	sc, raw := sh.doWithCookie("GET", "/api/v1/principals", nil, "")
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	if sc != http.StatusForbidden {
		t.Errorf("admin GET without elevation: status=%d body=%s, want 403", sc, raw)
	}
	if body["step_up_required"] != true {
		t.Errorf("step_up_required not true in 403 body: %v", body)
	}
	if body["elevation_scope"] != "admin" {
		t.Errorf("elevation_scope=%v, want admin", body["elevation_scope"])
	}
}

// TestStepUp_ElevationExpiry_BlocksAfterTTL asserts that once the elevation
// record expires, admin endpoints return 403 again.
func TestStepUp_ElevationExpiry_BlocksAfterTTL(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("stepup-expiry@example.com")

	if code, _ := sh.doLogin(email, password, nil); code != http.StatusOK {
		t.Fatalf("login: status=%d", code)
	}
	sh.clk.Advance(time.Second)
	if sc, _ := sh.doStepUp(secret); sc != http.StatusOK {
		t.Fatalf("step-up: status=%d", sc)
	}

	// Just inside the 15-minute window: admin endpoint accessible.
	sh.clk.Advance(14 * time.Minute)
	if sc, _ := sh.doWithCookie("GET", "/api/v1/principals", nil, ""); sc != http.StatusOK {
		t.Errorf("admin GET before expiry: status=%d, want 200", sc)
	}

	// Just past the window: elevation expired, admin endpoint returns 403.
	sh.clk.Advance(2 * time.Minute)
	sc, _ := sh.doWithCookie("GET", "/api/v1/principals", nil, "")
	if sc != http.StatusForbidden {
		t.Errorf("admin GET after elevation expiry: status=%d, want 403", sc)
	}
}

// TestStepUp_WhoamiShowsElevationExpiresAt asserts that GET /api/v1/auth/whoami
// returns a non-null elevation_expires_at after step-up, and null before (REQ-AUTH-74).
func TestStepUp_WhoamiShowsElevationExpiresAt(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("stepup-whoami@example.com")

	if code, _ := sh.doLogin(email, password, nil); code != http.StatusOK {
		t.Fatalf("login: status=%d", code)
	}

	// Before step-up: elevation_expires_at key must be present, value null.
	sc, raw := sh.doWithCookie("GET", "/api/v1/auth/whoami", nil, "")
	if sc != http.StatusOK {
		t.Fatalf("whoami: status=%d", sc)
	}
	var pre map[string]any
	_ = json.Unmarshal(raw, &pre)
	if _, ok := pre["elevation_expires_at"]; !ok {
		t.Errorf("elevation_expires_at key missing from whoami response before step-up: %v", pre)
	}
	if pre["elevation_expires_at"] != nil {
		t.Errorf("elevation_expires_at should be null before step-up; got %v", pre["elevation_expires_at"])
	}

	// Step up.
	sh.clk.Advance(time.Second)
	if sc, _ := sh.doStepUp(secret); sc != http.StatusOK {
		t.Fatalf("step-up: status=%d", sc)
	}

	// After step-up: elevation_expires_at must be a non-empty RFC 3339 string.
	sc2, raw2 := sh.doWithCookie("GET", "/api/v1/auth/whoami", nil, "")
	if sc2 != http.StatusOK {
		t.Fatalf("whoami after step-up: status=%d", sc2)
	}
	var post map[string]any
	_ = json.Unmarshal(raw2, &post)
	elevStr, _ := post["elevation_expires_at"].(string)
	if elevStr == "" {
		t.Errorf("elevation_expires_at should be set after step-up; got %v", post)
	}
	if _, err := time.Parse(time.RFC3339, elevStr); err != nil {
		t.Errorf("elevation_expires_at parse: %v", err)
	}
}

// TestStepUp_AuthMe_ShowsElevationExpiresAt asserts that GET /api/v1/auth/me
// also returns elevation_expires_at (used by the Suite SPA, REQ-AUTH-74).
func TestStepUp_AuthMe_ShowsElevationExpiresAt(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("stepup-authme@example.com")

	if code, _ := sh.doLogin(email, password, nil); code != http.StatusOK {
		t.Fatalf("login: status=%d", code)
	}

	// Before step-up.
	sc, raw := sh.doWithCookie("GET", "/api/v1/auth/me", nil, "")
	if sc != http.StatusOK {
		t.Fatalf("auth/me: status=%d body=%s", sc, raw)
	}
	var pre map[string]any
	_ = json.Unmarshal(raw, &pre)
	if pre["elevation_expires_at"] != nil {
		t.Errorf("elevation_expires_at before step-up: %v, want null", pre["elevation_expires_at"])
	}

	// Step up.
	sh.clk.Advance(time.Second)
	if sc, _ := sh.doStepUp(secret); sc != http.StatusOK {
		t.Fatalf("step-up: status=%d", sc)
	}

	// After step-up: field must be a non-empty RFC 3339 string.
	sc2, raw2 := sh.doWithCookie("GET", "/api/v1/auth/me", nil, "")
	if sc2 != http.StatusOK {
		t.Fatalf("auth/me after step-up: status=%d", sc2)
	}
	var post map[string]any
	_ = json.Unmarshal(raw2, &post)
	elevStr, _ := post["elevation_expires_at"].(string)
	if elevStr == "" {
		t.Errorf("elevation_expires_at missing from /auth/me after step-up: %v", post)
	}
}

// TestStepUp_BearerAdminNoElevationNeeded asserts that an admin Bearer API
// key can reach admin endpoints without any TOTP step-up. Bearer keys carry
// explicit ScopeAdmin and bypass the elevation gate (REQ-AUTH-SCOPE-02).
func TestStepUp_BearerAdminNoElevationNeeded(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	_, _, apiKey := sh.bootstrapWithPassword("stepup-bearer-admin@example.com")

	// Directly use the Bearer key on an admin endpoint — no login, no step-up.
	res, _ := sh.doRequest("GET", "/api/v1/principals", apiKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Errorf("Bearer admin GET: status=%d, want 200", res.StatusCode)
	}
}

// TestStepUp_Re_ElevationRefreshesWindow asserts that a second step-up
// while already elevated resets the expiry window.
func TestStepUp_Re_ElevationRefreshesWindow(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("stepup-refresh@example.com")

	if code, _ := sh.doLogin(email, password, nil); code != http.StatusOK {
		t.Fatalf("login: status=%d", code)
	}

	// First step-up.
	sh.clk.Advance(time.Second)
	if sc, _ := sh.doStepUp(secret); sc != http.StatusOK {
		t.Fatalf("first step-up: status=%d", sc)
	}

	// Advance 10 minutes (still inside 15-minute window), then re-elevate.
	sh.clk.Advance(10 * time.Minute)
	sh.clk.Advance(time.Second)
	if sc, _ := sh.doStepUp(secret); sc != http.StatusOK {
		t.Fatalf("second step-up: status=%d", sc)
	}

	// 14 more minutes: without re-elevation original expiry (t+15m) would
	// have passed (t+10m+14m = t+24m > t+15m). With re-elevation the window
	// is t+10m+1s+15m, so at t+10m+1s+14m we are still inside it.
	sh.clk.Advance(14 * time.Minute)
	sc, _ := sh.doWithCookie("GET", "/api/v1/principals", nil, "")
	if sc != http.StatusOK {
		t.Errorf("admin GET after re-elevation: status=%d, want 200", sc)
	}
}

// TestStepUp_WhoamiNullBeforeElevation asserts that the elevation_expires_at
// key is present but null in whoami when the principal lacks TOTP enrollment.
func TestStepUp_WhoamiNullBeforeElevation(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _ := sh.bootstrapWithPassword(
		fmt.Sprintf("stepup-null-%d@example.com", 1))

	if code, _ := sh.doLogin(email, password, nil); code != http.StatusOK {
		t.Fatalf("login: status=%d", code)
	}

	sc, raw := sh.doWithCookie("GET", "/api/v1/auth/whoami", nil, "")
	if sc != http.StatusOK {
		t.Fatalf("whoami: status=%d", sc)
	}
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	if _, ok := body["elevation_expires_at"]; !ok {
		t.Errorf("elevation_expires_at key missing from whoami response: %v", body)
	}
	if body["elevation_expires_at"] != nil {
		t.Errorf("elevation_expires_at should be null without elevation; got %v", body["elevation_expires_at"])
	}
}
