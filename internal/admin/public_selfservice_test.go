package admin

// public_selfservice_test.go is the integration test for Phase 4a
// (REQ-ADM-203): self-service REST routes mounted on the public listener
// so the Suite SPA /settings panel can reach them with the public-session
// cookie.
//
// Scenarios covered:
//  1. A non-admin principal can call GET /api/v1/principals/{ownPid} on the
//     public listener with the public-session cookie and receive 200.
//  2. The same principal calling GET /api/v1/principals/{otherPid} receives
//     403 (requireSelfOrAdmin blocks cross-principal reads).
//  3. GET /api/v1/queue on the public listener returns 404 (the admin-only
//     queue surface is not mounted on the public listener).
//  4. GET /api/v1/api-keys on the public listener with the public-session
//     cookie returns 200 — regression for #6.
//  5. POST /api/v1/principals/{pid}/api-keys on the public listener with
//     the public-session cookie + CSRF token returns 201 — regression for #6.
//  6. Same flows work when no signing_key_env is configured (ephemeral key
//     path) — regression for the Docker quickstart scenario reported in
//     #6 and #7.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/sysconfig"
)

// startTestServerWithCookies boots the full server with a session-cookie
// signing key configured so cookie-based auth works. The signing key is set
// in a per-test environment variable. Other test infrastructure (listeners,
// storage) mirrors minimalConfigFixture.
func startTestServerWithCookies(t *testing.T) (addrs map[string]string, doneCh <-chan struct{}, cancel func()) {
	t.Helper()

	// Set an env variable with a 32-byte signing key that both the admin
	// and public session cookie configs will read.
	const signingKeyEnvVar = "HEROLD_TEST_SESSION_SIGNING_KEY"
	t.Setenv(signingKeyEnvVar, "selfservice-test-signing-key-32b")

	d := t.TempDir()
	certPath, keyPath := generateSelfSignedCert(t, d, []string{"localhost"})

	secureFalse := false
	_ = secureFalse // referenced in TOML as literal false

	cfgTOML := fmt.Sprintf(`
[server]
hostname = "test.local"
data_dir = %q
run_as_user = ""
run_as_group = ""
port_report_file = %q

[server.admin_tls]
source = "file"
cert_file = %q
key_file = %q

[server.storage]
backend = "sqlite"
[server.storage.sqlite]
path = %q

[server.ui]
signing_key_env = %q
secure_cookies = false

[[listener]]
name = "smtp"
address = "127.0.0.1:0"
protocol = "smtp"
tls = "starttls"
cert_file = %q
key_file = %q

[[listener]]
name = "imap"
address = "127.0.0.1:0"
protocol = "imap"
tls = "starttls"
cert_file = %q
key_file = %q

[[listener]]
name = "public"
address = "127.0.0.1:0"
protocol = "admin"
kind = "public"
tls = "none"

[[listener]]
name = "admin"
address = "127.0.0.1:0"
protocol = "admin"
kind = "admin"
tls = "none"

[observability]
log_format = "text"
log_level = "warn"
metrics_bind = ""
`, d, filepath.Join(d, "ports.toml"), certPath, keyPath, filepath.Join(d, "db.sqlite"),
		signingKeyEnvVar,
		certPath, keyPath, certPath, keyPath)

	cfgPath := filepath.Join(d, "system.toml")
	if err := os.WriteFile(cfgPath, []byte(cfgTOML), 0o600); err != nil {
		t.Fatalf("write system.toml: %v", err)
	}

	cfg, err := sysconfig.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	ctx, cancelFn := context.WithCancel(context.Background())
	addrsMap := make(map[string]string)
	addrsMu := &sync.Mutex{}
	ready := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := StartServer(ctx, cfg, StartOpts{
			Logger:           slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
			Ready:            ready,
			ListenerAddrs:    addrsMap,
			ListenerAddrsMu:  addrsMu,
			ExternalShutdown: true,
		}); err != nil {
			t.Logf("StartServer exited: %v", err)
		}
	}()
	select {
	case <-ready:
	case <-time.After(15 * time.Second):
		cancelFn()
		t.Fatalf("server did not become ready within timeout")
	}
	return addrsMap, done, cancelFn
}

