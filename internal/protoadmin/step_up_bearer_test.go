package protoadmin_test

// step_up_bearer_test.go covers POST /api/v1/auth/step-up for
// Bearer-authenticated callers -- a device token (POST
// /api/v1/auth/device-token) or an OAuth2 access token (the
// authorization-code + PKCE grant) -- through the composed public
// handler (REQ-AUTH-74, REQ-AUTH-78, issue #357).
//
// Test matrix, run for each Bearer credential kind:
//   - self-service op returns step_up_required before any elevation
//   - POST /api/v1/auth/step-up with a valid TOTP code elevates the
//     credential; the op then succeeds
//   - the elevation expires per the configured window (fake clock)
//   - a wrong TOTP code is refused (401)
//   - five consecutive wrong codes are rate-limited the same way the
//     cookie path is -- directory.VerifyTOTP's lockout is keyed on
//     (principal email, source), not on credential kind, so the same
//     mechanism protects both
//   - a principal with no TOTP enrolled: the op succeeds without any
//     step-up (same rule as the cookie path, REQ-AUTH-78)

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"
)

// mustDeviceToken issues a Bearer device token for (email, password),
// supplying totpSecret's current code when non-empty. Fails the test on
// any non-201 response.
func mustDeviceToken(t *testing.T, h *harness, email, password, totpSecret string) string {
	t.Helper()
	body := map[string]any{"email": email, "password": password, "device_label": "step-up-bearer-test"}
	if totpSecret != "" {
		code, err := otpGenerateCode(totpSecret, h.clk.Now())
		if err != nil {
			t.Fatalf("otpGenerateCode: %v", err)
		}
		body["totp_code"] = code
	}
	res, buf := h.doRequest("POST", "/api/v1/auth/device-token", "", body)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("device-token: status=%d body=%s", res.StatusCode, buf)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatalf("unmarshal device-token response: %v: %s", err, buf)
	}
	return out.Token
}

