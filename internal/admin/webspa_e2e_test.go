package admin

import (
	"io"
	"net/http"
	"testing"
	"time"
)

// TestPublicListener_Root_ServesSuite asserts that the public listener
// serves the embedded suite SPA shell at `/` (REQ-DEPLOY-COLOC-01..02
// per Wave 3.7). The test fixture leaves Suite.AssetDir empty so the
// embedded placeholder is exercised; production builds replace the
// embedded placeholder but the contract is the
// same: <title>Herold</title>, no-cache, CSP set.
func TestPublicListener_Root_ServesSuite(t *testing.T) {
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
		t.Fatalf("public listener not bound")
	}
	resp, err := http.Get("http://" + publicAddr + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("/: status=%d body=%s", resp.StatusCode, string(body))
	}
	body, _ := io.ReadAll(resp.Body)
	if !contains(string(body), "<title>Herold</title>") {
		t.Errorf("body=%q; want SPA index.html", string(body))
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP=%q; want frame-ancestors 'none'", csp)
	}
}

// TestPublicListener_API_TakesPriority asserts that the JMAP session
// endpoint mounted on the public mux retains its handler even though
// the SPA handler is registered as the catch-all `/`. Go's
// longest-prefix mux routing is the mechanism; this is the
// integration-level proof (REQ-DEPLOY-COLOC-02).
func TestPublicListener_API_TakesPriority(t *testing.T) {
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
		t.Fatalf("public listener not bound")
	}
	resp, err := http.Get("http://" + publicAddr + "/.well-known/jmap")
	if err != nil {
		t.Fatalf("GET /.well-known/jmap: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	// /.well-known/jmap requires authentication; a 401 / 403 / 200
	// (depending on test fixture) all prove the JMAP handler ran
	// instead of the SPA. What we care about is that the body is
	// NOT the SPA shell.
	if contains(string(body), "<title>Herold</title>") {
		t.Errorf("/.well-known/jmap returned SPA shell; status=%d body=%s",
			resp.StatusCode, string(body))
	}
	// The SPA handler emits a no-cache Cache-Control header; the
	// JMAP handler does not. A response with no Cache-Control or a
	// JSON-shaped body is the JMAP handler.
	if cc := resp.Header.Get("Cache-Control"); contains(cc, "no-cache, must-revalidate") {
		t.Errorf("/.well-known/jmap looks like SPA fallback; Cache-Control=%q", cc)
	}
}

// TestAdminListener_Root_DoesNotServeSuite asserts that hitting `/`
// on the admin listener does NOT return the SPA shell. The admin
// listener's root either redirects to the admin login or 404s; in
// either case the SPA must not appear there (REQ-OPS-ADMIN-LISTENER-01
// + REQ-DEPLOY-COLOC-02: the SPA is public-listener-only).
func TestAdminListener_Root_DoesNotServeSuite(t *testing.T) {
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
		t.Fatalf("admin listener not bound")
	}
	// Disable redirect-following so a 303 to /ui/login does not
	// resolve into the admin UI body and confuse the assertion.
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get("http://" + adminAddr + "/")
	if err != nil {
		t.Fatalf("GET / on admin: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if contains(string(body), "<title>Herold</title>") {
		t.Errorf("admin listener / returned SPA shell; status=%d body=%s",
			resp.StatusCode, string(body))
	}
}

// TestPublicListener_SPAFallback_RouterPath asserts that an unknown
// non-API path on the public listener returns the SPA shell so the
// SPA's client-side router takes over (REQ-DEPLOY-COLOC-02 SPA
// fallback).
func TestPublicListener_SPAFallback_RouterPath(t *testing.T) {
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
		t.Fatalf("public listener not bound")
	}
	resp, err := http.Get("http://" + publicAddr + "/inbox/12345")
	if err != nil {
		t.Fatalf("GET /inbox/12345: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("/inbox/12345: status=%d body=%s", resp.StatusCode, string(body))
	}
	body, _ := io.ReadAll(resp.Body)
	if !contains(string(body), "<title>Herold</title>") {
		t.Errorf("SPA fallback body=%q; want SPA shell", string(body))
	}
}