// bootstrapAndGetAPIKey bootstraps the first admin principal and returns
// (adminPrincipalID, apiKey, email, initialPassword).
func bootstrapAndGetAPIKey(t *testing.T, adminAddr string) (adminPID uint64, apiKey, email, password string) {
	t.Helper()
	email = "selfservice-admin@example.com"
	b, _ := json.Marshal(map[string]any{
		"email":        email,
		"display_name": "Self-Service Test Admin",
	})
	resp, err := http.Post("http://"+adminAddr+"/api/v1/bootstrap",
		"application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("bootstrap: status=%d body=%s", resp.StatusCode, raw)
	}
	var out struct {
		PrincipalID     uint64 `json:"principal_id"`
		InitialPassword string `json:"initial_password"`
		InitialAPIKey   string `json:"initial_api_key"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("bootstrap unmarshal: %v body=%s", err, raw)
	}
	return out.PrincipalID, out.InitialAPIKey, email, out.InitialPassword
}

// TestPublicSelfService_GetOwnPrincipal verifies that a logged-in end-user
// can retrieve their own principal record via the public listener and receives
// 403 when attempting to read another principal's record.
func TestPublicSelfService_GetOwnPrincipal(t *testing.T) {
	addrs, done, cancel := startTestServerWithCookies(t)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("server did not shut down")
		}
	})

	publicAddr := addrs["public"]
	adminAddr := addrs["admin"]
	if publicAddr == "" {
		t.Fatalf("public listener not bound; addrs=%+v", addrs)
	}
	if adminAddr == "" {
		t.Fatalf("admin listener not bound; addrs=%+v", addrs)
	}

	// Step 1: bootstrap the first (admin) principal and obtain its API key.
	adminPID, adminAPIKey, _, _ := bootstrapAndGetAPIKey(t, adminAddr)

	// Step 2: create a non-admin end-user principal via the admin API using
	// the Bearer API key.
	endUserEmail := "enduser-selfservice@example.com"
	endUserPassword := "correct-horse-battery-staple"
	createBody, _ := json.Marshal(map[string]any{
		"email":    endUserEmail,
		"password": endUserPassword,
	})
	createReq, err := http.NewRequest(
		"POST",
		"http://"+adminAddr+"/api/v1/principals",
		bytes.NewReader(createBody),
	)
	if err != nil {
		t.Fatalf("new create principal request: %v", err)
	}
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("Authorization", "Bearer "+adminAPIKey)

	createResp, err := http.DefaultClient.Do(createReq)
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	defer createResp.Body.Close()
	createRespBody, _ := io.ReadAll(createResp.Body)
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create principal: status=%d body=%s", createResp.StatusCode, createRespBody)
	}
	var createdPrincipal struct {
		ID uint64 `json:"id"`
	}
	if err := json.Unmarshal(createRespBody, &createdPrincipal); err != nil {
		t.Fatalf("unmarshal created principal: %v body=%s", err, createRespBody)
	}
	endUserPID := createdPrincipal.ID

	// Step 3: log in the end-user on the public listener to get the
	// public-session cookie.
	noRedirectClient := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	loginBody, _ := json.Marshal(map[string]any{
		"email":    endUserEmail,
		"password": endUserPassword,
	})
	publicLoginResp, err := noRedirectClient.Post(
		"http://"+publicAddr+"/api/v1/auth/login",
		"application/json",
		bytes.NewReader(loginBody),
	)
	if err != nil {
		t.Fatalf("public login: %v", err)
	}
	defer publicLoginResp.Body.Close()
	publicLoginBody, _ := io.ReadAll(publicLoginResp.Body)
	if publicLoginResp.StatusCode != http.StatusOK {
		t.Fatalf("public login: status=%d body=%s", publicLoginResp.StatusCode, publicLoginBody)
	}

	var publicSessionCookie *http.Cookie
	for _, c := range publicLoginResp.Cookies() {
		if c.Name == "herold_public_session" {
			publicSessionCookie = c
		}
	}
	if publicSessionCookie == nil {
		t.Fatal("herold_public_session not found after public login")
	}

	// Step 4: GET /api/v1/principals/{endUserPID} on the public listener.
	// Must return 200 with the principal's own row.
	getOwnReq, err := http.NewRequest(
		"GET",
		fmt.Sprintf("http://%s/api/v1/principals/%d", publicAddr, endUserPID),
		nil,
	)
	if err != nil {
		t.Fatalf("new get own principal request: %v", err)
	}
	getOwnReq.AddCookie(publicSessionCookie)

	getOwnResp, err := noRedirectClient.Do(getOwnReq)
	if err != nil {
		t.Fatalf("GET own principal: %v", err)
	}
	defer getOwnResp.Body.Close()
	getOwnBody, _ := io.ReadAll(getOwnResp.Body)
	if getOwnResp.StatusCode != http.StatusOK {
		t.Fatalf("GET own principal on public listener: status=%d body=%s (want 200)",
			getOwnResp.StatusCode, getOwnBody)
	}
	var ownPrincipalOut struct {
		ID    uint64 `json:"id"`
		Email string `json:"email"`
	}
	if err := json.Unmarshal(getOwnBody, &ownPrincipalOut); err != nil {
		t.Fatalf("unmarshal own principal: %v body=%s", err, getOwnBody)
	}
	if ownPrincipalOut.ID != endUserPID {
		t.Errorf("own principal ID=%d, want %d", ownPrincipalOut.ID, endUserPID)
	}

	// Step 5: GET /api/v1/principals/{adminPID} on the public listener with
	// the end-user's cookie. requireSelfOrAdmin must deny this with 403.
	getOtherReq, err := http.NewRequest(
		"GET",
		fmt.Sprintf("http://%s/api/v1/principals/%d", publicAddr, adminPID),
		nil,
	)
	if err != nil {
		t.Fatalf("new get other principal request: %v", err)
	}
	getOtherReq.AddCookie(publicSessionCookie)

	getOtherResp, err := noRedirectClient.Do(getOtherReq)
	if err != nil {
		t.Fatalf("GET other principal: %v", err)
	}
	defer getOtherResp.Body.Close()
	if getOtherResp.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(getOtherResp.Body)
		t.Errorf("GET other principal on public listener: status=%d body=%s (want 403)",
			getOtherResp.StatusCode, body)
	}
}

// startTestServerNoCookieKey boots the full server WITHOUT a
// signing_key_env configured, mirroring the default Docker quickstart
// system.toml. With the ephemeral-key fix the server must auto-generate a
// signing key so cookie auth works out-of-the-box.
func startTestServerNoCookieKey(t *testing.T) (addrs map[string]string, doneCh <-chan struct{}, cancel func()) {
	t.Helper()

	d := t.TempDir()
	certPath, keyPath := generateSelfSignedCert(t, d, []string{"localhost"})

	cfgTOML := fmt.Sprintf(`
[server]
hostname = "test.local"
data_dir = %q
run_as_user = ""
run_as_group = ""
port_report_file = %q

[server.admin_tls]
source = "file"
cert_file = %q
key_file = %q

[server.storage]
backend = "sqlite"
[server.storage.sqlite]
path = %q

[[listener]]
name = "smtp"
address = "127.0.0.1:0"
protocol = "smtp"
tls = "starttls"
cert_file = %q
key_file = %q

[[listener]]
name = "imap"
address = "127.0.0.1:0"
protocol = "imap"
tls = "starttls"
cert_file = %q
key_file = %q

[[listener]]
name = "public"
address = "127.0.0.1:0"
protocol = "admin"
kind = "public"
tls = "none"

[[listener]]
name = "admin"
address = "127.0.0.1:0"
protocol = "admin"
kind = "admin"
tls = "none"

[observability]
log_format = "text"
log_level = "warn"
metrics_bind = ""
`, d, filepath.Join(d, "ports.toml"), certPath, keyPath, filepath.Join(d, "db.sqlite"),
		certPath, keyPath, certPath, keyPath)

	cfgPath := filepath.Join(d, "system.toml")
	if err := os.WriteFile(cfgPath, []byte(cfgTOML), 0o600); err != nil {
		t.Fatalf("write system.toml: %v", err)
	}

	cfg, err := sysconfig.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	ctx, cancelFn := context.WithCancel(context.Background())
	addrsMap := make(map[string]string)
	addrsMu := &sync.Mutex{}
	ready := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := StartServer(ctx, cfg, StartOpts{
			Logger:           slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
			Ready:            ready,
			ListenerAddrs:    addrsMap,
			ListenerAddrsMu:  addrsMu,
			ExternalShutdown: true,
		}); err != nil {
			t.Logf("StartServer exited: %v", err)
		}
	}()
	select {
	case <-ready:
	case <-time.After(15 * time.Second):
		cancelFn()
		t.Fatalf("server did not become ready within timeout")
	}
	return addrsMap, done, cancelFn
}

// loginOnPublicListener performs POST /api/v1/auth/login on the public
// listener for the given credentials and returns the herold_public_session
// and herold_public_csrf cookies. The test fails if either cookie is absent.
func loginOnPublicListener(t *testing.T, publicAddr, email, password string) (sessionCookie, csrfCookie *http.Cookie) {
	t.Helper()
	noRedirectClient := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	body, _ := json.Marshal(map[string]any{"email": email, "password": password})
	resp, err := noRedirectClient.Post(
		"http://"+publicAddr+"/api/v1/auth/login",
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		t.Fatalf("public login: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("public login: status=%d body=%s", resp.StatusCode, raw)
	}
	for _, c := range resp.Cookies() {
		switch c.Name {
		case "herold_public_session":
			sessionCookie = c
		case "herold_public_csrf":
			csrfCookie = c
		}
	}
	if sessionCookie == nil {
		t.Fatal("herold_public_session cookie not found after login")
	}
	if csrfCookie == nil {
		t.Fatal("herold_public_csrf cookie not found after login")
	}
	return
}

// createEndUser creates a non-admin principal via the admin API and returns
// its numeric ID.
func createEndUser(t *testing.T, adminAddr, apiKey, email, password string) uint64 {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"email": email, "password": password})
	req, _ := http.NewRequest("POST", "http://"+adminAddr+"/api/v1/principals", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create principal: status=%d body=%s", resp.StatusCode, raw)
	}
	var out struct {
		ID uint64 `json:"id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal created principal: %v body=%s", err, raw)
	}
	return out.ID
}

