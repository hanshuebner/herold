package admin

import (
	"io"
	"net/http"
	"testing"
	"time"
)

// TestAdminListener_Login_RedirectsToAdminSPA checks that the admin
// listener's legacy /ui/login 308-redirects to /admin/. As of Phase 3b
// of the merge plan the HTMX UI mount on the admin listener is gone;
// /ui/* exists only as a redirect breadcrumb for stale bookmarks.
// (See docs/design/server/notes/plan-tabard-merge-and-admin-rewrite.md.)
func TestAdminListener_Login_RedirectsToAdminSPA(t *testing.T) {
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

	client := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Get("http://" + adminAddr + "/ui/login")
	if err != nil {
		t.Fatalf("GET /ui/login on admin: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPermanentRedirect {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("admin /ui/login: status=%d, want 308; body=%s",
			resp.StatusCode, string(body))
	}
	if loc := resp.Header.Get("Location"); loc != "/admin/" {
		t.Errorf("admin /ui/login Location=%q, want /admin/", loc)
	}
}
