package admin

import (
	"io"
	"net/http"
	"testing"
	"time"
)

// TestListenerSplit_AdminPathReturns404OnPublic asserts that a request
// for an admin REST path arriving at the public listener returns 404
// (REQ-OPS-ADMIN-LISTENER-01: an admin path on the public listener
// "doesn't exist on this origin"; NOT 403, the path is unmounted).
func TestListenerSplit_AdminPathReturns404OnPublic(t *testing.T) {
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
	// Admin REST surface: /api/v1/principals is mounted on the admin
	// handler. Hitting it on the public listener must return 404.
	resp, err := http.Get("http://" + publicAddr + "/api/v1/principals")
	if err != nil {
		t.Fatalf("GET principals on public: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("admin path on public listener: status=%d body=%s; want 404",
			resp.StatusCode, string(body))
	}
}

// TestListenerSplit_PublicPathReturns404OnAdmin asserts that a request
// for a public-only path (JMAP) arriving at the admin listener
// returns 404. Mirror of the previous test for the other direction.
func TestListenerSplit_PublicPathReturns404OnAdmin(t *testing.T) {
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
	if resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("public path on admin listener: status=%d body=%s; want 404",
			resp.StatusCode, string(body))
	}
}

// TestListenerSplit_PublicRootServesSPA asserts that the public
// listener's bare `/` returns the tabard SPA shell (Wave 3.7,
// REQ-DEPLOY-COLOC-01..02). The default test fixture leaves
// Tabard.AssetDir empty so the embedded placeholder is served; the
// placeholder carries a <title>Herold</title> we assert on rather
// than the full body so a future tabard build swap does not break
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
