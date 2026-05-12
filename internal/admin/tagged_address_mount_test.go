package admin

// tagged_address_mount_test.go pins the public-listener mount for the
// REST surface introduced by REQ-TAG-50..62.
//
// public_routes_contract_test.go discovers /api/v1/* paths by walking
// the suite SPA source. Until the SPA side of #27 lands, the discovery
// pass does not see the new endpoints; this dedicated test fills the
// gap so a regression where someone removes the publicMux.Handle for
// /api/v1/tagged-address-dismissals or /api/v1/tagged-address-filters
// surfaces immediately rather than waiting for the SPA work.
//
// What this test asserts: a GET / POST / DELETE against each new
// endpoint on the PUBLIC listener returns ANY status other than the
// stdlib "404 page not found" body. A registered handler may return
// 401 (no session), 400 (no body), 405 (wrong verb), or any other
// status — the only failure mode is the stdlib body, which means the
// public mux had no match.
//
// What this test does NOT assert: the handler's response shape or
// auth gate. Those live in protoadmin's per-handler tests.

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestPublicListener_MountsTaggedAddressDismissals(t *testing.T) {
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
	client := &http.Client{Timeout: 10 * time.Second}
	cases := []struct {
		method string
		path   string
	}{
		{"POST", "/api/v1/tagged-address-dismissals"},
		{"GET", "/api/v1/tagged-address-dismissals"},
		{"DELETE", "/api/v1/tagged-address-dismissals/some-id/amazon"},
		{"POST", "/api/v1/tagged-address-filters/filter-1/convert-to-sieve"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, "http://"+publicAddr+tc.path, nil)
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode == http.StatusNotFound &&
				strings.Contains(string(body), "404 page not found") {
				t.Errorf("public listener returned stdlib 404 for %s %s — "+
					"the suite SPA hits this path but no handler is mounted. "+
					"Body: %q", tc.method, tc.path, body)
			}
		})
	}
}
