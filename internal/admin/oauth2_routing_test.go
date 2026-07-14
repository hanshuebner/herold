package admin

// oauth2_routing_test.go pins herold issue #249: /oauth2/authorize,
// /oauth2/token, /oauth2/authorize/federated and
// /oauth2/authorize/federated/callback are registered on the protoadmin
// mux (internal/protoadmin/routes.go), backing the native-client
// authorization-code + PKCE grant (issue #199) and the external-OIDC
// federated sign-in built on it (issue #238) -- but publicMux in
// composeAdminAndUI (internal/admin/server.go) never forwarded the
// /oauth2/ subtree to that handler, so every request fell through to the
// Suite SPA's root catch-all on every real listener.
//
// internal/protoadmin's own OAuth2 tests (oauth2_native_test.go,
// oauth2_federated_test.go) build an httptest.Server directly around the
// protoadmin mux, so they never traversed publicMux and could not catch
// this gap. These tests boot the real composed server via StartServer /
// composeAdminAndUI -- the same path production listens on -- and hit
// /oauth2/* on the public listener the way a native client's browser or
// Custom Tab does.
//
// Each test below is RED (fails) without the publicMux.Handle("/oauth2/",
// taggedAdminHandler) forward and GREEN with it.

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/storesqlite"
	"github.com/hanshuebner/herold/internal/sysconfig"
)

const (
	oauth2RoutingTestClientID    = "herold-android-routing-test"
	oauth2RoutingTestRedirectURI = "net.netzhansa.herold:/oauth2redirect"
	// RFC 7636 §4.1 example code_challenge. GET /oauth2/authorize only
	// checks the parameter is present and code_challenge_method=S256; the
	// value itself is verified later, at token exchange. Not exercised
	// past the login-form render in these routing tests.
	oauth2RoutingTestChallenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
)

// seedOAuth2RoutingClient opens the sqlite store directly, before the
// server boots, and registers the native client these tests drive --
// mirroring internal/protoadmin/oauth2_native_test.go's
// mustRegisterHTTPAndroidClient, but through storesqlite directly since
// there is no running server yet to call the admin REST CRUD surface
// against. The store is closed before returning so the server's own
// storesqlite.Open (during StartServer) does not race it.
func seedOAuth2RoutingClient(t *testing.T, dbPath string) {
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
	d := directory.New(st.Meta(), discardLogger(), clk, nil)
	if _, _, err := d.RegisterOAuthClient(ctx, directory.OAuthClientRegistration{
		ClientID:     oauth2RoutingTestClientID,
		Name:         "oauth2 routing test client",
		RedirectURIs: []string{oauth2RoutingTestRedirectURI},
	}); err != nil {
		t.Fatalf("seed: RegisterOAuthClient: %v", err)
	}
}

// startOAuth2RoutingTestServer boots the real composed server (the
// StartServer -> composeAdminAndUI path production listens on) against a
// pre-built cfg, so callers can seed the sqlite store first. Mirrors
// startTestServer in admin_test.go / startOfflineE2EServer in
// oauth_offline_access_e2e_test.go.
func startOAuth2RoutingTestServer(t *testing.T, cfg *sysconfig.Config) (publicAddr string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	addrs := make(map[string]string)
	addrsMu := &sync.Mutex{}
	ready := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := StartServer(ctx, cfg, StartOpts{
			Logger:           slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
			Ready:            ready,
			ListenerAddrs:    addrs,
			ListenerAddrsMu:  addrsMu,
			ExternalShutdown: true,
		}); err != nil {
			t.Logf("oauth2 routing test: StartServer exited: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Errorf("oauth2 routing test: server did not shut down")
		}
	})
	select {
	case <-ready:
	case <-time.After(20 * time.Second):
		cancel()
		t.Fatalf("oauth2 routing test: server not ready")
	}
	addrsMu.Lock()
	publicAddr = addrs["public"]
	addrsMu.Unlock()
	if publicAddr == "" {
		t.Fatalf("oauth2 routing test: public listener not bound; addrs=%+v", addrs)
	}
	return publicAddr
}