// TestPublicSelfService_APIKeys verifies that GET /api/v1/api-keys and
// POST /api/v1/principals/{pid}/api-keys succeed on the public listener
// when the caller presents the herold_public_session cookie (and the
// CSRF token for the mutating POST). Regression test for issues #6 and #7.
func TestPublicSelfService_APIKeys(t *testing.T) {
	addrs, done, cancel := startTestServerWithCookies(t)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("server did not shut down")
		}
	})

	publicAddr := addrs["public"]
	adminAddr := addrs["admin"]
	if publicAddr == "" {
		t.Fatalf("public listener not bound; addrs=%+v", addrs)
	}
	if adminAddr == "" {
		t.Fatalf("admin listener not bound; addrs=%+v", addrs)
	}

	_, adminAPIKey, _, _ := bootstrapAndGetAPIKey(t, adminAddr)

	endUserEmail := "enduser-apikeys@example.com"
	endUserPassword := "correct-horse-battery-staple"
	endUserPID := createEndUser(t, adminAddr, adminAPIKey, endUserEmail, endUserPassword)

	sessionCookie, csrfCookie := loginOnPublicListener(t, publicAddr, endUserEmail, endUserPassword)

	noRedirectClient := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// Step 1: GET /api/v1/api-keys — must return 200 with the cookie only
	// (no CSRF needed for GET). This is the endpoint that caused #6.
	listReq, _ := http.NewRequest("GET", "http://"+publicAddr+"/api/v1/api-keys", nil)
	listReq.AddCookie(sessionCookie)
	listResp, err := noRedirectClient.Do(listReq)
	if err != nil {
		t.Fatalf("GET /api/v1/api-keys: %v", err)
	}
	listBody, _ := io.ReadAll(listResp.Body)
	listResp.Body.Close()
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/api-keys on public listener: status=%d body=%s (want 200)",
			listResp.StatusCode, listBody)
	}

	// Step 2: POST /api/v1/principals/{pid}/api-keys — must return 201
	// with session cookie + CSRF token. This is the key-creation path.
	createBody, _ := json.Marshal(map[string]any{
		"label": "test-key",
		"scope": []string{"mail.send"},
	})
	createReq, _ := http.NewRequest(
		"POST",
		fmt.Sprintf("http://%s/api/v1/principals/%d/api-keys", publicAddr, endUserPID),
		bytes.NewReader(createBody),
	)
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("X-CSRF-Token", csrfCookie.Value)
	createReq.AddCookie(sessionCookie)
	createReq.AddCookie(csrfCookie)
	createResp, err := noRedirectClient.Do(createReq)
	if err != nil {
		t.Fatalf("POST /api/v1/principals/{pid}/api-keys: %v", err)
	}
	createRespBody, _ := io.ReadAll(createResp.Body)
	createResp.Body.Close()
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/v1/principals/{pid}/api-keys on public listener: status=%d body=%s (want 201)",
			createResp.StatusCode, createRespBody)
	}
}

