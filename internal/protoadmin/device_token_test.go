package protoadmin_test

// device_token_test.go covers POST /api/v1/auth/device-token, the
// bearer-token grant native clients use to obtain a Bearer token without
// a browser session (issue #199).
//
// Test matrix:
//   - happy path: password only (no TOTP enrolled) -> 201 + a usable
//     Bearer token
//   - bad password / unknown email -> 401, identical message
//     (anti-enumeration)
//   - TOTP enrolled: missing totp_code -> 401 step_up_required=true
//   - TOTP enrolled: wrong totp_code -> 401 step_up_required=true
//   - TOTP enrolled: correct totp_code -> 201
//   - the minted token authenticates GET /api/v1/auth/whoami
//   - self-service revoke (DELETE /api/v1/api-keys/{id} using the token
//     itself) makes the token unusable immediately after

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/directory"
)

func TestDeviceToken_Success(t *testing.T) {
	h := newHarness(t)
	adminPID, adminKey := h.bootstrap("device-token-admin@example.com")
	_ = adminPID
	const email = "device-token-user@example.com"
	const password = "correct-horse-battery-staple"
	pid := h.createPrincipal(adminKey, email)

	res, buf := h.doRequest("POST", "/api/v1/auth/device-token", "", map[string]any{
		"email":        email,
		"password":     password,
		"device_label": "Pixel 8",
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("device-token: status=%d body=%s", res.StatusCode, buf)
	}
	var out struct {
		APIKeyID    uint64   `json:"api_key_id"`
		PrincipalID uint64   `json:"principal_id"`
		Label       string   `json:"label"`
		Token       string   `json:"token"`
		Scope       []string `json:"scope"`
		CreatedAt   string   `json:"created_at"`
	}
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, buf)
	}
	if !strings.HasPrefix(out.Token, directory.DeviceTokenPrefix) {
		t.Fatalf("token missing Bearer prefix: %q", out.Token)
	}
	if out.PrincipalID != pid {
		t.Fatalf("principal_id = %d, want %d", out.PrincipalID, pid)
	}
	if out.Label != "Pixel 8" {
		t.Fatalf("label = %q, want %q", out.Label, "Pixel 8")
	}
	if len(out.Scope) == 0 {
		t.Fatalf("scope should not be empty")
	}
	for _, sc := range out.Scope {
		if sc == "admin" {
			t.Fatalf("device token must never carry admin scope: %v", out.Scope)
		}
	}

	// The minted token authenticates like any other Bearer credential.
	whoRes, whoBody := h.doRequest("GET", "/api/v1/auth/whoami", out.Token, nil)
	if whoRes.StatusCode != http.StatusOK {
		t.Fatalf("whoami with device token: status=%d body=%s", whoRes.StatusCode, whoBody)
	}
	var who struct {
		PrincipalID uint64 `json:"principal_id"`
		Email       string `json:"email"`
	}
	if err := json.Unmarshal(whoBody, &who); err != nil {
		t.Fatalf("unmarshal whoami: %v: %s", err, whoBody)
	}
	if who.PrincipalID != pid || who.Email != email {
		t.Fatalf("whoami = %+v, want principal_id=%d email=%s", who, pid, email)
	}
}

func TestDeviceToken_DefaultLabel(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("device-token-admin2@example.com")
	const email = "device-token-user2@example.com"
	h.createPrincipal(adminKey, email)

	res, buf := h.doRequest("POST", "/api/v1/auth/device-token", "", map[string]any{
		"email":    email,
		"password": "correct-horse-battery-staple",
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("device-token: status=%d body=%s", res.StatusCode, buf)
	}
	var out struct {
		Label string `json:"label"`
	}
	_ = json.Unmarshal(buf, &out)
	if out.Label != "device" {
		t.Fatalf("label = %q, want default \"device\"", out.Label)
	}
}

func TestDeviceToken_BadPassword(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("device-token-admin3@example.com")
	const email = "device-token-user3@example.com"
	h.createPrincipal(adminKey, email)

	res, buf := h.doRequest("POST", "/api/v1/auth/device-token", "", map[string]any{
		"email":    email,
		"password": "totally-wrong",
	})
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s, want 401", res.StatusCode, buf)
	}
}

func TestDeviceToken_UnknownEmail(t *testing.T) {
	h := newHarness(t)
	res, buf := h.doRequest("POST", "/api/v1/auth/device-token", "", map[string]any{
		"email":    "nobody@example.com",
		"password": "whatever12345",
	})
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s, want 401", res.StatusCode, buf)
	}
}

