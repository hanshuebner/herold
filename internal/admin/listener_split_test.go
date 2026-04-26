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

// TestListenerSplit_PublicPlaceholderRoot asserts that the public
// listener's bare `/` returns the SPA-mount-pending placeholder body.
// Wave 3.7 lands the actual SPA; until then this body keeps API
// consumers and curl from confusing the dev experience.
func TestListenerSplit_PublicPlaceholderRoot(t *testing.T) {
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
	if want := "tabard SPA mount lands"; !contains(string(body), want) {
		t.Errorf("/: body=%q; want substring %q", string(body), want)
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