// oauth2AuthorizeURL builds a GET /oauth2/authorize query string for the
// seeded routing-test client.
func oauth2AuthorizeURL(base, state string) string {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {oauth2RoutingTestClientID},
		"redirect_uri":          {oauth2RoutingTestRedirectURI},
		"state":                 {state},
		"code_challenge":        {oauth2RoutingTestChallenge},
		"code_challenge_method": {"S256"},
	}
	return base + "/oauth2/authorize?" + q.Encode()
}

// TestOAuth2Authorize_RoutesToPublicListener_RendersLoginForm is the
// critical deliverable for issue #249: GET /oauth2/authorize through the
// REAL composed public handler must render the OAuth login form -- not
// the Suite SPA shell publicMux's root catch-all was serving instead.
//
// Without the fix: publicMux has no /oauth2/ entry, so the request falls
// through to the SPA catch-all at "/" and this test fails (200, but the
// SPA shell: Content-Type text/html, body contains "<title>Herold</title>"
// and no "/oauth2/authorize" form action).
//
// With the fix: publicMux forwards /oauth2/ to the tagged protoadmin
// handler, which renders the login form.
func TestOAuth2Authorize_RoutesToPublicListener_RendersLoginForm(t *testing.T) {
	_, cfg := minimalConfigFixture(t)
	seedOAuth2RoutingClient(t, cfg.Server.Storage.SQLite.Path)
	publicAddr := startOAuth2RoutingTestServer(t, cfg)

	resp, err := http.Get("http://" + publicAddr + oauth2AuthorizeURL("", "state-1"))
	if err != nil {
		t.Fatalf("GET /oauth2/authorize: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /oauth2/authorize: status=%d body=%s", resp.StatusCode, body)
	}
	// Distinguishing marker of the OAuth login form: its own POST target.
	if !strings.Contains(string(body), `action="/oauth2/authorize"`) {
		t.Errorf("response is missing the OAuth login form (action=\"/oauth2/authorize\"); "+
			"publicMux likely routed this to the SPA fallback instead of the protoadmin handler.\nbody: %s", body)
	}
	// Distinguishing marker of the Suite SPA shell -- must NOT be present.
	if strings.Contains(string(body), "<title>Herold</title>") {
		t.Errorf("response is the Suite SPA shell (<title>Herold</title>), not the OAuth login form; "+
			"publicMux is not forwarding /oauth2/ to the protoadmin handler (re #249).\nbody: %s", body)
	}
	if strings.Contains(string(body), `id="app"`) {
		t.Errorf("response contains the SPA root mount point (id=\"app\"); "+
			"publicMux served the SPA fallback instead of the OAuth login form.\nbody: %s", body)
	}
}

// TestOAuth2Authorize_Federated_RoutesToPublicListener_RendersButton
// additionally pins issue #238: when an OIDC provider is registered, the
// same login form must list a federated "sign in with <provider>" option
// whose form posts to /oauth2/authorize/federated. Registering the
// provider requires live discovery against a real issuer, which is out of
// scope for this routing test (internal/protoadmin/oauth2_federated_test.go
// already covers the federated leg's own handler behaviour end to end
// against the in-tree fake OIDC provider); this test only pins that the
// federated POST target is reachable through the composed public handler
// once a client renders the form, distinguishing "reached the handler"
// (a structured OAuth2/problem+json error) from "fell through to the SPA"
// (200 SPA shell) or "fell through to a GET-only route" (405).
func TestOAuth2AuthorizeFederated_RoutesToPublicListener(t *testing.T) {
	_, cfg := minimalConfigFixture(t)
	seedOAuth2RoutingClient(t, cfg.Server.Storage.SQLite.Path)
	publicAddr := startOAuth2RoutingTestServer(t, cfg)

	// No CSRF cookie / form fields: the handler must still be reached and
	// return its own CSRF-mismatch error, not a stdlib 404, not a 405
	// (the SPA root handler is GET-only), and not a 200 SPA shell.
	resp, err := http.Post("http://"+publicAddr+"/oauth2/authorize/federated", "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatalf("POST /oauth2/authorize/federated: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusNotFound {
		t.Fatalf("POST /oauth2/authorize/federated: got 404; route not mounted on the public listener (re #249)")
	}
	if resp.StatusCode == http.StatusMethodNotAllowed {
		t.Fatalf("POST /oauth2/authorize/federated: got 405; publicMux routed this to a GET-only handler (the SPA fallback), not the protoadmin OAuth2 handler (re #249)")
	}
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("POST /oauth2/authorize/federated: got 200; likely the Suite SPA shell, not the OAuth2 handler (re #249). body: %s", body)
	}
	ct := resp.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "text/html") && strings.Contains(string(body), "<title>Herold</title>") {
		t.Fatalf("POST /oauth2/authorize/federated returned the Suite SPA shell instead of a handler response (re #249). body: %s", body)
	}
}

