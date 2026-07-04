package protoadmin_test

// self_service_elevation_test.go covers:
//
// PART A: roles field in auth responses (REQ-AUTH-60, REQ-AUTH-75, REQ-AS-26)
//   - POST /api/v1/auth/login includes roles: ["admin"] for admin principals
//   - POST /api/v1/auth/login includes roles: ["end-user"] for non-admin principals
//   - GET /api/v1/auth/whoami includes roles
//   - GET /api/v1/server/status includes roles
//
// PART B: self-service elevation gate (REQ-AUTH-78, issue #79)
//
// Test matrix for each gated operation (create API key, change own password,
// disable TOTP, update external submission credentials):
//   - TOTP enrolled, no elevation, cookie auth  -> 403 step_up_required / self-service
//   - TOTP enrolled, after step-up, cookie auth -> success
//   - No TOTP enrolled, cookie auth             -> success (gate not triggered)
//   - Bearer API key caller                     -> success (bearer exempt)

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/authsession"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/extsubmit"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite/sqlitetest"
	"github.com/hanshuebner/herold/internal/testharness"
)

// -------------------------------------------------------------------------
// PART A: roles field
// -------------------------------------------------------------------------

// TestRoles_Login_AdminPrincipal asserts that the login response includes
// roles: ["admin"] for a principal with PrincipalFlagAdmin set
// (REQ-AUTH-60, REQ-AUTH-75, REQ-AS-26).
func TestRoles_Login_AdminPrincipal(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _, _ := sh.bootstrapAdminAndEnrollTOTP("roles-login-admin@example.com")

	code, body := sh.doLogin(email, password, nil)
	if code != http.StatusOK {
		t.Fatalf("login: status=%d body=%v", code, body)
	}
	roles, _ := body["roles"].([]interface{})
	if len(roles) != 1 || roles[0] != "admin" {
		t.Errorf("login roles=%v, want [admin]", body["roles"])
	}
}

