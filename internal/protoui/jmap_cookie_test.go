package protoui_test

// TestJMAP_CookieAuth verifies the full Wave 3.7-A login-to-JMAP flow:
//
//  1. POST /ui/login on the public listener -> 303, sets herold_public_session.
//  2. GET /.well-known/jmap with the cookie -> 200 with JMAP session descriptor.
//  3. The session descriptor's accountId matches the logged-in principal.
//
// This covers the concrete breakage chain described in the wave brief:
// the cookie's Path=/ now accompanies requests to /.well-known/jmap,
// and the JMAP auth middleware's SessionResolver path resolves the
// principal from the cookie.
//
// Also verifies the negative case: the same flow against the admin
// listener must NOT produce a cookie that authenticates JMAP.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/auth"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/protoui"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
)

// jmapCookieHarness is a self-contained harness that runs both a
// protoui server and a protojmap server on the same in-process
// http.ServeMux, bound to an httptest.Server.  This mirrors the
// production public-listener composition in internal/admin/server.go
// without importing the admin package (which would introduce cycles).
type jmapCookieHarness struct {
	t         *testing.T
	clk       *clock.FakeClock
	dir       *directory.Directory
	st        *fakestore.Store
	uiSrv     *protoui.Server
	jmapSrv   *protojmap.Server
	httpSrv   *httptest.Server
	client    *http.Client
	cookieJar *cookiejar.Jar
}

func newJMAPCookieHarness(t *testing.T, listenerKind string) *jmapCookieHarness {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := fakestore.New(fakestore.Options{Clock: clk, BlobDir: t.TempDir()})
	if err != nil {
		t.Fatalf("fakestore: %v", err)
	}
	dir := directory.New(fs.Meta(), nil, clk, nil)
	rp := directoryoidc.New(fs.Meta(), nil, &http.Client{Timeout: 5 * time.Second}, clk)

	cookieName := "herold_" + listenerKind + "_session"
	csrfName := "herold_" + listenerKind + "_csrf"

	uiSrv, err := protoui.NewServer(fs, dir, rp, clk, protoui.Options{
		PathPrefix:   "/ui",
		ListenerKind: listenerKind,
		Session: protoui.SessionConfig{
			SigningKey:     []byte("jmap-cookie-test-key-32-bytes..."),
			TTL:            1 * time.Hour,
			SecureCookies:  false,
			CookieName:     cookieName,
			CSRFCookieName: csrfName,
		},
	})
	if err != nil {
		t.Fatalf("protoui.NewServer: %v", err)
	}

	// Compose the mux mirroring the public-listener shape.
	mux := http.NewServeMux()
	mux.Handle("/ui/", uiSrv.Handler())

	// Root-path adapter: /login -> /ui/login (mirrors admin/server.go).
	uiHandler := uiSrv.Handler()
	for _, root := range []string{"/login", "/logout"} {
		root := root
		mux.HandleFunc(root, func(w http.ResponseWriter, r *http.Request) {
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/ui" + r.URL.Path
			uiHandler.ServeHTTP(w, r2)
		})
	}

	// Build the JMAP server with a SessionResolver wired from protoui.
	var sessionResolver protojmap.SessionResolver
	if listenerKind == "public" {
		sessionResolver = uiSrv.ResolveSessionWithScope
	}
	jmapSrv := protojmap.NewServer(
		fs, dir, nil,
		nil, // logger
		clk,
		protojmap.Options{
			SessionResolver:    sessionResolver,
			DownloadRatePerSec: -1, // disable rate limit in tests
		},
	)
	mux.Handle("/.well-known/jmap", jmapSrv.Handler())
	mux.Handle("/jmap", jmapSrv.Handler())
	mux.Handle("/jmap/", jmapSrv.Handler())

	httpSrv := httptest.NewServer(mux)
	t.Cleanup(httpSrv.Close)

	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar: jar,
		// Do not follow redirects so tests can inspect 303 Location.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	return &jmapCookieHarness{
		t:         t,
		clk:       clk,
		dir:       dir,
		st:        fs,
		uiSrv:     uiSrv,
		jmapSrv:   jmapSrv,
		httpSrv:   httpSrv,
		client:    client,
		cookieJar: jar,
	}
}

func (h *jmapCookieHarness) createPrincipal(email, password string) store.PrincipalID {
	h.t.Helper()
	pid, err := h.dir.CreatePrincipal(context.Background(), email, password)
	if err != nil {
		h.t.Fatalf("CreatePrincipal(%q): %v", email, err)
	}
	return pid
}

