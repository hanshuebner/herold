package observe

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestMetricsHandler_ServesOKWithOpenMetricsTextContentType(t *testing.T) {
	srv := httptest.NewServer(MetricsHandler())
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	// Default prometheus text exposition format.
	if !strings.Contains(ct, "text/plain") || !strings.Contains(ct, "version=0.0.4") {
		t.Fatalf("Content-Type = %q, want text/plain; version=0.0.4", ct)
	}
}

func TestMustRegister_ExposesCollectorOnHandler(t *testing.T) {
	// Use a uniquely named collector to avoid clashes with other tests.
	c := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "herold_observe_metrics_test_total",
		Help: "test-only counter",
	})
	MustRegister(c)
	c.Inc()
	t.Cleanup(func() { Registry.Unregister(c) })

	srv := httptest.NewServer(MetricsHandler())
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "herold_observe_metrics_test_total 1") {
		t.Fatalf("metric not exposed in scrape output:\n%s", string(body))
	}
}
