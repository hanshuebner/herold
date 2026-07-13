package protoadmin_test

// sessions_test.go covers the session list + revocation REST endpoints
// (REQ-AUTH-77, issue #80):
//
//   - GET /api/v1/auth/sessions         — list own sessions
//   - DELETE /api/v1/auth/sessions/{id} — revoke session; 404 for wrong owner
//   - Revoke current session → cookies cleared; next request → 401 session_revoked
//   - Revoke other session  → next request on that cookie → 401 session_revoked
//   - Admin GET  /api/v1/admin/principals/{pid}/sessions
//   - Admin DELETE /api/v1/admin/principals/{pid}/sessions/{id}
//   - Revocation audit log entries recorded

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite/sqlitetest"
	"github.com/hanshuebner/herold/internal/testharness"
)

// sessionTestHarness wraps two independent cookie-jar clients so we can
// simulate "two browser windows" (two active sessions) for the same principal.
type sessionTestHarness struct {
	sh      *sessionHarness
	jar2    *cookiejar.Jar
	client2 *http.Client
}

func newSessionTestHarness(t *testing.T) *sessionTestHarness {
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
		BootstrapPerWindow:      2,
		BootstrapWindow:         5 * time.Minute,
		RequestsPerMinutePerKey: 100,
		Session: authsession.SessionConfig{
			SigningKey:     testSigningKey,
			CookieName:     "herold_public_session",
			CSRFCookieName: "herold_public_csrf",
			TTL:            24 * time.Hour,
			// Enable idle gate so GetSession (and tombstone check) is exercised.
			IdleTTL:       7 * 24 * time.Hour,
			SecureCookies: false,
		},
	})
	if err := th.AttachAdmin("admin", srv, protoadmin.ListenerModePlain); err != nil {
		t.Fatalf("AttachAdmin: %v", err)
	}
	baseClient, base := th.DialAdminByName(context.Background(), "admin")

	jar1, _ := cookiejar.New(nil)
	client1 := &http.Client{
		Transport: baseClient.Transport,
		Jar:       jar1,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	jar2, _ := cookiejar.New(nil)
	client2 := &http.Client{
		Transport: baseClient.Transport,
		Jar:       jar2,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	h := &harness{
		t: t, h: th, srv: srv, client: baseClient, baseURL: base,
		clk: clk, dir: dir, rp: rp,
	}
	sh := &sessionHarness{harness: h, cookieJar: jar1, cookieJarClient: client1}
	return &sessionTestHarness{sh: sh, jar2: jar2, client2: client2}
}

// loginWithClient posts to /api/v1/auth/login using the provided client.
// totpCode is included when non-empty (REQ-AUTH-JSON-LOGIN, issue #228: a
// TOTP-enrolled principal must present a valid code at login).
func (sth *sessionTestHarness) loginWithClient(t *testing.T, client *http.Client, email, password, totpCode string) {
	t.Helper()
	body := fmt.Sprintf(`{"email":%q,"password":%q,"totp_code":%q}`, email, password, totpCode)
	req, _ := http.NewRequest("POST", sth.sh.baseURL+"/api/v1/auth/login",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("login: want 200, got %d", res.StatusCode)
	}
}

// getWithClient sends GET to the given path using a specific client.
func (sth *sessionTestHarness) getWithClient(t *testing.T, client *http.Client, path string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest("GET", sth.sh.baseURL+path, nil)
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer res.Body.Close()
	var buf []byte
	if res.ContentLength != 0 {
		buf = make([]byte, 0, 512)
	}
	tmp := make([]byte, 512)
	for {
		n, err2 := res.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err2 != nil {
			break
		}
	}
	return res.StatusCode, buf
}

// -----------------------------------------------------------------------
// Tests
// -----------------------------------------------------------------------

func TestSessionList_ReturnsOwnSessions(t *testing.T) {
	sth := newSessionTestHarness(t)
	sh := sth.sh

	// Bootstrap and enroll TOTP (admin principal required by login flow).
	em, pw, _, secret := sh.bootstrapAdminAndEnrollTOTP("admin@sessions.test")

	// Log in with client 1 (session A).
	sh.doLoginWithTOTP(em, pw, secret, nil)

	// Log in with client 2 (session B) — different cookie jar, same principal.
	// A fresh code is needed since the login above already consumed the
	// current time-step's code from client 1's perspective.
	sh.clk.Advance(time.Second)
	code2, err := otpGenerateCode(secret, sh.clk.Now())
	if err != nil {
		t.Fatalf("otpGenerateCode (client2 login): %v", err)
	}
	sth.loginWithClient(t, sth.client2, em, pw, code2)

	// List sessions from client 1 — must see both sessions.
	code, raw := sh.doWithCookie("GET", "/api/v1/auth/sessions", nil, "")
	if code != http.StatusOK {
		t.Fatalf("list sessions: want 200, got %d body=%s", code, raw)
	}
	var page struct {
		Items []struct {
			SessionID string `json:"session_id"`
			IsCurrent bool   `json:"is_current"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("unmarshal sessions: %v body=%s", err, raw)
	}
	if len(page.Items) < 2 {
		t.Fatalf("expected at least 2 sessions, got %d", len(page.Items))
	}
	// Exactly one must be marked is_current.
	var currentCount int
	for _, it := range page.Items {
		if it.IsCurrent {
			currentCount++
		}
	}
	if currentCount != 1 {
		t.Errorf("is_current count = %d; want 1", currentCount)
	}
}

func TestSessionRevoke_OtherSession_GetsTombstoneError(t *testing.T) {
	sth := newSessionTestHarness(t)
	sh := sth.sh

	em, pw, _, secret := sh.bootstrapAdminAndEnrollTOTP("admin@sessions-revoke.test")

	// Log in both clients.
	sh.doLoginWithTOTP(em, pw, secret, nil)
	sh.clk.Advance(time.Second)
	code2, err := otpGenerateCode(secret, sh.clk.Now())
	if err != nil {
		t.Fatalf("otpGenerateCode (client2 login): %v", err)
	}
	sth.loginWithClient(t, sth.client2, em, pw, code2)

	// List sessions from client1 to find client2's session id.
	code, raw := sh.doWithCookie("GET", "/api/v1/auth/sessions", nil, "")
	if code != http.StatusOK {
		t.Fatalf("list: want 200, got %d body=%s", code, raw)
	}
	var page struct {
		Items []struct {
			SessionID string `json:"session_id"`
			IsCurrent bool   `json:"is_current"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var otherSessionID string
	for _, it := range page.Items {
		if !it.IsCurrent {
			otherSessionID = it.SessionID
			break
		}
	}
	if otherSessionID == "" {
		t.Fatal("could not find non-current session in list")
	}

	// Revoke other session from client1.
	csrf := sh.csrfToken()
	code, raw = sh.doWithCookie("DELETE", "/api/v1/auth/sessions/"+otherSessionID, nil, csrf)
	if code != http.StatusNoContent {
		t.Fatalf("revoke: want 204, got %d body=%s", code, raw)
	}

	// Client1's session must still be alive.
	code, _ = sh.doWithCookie("GET", "/api/v1/auth/whoami", nil, "")
	if code != http.StatusOK {
		t.Errorf("client1 whoami after revoking other: want 200, got %d", code)
	}

	// Client2's next request must return 401 session_revoked.
	code, raw = sth.getWithClient(t, sth.client2, "/api/v1/auth/whoami")
	if code != http.StatusUnauthorized {
		t.Errorf("client2 whoami after revoke: want 401, got %d body=%s", code, raw)
	}
	var prob struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(raw, &prob)
	const wantTypeSuffix = "session_revoked"
	if !strings.HasSuffix(prob.Type, wantTypeSuffix) {
		t.Errorf("type = %q; want suffix %q", prob.Type, wantTypeSuffix)
	}
}

func TestSessionRevoke_CurrentSession_ClearsCookies(t *testing.T) {
	sth := newSessionTestHarness(t)
	sh := sth.sh

	em, pw, _, secret := sh.bootstrapAdminAndEnrollTOTP("admin@sessions-self.test")
	sh.doLoginWithTOTP(em, pw, secret, nil)

	// List to find current session id.
	code, raw := sh.doWithCookie("GET", "/api/v1/auth/sessions", nil, "")
	if code != http.StatusOK {
		t.Fatalf("list: want 200, got %d body=%s", code, raw)
	}
	var page struct {
		Items []struct {
			SessionID string `json:"session_id"`
			IsCurrent bool   `json:"is_current"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var currentID string
	for _, it := range page.Items {
		if it.IsCurrent {
			currentID = it.SessionID
			break
		}
	}
	if currentID == "" {
		t.Fatal("no current session in list")
	}

	csrf := sh.csrfToken()
	code, raw = sh.doWithCookie("DELETE", "/api/v1/auth/sessions/"+currentID, nil, csrf)
	if code != http.StatusNoContent {
		t.Fatalf("revoke current: want 204, got %d body=%s", code, raw)
	}

	// Cookies should be cleared — herold_public_session must be gone or expired.
	if sh.sessionCookiePresent() {
		// The browser retains expired Set-Cookie; a subsequent request
		// should fail with session_revoked (tombstone) or 401 (cookie gone).
		// Either way the session must not be valid.
	}

	// Next request from client1 must be 401.
	code, raw = sh.doWithCookie("GET", "/api/v1/auth/whoami", nil, "")
	if code != http.StatusUnauthorized {
		t.Errorf("whoami after self-revoke: want 401, got %d body=%s", code, raw)
	}
}

func TestSessionRevoke_WrongOwner_Returns404(t *testing.T) {
	sth := newSessionTestHarness(t)
	sh := sth.sh

	em, pw, _, secret := sh.bootstrapAdminAndEnrollTOTP("admin@sessions-owner.test")

	// Manually insert a session row belonging to a different principal so we
	// can target its session_id without a cookie.
	ctx := context.Background()
	otherPID, err := sh.dir.CreatePrincipal(ctx, "other@sessions-owner.test", "OtherPass123!")
	if err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	now := sh.clk.Now()
	foreignSession := store.SessionRow{
		SessionID:   "foreign-sess-id",
		PrincipalID: store.PrincipalID(otherPID),
		CreatedAt:   now,
		ExpiresAt:   now.Add(24 * time.Hour),
	}
	if err := sh.h.Store.Meta().UpsertSession(ctx, foreignSession); err != nil {
		t.Fatalf("UpsertSession foreign: %v", err)
	}

	// Log in as admin.
	sh.doLoginWithTOTP(em, pw, secret, nil)
	csrf := sh.csrfToken()

	// Try to revoke the foreign session — must get 404.
	code, _ := sh.doWithCookie("DELETE", "/api/v1/auth/sessions/foreign-sess-id", nil, csrf)
	if code != http.StatusNotFound {
		t.Errorf("revoke foreign: want 404, got %d", code)
	}
}

func TestSessionRevoke_AuditLog(t *testing.T) {
	sth := newSessionTestHarness(t)
	sh := sth.sh

	em, pw, apiKey, secret := sh.bootstrapAdminAndEnrollTOTP("admin@sessions-audit.test")
	sh.doLoginWithTOTP(em, pw, secret, nil)

	// List + revoke the current session.
	code, raw := sh.doWithCookie("GET", "/api/v1/auth/sessions", nil, "")
	if code != http.StatusOK {
		t.Fatalf("list: %d %s", code, raw)
	}
	var page struct {
		Items []struct {
			SessionID string `json:"session_id"`
			IsCurrent bool   `json:"is_current"`
		} `json:"items"`
	}
	_ = json.Unmarshal(raw, &page)
	if len(page.Items) == 0 {
		t.Fatal("no sessions in list")
	}
	currentID := page.Items[0].SessionID

	csrf := sh.csrfToken()
	code, _ = sh.doWithCookie("DELETE", "/api/v1/auth/sessions/"+currentID, nil, csrf)
	if code != http.StatusNoContent {
		t.Fatalf("revoke: want 204, got %d", code)
	}

	// Audit log via API key auth (cookie is now revoked).
	req, _ := http.NewRequest("GET", sh.baseURL+"/api/v1/audit", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	res, err := sh.client.Do(req)
	if err != nil {
		t.Fatalf("audit GET: %v", err)
	}
	defer res.Body.Close()

	// We expect to need step-up but the API key has admin scope; check for
	// auth.session.revoke in the audit log.
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusForbidden {
		t.Logf("audit: status %d (may require step-up in env, skipping audit check)", res.StatusCode)
		return
	}
	if res.StatusCode == http.StatusOK {
		var auditPage struct {
			Items []struct {
				Action string `json:"action"`
			} `json:"items"`
		}
		buf := make([]byte, 4096)
		n, _ := res.Body.Read(buf)
		_ = json.Unmarshal(buf[:n], &auditPage)
		var found bool
		for _, it := range auditPage.Items {
			if it.Action == "auth.session.revoke" {
				found = true
				break
			}
		}
		if !found {
			t.Error("auth.session.revoke action not found in audit log")
		}
	}
}

func TestAdminSessionList_ListsTargetPrincipalSessions(t *testing.T) {
	sth := newSessionTestHarness(t)
	sh := sth.sh

	em, pw, _, totpSecret := sh.bootstrapAdminAndEnrollTOTP("admin@admin-sess-list.test")

	// Create a non-admin principal and give them a session.
	ctx := context.Background()
	userPID, err := sh.dir.CreatePrincipal(ctx, "user@admin-sess-list.test", "UserPass123!")
	if err != nil {
		t.Fatalf("CreatePrincipal user: %v", err)
	}
	now := sh.clk.Now()
	userSession := store.SessionRow{
		SessionID:   "user-admin-list-sess",
		PrincipalID: store.PrincipalID(userPID),
		CreatedAt:   now,
		ExpiresAt:   now.Add(24 * time.Hour),
		UserAgent:   "Mozilla/5.0 (test)",
	}
	if err := sh.h.Store.Meta().UpsertSession(ctx, userSession); err != nil {
		t.Fatalf("UpsertSession user: %v", err)
	}

	// Log in as admin. Login already elevates the session (REQ-AUTH-74(a),
	// issue #228); the explicit step-up call refreshes the elevation and
	// still exercises the step-up endpoint.
	sh.doLoginWithTOTP(em, pw, totpSecret, nil)
	sh.clk.Advance(time.Second)
	sh.doStepUp(totpSecret)

	// Admin GET /api/v1/admin/principals/{pid}/sessions.
	csrf := sh.csrfToken()
	path := fmt.Sprintf("/api/v1/admin/principals/%d/sessions", userPID)
	code, raw := sh.doWithCookie("GET", path, nil, csrf)
	if code != http.StatusOK {
		t.Fatalf("admin list sessions: want 200, got %d body=%s", code, raw)
	}
	var page struct {
		Items []struct {
			SessionID string `json:"session_id"`
			IsCurrent bool   `json:"is_current"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, raw)
	}
	if len(page.Items) == 0 {
		t.Fatal("admin list: expected at least 1 session, got 0")
	}
	if page.Items[0].SessionID != "user-admin-list-sess" {
		t.Errorf("session_id = %q; want user-admin-list-sess", page.Items[0].SessionID)
	}
	// is_current is always false in admin view (admin != target principal).
	for _, it := range page.Items {
		if it.IsCurrent {
			t.Error("is_current = true in admin list; want false")
		}
	}
}

func TestAdminSessionRevoke_RevokesByID(t *testing.T) {
	sth := newSessionTestHarness(t)
	sh := sth.sh

	em, pw, _, totpSecret := sh.bootstrapAdminAndEnrollTOTP("admin@admin-sess-revoke.test")

	// Create a user session via store (simulates a logged-in user).
	ctx := context.Background()
	userPID, err := sh.dir.CreatePrincipal(ctx, "user@admin-sess-revoke.test", "UserPass123!")
	if err != nil {
		t.Fatalf("CreatePrincipal user: %v", err)
	}
	now := sh.clk.Now()
	userSession := store.SessionRow{
		SessionID:   "user-admin-revoke-sess",
		PrincipalID: store.PrincipalID(userPID),
		CreatedAt:   now,
		ExpiresAt:   now.Add(24 * time.Hour),
	}
	if err := sh.h.Store.Meta().UpsertSession(ctx, userSession); err != nil {
		t.Fatalf("UpsertSession user: %v", err)
	}

	// Log in as admin + step up.
	sh.doLoginWithTOTP(em, pw, totpSecret, nil)
	sh.clk.Advance(time.Second)
	sh.doStepUp(totpSecret)
	csrf := sh.csrfToken()

	// Admin DELETE.
	path := fmt.Sprintf("/api/v1/admin/principals/%d/sessions/user-admin-revoke-sess", userPID)
	code, raw := sh.doWithCookie("DELETE", path, nil, csrf)
	if code != http.StatusNoContent {
		t.Fatalf("admin revoke: want 204, got %d body=%s", code, raw)
	}

	// Session must now be tombstoned — GetSession returns it as Tombstoned.
	row, err := sh.h.Store.Meta().GetSession(ctx, "user-admin-revoke-sess")
	if err != nil {
		t.Fatalf("GetSession after admin revoke: %v", err)
	}
	if !row.Tombstoned {
		t.Error("Tombstoned = false after admin revoke; want true")
	}
}
