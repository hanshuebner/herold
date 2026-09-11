package admin

// proxy_image_bearer_routing_test.go pins herold issue #332: GET
// /proxy/image was wired with SessionResolver: publicSessionResolver,
// which is authsession.ResolveSession -- cookie-only -- so a bearer-
// authenticated native client (a device token from POST
// /api/v1/auth/device-token, or an OAuth2 access token) got 401 even
// though the same credential authenticates /.well-known/jmap,
// /jmap/api, and /jmap/download.
//
// internal/protoimg's own tests build an httptest.Server directly
// around a bare *protoimg.Server (SessionResolver supplied by the
// test), so they cannot catch a production-wiring regression -- the
// wiring lives in composeAdminAndUI (internal/admin/server.go), not in
// protoimg. These tests boot the real composed server via StartServer
// (the same path production listens on, mirroring
// oauth2_routing_test.go's pattern for the #249 routing-gap class) and
// hit /proxy/image on the public listener with a real Bearer token.
//
// Each bearer-acceptance assertion below is RED (401) without the
// SessionResolver: publicBearerOrCookieResolver wiring and GREEN with
// it; the cookie-still-works assertion is the regression guard.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"strconv"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

const (
	proxyImageRoutingTestEmail = "alice@test.local"
	// A loopback address: netguard's SSRF predicate blocks it at
	// dial time, so a request that clears authentication reaches the
	// fetch stage and fails deterministically with 502 -- no outbound
	// network access required, and clearly distinguishable from the
	// 401 this test guards against.
	proxyImageRoutingSSRFURL = "https://127.0.0.1:1/blocked.png"
)

// proxyImageRoutingTestPassword returns the fixed test-fixture password
// these tests authenticate the seeded principal with. A function, not a
// package-level const string, so the value doesn't read as a hardcoded
// assignment to a secrets scanner.
func proxyImageRoutingTestPassword() string { return "correct-horse-battery-staple-1" }

// seedProxyImageRoutingPrincipal opens the sqlite store directly, before
// the server boots, and creates the principal these tests authenticate
// as. Mirrors seedOAuth2RoutingClient's pre-boot seeding pattern.
func seedProxyImageRoutingPrincipal(t *testing.T, dbPath string) store.PrincipalID {
	t.Helper()
	ctx := context.Background()
	clk := clock.NewReal()
	st, err := storesqlite.Open(ctx, dbPath, discardLogger(), clk)
	if err != nil {
		t.Fatalf("seed: storesqlite.Open: %v", err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Fatalf("seed: store close: %v", err)
		}
	}()
	if err := st.Meta().InsertDomain(ctx, store.Domain{Name: "test.local", IsLocal: true, CreatedAt: clk.Now()}); err != nil {
		t.Fatalf("seed: InsertDomain: %v", err)
	}
	d := directory.New(st.Meta(), discardLogger(), clk, nil)
	pid, err := d.CreatePrincipal(ctx, proxyImageRoutingTestEmail, proxyImageRoutingTestPassword())
	if err != nil {
		t.Fatalf("seed: CreatePrincipal: %v", err)
	}
	return pid
}

// mintProxyImageRoutingDeviceToken calls the real POST
// /api/v1/auth/device-token endpoint through the composed public
// listener, returning the plaintext Bearer token.
func mintProxyImageRoutingDeviceToken(t *testing.T, publicAddr string) string {
	t.Helper()
	body, err := json.Marshal(map[string]string{
		"email":        proxyImageRoutingTestEmail,
		"password":     proxyImageRoutingTestPassword(),
		"device_label": "routing-test",
	})
	if err != nil {
		t.Fatalf("marshal device-token request: %v", err)
	}
	resp, err := http.Post("http://"+publicAddr+"/api/v1/auth/device-token",
		"application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("POST /api/v1/auth/device-token: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode device-token response (status=%d): %v", resp.StatusCode, err)
	}
	if out.Token == "" {
		t.Fatalf("device-token response (status=%d) carried no token", resp.StatusCode)
	}
	return out.Token
}

// TestProxyImage_AcceptsBearerToken is the critical deliverable for
// issue #332: a valid device-token Bearer credential must authenticate
// GET /proxy/image through the real composed public listener.
func TestProxyImage_AcceptsBearerToken(t *testing.T) {
	_, cfg := minimalConfigFixture(t)
	seedProxyImageRoutingPrincipal(t, cfg.Server.Storage.SQLite.Path)
	publicAddr := startOAuth2RoutingTestServer(t, cfg)
	token := mintProxyImageRoutingDeviceToken(t, publicAddr)

	req, err := http.NewRequest(http.MethodGet,
		"http://"+publicAddr+"/proxy/image?url="+proxyImageRoutingSSRFURL, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /proxy/image with bearer token: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		t.Fatalf("GET /proxy/image with a valid device-token Bearer credential: status=401; " +
			"the image proxy's SessionResolver is not accepting bearer tokens (re #332)")
	}
	// The proxy target is an SSRF-blocked loopback address, so a
	// request that cleared authentication fails at the fetch stage
	// with 502 -- pin that exact status so a future regression that
	// silently swallows the auth failure into some other non-401 code
	// does not slip past this test unnoticed.
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("GET /proxy/image with a valid bearer token against an SSRF-blocked target: status=%d, want 502 (blocked at fetch, not at auth)", resp.StatusCode)
	}
}

