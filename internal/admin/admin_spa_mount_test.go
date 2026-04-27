package admin

import (
	"io"
	"net/http"
	"testing"
	"time"
)

// TestAdminSPA_ServesShell asserts that /admin/ on the admin listener
// returns the embedded admin SPA shell. As of Phase 3b the SPA is the
// only admin UI and the previous AdminSPA.Enabled gate is gone -- the
// test fixture's standard server boots with /admin/ already mounted.
//
// The placeholder index.html in internal/webspa/dist/admin/ carries
// <h1>Herold admin</h1>; release builds replace the placeholder via
// scripts/build-web.sh but keep the same heading copy.
func TestAdminSPA_ServesShell(t *testing.T) {
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
	resp, err := http.Get("http://" + adminAddr + "/admin/")
	if err != nil {
		t.Fatalf("GET /admin/: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("/admin/: status=%d body=%s", resp.StatusCode, string(body))
	}
	body, _ := io.ReadAll(resp.Body)
	if !contains(string(body), "Herold admin") {
		t.Errorf("body=%q; want admin SPA shell", string(body))
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP=%q; want frame-ancestors 'none'", csp)
	}
}

// TestAdminSPA_RouterFallback asserts that an unknown non-asset path
// under /admin/ falls back to the SPA shell so the SPA's client-side
// router takes over. /admin/principals, /admin/queue, etc. -- none
// are real backend routes; the SPA renders them.
func TestAdminSPA_RouterFallback(t *testing.T) {
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
	resp, err := http.Get("http://" + adminAddr + "/admin/principals")
	if err != nil {
		t.Fatalf("GET /admin/principals: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("/admin/principals: status=%d body=%s", resp.StatusCode, string(body))
	}
	body, _ := io.ReadAll(resp.Body)
	if !contains(string(body), "Herold admin") {
		t.Errorf("router fallback body=%q; want SPA shell", string(body))
	}
}

// TestAdminListener_UI_RedirectsToAdmin asserts that legacy /ui/*
// paths on the admin listener return 308 Permanent Redirect to
// /admin/ (Phase 3b cutover). 308 preserves request method on retry,
// which is intentional: a stale POST to /ui/login lands on /admin/
// where the SPA serves its login page; the operator notices the URL
// change.
func TestAdminListener_UI_RedirectsToAdmin(t *testing.T) {
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
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	for _, path := range []string{"/ui/dashboard", "/ui/login", "/ui/principals", "/ui/"} {
		resp, err := client.Get("http://" + adminAddr + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusPermanentRedirect {
			t.Errorf("%s: status=%d, want 308", path, resp.StatusCode)
			continue
		}
		if loc := resp.Header.Get("Location"); loc != "/admin/" {
			t.Errorf("%s: Location=%q, want /admin/", path, loc)
		}
	}
}
