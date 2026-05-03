package admin

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestManualMount_Public asserts that GET /manual/ on the public listener
// returns 200 with text/html. The standalone manual is intentionally PUBLIC
// (no session check) and served at /manual/ from both listeners.
func TestManualMount_Public(t *testing.T) {
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

	cases := []struct {
		path string
		want string // expected substring in body
	}{
		{"/manual/", "Herold"},
		// Arbitrary sub-paths fall back to root index (SPA-router-style
		// fallback via the manual server's extension-free miss path).
		{"/manual/user/", "Herold"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			resp, err := http.Get("http://" + publicAddr + tc.path)
			if err != nil {
				t.Fatalf("GET %s: %v", tc.path, err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("%s: status=%d body=%s", tc.path, resp.StatusCode, string(body))
			}
			body, _ := io.ReadAll(resp.Body)
			if !contains(string(body), tc.want) {
				t.Errorf("%s: body=%q, want to contain %q", tc.path, string(body), tc.want)
			}
			if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Errorf("%s: Content-Type=%q, want text/html", tc.path, ct)
			}
		})
	}
}

// TestManualMount_Admin asserts that GET /admin/manual/ on the admin
// listener also returns 200 with text/html. The manual is available on both
// listeners so operators can read it without switching to the public URL.
func TestManualMount_Admin(t *testing.T) {
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

	resp, err := http.Get("http://" + adminAddr + "/admin/manual/")
	if err != nil {
		t.Fatalf("GET /admin/manual/: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("/admin/manual/: status=%d body=%s", resp.StatusCode, string(body))
	}
	body, _ := io.ReadAll(resp.Body)
	if !contains(string(body), "Herold") {
		t.Errorf("/admin/manual/: body=%q, want Herold content", string(body))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("/admin/manual/: Content-Type=%q, want text/html", ct)
	}
}

// TestManualMount_Public_NoAuth asserts that /manual/ is reachable without
// any session cookie (i.e. it is intentionally public). The test issues a
// bare GET with no cookies set and expects 200, not 401/403.
func TestManualMount_Public_NoAuth(t *testing.T) {
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

	// Use a bare http.Client with no cookies.
	client := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequest(http.MethodGet, "http://"+publicAddr+"/manual/", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	// No Cookie header set -- the manual must be public.
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /manual/: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		t.Errorf("/manual/: status=%d, manual must be public (no auth check)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("/manual/: unexpected status=%d body=%q", resp.StatusCode, string(body))
	}
}

// TestManualMount_NotAbsorbedBySuiteSPA asserts that the /manual/ prefix
// is handled by the manual handler and NOT by the suite SPA catch-all.
// The manual server returns its own content, not the suite SPA shell.
func TestManualMount_NotAbsorbedBySuiteSPA(t *testing.T) {
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

	resp, err := http.Get("http://" + publicAddr + "/manual/")
	if err != nil {
		t.Fatalf("GET /manual/: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	// The suite SPA placeholder contains "Suite SPA"; the manual
	// placeholder does not. This ensures the routing is correct.
	if contains(string(body), "Suite SPA") {
		t.Errorf("/manual/: got suite SPA response; manual handler was not mounted correctly")
	}
}
