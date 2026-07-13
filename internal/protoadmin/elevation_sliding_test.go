package protoadmin_test

// elevation_sliding_test.go covers the idle/absolute elevation model
// (REQ-AUTH-74, REQ-AUTH-ELEV-CONFIG, issue #225): admin elevation must
// slide forward on continued admin-SPA activity instead of expiring on a
// fixed wall-clock deadline from grant time, while a hard absolute cap
// still forces re-step-up regardless of activity. All timing is driven by
// the harness's fake clock (clock.NewFake / sh.clk.Advance) -- no real
// sleeps.
//
// Test matrix:
//   - idle past the idle deadline with NO activity -> elevation gone, 403
//     step_up_required (not 200).
//   - continuous admin-route activity extends the elevation PAST the
//     original fixed idle deadline -- this is the bug from issue #225;
//     it must fail against the pre-fix behaviour and pass after the fix.
//   - continuous activity never extends the elevation past the absolute
//     cap -- the operator is eventually forced to re-step-up regardless
//     of how active they are.
//   - a non-admin principal's login-time elevation record (issue #228)
//     cannot be used to reach an admin-only route: the caller is rejected
//     on the admin-role check before the elevation is ever consulted or
//     extended.
//   - logout (session revocation) terminates the elevation immediately:
//     a subsequent admin request on the same (now-tombstoned) cookie
//     fails, it does not fall through to a stale-but-still-active
//     elevation.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/authsession"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/storesqlite/sqlitetest"
	"github.com/hanshuebner/herold/internal/testharness"
)

