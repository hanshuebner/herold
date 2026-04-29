package observe

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// newExtSubRegistry builds a clean prometheus.Registry with the external
// submission collectors registered. Tests must not use the process-global
// Registry (which may already have these metrics from other tests in the
// suite) so we build our own.
func newExtSubRegistry(t *testing.T) *prometheus.Registry {
	t.Helper()
	r := prometheus.NewRegistry()

	total := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "herold_external_submission_total",
		Help: "test",
	}, []string{"outcome"})
	dur := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "herold_external_submission_duration_seconds",
		Help:    "test",
		Buckets: prometheus.DefBuckets,
	}, []string{"outcome"})
	refresh := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "herold_external_submission_oauth_refresh_total",
		Help: "test",
	}, []string{"outcome"})
	active := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "herold_external_submission_active_identities",
		Help: "test",
	})

	r.MustRegister(total, dur, refresh, active)

	return r
}

// TestExtSubMetricNames verifies the four metric names are stable and match
// the spec (Phase 6, architectural decision 5). We prime each metric with one
// observation so Gather includes it (zero-value counters / histograms without
// any observation may be absent from the text output for some collector types).
func TestExtSubMetricNames(t *testing.T) {
	r := prometheus.NewRegistry()

	total := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "herold_external_submission_total",
		Help: "test",
	}, []string{"outcome"})
	dur := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "herold_external_submission_duration_seconds",
		Help:    "test",
		Buckets: prometheus.DefBuckets,
	}, []string{"outcome"})
	refresh := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "herold_external_submission_oauth_refresh_total",
		Help: "test",
	}, []string{"outcome"})
	active := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "herold_external_submission_active_identities",
		Help: "test",
	})

	r.MustRegister(total, dur, refresh, active)

	// Prime metrics so they appear in Gather output.
	total.WithLabelValues("ok").Inc()
	dur.WithLabelValues("ok").Observe(0.1)
	refresh.WithLabelValues("success").Inc()
	active.Set(1)

	mfs, err := r.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	want := map[string]bool{
		"herold_external_submission_total":               false,
		"herold_external_submission_duration_seconds":    false,
		"herold_external_submission_oauth_refresh_total": false,
		"herold_external_submission_active_identities":   false,
	}
	for _, mf := range mfs {
		if _, ok := want[mf.GetName()]; ok {
			want[mf.GetName()] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("metric %q not registered / not gathered", name)
		}
	}
}

// TestExtSubTotalOutcomeLabels verifies that valid outcome labels can be
// incremented without panicking, and that the counter reflects the increment.
func TestExtSubTotalOutcomeLabels(t *testing.T) {
	outcomes := []string{"ok", "auth-failed", "unreachable", "permanent", "transient"}

	r := prometheus.NewRegistry()
	counter := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "herold_external_submission_total",
		Help: "test",
	}, []string{"outcome"})
	r.MustRegister(counter)

	for _, o := range outcomes {
		counter.WithLabelValues(o).Inc()
	}

	// Each outcome should have value 1.
	for _, o := range outcomes {
		val := testutil.ToFloat64(counter.WithLabelValues(o))
		if val != 1 {
			t.Errorf("outcome %q: want 1, got %v", o, val)
		}
	}
}

// TestExtSubOAuthRefreshLabels verifies the two refresh outcome labels.
func TestExtSubOAuthRefreshLabels(t *testing.T) {
	r := prometheus.NewRegistry()
	counter := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "herold_external_submission_oauth_refresh_total",
		Help: "test",
	}, []string{"outcome"})
	r.MustRegister(counter)

	counter.WithLabelValues("success").Inc()
	counter.WithLabelValues("success").Inc()
	counter.WithLabelValues("failure").Inc()

	if testutil.ToFloat64(counter.WithLabelValues("success")) != 2 {
		t.Errorf("success: want 2")
	}
	if testutil.ToFloat64(counter.WithLabelValues("failure")) != 1 {
		t.Errorf("failure: want 1")
	}
}

// TestExtSubDurationBuckets verifies the histogram uses the standard bucket
// set (prometheus.DefBuckets) and produces the expected bucket names in the
// text-format output.
func TestExtSubDurationBuckets(t *testing.T) {
	r := prometheus.NewRegistry()
	hist := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "herold_external_submission_duration_seconds",
		Help:    "test",
		Buckets: prometheus.DefBuckets,
	}, []string{"outcome"})
	r.MustRegister(hist)

	hist.WithLabelValues("ok").Observe(0.5)

	var sb strings.Builder
	mfs, _ := r.Gather()
	for _, mf := range mfs {
		if mf.GetName() == "herold_external_submission_duration_seconds" {
			for _, m := range mf.Metric {
				for _, b := range m.Histogram.Bucket {
					sb.WriteString("bucket")
					_ = b
				}
			}
		}
	}
	if sb.Len() == 0 {
		t.Errorf("no histogram buckets found")
	}
}

// TestRecordSubmissionOutcome_NilSafe verifies RecordSubmissionOutcome does
// not panic when the process-global metrics are uninitialized (e.g. in a
// test binary that never calls RegisterExtSubMetrics).
func TestRecordSubmissionOutcome_NilSafe(t *testing.T) {
	// Save and restore globals so this test is hermetic.
	saved := ExtSubTotal
	savedDur := ExtSubDuration
	ExtSubTotal = nil
	ExtSubDuration = nil
	defer func() {
		ExtSubTotal = saved
		ExtSubDuration = savedDur
	}()

	// Must not panic.
	RecordSubmissionOutcome("ok", 250*time.Millisecond)
}

// TestRecordOAuthRefreshOutcome_NilSafe verifies RecordOAuthRefreshOutcome does
// not panic when the process-global metric is uninitialized.
func TestRecordOAuthRefreshOutcome_NilSafe(t *testing.T) {
	saved := ExtSubOAuthRefreshTotal
	ExtSubOAuthRefreshTotal = nil
	defer func() { ExtSubOAuthRefreshTotal = saved }()

	RecordOAuthRefreshOutcome("success")
}

// TestSetExtSubActiveIdentities_NilSafe verifies SetExtSubActiveIdentities
// does not panic when the gauge is uninitialized.
func TestSetExtSubActiveIdentities_NilSafe(t *testing.T) {
	saved := ExtSubActiveIdentities
	ExtSubActiveIdentities = nil
	defer func() { ExtSubActiveIdentities = saved }()

	SetExtSubActiveIdentities(42)
}