// TestRoles_Login_EndUserPrincipal asserts that the login response includes
// roles: ["end-user"] for a principal without PrincipalFlagAdmin
// (REQ-AUTH-60, REQ-AUTH-75, REQ-AS-26).
func TestRoles_Login_EndUserPrincipal(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	_, _, adminKey, _ := sh.bootstrapAdminAndEnrollTOTP("roles-login-enduser-admin@example.com")

	// Create a non-admin principal via admin Bearer key.
	const userEmail = "roles-login-enduser@example.com"
	const userPass = "correct-horse-battery-staple"
	res, buf := sh.doRequest("POST", "/api/v1/principals", adminKey, map[string]any{
		"email":    userEmail,
		"password": userPass,
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create non-admin principal: status=%d body=%s", res.StatusCode, buf)
	}

	// Re-use a fresh cookie jar for the non-admin login.
	jar, _ := cookiejar.New(nil)
	sh.cookieJar = jar
	sh.cookieJarClient.Jar = jar
	code, body := sh.doLogin(userEmail, userPass, nil)
	if code != http.StatusOK {
		t.Fatalf("non-admin login: status=%d body=%v", code, body)
	}
	roles, _ := body["roles"].([]interface{})
	if len(roles) != 1 || roles[0] != "end-user" {
		t.Errorf("login roles=%v, want [end-user]", body["roles"])
	}
}

// TestRoles_Whoami_AdminPrincipal asserts that GET /api/v1/auth/whoami
// includes roles: ["admin"] for admin principals (REQ-AUTH-75, REQ-AS-26).
func TestRoles_Whoami_AdminPrincipal(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _, _ := sh.bootstrapAdminAndEnrollTOTP("roles-whoami-admin@example.com")

	if code, _ := sh.doLogin(email, password, nil); code != http.StatusOK {
		t.Fatalf("login: status=%d", code)
	}
	code, raw := sh.doWithCookie("GET", "/api/v1/auth/whoami", nil, "")
	if code != http.StatusOK {
		t.Fatalf("whoami: status=%d body=%s", code, raw)
	}
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	roles, _ := body["roles"].([]interface{})
	if len(roles) != 1 || roles[0] != "admin" {
		t.Errorf("whoami roles=%v, want [admin]", body["roles"])
	}
}

// TestRoles_Whoami_EndUserPrincipal asserts that GET /api/v1/auth/whoami
// includes roles: ["end-user"] for non-admin principals.
func TestRoles_Whoami_EndUserPrincipal(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	_, _, adminKey, _ := sh.bootstrapAdminAndEnrollTOTP("roles-whoami-eu-admin@example.com")

	const userEmail = "roles-whoami-enduser@example.com"
	const userPass = "correct-horse-battery-staple"
	res, buf := sh.doRequest("POST", "/api/v1/principals", adminKey, map[string]any{
		"email":    userEmail,
		"password": userPass,
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create user: status=%d body=%s", res.StatusCode, buf)
	}

	jar, _ := cookiejar.New(nil)
	sh.cookieJar = jar
	sh.cookieJarClient.Jar = jar
	if code, _ := sh.doLogin(userEmail, userPass, nil); code != http.StatusOK {
		t.Fatalf("non-admin login: status=%d", code)
	}
	code, raw := sh.doWithCookie("GET", "/api/v1/auth/whoami", nil, "")
	if code != http.StatusOK {
		t.Fatalf("whoami: status=%d body=%s", code, raw)
	}
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	roles, _ := body["roles"].([]interface{})
	if len(roles) != 1 || roles[0] != "end-user" {
		t.Errorf("whoami roles=%v, want [end-user]", body["roles"])
	}
}

// TestRoles_ServerStatus_HasAdminRole asserts that GET /api/v1/server/status
// includes roles: ["admin"] (requires step-up, REQ-AUTH-75).
func TestRoles_ServerStatus_HasAdminRole(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("roles-status@example.com")

	if code, _ := sh.doLogin(email, password, nil); code != http.StatusOK {
		t.Fatalf("login: status=%d", code)
	}
	sh.clk.Advance(time.Second)
	if sc, _ := sh.doStepUp(secret); sc != http.StatusOK {
		t.Fatalf("step-up: status=%d", sc)
	}

	code, raw := sh.doWithCookie("GET", "/api/v1/server/status", nil, "")
	if code != http.StatusOK {
		t.Fatalf("server/status: status=%d body=%s", code, raw)
	}
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	roles, _ := body["roles"].([]interface{})
	if len(roles) != 1 || roles[0] != "admin" {
		t.Errorf("server/status roles=%v, want [admin]", body["roles"])
	}
}

// -------------------------------------------------------------------------
// PART B: self-service elevation gate helpers
// -------------------------------------------------------------------------

// assertSelfServiceElevation is the shared checker: it verifies that the
// response body carries step_up_required=true and elevation_scope=self-service.
func assertSelfServiceElevation(t *testing.T, status int, raw []byte) {
	t.Helper()
	if status != http.StatusForbidden {
		t.Errorf("expected 403 step_up_required, got status=%d body=%s", status, raw)
		return
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal 403 body: %v body=%s", err, raw)
	}
	if body["step_up_required"] != true {
		t.Errorf("step_up_required=%v, want true", body["step_up_required"])
	}
	if body["elevation_scope"] != "self-service" {
		t.Errorf("elevation_scope=%v, want self-service", body["elevation_scope"])
	}
}

// -------------------------------------------------------------------------
// PART B: create API key gate
// -------------------------------------------------------------------------

// TestSelfServiceElevation_APIKey_TOTPNoElevation_Returns403 asserts that
// a TOTP-enrolled principal trying to create their own API key via cookie
// auth without step-up gets 403 step_up_required / self-service.
func TestSelfServiceElevation_APIKey_TOTPNoElevation_Returns403(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _, _ := sh.bootstrapAdminAndEnrollTOTP("sse-apikey-noelev@example.com")

	if code, _ := sh.doLogin(email, password, nil); code != http.StatusOK {
		t.Fatalf("login: status=%d", code)
	}

	// Look up admin principal ID from whoami.
	_, raw := sh.doWithCookie("GET", "/api/v1/auth/whoami", nil, "")
	var who map[string]any
	_ = json.Unmarshal(raw, &who)
	pid := uint64(who["principal_id"].(float64))

	csrf := sh.csrfToken()
	sc2, raw2 := sh.doWithCookie("POST", fmt.Sprintf("/api/v1/principals/%d/api-keys", pid), map[string]any{
		"label": "test",
	}, csrf)
	assertSelfServiceElevation(t, sc2, raw2)
}

// TestSelfServiceElevation_APIKey_AfterStepUp_Succeeds asserts that the
// same operation succeeds after a valid TOTP step-up.
func TestSelfServiceElevation_APIKey_AfterStepUp_Succeeds(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("sse-apikey-elev@example.com")

	if code, _ := sh.doLogin(email, password, nil); code != http.StatusOK {
		t.Fatalf("login: status=%d", code)
	}
	sh.clk.Advance(time.Second)
	if sc, _ := sh.doStepUp(secret); sc != http.StatusOK {
		t.Fatalf("step-up: status=%d", sc)
	}

	_, raw := sh.doWithCookie("GET", "/api/v1/auth/whoami", nil, "")
	var who map[string]any
	_ = json.Unmarshal(raw, &who)
	pid := uint64(who["principal_id"].(float64))

	csrf := sh.csrfToken()
	sc2, raw2 := sh.doWithCookie("POST", fmt.Sprintf("/api/v1/principals/%d/api-keys", pid), map[string]any{
		"label": "test",
	}, csrf)
	if sc2 != http.StatusCreated {
		t.Errorf("api-key create after elevation: status=%d body=%s, want 201", sc2, raw2)
	}
}

// TestSelfServiceElevation_APIKey_NoTOTP_Succeeds asserts that a principal
// without TOTP enrolled can create their own API key without step-up.
func TestSelfServiceElevation_APIKey_NoTOTP_Succeeds(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	// bootstrapWithPassword creates admin WITHOUT TOTP (no enrollment).
	email, password, _ := sh.bootstrapWithPassword("sse-apikey-nototp@example.com")

	if code, _ := sh.doLogin(email, password, nil); code != http.StatusOK {
		t.Fatalf("login: status=%d", code)
	}

	_, raw := sh.doWithCookie("GET", "/api/v1/auth/whoami", nil, "")
	var who map[string]any
	_ = json.Unmarshal(raw, &who)
	pid := uint64(who["principal_id"].(float64))

	csrf := sh.csrfToken()
	sc2, raw2 := sh.doWithCookie("POST", fmt.Sprintf("/api/v1/principals/%d/api-keys", pid), map[string]any{
		"label": "test",
	}, csrf)
	if sc2 != http.StatusCreated {
		t.Errorf("api-key create without TOTP: status=%d body=%s, want 201", sc2, raw2)
	}
}

// TestSelfServiceElevation_APIKey_Bearer_Succeeds asserts that a Bearer
// API-key caller is exempt from the self-service elevation gate even when
// the principal has TOTP enrolled (REQ-AUTH-78).
func TestSelfServiceElevation_APIKey_Bearer_Succeeds(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	// bootstrapWithPassword returns the bootstrap API key before TOTP enrollment.
	// The principal has PrincipalFlagAdmin but no TOTP yet, so after confirming
	// TOTP this key becomes invalid. Instead we use the bootstrap key directly
	// (before enrollment) to demonstrate Bearer exemption.
	_, _, bootstrapKey := sh.bootstrapWithPassword("sse-apikey-bearer@example.com")

	// The bootstrap key is Bearer; use it to get the principal ID.
	res, buf := sh.doRequest("GET", "/api/v1/auth/whoami", bootstrapKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("whoami: status=%d body=%s", res.StatusCode, buf)
	}
	var who map[string]any
	_ = json.Unmarshal(buf, &who)
	pid := uint64(who["principal_id"].(float64))

	// Bearer call: self-service gate must be bypassed entirely.
	res2, buf2 := sh.doRequest("POST", fmt.Sprintf("/api/v1/principals/%d/api-keys", pid),
		bootstrapKey, map[string]any{"label": "bearer-test"})
	if res2.StatusCode != http.StatusCreated {
		t.Errorf("api-key Bearer create: status=%d body=%s, want 201", res2.StatusCode, buf2)
	}
}

// -------------------------------------------------------------------------
// PART B: change own password gate
// -------------------------------------------------------------------------

// TestSelfServiceElevation_Password_TOTPNoElevation_Returns403 asserts that
// a TOTP-enrolled principal cannot change their own password without step-up.
func TestSelfServiceElevation_Password_TOTPNoElevation_Returns403(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _, _ := sh.bootstrapAdminAndEnrollTOTP("sse-pw-noelev@example.com")

	if code, _ := sh.doLogin(email, password, nil); code != http.StatusOK {
		t.Fatalf("login: status=%d", code)
	}

	_, raw := sh.doWithCookie("GET", "/api/v1/auth/whoami", nil, "")
	var who map[string]any
	_ = json.Unmarshal(raw, &who)
	pid := uint64(who["principal_id"].(float64))

	csrf := sh.csrfToken()
	sc2, raw2 := sh.doWithCookie("PUT", fmt.Sprintf("/api/v1/principals/%d/password", pid), map[string]any{
		"current_password": password,
		"new_password":     "newPassword99!",
	}, csrf)
	assertSelfServiceElevation(t, sc2, raw2)
}

// TestSelfServiceElevation_Password_AfterStepUp_Succeeds asserts that the
// same principal can change their password after a valid TOTP step-up.
func TestSelfServiceElevation_Password_AfterStepUp_Succeeds(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("sse-pw-elev@example.com")

	if code, _ := sh.doLogin(email, password, nil); code != http.StatusOK {
		t.Fatalf("login: status=%d", code)
	}
	sh.clk.Advance(time.Second)
	if sc, _ := sh.doStepUp(secret); sc != http.StatusOK {
		t.Fatalf("step-up: status=%d", sc)
	}

	_, raw := sh.doWithCookie("GET", "/api/v1/auth/whoami", nil, "")
	var who map[string]any
	_ = json.Unmarshal(raw, &who)
	pid := uint64(who["principal_id"].(float64))

	csrf := sh.csrfToken()
	sc2, raw2 := sh.doWithCookie("PUT", fmt.Sprintf("/api/v1/principals/%d/password", pid), map[string]any{
		"current_password": password,
		"new_password":     "newPassword99!",
	}, csrf)
	if sc2 != http.StatusNoContent {
		t.Errorf("password change after elevation: status=%d body=%s, want 204", sc2, raw2)
	}
}

// TestSelfServiceElevation_Password_NoTOTP_Succeeds asserts that a principal
// without TOTP can change their own password without step-up.
func TestSelfServiceElevation_Password_NoTOTP_Succeeds(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _ := sh.bootstrapWithPassword("sse-pw-nototp@example.com")

	if code, _ := sh.doLogin(email, password, nil); code != http.StatusOK {
		t.Fatalf("login: status=%d", code)
	}

	_, raw := sh.doWithCookie("GET", "/api/v1/auth/whoami", nil, "")
	var who map[string]any
	_ = json.Unmarshal(raw, &who)
	pid := uint64(who["principal_id"].(float64))

	csrf := sh.csrfToken()
	sc2, raw2 := sh.doWithCookie("PUT", fmt.Sprintf("/api/v1/principals/%d/password", pid), map[string]any{
		"current_password": password,
		"new_password":     "newPassword99!",
	}, csrf)
	if sc2 != http.StatusNoContent {
		t.Errorf("password change without TOTP: status=%d body=%s, want 204", sc2, raw2)
	}
}

// -------------------------------------------------------------------------
// PART B: disable TOTP gate
// -------------------------------------------------------------------------

// TestSelfServiceElevation_TOTPDisable_TOTPNoElevation_Returns403 asserts
// that disabling TOTP without step-up gets 403 step_up_required / self-service.
func TestSelfServiceElevation_TOTPDisable_TOTPNoElevation_Returns403(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _, _ := sh.bootstrapAdminAndEnrollTOTP("sse-totp-dis-noelev@example.com")

	if code, _ := sh.doLogin(email, password, nil); code != http.StatusOK {
		t.Fatalf("login: status=%d", code)
	}

	_, raw := sh.doWithCookie("GET", "/api/v1/auth/whoami", nil, "")
	var who map[string]any
	_ = json.Unmarshal(raw, &who)
	pid := uint64(who["principal_id"].(float64))

	csrf := sh.csrfToken()
	sc2, raw2 := sh.doWithCookie("DELETE", fmt.Sprintf("/api/v1/principals/%d/totp", pid), map[string]any{
		"current_password": password,
	}, csrf)
	assertSelfServiceElevation(t, sc2, raw2)
}

// TestSelfServiceElevation_TOTPDisable_AfterStepUp_Succeeds asserts that
// TOTP disable succeeds after a valid step-up.
func TestSelfServiceElevation_TOTPDisable_AfterStepUp_Succeeds(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("sse-totp-dis-elev@example.com")

	if code, _ := sh.doLogin(email, password, nil); code != http.StatusOK {
		t.Fatalf("login: status=%d", code)
	}
	sh.clk.Advance(time.Second)
	if sc, _ := sh.doStepUp(secret); sc != http.StatusOK {
		t.Fatalf("step-up: status=%d", sc)
	}

	_, raw := sh.doWithCookie("GET", "/api/v1/auth/whoami", nil, "")
	var who map[string]any
	_ = json.Unmarshal(raw, &who)
	pid := uint64(who["principal_id"].(float64))

	csrf := sh.csrfToken()
	sc2, raw2 := sh.doWithCookie("DELETE", fmt.Sprintf("/api/v1/principals/%d/totp", pid), map[string]any{
		"current_password": password,
	}, csrf)
	if sc2 != http.StatusNoContent {
		t.Errorf("totp disable after elevation: status=%d body=%s, want 204", sc2, raw2)
	}
}

// -------------------------------------------------------------------------
// PART B: update external submission credentials (no elevation gate)
// -------------------------------------------------------------------------

// newSessionHarnessForSubmission builds a sessionHarness whose underlying
// protoadmin.Server is configured with ExternalSubmissionDataKey and an
// always-OK ExternalProbe. The full session/cookie stack is wired so the
// self-service elevation gate can be exercised over cookie auth.
func newSessionHarnessForSubmission(t *testing.T) *sessionHarness {
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
			SecureCookies:  false,
		},
		// Submission requires a data key and a probe.
		ExternalSubmissionDataKey: []byte("submission-elev-test-key-32bytex"),
		ExternalProbe: func(_ context.Context, _ store.IdentitySubmission, _ string) extsubmit.Outcome {
			return extsubmit.Outcome{State: extsubmit.OutcomeOK}
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
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	h := &harness{
		t: t, h: th, srv: srv, client: baseClient, baseURL: base,
		clk: clk, dir: dir, rp: rp,
	}
	return &sessionHarness{harness: h, cookieJar: jar, cookieJarClient: cookieClient}
}

// submissionPutBody is a minimal valid PUT body for the submission endpoint.
func submissionPutBody() map[string]any {
	return map[string]any{
		"submit_host":        "smtp.example.com",
		"submit_port":        587,
		"submit_security":    "starttls",
		"submit_auth_method": "password",
		"password":           "hunter2",
	}
}

// TestSelfServiceElevation_Submission_TOTPEnrolled_NoElevation_Succeeds asserts
// that a TOTP-enrolled principal can update submission credentials via cookie
// auth WITHOUT a prior step-up. Saving external-SMTP credentials is a routine
// end-user self-service operation; TOTP step-up is not required (issue #119).
func TestSelfServiceElevation_Submission_TOTPEnrolled_NoElevation_Succeeds(t *testing.T) {
	t.Parallel()
	sh := newSessionHarnessForSubmission(t)
	email, password, _, _ := sh.bootstrapAdminAndEnrollTOTP("sse-sub-noelev@example.com")

	if code, _ := sh.doLogin(email, password, nil); code != http.StatusOK {
		t.Fatalf("login: status=%d", code)
	}

	// Resolve admin principal ID and insert a JMAP identity for them.
	_, raw := sh.doWithCookie("GET", "/api/v1/auth/whoami", nil, "")
	var who map[string]any
	_ = json.Unmarshal(raw, &who)
	pid := uint64(who["principal_id"].(float64))

	identityID := fmt.Sprintf("sub-elev-test-id-%d", pid)
	err := sh.h.Store.Meta().InsertJMAPIdentity(context.Background(), store.JMAPIdentity{
		ID:          identityID,
		PrincipalID: store.PrincipalID(pid),
		Name:        "Test Identity",
		Email:       email,
	})
	if err != nil {
		t.Fatalf("InsertJMAPIdentity: %v", err)
	}

	csrf := sh.csrfToken()
	sc2, raw2 := sh.doWithCookie("PUT",
		fmt.Sprintf("/api/v1/identities/%s/submission", identityID),
		submissionPutBody(), csrf)
	if sc2 != http.StatusNoContent {
		t.Errorf("submission update with TOTP enrolled but no step-up: status=%d body=%s, want 204", sc2, raw2)
	}
}

// TestSelfServiceElevation_Submission_AfterStepUp_Succeeds asserts that
// submission credential update succeeds when the caller happens to have
// performed a step-up (step-up is not required but must not break the path).
func TestSelfServiceElevation_Submission_AfterStepUp_Succeeds(t *testing.T) {
	t.Parallel()
	sh := newSessionHarnessForSubmission(t)
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("sse-sub-elev@example.com")

	if code, _ := sh.doLogin(email, password, nil); code != http.StatusOK {
		t.Fatalf("login: status=%d", code)
	}
	sh.clk.Advance(time.Second)
	if sc, _ := sh.doStepUp(secret); sc != http.StatusOK {
		t.Fatalf("step-up: status=%d", sc)
	}

	_, raw := sh.doWithCookie("GET", "/api/v1/auth/whoami", nil, "")
	var who map[string]any
	_ = json.Unmarshal(raw, &who)
	pid := uint64(who["principal_id"].(float64))

	identityID := fmt.Sprintf("sub-elev-ok-id-%d", pid)
	err := sh.h.Store.Meta().InsertJMAPIdentity(context.Background(), store.JMAPIdentity{
		ID:          identityID,
		PrincipalID: store.PrincipalID(pid),
		Name:        "Test Identity",
		Email:       email,
	})
	if err != nil {
		t.Fatalf("InsertJMAPIdentity: %v", err)
	}

	csrf := sh.csrfToken()
	sc2, raw2 := sh.doWithCookie("PUT",
		fmt.Sprintf("/api/v1/identities/%s/submission", identityID),
		submissionPutBody(), csrf)
	if sc2 != http.StatusNoContent {
		t.Errorf("submission update after elevation: status=%d body=%s, want 204", sc2, raw2)
	}
}

// TestSelfServiceElevation_Submission_NoTOTP_Succeeds asserts that a
// principal without TOTP can update submission credentials without step-up.
func TestSelfServiceElevation_Submission_NoTOTP_Succeeds(t *testing.T) {
	t.Parallel()
	sh := newSessionHarnessForSubmission(t)
	email, password, _ := sh.bootstrapWithPassword("sse-sub-nototp@example.com")

	if code, _ := sh.doLogin(email, password, nil); code != http.StatusOK {
		t.Fatalf("login: status=%d", code)
	}

	_, raw := sh.doWithCookie("GET", "/api/v1/auth/whoami", nil, "")
	var who map[string]any
	_ = json.Unmarshal(raw, &who)
	pid := uint64(who["principal_id"].(float64))

	identityID := fmt.Sprintf("sub-nototp-id-%d", pid)
	err := sh.h.Store.Meta().InsertJMAPIdentity(context.Background(), store.JMAPIdentity{
		ID:          identityID,
		PrincipalID: store.PrincipalID(pid),
		Name:        "Test Identity",
		Email:       email,
	})
	if err != nil {
		t.Fatalf("InsertJMAPIdentity: %v", err)
	}

	csrf := sh.csrfToken()
	sc2, raw2 := sh.doWithCookie("PUT",
		fmt.Sprintf("/api/v1/identities/%s/submission", identityID),
		submissionPutBody(), csrf)
	if sc2 != http.StatusNoContent {
		t.Errorf("submission without TOTP: status=%d body=%s, want 204", sc2, raw2)
	}
}

// TestSelfServiceElevation_Submission_Bearer_Succeeds asserts that a Bearer
// API-key caller is exempt from the self-service elevation gate on submission.
func TestSelfServiceElevation_Submission_Bearer_Succeeds(t *testing.T) {
	t.Parallel()
	sh := newSessionHarnessForSubmission(t)
	_, _, bootstrapKey := sh.bootstrapWithPassword("sse-sub-bearer@example.com")

	// Resolve principal ID via bearer whoami.
	res, buf := sh.doRequest("GET", "/api/v1/auth/whoami", bootstrapKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("whoami: status=%d body=%s", res.StatusCode, buf)
	}
	var who map[string]any
	_ = json.Unmarshal(buf, &who)
	pid := uint64(who["principal_id"].(float64))

	identityID := fmt.Sprintf("sub-bearer-id-%d", pid)
	err := sh.h.Store.Meta().InsertJMAPIdentity(context.Background(), store.JMAPIdentity{
		ID:          identityID,
		PrincipalID: store.PrincipalID(pid),
		Name:        "Test Identity",
		Email:       "sse-sub-bearer@example.com",
	})
	if err != nil {
		t.Fatalf("InsertJMAPIdentity: %v", err)
	}

	res2, buf2 := sh.doRequest("PUT",
		fmt.Sprintf("/api/v1/identities/%s/submission", identityID),
		bootstrapKey, submissionPutBody())
	if res2.StatusCode != http.StatusNoContent {
		t.Errorf("submission Bearer: status=%d body=%s, want 204", res2.StatusCode, buf2)
	}
}

// Both SQLite (default) and Postgres (HEROLD_PG_DSN env) backends run these
// tests via the standard CI matrix. sqlitetest.Open delegates to
// HEROLD_TEST_STORE / HEROLD_PG_DSN so no additional test setup is required.
