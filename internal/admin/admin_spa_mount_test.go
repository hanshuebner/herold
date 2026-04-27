package admin

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"
)

// TestAdminSPA_Disabled_ServesNotFound asserts that /admin/ on the
// admin listener returns 404 when [server.admin_spa].enabled is left
// at its default false. This is the Phase 2 dual-mount default --
// operators get the protoui HTMX UI at /ui/ unchanged; the new
// Svelte SPA at /admin/ is opt-in until Phase 3 cutover.
func TestAdminSPA_Disabled_ServesNotFound(t *testing.T) {
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
	if resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("/admin/ with admin_spa disabled: status=%d, want 404; body=%q",
			resp.StatusCode, string(body))
	}
}

// TestAdminSPA_Enabled_ServesShell asserts that /admin/ on the admin
// listener returns the embedded admin SPA shell when
// [server.admin_spa].enabled = true. The placeholder index.html in
// internal/webspa/dist/admin/ carries <h1>Herold admin</h1>; release
// builds replace the placeholder via scripts/build-web.sh but keep
// the same heading copy.
func TestAdminSPA_Enabled_ServesShell(t *testing.T) {
	_, addrs, done, cancel := startTestServerWithAdminSPA(t, true)
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
	// CSP must be set (REQ-DEPLOY-COLOC-04 spirit -- admin SPA gets a
	// stricter CSP than suite since it never opens a chat WebSocket).
	if csp := resp.Header.Get("Content-Security-Policy"); !contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP=%q; want frame-ancestors 'none'", csp)
	}
}

// TestAdminSPA_Enabled_RouterFallback asserts that an unknown
// non-asset path under /admin/ falls back to the SPA shell so the
// SPA's client-side router takes over. /admin/principals,
// /admin/queue, etc. -- none of these are real backend routes; the
// SPA renders them.
func TestAdminSPA_Enabled_RouterFallback(t *testing.T) {
	_, addrs, done, cancel := startTestServerWithAdminSPA(t, true)
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

// startTestServerWithAdminSPA mirrors startTestServer but flips
// [server.admin_spa].enabled before StartServer reads cfg.
func startTestServerWithAdminSPA(t *testing.T, enabled bool) (cfg interface{}, addrs map[string]string, doneCh <-chan struct{}, cancel func()) {
	t.Helper()
	_, cfgRaw := minimalConfigFixture(t)
	cfgRaw.Server.AdminSPA.Enabled = enabled
	ctx, cancelFn := context.WithCancel(context.Background())
	addrs = make(map[string]string)
	addrsMu := &sync.Mutex{}
	ready := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := StartServer(ctx, cfgRaw, StartOpts{
			Logger:           slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
			Ready:            ready,
			ListenerAddrs:    addrs,
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
	return cfgRaw, addrs, done, cancelFn
}