// newSessionHarnessWithElevationTTLs builds a sessionHarness whose
// protoadmin.Server is configured with explicit elevation idle/absolute
// TTLs (REQ-AUTH-74, REQ-AUTH-ELEV-CONFIG, issue #225), so tests can drive
// short, deterministic windows against the fake clock without waiting on
// the 15m/8h production defaults.
func newSessionHarnessWithElevationTTLs(t *testing.T, idleTTL, absoluteTTL time.Duration) *sessionHarness {
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
		RequestsPerMinutePerKey: 1_000_000,
		Session: authsession.SessionConfig{
			SigningKey:     testSigningKey,
			CookieName:     "herold_public_session",
			CSRFCookieName: "herold_public_csrf",
			TTL:            24 * time.Hour,
			SecureCookies:  false,
		},
		ElevationIdleTTL:     idleTTL,
		ElevationAbsoluteTTL: absoluteTTL,
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

// adminGET issues GET /api/v1/principals (an authAdmin-gated route) over
// the harness's cookie jar and returns the status code.
func (sh *sessionHarness) adminGET() int {
	sh.t.Helper()
	code, _ := sh.doWithCookie("GET", "/api/v1/principals", nil, "")
	return code
}

// TestElevation_IdlePastDeadline_NoActivity_Returns403 verifies that an
// elevation with no interim admin-route activity expires at its idle
// deadline: the admin route returns 403 step_up_required, not 200
// (REQ-AUTH-74).
func TestElevation_IdlePastDeadline_NoActivity_Returns403(t *testing.T) {
	t.Parallel()
	idleTTL := 15 * time.Minute
	sh := newSessionHarnessWithElevationTTLs(t, idleTTL, 8*time.Hour)
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("elev-idle-noact@example.com")

	if code, _ := sh.doLoginWithTOTP(email, password, secret, nil); code != http.StatusOK {
		t.Fatalf("login: status=%d", code)
	}

	// Confirm elevated immediately after login.
	if code := sh.adminGET(); code != http.StatusOK {
		t.Fatalf("admin route immediately after login: status=%d, want 200", code)
	}

	// Go idle past the idle deadline with no interim activity.
	sh.clk.Advance(idleTTL + time.Minute)

	code, raw := sh.doWithCookie("GET", "/api/v1/principals", nil, "")
	if code != http.StatusForbidden {
		t.Fatalf("admin route after idle expiry: status=%d body=%s, want 403", code, raw)
	}
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	if body["step_up_required"] != true {
		t.Errorf("step_up_required=%v, want true: %s", body["step_up_required"], raw)
	}
	if body["elevation_scope"] != "admin" {
		t.Errorf("elevation_scope=%v, want admin: %s", body["elevation_scope"], raw)
	}
}

// TestElevation_ContinuousActivity_ExtendsPastOriginalFixedDeadline is the
// regression test for issue #225: an operator who keeps issuing admin
// requests must never be interrupted by the elevation's ORIGINAL fixed
// idle deadline, because each passing request slides the idle deadline
// forward. Before the fix, this test fails at the "past original
// deadline, second call" step with 403 -- ExtendElevation did not exist
// and no code path ever advanced idle_deadline_us.
func TestElevation_ContinuousActivity_ExtendsPastOriginalFixedDeadline(t *testing.T) {
	t.Parallel()
	idleTTL := 15 * time.Minute
	sh := newSessionHarnessWithElevationTTLs(t, idleTTL, 4*time.Hour)
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("elev-slide@example.com")

	if code, _ := sh.doLoginWithTOTP(email, password, secret, nil); code != http.StatusOK {
		t.Fatalf("login: status=%d", code)
	}
	// Elevation granted at T0: idle_deadline = T0+15m.

	// T0+10m: within the original idle window. This call, on the
	// pre-fix code, would still succeed (the fixed deadline has not yet
	// elapsed) but would NOT extend idle_deadline_us.
	sh.clk.Advance(10 * time.Minute)
	if code := sh.adminGET(); code != http.StatusOK {
		t.Fatalf("admin route at T0+10m: status=%d, want 200", code)
	}

	// T0+20m: PAST the original fixed idle deadline (T0+15m). On the
	// pre-fix code this returns 403 step_up_required even though the
	// operator has been continuously active. With the fix, the T0+10m
	// call above slid idle_deadline_us to T0+10m+15m = T0+25m, so this
	// call at T0+20m must still succeed.
	sh.clk.Advance(10 * time.Minute)
	code, raw := sh.doWithCookie("GET", "/api/v1/principals", nil, "")
	if code != http.StatusOK {
		t.Fatalf("admin route at T0+20m (past original fixed 15m deadline): status=%d body=%s, want 200 -- elevation must slide on continued activity (REQ-AUTH-74, issue #225)", code, raw)
	}

	// Keep going: three more 10-minute hops with an admin call at each,
	// each one past where the ORIGINAL fixed deadline would have fired,
	// demonstrating the window keeps sliding rather than being a one-off
	// grace period.
	elapsed := 20 * time.Minute
	for hop := 0; hop < 3; hop++ {
		sh.clk.Advance(10 * time.Minute)
		elapsed += 10 * time.Minute
		if code := sh.adminGET(); code != http.StatusOK {
			t.Fatalf("admin route at T0+%v (hop %d): status=%d, want 200", elapsed, hop, code)
		}
	}
}

// TestElevation_ContinuousActivity_DoesNotExceedAbsoluteCap verifies that
// no amount of continuous, idle-window-respecting activity extends the
// elevation past its absolute deadline (REQ-AUTH-74, issue #225): the
// absolute cap is fixed at grant time and ExtendElevation clamps every
// slide to it.
func TestElevation_ContinuousActivity_DoesNotExceedAbsoluteCap(t *testing.T) {
	t.Parallel()
	idleTTL := 10 * time.Minute
	absoluteTTL := 20 * time.Minute
	sh := newSessionHarnessWithElevationTTLs(t, idleTTL, absoluteTTL)
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("elev-abscap@example.com")

	if code, _ := sh.doLoginWithTOTP(email, password, secret, nil); code != http.StatusOK {
		t.Fatalf("login: status=%d", code)
	}
	// Elevation granted at T0: idle_deadline = T0+10m, absolute_deadline = T0+20m.

	// Every 5 minutes -- comfortably inside the 10m idle window each
	// time -- issue an admin request. Each one succeeds and slides the
	// idle deadline forward, clamped to the T0+20m absolute deadline.
	// Cumulative elapsed times: T0+5m, T0+10m, T0+15m, T0+19m (the last
	// hop stops one minute short of the absolute deadline so the request
	// still lands strictly before it).
	cumulative := time.Duration(0)
	for _, hop := range []time.Duration{5 * time.Minute, 5 * time.Minute, 5 * time.Minute, 4 * time.Minute} {
		sh.clk.Advance(hop)
		cumulative += hop
		if code := sh.adminGET(); code != http.StatusOK {
			t.Fatalf("admin route at T0+%v: status=%d, want 200", cumulative, code)
		}
	}

	// T0+24m: past the absolute deadline (T0+20m). Despite the operator
	// never having gone idle for longer than 5 minutes at a stretch, the
	// absolute cap must still force re-step-up.
	sh.clk.Advance(5 * time.Minute)
	code, raw := sh.doWithCookie("GET", "/api/v1/principals", nil, "")
	if code != http.StatusForbidden {
		t.Fatalf("admin route past absolute cap despite continuous activity: status=%d body=%s, want 403", code, raw)
	}
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	if body["step_up_required"] != true {
		t.Errorf("step_up_required=%v, want true: %s", body["step_up_required"], raw)
	}
}

// TestElevation_NonAdminLoginElevation_CannotReachAdminRoute verifies that
// a non-admin principal's login-time elevation record (issue #228: a
// TOTP-gated login elevates the session regardless of role) confers no
// admin reach: requireElevation rejects the caller on the admin-role check
// before the elevation record is ever consulted, so the response is the
// generic "forbidden" (403, no step_up_required) rather than a
// step_up_required prompt or, worse, a 200.
func TestElevation_NonAdminLoginElevation_CannotReachAdminRoute(t *testing.T) {
	t.Parallel()
	sh := newSessionHarnessWithElevationTTLs(t, 15*time.Minute, 8*time.Hour)
	_, _, adminKey := sh.bootstrapWithPassword("elev-nonadmin-admin@example.com")

	// Create a non-admin principal and enroll TOTP for them via the
	// admin Bearer key.
	const userEmail = "elev-nonadmin-user@example.com"
	const userPass = "correct-horse-battery-staple"
	res, buf := sh.doRequest("POST", "/api/v1/principals", adminKey, map[string]any{
		"email":    userEmail,
		"password": userPass,
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create non-admin principal: status=%d body=%s", res.StatusCode, buf)
	}

	pidCtx, err := sh.dir.Authenticate(context.Background(), userEmail, userPass)
	if err != nil {
		t.Fatalf("Authenticate non-admin: %v", err)
	}
	secret, _, err := sh.dir.EnrollTOTP(context.Background(), pidCtx)
	if err != nil {
		t.Fatalf("EnrollTOTP non-admin: %v", err)
	}
	code, err := otpGenerateCode(secret, sh.clk.Now())
	if err != nil {
		t.Fatalf("otpGenerateCode (enroll): %v", err)
	}
	if err := sh.dir.ConfirmTOTP(context.Background(), pidCtx, code); err != nil {
		t.Fatalf("ConfirmTOTP non-admin: %v", err)
	}
	sh.clk.Advance(time.Second)

	// Log in as the non-admin principal via a fresh cookie jar.
	jar, _ := cookiejar.New(nil)
	sh.cookieJar = jar
	sh.cookieJarClient.Jar = jar
	loginCode, loginBody := sh.doLoginWithTOTP(userEmail, userPass, secret, nil)
	if loginCode != http.StatusOK {
		t.Fatalf("non-admin TOTP-gated login: status=%d body=%v", loginCode, loginBody)
	}
	// Confirm the login-time elevation was in fact created for this
	// non-admin session (issue #228) -- otherwise this test would not
	// be exercising the intended scenario.
	if loginBody["elevation_expires_at"] == nil {
		t.Fatalf("non-admin login elevation_expires_at missing/nil; issue #228 behaviour not exercised: %v", loginBody)
	}

	// The non-admin session, despite carrying a live elevation record,
	// must not reach an admin-only route.
	getCode, raw := sh.doWithCookie("GET", "/api/v1/principals", nil, "")
	if getCode != http.StatusForbidden {
		t.Fatalf("admin route with non-admin elevated session: status=%d body=%s, want 403", getCode, raw)
	}
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	if body["step_up_required"] == true {
		t.Errorf("non-admin rejection must not carry step_up_required=true (that would imply an admin identity just needs to re-elevate): %s", raw)
	}
	typeStr, _ := body["type"].(string)
	if !strings.Contains(typeStr, "forbidden") {
		t.Errorf(`response type=%v, want it to contain "forbidden": %s`, body["type"], raw)
	}
}

// TestElevation_Logout_TerminatesElevationImmediately verifies that
// logging out tombstones the session (and cascades to its elevation
// record) so a subsequent admin request on the same, now-stale cookie is
// rejected -- it does not fall through to a still-active elevation
// (REQ-AUTH-74, REQ-AUTH-76, REQ-AUTH-77).
func TestElevation_Logout_TerminatesElevationImmediately(t *testing.T) {
	t.Parallel()
	sh := newSessionHarnessWithElevationTTLs(t, 15*time.Minute, 8*time.Hour)
	email, password, _, secret := sh.bootstrapAdminAndEnrollTOTP("elev-logout@example.com")

	if code, _ := sh.doLoginWithTOTP(email, password, secret, nil); code != http.StatusOK {
		t.Fatalf("login: status=%d", code)
	}
	if code := sh.adminGET(); code != http.StatusOK {
		t.Fatalf("admin route before logout: status=%d, want 200", code)
	}

	csrf := sh.csrfToken()
	if code, _ := sh.doWithCookie("POST", "/api/v1/auth/logout", nil, csrf); code != http.StatusNoContent {
		t.Fatalf("logout: status=%d, want 204", code)
	}

	// The browser still holds the (now server-side-tombstoned) cookie;
	// a subsequent admin request must fail rather than succeed off a
	// stale elevation record.
	code, raw := sh.doWithCookie("GET", "/api/v1/principals", nil, "")
	if code == http.StatusOK {
		t.Fatalf("admin route after logout: status=%d body=%s, want failure (401/403), got success", code, raw)
	}
}
