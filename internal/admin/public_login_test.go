package admin

import (
	"io"
	"net/http"
	"testing"
	"time"
)

// TestPublicListener_Login_ReturnsOK verifies that GET /login on the public
// listener returns 200 (or 302 to /ui/login) and NOT 404.
//
// Before the Bug B fix, /login was only reachable as /ui/login on the public
// listener because protoui registers its routes under the /ui prefix. The
// suite SPA handler's reserved-API-prefix list defensively 404-d /login when
// it fell through to the catch-all, so suite-login redirect target broke.
//
// The fix registers adapter handlers at /login, /logout, and /oidc/ on the
// public mux that rewrite the path to the prefix-based equivalent and delegate
// to the same protoui handler.
func TestPublicListener_Login_ReturnsOK(t *testing.T) {
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

	// Do not follow redirects: a 302 to /ui/login counts as success (the
	// adapter routed the request to protoui which issued a redirect). A
	// 404 would mean the request hit the SPA or the default mux handler.
	client := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Get("http://" + publicAddr + "/login")
	if err != nil {
		t.Fatalf("GET /login: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusNotFound {
		t.Errorf("public /login returned 404; want 200 or 3xx. body=%s", string(body))
	}
	// The adapter must NOT serve the SPA shell for /login: the SPA shell
	// would indicate that the request fell through to the suite catch-all
	// (i.e. the adapter is not registered, or the SPA's reserved-prefix
	// list is not triggering as expected).
	if contains(string(body), "<title>Herold</title>") && resp.StatusCode == http.StatusOK {
		// Only flag this if it also lacks the login form marker.
		if !contains(string(body), "login") {
			t.Errorf("public /login returned SPA shell; status=%d", resp.StatusCode)
		}
	}
}

// TestPublicListener_Login_ProtoUIPath also works, i.e. /ui/login still
// resolves normally on the public listener (the adapter must not shadow the
// prefix-mounted protoui handler).
func TestPublicListener_Login_PrefixPath_StillWorks(t *testing.T) {
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

	client := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Get("http://" + publicAddr + "/ui/login")
	if err != nil {
		t.Fatalf("GET /ui/login: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusNotFound {
		t.Errorf("public /ui/login returned 404; want 200. body=%s", string(body))
	}
}

// TestAdminListener_Login_StillWorks checks that the admin listener's /ui/login
// is unaffected by the public-listener adapter changes.
func TestAdminListener_Login_StillWorks(t *testing.T) {
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
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusNotFound {
		t.Errorf("admin /ui/login returned 404; want 200. body=%s", string(body))
	}
}
