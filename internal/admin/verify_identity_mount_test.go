package admin

// verify_identity_mount_test.go pins the public-listener mount for
// the identity-verification link callback (REQ-IDENT-40).
//
// Unlike the SPA-driven /api/v1/* surfaces, /verify-identity is not
// reachable by any SPA call site, so public_routes_contract_test.go
// does not auto-discover it. A regression where someone moves the
// route registration without the matching publicMux.Handle entry
// would otherwise go unnoticed until a user actually clicks a
// verification link.
//
// What this test asserts: GET /verify-identity (with no query) on the
// public listener returns the handler's 400 HTML failure page rather
// than the stdlib "404 page not found" body. We do NOT assert success
// semantics here (those live in
// internal/protoadmin/identity_verify_test.go); we only assert the
// route reaches a handler.

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestPublicListener_MountsVerifyIdentityCallback(t *testing.T) {
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

	// Build a client that does NOT follow redirects so a successful
	// redeem (302) would surface as a status check rather than as a
	// follow-up GET against /#/settings.
	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// No token: handler renders the failure HTML page with a 400.
	resp, err := client.Get("http://" + publicAddr + "/verify-identity")
	if err != nil {
		t.Fatalf("GET /verify-identity: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 from the verify handler; got %d: %s",
			resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q; want text/html (failure page)", ct)
	}
	if strings.Contains(string(body), "404 page not found") {
		t.Errorf("public listener served stdlib 404 — /verify-identity not mounted; body: %s", body)
	}
	if !strings.Contains(string(body), `href="/#/settings"`) {
		t.Errorf("failure page missing Settings link; body: %s", body)
	}
}

// TestAdminListener_VerifyIdentityAlsoMounted asserts that the admin listener
// (now aliased to the public handler, re #58) also serves /verify-identity.
// End-users click the verification link from a mail client and land on the
// public listener; the admin listener alias serves the same route.
// No token -> 400 (failure HTML page), not 404.
func TestAdminListener_VerifyIdentityAlsoMounted(t *testing.T) {
	_, addrs, done, cancel := startTestServer(t)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("server did not shut down")
		}
	})
	adminAddr := addrs["admin"]
	if adminAddr == "" {
		t.Fatalf("admin listener not bound; addrs=%+v", addrs)
	}
	resp, err := http.Get("http://" + adminAddr + "/verify-identity")
	if err != nil {
		t.Fatalf("GET /verify-identity on admin: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("admin-alias /verify-identity: status=%d body=%s; want 400 (handler is mounted)",
			resp.StatusCode, body)
	}
}
