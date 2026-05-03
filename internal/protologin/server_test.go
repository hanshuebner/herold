package protologin_test

// server_test.go unit-tests the handler shape for protologin.Server:
//   - POST /api/v1/auth/login with valid creds -> 200 + Set-Cookie
//   - POST /api/v1/auth/login with bad creds -> 401
//   - POST /api/v1/auth/login with missing fields -> 400
//   - POST /api/v1/auth/logout -> 204

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/auth"
	"github.com/hanshuebner/herold/internal/authsession"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/protologin"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

var testSigningKey = []byte("protologin-test-key-32bytes-xxxx")

// publicSessionScopes returns the end-user scope set for tests.
func publicSessionScopes(_ store.Principal) auth.ScopeSet {
	return auth.NewScopeSet(auth.AllEndUserScopes...)
}

// newTestServer creates a minimal protologin.Server backed by storesqlite
// and a real directory. It inserts one principal with email / password and
// returns the server, the base test server, and the email + password.
func newTestServer(t *testing.T) (*httptest.Server, store.Store, *directory.Directory, string, string) {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	fs, err := storesqlite.OpenWithRand(context.Background(), dbPath, nil, clk, rand.Reader)
	if err != nil {
		t.Fatalf("storesqlite.OpenWithRand: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	if err := fs.Meta().InsertDomain(context.Background(), store.Domain{Name: "example.com", IsLocal: true}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}
	dir := directory.New(fs.Meta(), nil, clk, nil)

	// Bootstrap a principal via the directory so the password hash is set.
	email := "user@example.com"
	password := "hunter2hunter2hunter2"
	if _, err := dir.CreatePrincipal(context.Background(), email, password); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}

	cfg := authsession.SessionConfig{
		SigningKey:     testSigningKey,
		CookieName:     "herold_public_session",
		CSRFCookieName: "herold_public_csrf",
		TTL:            24 * time.Hour,
		SecureCookies:  false,
	}

	srv := protologin.New(protologin.Options{
		Session:   cfg,
		Store:     fs,
		Directory: dir,
		Clock:     clk,
		Listener:  "public",
		Scopes:    publicSessionScopes,
	})

	mux := http.NewServeMux()
	srv.Mount(mux)

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, fs, dir, email, password
}

// doLogin POSTs to /api/v1/auth/login with email/password and optional extras.
func doLogin(t *testing.T, client *http.Client, base, email, password string, extra map[string]any) (int, map[string]any) {
	t.Helper()
	body := map[string]any{"email": email, "password": password}
	for k, v := range extra {
		body[k] = v
	}
	b, _ := json.Marshal(body)
	resp, err := client.Post(base+"/api/v1/auth/login", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("POST /api/v1/auth/login: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

// TestLogin_ValidCreds_Returns200AndCookies exercises the happy path.
func TestLogin_ValidCreds_Returns200AndCookies(t *testing.T) {
	t.Parallel()
	ts, _, _, email, password := newTestServer(t)

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	code, body := doLogin(t, client, ts.URL, email, password, nil)
	if code != http.StatusOK {
		t.Fatalf("login: status=%d, want 200; body=%v", code, body)
	}
	if body["principal_id"] == nil {
		t.Errorf("login response missing principal_id: %v", body)
	}
	if body["email"] != email {
		t.Errorf("login response email=%v, want %q", body["email"], email)
	}
	scopes, _ := body["scopes"].([]interface{})
	if len(scopes) == 0 {
		t.Errorf("login response has no scopes: %v", body)
	}

	// Cookies must be in the jar.
	u, _ := url.Parse(ts.URL)
	var gotSession, gotCSRF bool
	for _, c := range jar.Cookies(u) {
		if c.Name == "herold_public_session" {
			gotSession = true
		}
		if c.Name == "herold_public_csrf" {
			gotCSRF = true
		}
	}
	if !gotSession {
		t.Error("herold_public_session not set in cookie jar")
	}
	if !gotCSRF {
		t.Error("herold_public_csrf not set in cookie jar")
	}
}

// TestLogin_BadCreds_Returns401 asserts wrong password -> 401.
func TestLogin_BadCreds_Returns401(t *testing.T) {
	t.Parallel()
	ts, _, _, email, _ := newTestServer(t)

	code, _ := doLogin(t, &http.Client{}, ts.URL, email, "wrongpassword", nil)
	if code != http.StatusUnauthorized {
		t.Errorf("bad creds: status=%d, want 401", code)
	}
}

// TestLogin_MissingFields_Returns400 asserts empty email/password -> 400.
func TestLogin_MissingFields_Returns400(t *testing.T) {
	t.Parallel()
	ts, _, _, _, _ := newTestServer(t)

	for _, tc := range []struct{ email, password string }{
		{"", "password"},
		{"user@example.com", ""},
		{"", ""},
	} {
		code, _ := doLogin(t, &http.Client{}, ts.URL, tc.email, tc.password, nil)
		if code != http.StatusBadRequest {
			t.Errorf("empty fields email=%q pass=%q: status=%d, want 400", tc.email, tc.password, code)
		}
	}
}

// TestLogout_Returns204 asserts POST /api/v1/auth/logout -> 204.
func TestLogout_Returns204(t *testing.T) {
	t.Parallel()
	ts, _, _, email, password := newTestServer(t)

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	// Login first so the cookie jar is populated.
	if code, _ := doLogin(t, client, ts.URL, email, password, nil); code != http.StatusOK {
		t.Fatalf("login: status=%d", code)
	}

	resp, err := client.Post(ts.URL+"/api/v1/auth/logout", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/v1/auth/logout: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("logout: status=%d, want 204", resp.StatusCode)
	}
}

// TestLogin_InvalidJSON_Returns400 asserts a malformed body -> 400.
func TestLogin_InvalidJSON_Returns400(t *testing.T) {
	t.Parallel()
	ts, _, _, _, _ := newTestServer(t)

	resp, err := http.Post(ts.URL+"/api/v1/auth/login", "application/json",
		bytes.NewReader([]byte("not json at all")))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid JSON: status=%d, want 400", resp.StatusCode)
	}
}

// TestMe_NoCookie_Returns401 asserts an unauthenticated GET /auth/me -> 401.
func TestMe_NoCookie_Returns401(t *testing.T) {
	t.Parallel()
	ts, _, _, _, _ := newTestServer(t)

	resp, err := http.Get(ts.URL + "/api/v1/auth/me")
	if err != nil {
		t.Fatalf("GET /api/v1/auth/me: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("/auth/me without cookie: status=%d, want 401", resp.StatusCode)
	}
}

// TestMe_AfterLogin_ReturnsPrincipalID asserts the cookie issued by login
// resolves through /auth/me to {principal_id, email, scopes}.
func TestMe_AfterLogin_ReturnsPrincipalID(t *testing.T) {
	t.Parallel()
	ts, _, _, email, password := newTestServer(t)

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	if code, _ := doLogin(t, client, ts.URL, email, password, nil); code != http.StatusOK {
		t.Fatalf("login: status=%d", code)
	}

	resp, err := client.Get(ts.URL + "/api/v1/auth/me")
	if err != nil {
		t.Fatalf("GET /api/v1/auth/me: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/auth/me: status=%d, want 200", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["email"] != email {
		t.Errorf("/auth/me email=%v, want %q", body["email"], email)
	}
	if body["principal_id"] == nil {
		t.Errorf("/auth/me missing principal_id: %v", body)
	}
	if scopes, _ := body["scopes"].([]interface{}); len(scopes) == 0 {
		t.Errorf("/auth/me missing scopes: %v", body)
	}
}
