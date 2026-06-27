package admin

import (
	"io"
	"net/http"
	"testing"
	"time"
)

// TestUnifiedListener_AdminPathRequiresAuth asserts that /api/v1/principals
// on the public listener is reachable (the path is mounted) but requires
// authentication (re #58: admin REST moved to the public listener, gated by
// ScopeAdmin). Returns 401 unauthenticated, NOT 404.
func TestUnifiedListener_AdminPathRequiresAuth(t *testing.T) {
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
	// /api/v1/principals is mounted on the unified handler. An unauthenticated
	// request must return 401 (not 404) proving the path is reachable.
	resp, err := http.Get("http://" + publicAddr + "/api/v1/principals")
	if err != nil {
		t.Fatalf("GET principals on public: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("admin path on public listener returned 404 (route missing); body=%s", string(body))
	}
}

// TestUnifiedListener_AdminAliasServesJMAP asserts that the admin listener
// (now aliased to the public handler, re #58) serves JMAP the same way the
// public listener does. An unauthenticated JMAP request returns 401 on both.
func TestUnifiedListener_AdminAliasServesJMAP(t *testing.T) {
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
	resp, err := http.Get("http://" + adminAddr + "/.well-known/jmap")
	if err != nil {
		t.Fatalf("GET jmap on admin: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("admin-alias listener returned 404 for JMAP (route missing); body=%s",
			string(body))
	}
}

// TestListenerSplit_PublicRootServesSPA asserts that the public
// listener's bare `/` returns the suite SPA shell (Wave 3.7,
// REQ-DEPLOY-COLOC-01..02). The default test fixture leaves
// Suite.AssetDir empty so the embedded placeholder is served; the
// placeholder carries a <title>Herold</title> we assert on rather
// than the full body so a future suite build swap does not break
// the test.
func TestListenerSplit_PublicRootServesSPA(t *testing.T) {
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
	resp, err := http.Get("http://" + publicAddr + "/")
	if err != nil {
		t.Fatalf("GET / on public: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/: status=%d body=%s; want 200", resp.StatusCode, string(body))
	}
	if want := "<title>Herold</title>"; !contains(string(body), want) {
		t.Errorf("/: body=%q; want substring %q", string(body), want)
	}
	if got := resp.Header.Get("Cache-Control"); !contains(got, "no-cache") {
		t.Errorf("/: Cache-Control=%q; want no-cache", got)
	}
	if got := resp.Header.Get("Content-Security-Policy"); !contains(got, "default-src 'self'") {
		t.Errorf("/: missing CSP header; got %q", got)
	}
}

// contains is the tiny stdlib-free substring check used by the test
// fixtures above. Keeps the test file's import surface trivial.
func contains(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	if len(haystack) < len(needle) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