func (h *jmapCookieHarness) login(email, password string) {
	h.t.Helper()
	form := url.Values{
		"email":    []string{email},
		"password": []string{password},
	}
	req, _ := http.NewRequest("POST", h.httpSrv.URL+"/ui/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("login POST: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther {
		h.t.Fatalf("login: status=%d, want 303", res.StatusCode)
	}
}

// TestJMAP_PublicCookieAuth_WellKnownJMAP performs the end-to-end
// login -> JMAP discovery flow and asserts the response is a valid
// JMAP session descriptor referencing the logged-in principal's
// account.
func TestJMAP_PublicCookieAuth_WellKnownJMAP(t *testing.T) {
	t.Parallel()
	h := newJMAPCookieHarness(t, "public")
	pid := h.createPrincipal("jmap-user@example.test", "hunter2hunter2hunter2")

	// Step 1: log in via the protoui login flow.
	h.login("jmap-user@example.test", "hunter2hunter2hunter2")

	// Step 2: GET /.well-known/jmap with the cookie jar.
	req, _ := http.NewRequest("GET", h.httpSrv.URL+"/.well-known/jmap", nil)
	res, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("GET /.well-known/jmap: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("GET /.well-known/jmap: status=%d, want 200; body=%s", res.StatusCode, body)
	}

	// Step 3: parse the JMAP session descriptor and verify the
	// principal's accountId appears.
	var descriptor struct {
		Accounts map[string]json.RawMessage `json:"accounts"`
	}
	body, _ := io.ReadAll(res.Body)
	if err := json.Unmarshal(body, &descriptor); err != nil {
		t.Fatalf("parse session descriptor: %v; body=%s", err, body)
	}
	// JMAP account IDs use the "a<pid>" format (see protojmap.AccountIDForPrincipal).
	wantAccountID := fmt.Sprintf("a%d", pid)
	if _, ok := descriptor.Accounts[wantAccountID]; !ok {
		t.Fatalf("session descriptor accounts=%v, want accountId %q", descriptor.Accounts, wantAccountID)
	}
}

// TestJMAP_PublicCookieAuth_UnauthenticatedReturns401 asserts that
// /.well-known/jmap without a valid session cookie returns 401.
func TestJMAP_PublicCookieAuth_UnauthenticatedReturns401(t *testing.T) {
	t.Parallel()
	h := newJMAPCookieHarness(t, "public")
	// Do NOT log in; the cookie jar is empty.

	req, _ := http.NewRequest("GET", h.httpSrv.URL+"/.well-known/jmap", nil)
	res, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("GET /.well-known/jmap: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401 (no cookie, no Authorization)", res.StatusCode)
	}
}

// TestJMAP_BearerWinsOverCookie verifies that an explicit Bearer
// credential is used in preference to the cookie when both are
// present, and that the Bearer principal wins.
func TestJMAP_BearerWinsOverCookie(t *testing.T) {
	t.Parallel()
	h := newJMAPCookieHarness(t, "public")
	pid1 := h.createPrincipal("cookie-user@example.test", "hunter2hunter2hunter2")
	pid2 := h.createPrincipal("bearer-user@example.test", "hunter2hunter2hunter2")
	_ = pid1

	// Log in as pid1 so the cookie jar has a valid cookie.
	h.login("cookie-user@example.test", "hunter2hunter2hunter2")

	// Create an API key for pid2 and use it as Bearer.
	// The hash mirrors protojmap.hashAPIKey (SHA-256 hex, same as
	// protoadmin.HashAPIKey — redeclared here to avoid cross-package import).
	ctx := context.Background()
	plainKey := "hk_testbearerkeyforpid2_32bytes!"
	sum := sha256.Sum256([]byte(plainKey))
	keyHash := hex.EncodeToString(sum[:])
	scopeJSON, _ := auth.NewScopeSet(auth.AllEndUserScopes...).MarshalJSON()
	if _, err := h.st.Meta().InsertAPIKey(ctx, store.APIKey{
		PrincipalID: pid2,
		Hash:        keyHash,
		Name:        "test-key",
		ScopeJSON:   string(scopeJSON),
		CreatedAt:   h.clk.Now(),
	}); err != nil {
		t.Fatalf("InsertAPIKey: %v", err)
	}

	req, _ := http.NewRequest("GET", h.httpSrv.URL+"/.well-known/jmap", nil)
	req.Header.Set("Authorization", "Bearer "+plainKey)
	res, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("GET /.well-known/jmap: %v", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", res.StatusCode, body)
	}
	var descriptor struct {
		Accounts map[string]json.RawMessage `json:"accounts"`
	}
	if err := json.Unmarshal(body, &descriptor); err != nil {
		t.Fatalf("parse descriptor: %v; body=%s", err, body)
	}
	// Bearer principal (pid2) should win.
	// JMAP account IDs use "a<pid>" format.
	wantID := fmt.Sprintf("a%d", pid2)
	if _, ok := descriptor.Accounts[wantID]; !ok {
		t.Fatalf("Bearer principal %s not in accounts=%v", wantID, descriptor.Accounts)
	}
	// Cookie principal (pid1) should NOT appear.
	pid1ID := fmt.Sprintf("a%d", pid1)
	if _, ok := descriptor.Accounts[pid1ID]; ok {
		t.Fatalf("cookie principal %s should not appear when Bearer is present", pid1ID)
	}
}