func TestDeviceToken_MissingFields(t *testing.T) {
	h := newHarness(t)
	res, buf := h.doRequest("POST", "/api/v1/auth/device-token", "", map[string]any{
		"email": "someone@example.com",
	})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", res.StatusCode, buf)
	}
}

// TestDeviceToken_TOTPStepUp exercises the TOTP-enrolled path: missing or
// wrong codes surface step_up_required; the correct code mints a token.
func TestDeviceToken_TOTPStepUp(t *testing.T) {
	sh := newSessionHarness(t)
	email, password, _, totpSecret := sh.bootstrapAdminAndEnrollTOTP("device-token-totp@example.com")

	// No code at all.
	res, buf := sh.doRequest("POST", "/api/v1/auth/device-token", "", map[string]any{
		"email":    email,
		"password": password,
	})
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no code: status=%d body=%s, want 401", res.StatusCode, buf)
	}
	var problem map[string]any
	_ = json.Unmarshal(buf, &problem)
	if problem["step_up_required"] != true {
		t.Fatalf("no code: body=%v, want step_up_required=true", problem)
	}

	// Wrong code.
	res2, buf2 := sh.doRequest("POST", "/api/v1/auth/device-token", "", map[string]any{
		"email":     email,
		"password":  password,
		"totp_code": "000000",
	})
	if res2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong code: status=%d body=%s, want 401", res2.StatusCode, buf2)
	}
	var problem2 map[string]any
	_ = json.Unmarshal(buf2, &problem2)
	if problem2["step_up_required"] != true {
		t.Fatalf("wrong code: body=%v, want step_up_required=true", problem2)
	}

	// Correct code.
	code, err := otpGenerateCode(totpSecret, sh.clk.Now())
	if err != nil {
		t.Fatalf("otpGenerateCode: %v", err)
	}
	res3, buf3 := sh.doRequest("POST", "/api/v1/auth/device-token", "", map[string]any{
		"email":     email,
		"password":  password,
		"totp_code": code,
	})
	if res3.StatusCode != http.StatusCreated {
		t.Fatalf("correct code: status=%d body=%s, want 201", res3.StatusCode, buf3)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(buf3, &out); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, buf3)
	}
	if out.Token == "" {
		t.Fatalf("expected a minted token")
	}
}

// TestDeviceToken_SelfServiceRevoke asserts that a device token can list
// and delete itself through the existing self-service API-key surface
// (REQ-AND-AUTH-21: sign-out revokes the token server-side), and that the
// revoked token is immediately rejected afterward.
func TestDeviceToken_SelfServiceRevoke(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("device-token-admin4@example.com")
	const email = "device-token-user4@example.com"
	h.createPrincipal(adminKey, email)

	res, buf := h.doRequest("POST", "/api/v1/auth/device-token", "", map[string]any{
		"email":    email,
		"password": "correct-horse-battery-staple",
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("device-token: status=%d body=%s", res.StatusCode, buf)
	}
	var out struct {
		APIKeyID uint64 `json:"api_key_id"`
		Token    string `json:"token"`
	}
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, buf)
	}

	// The token can list its own principal's keys (self-service surface).
	listRes, listBody := h.doRequest("GET", "/api/v1/api-keys", out.Token, nil)
	if listRes.StatusCode != http.StatusOK {
		t.Fatalf("list own keys: status=%d body=%s", listRes.StatusCode, listBody)
	}
	if !strings.Contains(string(listBody), "device:") {
		t.Fatalf("own-key listing should show the device-token label, got %s", listBody)
	}

	// Self-revoke.
	delRes, delBody := h.doRequest("DELETE",
		"/api/v1/api-keys/"+strconv.FormatUint(out.APIKeyID, 10), out.Token, nil)
	if delRes.StatusCode != http.StatusNoContent {
		t.Fatalf("self-revoke: status=%d body=%s, want 204", delRes.StatusCode, delBody)
	}

	// The revoked token is rejected immediately.
	whoRes, whoBody := h.doRequest("GET", "/api/v1/auth/whoami", out.Token, nil)
	if whoRes.StatusCode != http.StatusUnauthorized {
		t.Fatalf("whoami after revoke: status=%d body=%s, want 401", whoRes.StatusCode, whoBody)
	}
}