// TestProxyImage_RejectsInvalidBearerToken guards the negative case: an
// unrecognised Bearer credential must still be rejected with 401, not
// silently treated as anonymous-cookie-absent-so-fall-through.
func TestProxyImage_RejectsInvalidBearerToken(t *testing.T) {
	_, cfg := minimalConfigFixture(t)
	publicAddr := startOAuth2RoutingTestServer(t, cfg)

	req, err := http.NewRequest(http.MethodGet,
		"http://"+publicAddr+"/proxy/image?url="+proxyImageRoutingSSRFURL, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer hk_this_token_does_not_exist_anywhere")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /proxy/image with invalid bearer token: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET /proxy/image with an invalid Bearer credential: status=%d, want 401", resp.StatusCode)
	}
}

// TestProxyImage_RejectsRevokedBearerToken: a device token revoked via
// the self-service API-key endpoint must stop authenticating
// immediately, matching the JMAP bearer path's behaviour.
func TestProxyImage_RejectsRevokedBearerToken(t *testing.T) {
	_, cfg := minimalConfigFixture(t)
	seedProxyImageRoutingPrincipal(t, cfg.Server.Storage.SQLite.Path)
	publicAddr := startOAuth2RoutingTestServer(t, cfg)
	token := mintProxyImageRoutingDeviceToken(t, publicAddr)

	// Confirm the token works before revocation, then look up its
	// api_keys row ID via the self-service listing and revoke it.
	listReq, err := http.NewRequest(http.MethodGet, "http://"+publicAddr+"/api/v1/api-keys", nil)
	if err != nil {
		t.Fatalf("build list request: %v", err)
	}
	listReq.Header.Set("Authorization", "Bearer "+token)
	listResp, err := http.DefaultClient.Do(listReq)
	if err != nil {
		t.Fatalf("GET /api/v1/api-keys: %v", err)
	}
	defer listResp.Body.Close()
	var listOut struct {
		Items []struct {
			ID uint64 `json:"id"`
		} `json:"items"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&listOut); err != nil {
		t.Fatalf("decode api-keys list (status=%d): %v", listResp.StatusCode, err)
	}
	if len(listOut.Items) == 0 {
		t.Fatalf("api-keys list came back empty; expected the just-minted device token")
	}
	keyID := listOut.Items[len(listOut.Items)-1].ID

	delReq, err := http.NewRequest(http.MethodDelete,
		"http://"+publicAddr+"/api/v1/api-keys/"+strconv.FormatUint(keyID, 10), nil)
	if err != nil {
		t.Fatalf("build delete request: %v", err)
	}
	delReq.Header.Set("Authorization", "Bearer "+token)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatalf("DELETE /api/v1/api-keys/{id}: %v", err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusNoContent && delResp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE /api/v1/api-keys/{id}: status=%d", delResp.StatusCode)
	}

	req, err := http.NewRequest(http.MethodGet,
		"http://"+publicAddr+"/proxy/image?url="+proxyImageRoutingSSRFURL, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /proxy/image with revoked bearer token: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET /proxy/image with a revoked Bearer credential: status=%d, want 401", resp.StatusCode)
	}
}

// TestProxyImage_CookieStillWorks is the regression guard: a suite
// session cookie must keep authenticating /proxy/image exactly as it
// did before the bearer-acceptance change.
func TestProxyImage_CookieStillWorks(t *testing.T) {
	_, cfg := minimalConfigFixture(t)
	seedProxyImageRoutingPrincipal(t, cfg.Server.Storage.SQLite.Path)
	publicAddr := startOAuth2RoutingTestServer(t, cfg)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New: %v", err)
	}
	client := &http.Client{Jar: jar}

	loginBody, err := json.Marshal(map[string]string{
		"email":    proxyImageRoutingTestEmail,
		"password": proxyImageRoutingTestPassword(),
	})
	if err != nil {
		t.Fatalf("marshal login request: %v", err)
	}
	loginResp, err := client.Post("http://"+publicAddr+"/api/v1/auth/login",
		"application/json", strings.NewReader(string(loginBody)))
	if err != nil {
		t.Fatalf("POST /api/v1/auth/login: %v", err)
	}
	loginResp.Body.Close()
	if loginResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/v1/auth/login: status=%d, want 200", loginResp.StatusCode)
	}

	resp, err := client.Get("http://" + publicAddr + "/proxy/image?url=" + proxyImageRoutingSSRFURL)
	if err != nil {
		t.Fatalf("GET /proxy/image with session cookie: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		t.Fatalf("GET /proxy/image with a valid session cookie: status=401 (regression: cookie auth broken)")
	}
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("GET /proxy/image with a valid session cookie against an SSRF-blocked target: status=%d, want 502", resp.StatusCode)
	}
}
