package admin

import (
	"io"
	"net/http"
	"testing"
	"time"
)

// TestMetrics_ServedFromAdminListener verifies that /metrics is reachable
// on the admin listener (REQ-OPS-ADMIN-LISTENER-01). The Prometheus handler
// returns 200 with a text/plain body; we assert the status code only so
// the test does not depend on which metrics are registered.
func TestMetrics_ServedFromAdminListener(t *testing.T) {
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
	resp, err := http.Get("http://" + adminAddr + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics on admin: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("/metrics on admin listener: status=%d body=%s; want 200",
			resp.StatusCode, string(body))
	}
}

// TestMetrics_Returns404OnPublicListener asserts that the public listener
// does NOT serve /metrics (REQ-OPS-ADMIN-LISTENER-01).
func TestMetrics_Returns404OnPublicListener(t *testing.T) {
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
	resp, err := http.Get("http://" + publicAddr + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics on public: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("/metrics on public listener: status=%d body=%s; want 404",
			resp.StatusCode, string(body))
	}
}