// TestJMAP_LoginWithReturnParam tests the full suite-initiated login
// flow: GET /login?return=/#/mail -> form has redirect=/#/mail ->
// POST /ui/login with redirect=/#/mail -> 303 to /#/mail -> JMAP
// request with cookie returns 200.
func TestJMAP_LoginWithReturnParam(t *testing.T) {
	t.Parallel()
	h := newJMAPCookieHarness(t, "public")
	_ = h.createPrincipal("ret-user@example.test", "hunter2hunter2hunter2")

	// Step 1: GET /login?return=/#/mail and verify the form renders the
	// redirect field correctly.
	req, _ := http.NewRequest("GET", h.httpSrv.URL+"/login?return=%2F%23%2Fmail", nil)
	// Allow redirect following for the GET so we reach the final form.
	followClient := *h.client
	followClient.CheckRedirect = nil
	res, err := followClient.Do(req)
	if err != nil {
		t.Fatalf("GET /login?return=: %v", err)
	}
	defer res.Body.Close()
	formBody, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(formBody), `value="/#/mail"`) {
		t.Fatalf("login form missing redirect value for /#/mail; excerpt:\n%s",
			bodyExcerpt(string(formBody), "redirect"))
	}

	// Step 2: POST /ui/login with return=/#/mail (no redirect field).
	form := url.Values{
		"email":    []string{"ret-user@example.test"},
		"password": []string{"hunter2hunter2hunter2"},
		"return":   []string{"/#/mail"},
	}
	req2, _ := http.NewRequest("POST", h.httpSrv.URL+"/ui/login", strings.NewReader(form.Encode()))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res2, err := h.client.Do(req2)
	if err != nil {
		t.Fatalf("POST /ui/login: %v", err)
	}
	res2.Body.Close()
	if res2.StatusCode != http.StatusSeeOther {
		t.Fatalf("login status=%d, want 303", res2.StatusCode)
	}
	if loc := res2.Header.Get("Location"); loc != "/#/mail" {
		t.Fatalf("Location=%q, want /#/mail", loc)
	}

	// Step 3: cookie from the jar now accompanies JMAP.
	req3, _ := http.NewRequest("GET", h.httpSrv.URL+"/.well-known/jmap", nil)
	res3, err := h.client.Do(req3)
	if err != nil {
		t.Fatalf("GET /.well-known/jmap: %v", err)
	}
	res3.Body.Close()
	if res3.StatusCode != http.StatusOK {
		t.Fatalf("JMAP status=%d, want 200 after cookie login", res3.StatusCode)
	}
}

// TestJMAP_AdminCookieDoesNotAuthJMAP verifies the negative case:
// a cookie issued on the admin listener does NOT authenticate JMAP,
// even though the cookie now has Path=/ and IS sent to /.well-known/jmap.
//
// Isolation is no longer path-based (that was the Phase 1 model). It
// is now name-based (REQ-AUTH-SESSION-REST): the admin listener issues
// herold_admin_session; the JMAP session resolver only looks for
// herold_public_session. The distinct cookie names make cross-listener
// reuse mechanically impossible at the resolver level
// (REQ-OPS-ADMIN-LISTENER-03 + REQ-AUTH-SCOPE-01).
//
// The harness uses listenerKind="admin", so the protojmap SessionResolver
// is nil (wired only for "public" in newJMAPCookieHarness). The
// herold_admin_session cookie arrives at /.well-known/jmap but the
// JMAP server cannot decode it without the resolver, so 401 is returned.
func TestJMAP_AdminCookieDoesNotAuthJMAP(t *testing.T) {
	t.Parallel()

	// Admin-listener UI server.
	h := newJMAPCookieHarness(t, "admin")
	_ = h.createPrincipal("admin@example.test", "hunter2hunter2hunter2")

	// Log in on the admin listener -> cookie has Path=/ (REQ-AUTH-SESSION-REST).
	h.login("admin@example.test", "hunter2hunter2hunter2")

	// Verify cookie was issued and is now visible at / (Path widened).
	u, _ := url.Parse(h.httpSrv.URL + "/")
	jar := h.cookieJar.Cookies(u)
	var adminCookie *http.Cookie
	for _, c := range jar {
		if strings.Contains(c.Name, "admin") {
			adminCookie = c
			break
		}
	}
	if adminCookie == nil {
		t.Fatalf("admin cookie not issued; jar at /=%+v", jar)
	}

	// GET /.well-known/jmap: the jar attaches the admin cookie (Path=/),
	// but the JMAP server has no session resolver for the admin cookie
	// name, so it returns 401 (no valid credential).
	req, _ := http.NewRequest("GET", h.httpSrv.URL+"/.well-known/jmap", nil)
	res, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("GET /.well-known/jmap: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401 (admin cookie name not recognized by JMAP resolver)", res.StatusCode)
	}
}

// Ensure testharness is imported so this file can live in the same
// test package as scope_test.go which uses it.
var _ = testharness.ErrListenerHasNoHandler
