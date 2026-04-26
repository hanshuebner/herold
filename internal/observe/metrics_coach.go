package observe

// Phase 3 Wave 3.10: ShortcutCoachStat metrics (REQ-PROTO-110..112).
// Closed-enum label vocabulary per STANDARDS §7.

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	coachMetricsOnce sync.Once

	// CoachEventsTotal counts ShortcutCoachStat events appended by
	// ShortcutCoachStat/set calls. method ∈ {"keyboard", "mouse"} —
	// closed enum matching the CHECK constraint in 0020_coach.sql.
	CoachEventsTotal *prometheus.CounterVec

	// CoachGCDeletedTotal counts coach_events rows removed by the nightly
	// GC pass.
	CoachGCDeletedTotal prometheus.Counter
)

// RegisterCoachMetrics registers the shortcut-coach metric families;
// idempotent (safe to call from tests that construct multiple servers).
func RegisterCoachMetrics() {
	coachMetricsOnce.Do(func() {
		CoachEventsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_coach_events_total",
			Help: "ShortcutCoachStat event rows appended, by input method (REQ-PROTO-111).",
		}, []string{"method"})

		CoachGCDeletedTotal = prometheus.NewCounter(prometheus.CounterOpts{
			Name: "herold_coach_gc_deleted_total",
			Help: "coach_events rows deleted by the nightly GC pass (REQ-PROTO-110).",
		})

		MustRegister(
			CoachEventsTotal,
			CoachGCDeletedTotal,
		)
	})
}
