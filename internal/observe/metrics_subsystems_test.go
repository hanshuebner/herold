package observe

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestRegisterSubsystemMetrics_Idempotent confirms that each Register*
// helper can be invoked many times without panicking on duplicate
// registration. The first call creates the collectors; later ones are
// no-ops (sync.Once).
func TestRegisterSubsystemMetrics_Idempotent(t *testing.T) {
	// First call wires the collectors and registers them; later calls
	// must be no-ops. We exercise every subsystem because each was wired
	// in a separate constructor and a regression in any one of them
	// would crash the server on a second protocol.New (or in tests that
	// build multiple servers).
	for i := 0; i < 3; i++ {
		RegisterSMTPMetrics()
		RegisterIMAPMetrics()
		RegisterAdminMetrics()
		RegisterStoreMetrics()
		RegisterFTSMetrics(func() float64 { return 0 })
		RegisterPluginMetrics()
		RegisterAuthMetrics()
		RegisterRuntimeCollectors()
	}
}

// TestMetricsHandler_ExposesSubsystemMetrics drives a real /metrics
// scrape after registration and confirms the well-known subsystem
// metric names are present in the output. This is the smoke test that
// answers "is /metrics empty?" — the answer must be no.
func TestMetricsHandler_ExposesSubsystemMetrics(t *testing.T) {
	RegisterSMTPMetrics()
	RegisterIMAPMetrics()
	RegisterAdminMetrics()
	RegisterStoreMetrics()
	RegisterFTSMetrics(func() float64 { return 1.5 })
	RegisterPluginMetrics()
	RegisterAuthMetrics()
	RegisterRuntimeCollectors()

	// Drive at least one observation through each metric so it shows
	// up in the registry's text output (counters with zero observations
	// are emitted, but gauges/histograms with no labels appear without
	// a sample line — exercising one of each ensures the line lands).
	SMTPSessionsActive.WithLabelValues("relay_in").Inc()
	SMTPSessionsActive.WithLabelValues("relay_in").Dec()
	IMAPSessionsActive.Inc()
	IMAPSessionsActive.Dec()
	AdminRequestsTotal.WithLabelValues("/api/v1/x", "GET", "200").Inc()
	StoreMetadataOpDuration.WithLabelValues("insert_message").Observe(0.001)
	FTSIndexedMessagesTotal.Inc()
	PluginUp.WithLabelValues("p").Set(1)
	AuthAttemptsTotal.WithLabelValues("password", "ok").Inc()

	srv := httptest.NewServer(MetricsHandler())
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	out := string(body)
	wantOneOf := []string{
		"herold_smtp_sessions_active",
		"herold_imap_sessions_active",
		"herold_admin_requests_total",
		"herold_store_metadata_op_duration_seconds",
		"herold_fts_indexing_lag_seconds",
		"herold_plugin_up",
		"herold_auth_attempts_total",
		"go_goroutines", // from collectors.NewGoCollector
	}
	for _, m := range wantOneOf {
		if !strings.Contains(out, m) {
			t.Errorf("metric %q missing from /metrics output", m)
		}
	}
}

// TestStoreOpTimer_RecordsHistogramSample exercises the StartStoreOp /
// Done helper directly: a single op should land one observation in the
// herold_store_metadata_op_duration_seconds histogram under its op
// label.
func TestStoreOpTimer_RecordsHistogramSample(t *testing.T) {
	RegisterStoreMetrics()
	timer := StartStoreOp("insert_message")
	timer.Done()
	count := testutil.CollectAndCount(StoreMetadataOpDuration)
	if count == 0 {
		t.Fatalf("StoreMetadataOpDuration: no metric streams after one observation")
	}
}
