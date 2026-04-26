package observe

// Phase 3 Wave 3.9: Email reactions metrics (REQ-PROTO-100..103,
// REQ-FLOW-100..108). Closed-enum label vocabulary per STANDARDS §7.

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	reactionMetricsOnce sync.Once

	// ReactionConsumedTotal counts inbound reaction emails that were
	// successfully applied as native Email.reactions patches.
	// principal label is the local recipient's principal id numeric string.
	// Cardinality is bounded by the operator's principal set — finite and
	// admin-managed.
	ReactionConsumedTotal *prometheus.CounterVec

	// ReactionOutboundTotal counts outbound reaction email dispatch
	// decisions.
	// outcome ∈ {"queued", "skipped_local"}.
	ReactionOutboundTotal *prometheus.CounterVec
)

// RegisterReactionMetrics registers the email-reaction metric families;
// idempotent (safe to call from tests that construct multiple servers).
func RegisterReactionMetrics() {
	reactionMetricsOnce.Do(func() {
		ReactionConsumedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_reaction_consumed_total",
			Help: "Inbound reaction emails consumed as native Email.reactions patches, per principal (REQ-FLOW-107).",
		}, []string{"principal"})

		ReactionOutboundTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_reaction_outbound_total",
			Help: "Outbound reaction email dispatch decisions, by outcome (queued | skipped_local) (REQ-FLOW-100).",
		}, []string{"outcome"})

		MustRegister(
			ReactionConsumedTotal,
			ReactionOutboundTotal,
		)
	})
}
