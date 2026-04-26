package observe

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// Webhook (mail-arrival) dispatcher metrics. REQ-HOOK-40 enumerates
// the closed label vocabulary:
//
//   - name:   the operator-visible webhook identifier; bounded by the
//     row count in `webhooks` so we surface it as a label rather
//     than embedding it in the metric name.
//   - status: "2xx" | "4xx" | "5xx" | "timeout" | "network" |
//     "dropped_no_text"  (REQ-HOOK-EXTRACTED-03).
//
// Lives in its own file so the Phase 3 Wave 3.5c Track C metrics
// addition does not collide with parallel work in
// metrics_subsystems.go.
var (
	hookMetricsOnce sync.Once

	HookDeliveriesTotal         *prometheus.CounterVec
	HookExtractedTruncatedTotal *prometheus.CounterVec
)

// RegisterHookMetrics registers the webhook-dispatcher collector set;
// idempotent.
func RegisterHookMetrics() {
	hookMetricsOnce.Do(func() {
		HookDeliveriesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_hook_deliveries_total",
			Help: "Webhook delivery outcomes, by hook name and status (2xx | 4xx | 5xx | timeout | network | dropped_no_text).",
		}, []string{"name", "status"})
		HookExtractedTruncatedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_hook_extracted_truncated_total",
			Help: "Extracted-mode webhook deliveries whose body.text hit the per-subscription cap.",
		}, []string{"name"})
		MustRegister(
			HookDeliveriesTotal,
			HookExtractedTruncatedTotal,
		)
	})
}
