package queue

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/hanshuebner/herold/internal/observe"
)

// Metric label vocabulary:
//   - state (queue_items): "queued" | "deferred" | "inflight" | "done" |
//     "failed" | "held". Bounded by store.QueueState.String().
//   - outcome (deliveries_total / delivery_duration_seconds): "success" |
//     "permanent" | "transient" | "hold". Bounded by DeliveryStatus.String().
//   - kind (dsn_emitted_total): "success" | "failure" | "delay".

var (
	metricsOnce sync.Once

	queueItems              *prometheus.GaugeVec
	queueDeliveriesTotal    *prometheus.CounterVec
	queueDeliveryDuration   *prometheus.HistogramVec
	queueDSNEmittedTotal    *prometheus.CounterVec
	queueShutdownDrainTotal prometheus.Counter
)

// registerMetrics registers the queue collector set on first call and
// is a no-op on subsequent calls. Idempotent so multiple Queue
// instances in one process (typical in tests) do not panic on the
// shared observe.Registry.
func registerMetrics() {
	metricsOnce.Do(func() {
		queueItems = prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "herold_queue_items",
			Help: "Number of queue rows in each lifecycle state.",
		}, []string{"state"})
		queueDeliveriesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_queue_deliveries_total",
			Help: "Total queue delivery attempts terminated, by outcome.",
		}, []string{"outcome"})
		queueDeliveryDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "herold_queue_delivery_duration_seconds",
			Help:    "Queue delivery attempt duration, by outcome.",
			Buckets: prometheus.DefBuckets,
		}, []string{"outcome"})
		queueDSNEmittedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_queue_dsn_emitted_total",
			Help: "Total DSN messages enqueued, by kind.",
		}, []string{"kind"})
		queueShutdownDrainTotal = prometheus.NewCounter(prometheus.CounterOpts{
			Name: "herold_queue_shutdown_drains_total",
			Help: "Number of graceful-shutdown drain cycles completed.",
		})
		observe.MustRegister(
			queueItems,
			queueDeliveriesTotal,
			queueDeliveryDuration,
			queueDSNEmittedTotal,
			queueShutdownDrainTotal,
		)
	})
}