// TestOAuth2AuthorizeFederatedCallback_RoutesToPublicListener pins the
// federated callback leg (GET /oauth2/authorize/federated/callback,
// issue #238) reaches the protoadmin handler through the composed public
// listener rather than the SPA fallback.
func TestOAuth2AuthorizeFederatedCallback_RoutesToPublicListener(t *testing.T) {
	_, cfg := minimalConfigFixture(t)
	publicAddr := startOAuth2RoutingTestServer(t, cfg)

	// No state/code query params: the handler must still be reached and
	// return its own validation error, not the SPA shell.
	resp, err := http.Get("http://" + publicAddr + "/oauth2/authorize/federated/callback")
	if err != nil {
		t.Fatalf("GET /oauth2/authorize/federated/callback: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusOK {
		if strings.Contains(string(body), "<title>Herold</title>") {
			t.Fatalf("GET /oauth2/authorize/federated/callback returned the Suite SPA shell instead of a handler response (re #249). body: %s", body)
		}
	}
}

// TestOAuth2Token_RoutesToPublicListener pins POST /oauth2/token
// (issue #199's RFC 6749 token endpoint) is reachable through the
// composed public listener. Before the fix, this path falls through to
// the SPA root handler, which is GET-only and returns 405; after the fix
// it reaches protoadmin's handler, which validates the request body and
// returns its own 400 invalid_request JSON error.
func TestOAuth2Token_RoutesToPublicListener(t *testing.T) {
	_, cfg := minimalConfigFixture(t)
	publicAddr := startOAuth2RoutingTestServer(t, cfg)

	resp, err := http.Post("http://"+publicAddr+"/oauth2/token", "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatalf("POST /oauth2/token: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusMethodNotAllowed {
		t.Fatalf("POST /oauth2/token: got 405; publicMux routed this to the SPA's GET-only root handler instead of the protoadmin token handler (re #249)")
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("POST /oauth2/token: Content-Type=%q status=%d; want application/json from the token handler (re #249). body: %s", ct, resp.StatusCode, body)
	}
}

// TestOAuth2Routing_DoesNotShadowSPAOrAPI is the regression guard for the
// fix's precedence: adding publicMux.Handle("/oauth2/", ...) must not
// shadow the pre-existing "/api/v1/" forward or the SPA root fallback --
// Go 1.22 ServeMux picks the most specific pattern, and "/oauth2/" is
// disjoint from both, but this pins the composed behaviour rather than
// relying on that reasoning alone.
func TestOAuth2Routing_DoesNotShadowSPAOrAPI(t *testing.T) {
	_, cfg := minimalConfigFixture(t)
	publicAddr := startOAuth2RoutingTestServer(t, cfg)

	// /api/v1/ still reaches protoadmin (unauthenticated readiness probe).
	apiResp, err := http.Get("http://" + publicAddr + "/api/v1/healthz/live")
	if err != nil {
		t.Fatalf("GET /api/v1/healthz/live: %v", err)
	}
	defer apiResp.Body.Close()
	if apiResp.StatusCode != http.StatusOK {
		t.Errorf("/api/v1/healthz/live: status=%d, want 200 (regression: /oauth2/ forward must not shadow /api/v1/)", apiResp.StatusCode)
	}

	// The SPA root fallback still serves the Suite shell at "/".
	spaResp, err := http.Get("http://" + publicAddr + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer spaResp.Body.Close()
	spaBody, _ := io.ReadAll(spaResp.Body)
	if spaResp.StatusCode != http.StatusOK {
		t.Errorf("GET /: status=%d, want 200", spaResp.StatusCode)
	}
	if !strings.Contains(string(spaBody), "<title>Herold</title>") {
		t.Errorf("GET / no longer serves the Suite SPA shell (regression: /oauth2/ forward must not shadow \"/\"); body: %s", spaBody)
	}
}
