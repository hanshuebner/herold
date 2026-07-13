package observe

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// Mailing-list fan-out metrics (REQ-MLIST-43, issue #183). Label
// vocabulary:
//   - list: the list's posting address. Bounded by the small,
//     admin-configured set of hosted lists on a deployment -- not an
//     arbitrary user-supplied value -- so it is an acceptable label
//     (same cardinality class as "domain" elsewhere in this file).
//   - outcome (fanout_total): "delivered" | "unsealed" | "loop" |
//     "auto_submitted" | "oversize". "unsealed" (REQ-MLIST-21, issue #183)
//     is a fan-out copy delivered WITHOUT ARC sealing because the list has
//     ARCSeal enabled but sealing failed (most commonly no active DKIM key
//     for the domain) -- distinct from "delivered" so a dashboard/alert
//     can see the degradation.
//   - state (members): the roster state counted at the last fan-out scan.
var (
	mailingListMetricsOnce sync.Once

	MailingListFanoutTotal   *prometheus.CounterVec
	MailingListMembers       *prometheus.GaugeVec
	MailingListExpandSeconds *prometheus.HistogramVec
)

// RegisterMailingListMetrics registers the mailing-list collector set
// on first call and is a no-op on subsequent calls. Idempotent so test
// fixtures that build many *maillist.Expander instances against one
// process Registry stay race- and panic-free.
func RegisterMailingListMetrics() {
	mailingListMetricsOnce.Do(func() {
		MailingListFanoutTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_mlist_fanout_total",
			Help: "Total mailing-list fan-out copies, by list and outcome.",
		}, []string{"list", "outcome"})
		MailingListMembers = prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "herold_mlist_members",
			Help: "Mailing-list member count observed at the last fan-out, by list and state.",
		}, []string{"list", "state"})
		MailingListExpandSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "herold_mlist_expand_seconds",
			Help:    "Wall time to expand and enqueue one list post, by list.",
			Buckets: prometheus.DefBuckets,
		}, []string{"list"})
		MustRegister(
			MailingListFanoutTotal,
			MailingListMembers,
			MailingListExpandSeconds,
		)
	})
}