// mustOAuthAccessToken drives the full authorization-code + PKCE grant
// for (email, password) -- supplying totpSecret's code as the second
// step when non-empty -- and returns the minted access token. The
// caller must have already registered the "herold-android" client on h
// (mustRegisterHTTPAndroidClient).
func mustOAuthAccessToken(t *testing.T, h *harness, email, password, totpSecret string) string {
	t.Helper()
	client := oauthNoRedirectClient(h)
	verifier, challenge := oauthPKCE(t)
	redirectURI := "net.netzhansa.herold:/oauth2redirect"
	state := fmt.Sprintf("state-%d", h.clk.Now().UnixNano())

	_, csrfCookie, reqField := oauthAuthorizeGet(t, client, h.baseURL, redirectURI, state, challenge)

	passRes, passBody := postOAuthAuthorize(t, client, h.baseURL, csrfCookie, url.Values{
		"req": {reqField}, "csrf": {csrfCookie},
		"email": {email}, "password": {password},
	})

	codeRes, codeBody := passRes, passBody
	if totpSecret != "" {
		if passRes.StatusCode != http.StatusOK {
			t.Fatalf("password step: status=%d body=%s, want 200 (code-only form)", passRes.StatusCode, passBody)
		}
		reqField2 := scrapeReqField(passBody)
		if reqField2 == "" {
			t.Fatalf("missing req field in code-only form: %s", passBody)
		}
		code, err := otpGenerateCode(totpSecret, h.clk.Now())
		if err != nil {
			t.Fatalf("otpGenerateCode: %v", err)
		}
		codeRes, codeBody = postOAuthAuthorize(t, client, h.baseURL, csrfCookie, url.Values{
			"req": {reqField2}, "csrf": {csrfCookie}, "totp_code": {code},
		})
	}
	if codeRes.StatusCode != http.StatusFound {
		t.Fatalf("authorize: status=%d body=%s, want 302", codeRes.StatusCode, codeBody)
	}
	loc, err := url.Parse(codeRes.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	authCode := loc.Query().Get("code")
	if authCode == "" {
		t.Fatalf("Location = %q, missing code", loc.String())
	}

	tokenForm := url.Values{
		"grant_type": {"authorization_code"}, "code": {authCode},
		"redirect_uri": {redirectURI}, "client_id": {"herold-android"},
		"code_verifier": {verifier},
	}
	tokenRes, tokenBody := h.doRequestForm("POST", "/oauth2/token", tokenForm)
	if tokenRes.StatusCode != http.StatusOK {
		t.Fatalf("token exchange: status=%d body=%s", tokenRes.StatusCode, tokenBody)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(tokenBody, &tok); err != nil {
		t.Fatalf("unmarshal token response: %v: %s", err, tokenBody)
	}
	if tok.AccessToken == "" {
		t.Fatalf("empty access_token in response: %s", tokenBody)
	}
	return tok.AccessToken
}

// bearerElevatedOp exercises the self-service-elevation-gated "create an
// API key for the caller's own principal" operation with the given
// Bearer token, returning (status, body).
func bearerElevatedOp(h *harness, token string, pid uint64) (int, []byte) {
	res, buf := h.doRequest("POST", fmt.Sprintf("/api/v1/principals/%d/api-keys", pid),
		token, map[string]any{"label": "bearer-elev-test"})
	return res.StatusCode, buf
}

// bearerStepUp posts totpCode to POST /api/v1/auth/step-up with the given
// Bearer token, returning (status, decoded body).
func bearerStepUp(h *harness, token, totpCode string) (int, map[string]any) {
	res, buf := h.doRequest("POST", "/api/v1/auth/step-up", token, map[string]any{
		"totp_code": totpCode,
	})
	var body map[string]any
	_ = json.Unmarshal(buf, &body)
	return res.StatusCode, body
}

// -------------------------------------------------------------------------
// Device token
// -------------------------------------------------------------------------

// TestStepUpBearer_DeviceToken_RequiredThenSucceeds is the device-token
// leg of the issue #357 acceptance criterion: the self-service op
// returns step_up_required before elevation, POST
// /api/v1/auth/step-up with a valid code elevates the token, and the op
// then succeeds.
func TestStepUpBearer_DeviceToken_RequiredThenSucceeds(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("stepup-bearer-dt-admin@example.com")
	const email = "stepup-bearer-dt@example.com"
	const password = "correct-horse-battery-staple"
	pid := h.createPrincipal(adminKey, email)
	secret := mustEnrollTOTP(t, h, pid)
	h.clk.Advance(time.Second)

	token := mustDeviceToken(t, h, email, password, secret)

	sc, buf := bearerElevatedOp(h, token, pid)
	assertSelfServiceElevation(t, sc, buf)

	h.clk.Advance(time.Second)
	code, err := otpGenerateCode(secret, h.clk.Now())
	if err != nil {
		t.Fatalf("otpGenerateCode: %v", err)
	}
	sc2, body2 := bearerStepUp(h, token, code)
	if sc2 != http.StatusOK {
		t.Fatalf("step-up: status=%d body=%v, want 200", sc2, body2)
	}
	if body2["elevation_expires_at"] == nil || body2["elevation_expires_at"] == "" {
		t.Errorf("elevation_expires_at missing from step-up response: %v", body2)
	}

	sc3, buf3 := bearerElevatedOp(h, token, pid)
	if sc3 != http.StatusCreated {
		t.Errorf("api-key create after bearer step-up: status=%d body=%s, want 201", sc3, buf3)
	}
}

// TestStepUpBearer_DeviceToken_Expiry asserts that a device token's
// elevation lapses after the configured idle window, the same way a
// cookie session's does (REQ-AUTH-74).
func TestStepUpBearer_DeviceToken_Expiry(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("stepup-bearer-dt-exp-admin@example.com")
	const email = "stepup-bearer-dt-exp@example.com"
	const password = "correct-horse-battery-staple"
	pid := h.createPrincipal(adminKey, email)
	secret := mustEnrollTOTP(t, h, pid)
	h.clk.Advance(time.Second)

	token := mustDeviceToken(t, h, email, password, secret)

	code, err := otpGenerateCode(secret, h.clk.Now())
	if err != nil {
		t.Fatalf("otpGenerateCode: %v", err)
	}
	if sc, body := bearerStepUp(h, token, code); sc != http.StatusOK {
		t.Fatalf("step-up: status=%d body=%v", sc, body)
	}

	// Op succeeds immediately after step-up.
	sc, buf := bearerElevatedOp(h, token, pid)
	if sc != http.StatusCreated {
		t.Fatalf("api-key create right after step-up: status=%d body=%s, want 201", sc, buf)
	}

	// Idle straight past the default 15-minute window with no interim
	// elevated activity: the elevation lapses.
	h.clk.Advance(stepUpDefaultElevationTTL + time.Minute)
	sc2, buf2 := bearerElevatedOp(h, token, pid)
	assertSelfServiceElevation(t, sc2, buf2)
}

// TestStepUpBearer_DeviceToken_WrongCode_Returns401 asserts that a wrong
// TOTP code on a device-token step-up returns 401, matching the cookie
// path.
func TestStepUpBearer_DeviceToken_WrongCode_Returns401(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("stepup-bearer-dt-wrong-admin@example.com")
	const email = "stepup-bearer-dt-wrong@example.com"
	const password = "correct-horse-battery-staple"
	pid := h.createPrincipal(adminKey, email)
	secret := mustEnrollTOTP(t, h, pid)
	h.clk.Advance(time.Second)

	token := mustDeviceToken(t, h, email, password, secret)

	sc, body := bearerStepUp(h, token, "000000")
	if sc != http.StatusUnauthorized {
		t.Errorf("step-up with wrong code: status=%d body=%v, want 401", sc, body)
	}
}

// TestStepUpBearer_DeviceToken_RateLimitedSameAsCookie asserts that five
// consecutive wrong codes lock out further step-up attempts (401 then
// 429), the same posture REQ-AUTH-74 specifies for a cookie session --
// the underlying lockout (directory.VerifyTOTP) is keyed on
// (principal email, source), not on credential kind, so both paths share
// the exact same enforcement.
func TestStepUpBearer_DeviceToken_RateLimitedSameAsCookie(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("stepup-bearer-dt-rl-admin@example.com")
	const email = "stepup-bearer-dt-rl@example.com"
	const password = "correct-horse-battery-staple"
	pid := h.createPrincipal(adminKey, email)
	secret := mustEnrollTOTP(t, h, pid)
	h.clk.Advance(time.Second)

	token := mustDeviceToken(t, h, email, password, secret)

	for i := 0; i < 5; i++ {
		sc, body := bearerStepUp(h, token, "000000")
		if sc != http.StatusUnauthorized {
			t.Fatalf("wrong attempt %d: status=%d body=%v, want 401", i+1, sc, body)
		}
	}
	// The 6th attempt -- even with the CORRECT code -- must be
	// rate-limited: the lockout blocks on attempt count, not on code
	// correctness.
	code, err := otpGenerateCode(secret, h.clk.Now())
	if err != nil {
		t.Fatalf("otpGenerateCode: %v", err)
	}
	sc, body := bearerStepUp(h, token, code)
	if sc != http.StatusTooManyRequests {
		t.Errorf("6th attempt (correct code, after 5 wrong): status=%d body=%v, want 429", sc, body)
	}
}

// TestStepUpBearer_DeviceToken_NoTOTPEnrolled_Succeeds asserts that a
// device-token caller whose principal has no TOTP enrolled can perform
// the self-service operation without ever stepping up -- the cookie
// path's existing rule (REQ-AUTH-78) applies identically to a Bearer
// caller.
func TestStepUpBearer_DeviceToken_NoTOTPEnrolled_Succeeds(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("stepup-bearer-dt-nototp-admin@example.com")
	const email = "stepup-bearer-dt-nototp@example.com"
	const password = "correct-horse-battery-staple"
	pid := h.createPrincipal(adminKey, email)

	token := mustDeviceToken(t, h, email, password, "")

	sc, buf := bearerElevatedOp(h, token, pid)
	if sc != http.StatusCreated {
		t.Errorf("api-key create without TOTP enrolled: status=%d body=%s, want 201", sc, buf)
	}
}

// -------------------------------------------------------------------------
// OAuth2 access token
// -------------------------------------------------------------------------

// TestStepUpBearer_OAuth2AccessToken_RequiredThenSucceeds is the OAuth2
// access-token leg of the issue #357 acceptance criterion.
func TestStepUpBearer_OAuth2AccessToken_RequiredThenSucceeds(t *testing.T) {
	h := newHarness(t)
	mustRegisterHTTPAndroidClient(t, h)
	_, adminKey := h.bootstrap("stepup-bearer-oauth-admin@example.com")
	const email = "stepup-bearer-oauth@example.com"
	const password = "correct-horse-battery-staple"
	pid := h.createPrincipal(adminKey, email)
	secret := mustEnrollTOTP(t, h, pid)
	h.clk.Advance(time.Second)

	token := mustOAuthAccessToken(t, h, email, password, secret)

	sc, buf := bearerElevatedOp(h, token, pid)
	assertSelfServiceElevation(t, sc, buf)

	h.clk.Advance(time.Second)
	code, err := otpGenerateCode(secret, h.clk.Now())
	if err != nil {
		t.Fatalf("otpGenerateCode: %v", err)
	}
	sc2, body2 := bearerStepUp(h, token, code)
	if sc2 != http.StatusOK {
		t.Fatalf("step-up: status=%d body=%v, want 200", sc2, body2)
	}
	if body2["elevation_expires_at"] == nil || body2["elevation_expires_at"] == "" {
		t.Errorf("elevation_expires_at missing from step-up response: %v", body2)
	}

	sc3, buf3 := bearerElevatedOp(h, token, pid)
	if sc3 != http.StatusCreated {
		t.Errorf("api-key create after oauth2 bearer step-up: status=%d body=%s, want 201", sc3, buf3)
	}
}

// TestStepUpBearer_OAuth2AccessToken_Expiry asserts that an OAuth2
// access token's elevation lapses after the configured idle window.
func TestStepUpBearer_OAuth2AccessToken_Expiry(t *testing.T) {
	h := newHarness(t)
	mustRegisterHTTPAndroidClient(t, h)
	_, adminKey := h.bootstrap("stepup-bearer-oauth-exp-admin@example.com")
	const email = "stepup-bearer-oauth-exp@example.com"
	const password = "correct-horse-battery-staple"
	pid := h.createPrincipal(adminKey, email)
	secret := mustEnrollTOTP(t, h, pid)
	h.clk.Advance(time.Second)

	token := mustOAuthAccessToken(t, h, email, password, secret)

	code, err := otpGenerateCode(secret, h.clk.Now())
	if err != nil {
		t.Fatalf("otpGenerateCode: %v", err)
	}
	if sc, body := bearerStepUp(h, token, code); sc != http.StatusOK {
		t.Fatalf("step-up: status=%d body=%v", sc, body)
	}

	sc, buf := bearerElevatedOp(h, token, pid)
	if sc != http.StatusCreated {
		t.Fatalf("api-key create right after step-up: status=%d body=%s, want 201", sc, buf)
	}

	h.clk.Advance(stepUpDefaultElevationTTL + time.Minute)
	sc2, buf2 := bearerElevatedOp(h, token, pid)
	assertSelfServiceElevation(t, sc2, buf2)
}

// TestStepUpBearer_OAuth2AccessToken_WrongCode_Returns401 asserts that a
// wrong TOTP code on an OAuth2-access-token step-up returns 401.
func TestStepUpBearer_OAuth2AccessToken_WrongCode_Returns401(t *testing.T) {
	h := newHarness(t)
	mustRegisterHTTPAndroidClient(t, h)
	_, adminKey := h.bootstrap("stepup-bearer-oauth-wrong-admin@example.com")
	const email = "stepup-bearer-oauth-wrong@example.com"
	const password = "correct-horse-battery-staple"
	pid := h.createPrincipal(adminKey, email)
	secret := mustEnrollTOTP(t, h, pid)
	h.clk.Advance(time.Second)

	token := mustOAuthAccessToken(t, h, email, password, secret)

	sc, body := bearerStepUp(h, token, "000000")
	if sc != http.StatusUnauthorized {
		t.Errorf("step-up with wrong code: status=%d body=%v, want 401", sc, body)
	}
}

// TestStepUpBearer_OAuth2AccessToken_NoTOTPEnrolled_Succeeds asserts that
// an OAuth2-access-token caller whose principal has no TOTP enrolled can
// perform the self-service operation without ever stepping up.
func TestStepUpBearer_OAuth2AccessToken_NoTOTPEnrolled_Succeeds(t *testing.T) {
	h := newHarness(t)
	mustRegisterHTTPAndroidClient(t, h)
	_, adminKey := h.bootstrap("stepup-bearer-oauth-nototp-admin@example.com")
	const email = "stepup-bearer-oauth-nototp@example.com"
	const password = "correct-horse-battery-staple"
	pid := h.createPrincipal(adminKey, email)

	token := mustOAuthAccessToken(t, h, email, password, "")

	sc, buf := bearerElevatedOp(h, token, pid)
	if sc != http.StatusCreated {
		t.Errorf("api-key create without TOTP enrolled: status=%d body=%s, want 201", sc, buf)
	}
}