// TestPublicSelfService_EphemeralSigningKey verifies that the self-service
// endpoints work correctly when no signing_key_env is configured (the
// default Docker quickstart scenario). With the ephemeral-key fix, the
// server generates a 32-byte random key at startup and both the login
// cookie issuer and the self-service auth verifier use the same key, so
// cookie auth succeeds. Before the fix this scenario returned 401 on every
// self-service request because the empty key caused authenticateWithMode to
// skip cookie auth entirely (fixes #6, #7).
func TestPublicSelfService_EphemeralSigningKey(t *testing.T) {
	addrs, done, cancel := startTestServerNoCookieKey(t)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("server did not shut down")
		}
	})

	publicAddr := addrs["public"]
	adminAddr := addrs["admin"]
	if publicAddr == "" {
		t.Fatalf("public listener not bound; addrs=%+v", addrs)
	}
	if adminAddr == "" {
		t.Fatalf("admin listener not bound; addrs=%+v", addrs)
	}

	_, adminAPIKey, _, _ := bootstrapAndGetAPIKey(t, adminAddr)

	endUserEmail := "enduser-ephemeral@example.com"
	endUserPassword := "correct-horse-battery-staple"
	endUserPID := createEndUser(t, adminAddr, adminAPIKey, endUserEmail, endUserPassword)

	sessionCookie, csrfCookie := loginOnPublicListener(t, publicAddr, endUserEmail, endUserPassword)

	noRedirectClient := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// GET /api/v1/principals/{pid} — the failing endpoint in #7.
	getPrincipalReq, _ := http.NewRequest(
		"GET",
		fmt.Sprintf("http://%s/api/v1/principals/%d", publicAddr, endUserPID),
		nil,
	)
	getPrincipalReq.AddCookie(sessionCookie)
	getPrincipalResp, err := noRedirectClient.Do(getPrincipalReq)
	if err != nil {
		t.Fatalf("GET /api/v1/principals/{pid}: %v", err)
	}
	getPrincipalBody, _ := io.ReadAll(getPrincipalResp.Body)
	getPrincipalResp.Body.Close()
	if getPrincipalResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/principals/{pid} on public listener (ephemeral key): "+
			"status=%d body=%s (want 200)", getPrincipalResp.StatusCode, getPrincipalBody)
	}

	// GET /api/v1/api-keys — the failing endpoint in #6.
	listKeysReq, _ := http.NewRequest("GET", "http://"+publicAddr+"/api/v1/api-keys", nil)
	listKeysReq.AddCookie(sessionCookie)
	listKeysResp, err := noRedirectClient.Do(listKeysReq)
	if err != nil {
		t.Fatalf("GET /api/v1/api-keys: %v", err)
	}
	listKeysBody, _ := io.ReadAll(listKeysResp.Body)
	listKeysResp.Body.Close()
	if listKeysResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/api-keys on public listener (ephemeral key): "+
			"status=%d body=%s (want 200)", listKeysResp.StatusCode, listKeysBody)
	}

	// POST /api/v1/principals/{pid}/api-keys — create a key.
	createBody, _ := json.Marshal(map[string]any{
		"label": "ephemeral-test-key",
		"scope": []string{"mail.send"},
	})
	createReq, _ := http.NewRequest(
		"POST",
		fmt.Sprintf("http://%s/api/v1/principals/%d/api-keys", publicAddr, endUserPID),
		bytes.NewReader(createBody),
	)
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("X-CSRF-Token", csrfCookie.Value)
	createReq.AddCookie(sessionCookie)
	createReq.AddCookie(csrfCookie)
	createResp, err := noRedirectClient.Do(createReq)
	if err != nil {
		t.Fatalf("POST /api/v1/principals/{pid}/api-keys: %v", err)
	}
	createRespBody, _ := io.ReadAll(createResp.Body)
	createResp.Body.Close()
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/v1/principals/{pid}/api-keys on public listener (ephemeral key): "+
			"status=%d body=%s (want 201)", createResp.StatusCode, createRespBody)
	}
}

// TestPublicSelfService_QueueNotMounted verifies that GET /api/v1/queue
// returns 404 on the public listener — the admin-only queue surface must not
// be reachable from the public-facing port.
func TestPublicSelfService_QueueNotMounted(t *testing.T) {
	_, addrs, done, cancel := startTestServer(t)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("server did not shut down")
		}
	})

	publicAddr := addrs["public"]
	if publicAddr == "" {
		t.Fatalf("public listener not bound; addrs=%+v", addrs)
	}

	resp, err := http.Get("http://" + publicAddr + "/api/v1/queue")
	if err != nil {
		t.Fatalf("GET /api/v1/queue on public: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("GET /api/v1/queue on public: status=%d body=%s (want 404)",
			resp.StatusCode, body)
	}
}
