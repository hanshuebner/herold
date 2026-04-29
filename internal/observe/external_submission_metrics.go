package observe

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// External SMTP submission metrics (REQ-AUTH-EXT-SUBMIT-09, Phase 6).
//
// Naming follows the herold_<subsystem>_<what>_<unit> convention from
// STANDARDS §7. Label vocabularies are closed enums so cardinality is bounded.
//
// Labels:
//   - outcome (external_submission_total): "ok" | "auth-failed" | "unreachable" |
//     "permanent" | "transient"  (one value per extsubmit.OutcomeState).
//   - outcome (external_submission_duration_seconds): same vocab.
//   - outcome (external_submission_oauth_refresh_total): "success" | "failure".
//
// The active-identities gauge reads the most recently observed row count from
// the sweeper (set on every tick via SetExtSubActiveIdentities). It is a plain
// Gauge rather than GaugeFunc so the sweeper does not need to hold a reference
// to the store for scrape-time queries.
var (
	extSubMetricsOnce sync.Once

	// ExtSubTotal is incremented on every Submitter.Submit call.
	ExtSubTotal *prometheus.CounterVec

	// ExtSubDuration observes the wall-clock duration of every Submit call.
	ExtSubDuration *prometheus.HistogramVec

	// ExtSubOAuthRefreshTotal is incremented on every sweeper OAuth refresh
	// attempt (success or failure).
	ExtSubOAuthRefreshTotal *prometheus.CounterVec

	// ExtSubActiveIdentities is set to the total number of identity_submission
	// rows with submit_auth_method='oauth2', updated on every sweeper tick via
	// CountOAuthIdentitySubmissions. Counts all OAuth-configured identities,
	// not just those due for refresh in the current window.
	ExtSubActiveIdentities prometheus.Gauge
)

// RegisterExtSubMetrics registers the external-submission collector set;
// idempotent so test fixtures that construct multiple servers in one process
// stay race- and panic-free.
func RegisterExtSubMetrics() {
	extSubMetricsOnce.Do(func() {
		ExtSubTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_external_submission_total",
			Help: "Total external SMTP submission attempts, by outcome (REQ-AUTH-EXT-SUBMIT-09).",
		}, []string{"outcome"})

		ExtSubDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "herold_external_submission_duration_seconds",
			Help:    "Wall-clock duration of each external SMTP submission attempt, by outcome.",
			Buckets: prometheus.DefBuckets,
		}, []string{"outcome"})

		ExtSubOAuthRefreshTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_external_submission_oauth_refresh_total",
			Help: "Total OAuth token refresh attempts by the sweeper, by outcome (success|failure).",
		}, []string{"outcome"})

		ExtSubActiveIdentities = prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "herold_external_submission_active_identities",
			Help: "Number of identities with external SMTP submission configured via OAuth 2.0. Updated on every sweeper tick. Counts all oauth2-configured identities, not just those due for refresh in the current window.",
		})

		MustRegister(
			ExtSubTotal,
			ExtSubDuration,
			ExtSubOAuthRefreshTotal,
			ExtSubActiveIdentities,
		)
	})
}

// RecordSubmissionOutcome increments ExtSubTotal and observes ExtSubDuration
// for one submission attempt. outcome is the string form of extsubmit.OutcomeState
// (e.g. "ok", "auth-failed"). duration is the elapsed time of the Submit call.
//
// Safe to call before RegisterExtSubMetrics — the nil-check on the metric
// makes it a no-op until the metrics are registered.
func RecordSubmissionOutcome(outcome string, duration time.Duration) {
	if ExtSubTotal == nil {
		return
	}
	ExtSubTotal.WithLabelValues(outcome).Inc()
	ExtSubDuration.WithLabelValues(outcome).Observe(duration.Seconds())
}

// RecordOAuthRefreshOutcome increments ExtSubOAuthRefreshTotal. outcome is
// "success" or "failure".
//
// Safe to call before RegisterExtSubMetrics.
func RecordOAuthRefreshOutcome(outcome string) {
	if ExtSubOAuthRefreshTotal == nil {
		return
	}
	ExtSubOAuthRefreshTotal.WithLabelValues(outcome).Inc()
}

// SetExtSubActiveIdentities records the total count of OAuth-configured
// identity_submission rows (all rows with submit_auth_method='oauth2',
// regardless of refresh_due_us). Called by the sweeper on every tick via
// CountOAuthIdentitySubmissions.
//
// Safe to call before RegisterExtSubMetrics.
func SetExtSubActiveIdentities(n int) {
	if ExtSubActiveIdentities == nil {
		return
	}
	ExtSubActiveIdentities.Set(float64(n))
}
