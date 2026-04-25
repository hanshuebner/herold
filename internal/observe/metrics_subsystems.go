package observe

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Phase 1 metric taxonomy. STANDARDS §7 prescribes the
// herold_<subsystem>_<what>_<unit> name shape; the cardinality of every
// label set is bounded by an enumerated vocabulary. Unbounded values
// (principal_id, email, remote_addr) are NEVER used as labels.
//
// Each subsystem registers its collectors via the dedicated Register*
// function; the function is sync.Once-guarded so repeated calls (typical
// in tests, where multiple servers share the process-global Registry)
// are safe and idempotent. The metrics themselves are exposed as package
// vars so callers can increment / observe directly without going through
// the registry.

// SMTP listener-scoped metrics. Label vocabulary:
//   - listener: "relay_in" | "submission_starttls" | "submission_implicit_tls".
//   - outcome (sessions_total): "ok" | "error" | "panic".
//   - reason (messages_rejected_total): "size" | "policy" | "auth" |
//     "spf" | "dmarc" | "sieve".
//   - direction (data_bytes_total): "in" | "out".
var (
	smtpMetricsOnce sync.Once

	SMTPSessionsActive        *prometheus.GaugeVec
	SMTPSessionsTotal         *prometheus.CounterVec
	SMTPMessagesAcceptedTotal *prometheus.CounterVec
	SMTPMessagesRejectedTotal *prometheus.CounterVec
	SMTPDataBytesTotal        *prometheus.CounterVec
)

// RegisterSMTPMetrics registers the SMTP collector set on first call and
// is a no-op on subsequent calls. Idempotent so test fixtures that build
// many *protosmtp.Server instances against one process Registry stay
// race- and panic-free.
func RegisterSMTPMetrics() {
	smtpMetricsOnce.Do(func() {
		SMTPSessionsActive = prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "herold_smtp_sessions_active",
			Help: "Number of in-flight SMTP sessions per listener.",
		}, []string{"listener"})
		SMTPSessionsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_smtp_sessions_total",
			Help: "Total SMTP sessions terminated, by listener and outcome.",
		}, []string{"listener", "outcome"})
		SMTPMessagesAcceptedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_smtp_messages_accepted_total",
			Help: "Total SMTP messages accepted (post-DATA) per listener.",
		}, []string{"listener"})
		SMTPMessagesRejectedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_smtp_messages_rejected_total",
			Help: "Total SMTP messages rejected, by listener and reason.",
		}, []string{"listener", "reason"})
		SMTPDataBytesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_smtp_data_bytes_total",
			Help: "Total bytes transferred over SMTP, by listener and direction.",
		}, []string{"listener", "direction"})
		MustRegister(
			SMTPSessionsActive,
			SMTPSessionsTotal,
			SMTPMessagesAcceptedTotal,
			SMTPMessagesRejectedTotal,
			SMTPDataBytesTotal,
		)
	})
}

// IMAP session-scoped metrics. Label vocabulary:
//   - outcome (sessions_total): "ok" | "error" | "panic".
//   - command (commands_total): the IMAP command verb (CAPABILITY,
//     LOGIN, SELECT, FETCH, ...). The verb set is enumerated by the
//     dispatch table; unknown verbs fall back to "unknown" so cardinality
//     stays bounded.
var (
	imapMetricsOnce sync.Once

	IMAPSessionsActive  prometheus.Gauge
	IMAPSessionsTotal   *prometheus.CounterVec
	IMAPIdleActive      prometheus.Gauge
	IMAPFetchBytesTotal prometheus.Counter
	IMAPCommandsTotal   *prometheus.CounterVec
)

// RegisterIMAPMetrics registers the IMAP collector set; idempotent.
func RegisterIMAPMetrics() {
	imapMetricsOnce.Do(func() {
		IMAPSessionsActive = prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "herold_imap_sessions_active",
			Help: "Number of in-flight IMAP sessions across all listeners.",
		})
		IMAPSessionsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_imap_sessions_total",
			Help: "Total IMAP sessions terminated, by outcome.",
		}, []string{"outcome"})
		IMAPIdleActive = prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "herold_imap_idle_active",
			Help: "Number of IMAP sessions currently in IDLE.",
		})
		IMAPFetchBytesTotal = prometheus.NewCounter(prometheus.CounterOpts{
			Name: "herold_imap_fetch_bytes_total",
			Help: "Total bytes returned by IMAP FETCH responses.",
		})
		IMAPCommandsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_imap_commands_total",
			Help: "Total IMAP commands dispatched, by command verb.",
		}, []string{"command"})
		MustRegister(
			IMAPSessionsActive,
			IMAPSessionsTotal,
			IMAPIdleActive,
			IMAPFetchBytesTotal,
			IMAPCommandsTotal,
		)
	})
}

// Admin REST metrics. Label vocabulary:
//   - path_pattern: the route template (e.g. "/api/v1/principals/{pid}"),
//     never the resolved path. Bounded by the route table.
//   - method: HTTP verb.
//   - status: HTTP status code as a string (200, 401, ...).
//   - key (rate_limited_total): "api-key" | "bootstrap-ip".
var (
	adminMetricsOnce sync.Once

	AdminRequestsTotal    *prometheus.CounterVec
	AdminRequestDuration  *prometheus.HistogramVec
	AdminRateLimitedTotal *prometheus.CounterVec
)

// RegisterAdminMetrics registers the admin REST collector set; idempotent.
func RegisterAdminMetrics() {
	adminMetricsOnce.Do(func() {
		AdminRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_admin_requests_total",
			Help: "Total admin REST requests, by route template, method, and status.",
		}, []string{"path_pattern", "method", "status"})
		AdminRequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "herold_admin_request_duration_seconds",
			Help:    "Admin REST request duration, by route template.",
			Buckets: prometheus.DefBuckets,
		}, []string{"path_pattern"})
		AdminRateLimitedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_admin_rate_limited_total",
			Help: "Total admin REST requests denied by the rate limiter, by bucket key.",
		}, []string{"key"})
		MustRegister(
			AdminRequestsTotal,
			AdminRequestDuration,
			AdminRateLimitedTotal,
		)
	})
}

// Storage metrics. Label vocabulary:
//   - op (metadata_op_duration_seconds): "insert_message" |
//     "update_flags" | "expunge" | "read_change_feed" | "append_audit".
var (
	storeMetricsOnce sync.Once

	StoreMetadataOpDuration *prometheus.HistogramVec
	StoreBlobsBytes         prometheus.Gauge
	StoreBlobsCount         prometheus.Gauge
)

// RegisterStoreMetrics registers the store collector set; idempotent.
func RegisterStoreMetrics() {
	storeMetricsOnce.Do(func() {
		StoreMetadataOpDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "herold_store_metadata_op_duration_seconds",
			Help:    "Metadata operation latency, by op kind.",
			Buckets: prometheus.DefBuckets,
		}, []string{"op"})
		StoreBlobsBytes = prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "herold_store_blobs_bytes",
			Help: "Total bytes stored in the blob store (best-effort, sampled).",
		})
		StoreBlobsCount = prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "herold_store_blobs_count",
			Help: "Total blobs in the blob store (best-effort, sampled).",
		})
		MustRegister(
			StoreMetadataOpDuration,
			StoreBlobsBytes,
			StoreBlobsCount,
		)
	})
}

// FTS worker metrics.
var (
	ftsMetricsOnce sync.Once

	FTSIndexingLagSeconds   prometheus.GaugeFunc
	FTSIndexedMessagesTotal prometheus.Counter
	FTSQueryDuration        prometheus.Histogram

	// ftsLagSource holds the live Worker.Lag source that
	// FTSIndexingLagSeconds reads. Written exactly once via
	// RegisterFTSMetrics. Reading a nil source returns 0.
	ftsLagMu     sync.RWMutex
	ftsLagSource func() float64
)

// RegisterFTSMetrics registers the FTS collector set; idempotent. The
// lagSource closure is read on every scrape and should return the
// indexing lag in seconds; pass nil to keep the gauge at zero
// (unwired). Subsequent calls update the source so the FTS worker can
// hand its Lag() through after construction.
func RegisterFTSMetrics(lagSource func() float64) {
	ftsLagMu.Lock()
	if lagSource != nil {
		ftsLagSource = lagSource
	}
	ftsLagMu.Unlock()
	ftsMetricsOnce.Do(func() {
		FTSIndexingLagSeconds = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "herold_fts_indexing_lag_seconds",
			Help: "Wall-clock delta between now and the most recent FTS change indexed.",
		}, func() float64 {
			ftsLagMu.RLock()
			f := ftsLagSource
			ftsLagMu.RUnlock()
			if f == nil {
				return 0
			}
			return f()
		})
		FTSIndexedMessagesTotal = prometheus.NewCounter(prometheus.CounterOpts{
			Name: "herold_fts_indexed_messages_total",
			Help: "Total messages indexed by the FTS worker.",
		})
		FTSQueryDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "herold_fts_query_duration_seconds",
			Help:    "FTS query latency.",
			Buckets: prometheus.DefBuckets,
		})
		MustRegister(
			FTSIndexingLagSeconds,
			FTSIndexedMessagesTotal,
			FTSQueryDuration,
		)
	})
}

// Plugin supervisor metrics. Label vocabulary:
//   - name: the plugin's configured name (bounded by system.toml).
//   - method: the JSON-RPC method (bounded by manifest).
//   - outcome: "ok" | "error" | "timeout" | "unavailable".
var (
	pluginMetricsOnce sync.Once

	PluginCallsTotal   *prometheus.CounterVec
	PluginCallDuration *prometheus.HistogramVec
	PluginUp           *prometheus.GaugeVec
)

// RegisterPluginMetrics registers the plugin collector set; idempotent.
func RegisterPluginMetrics() {
	pluginMetricsOnce.Do(func() {
		PluginCallsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_plugin_calls_total",
			Help: "Total plugin RPC calls, by plugin name, method, and outcome.",
		}, []string{"name", "method", "outcome"})
		PluginCallDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "herold_plugin_call_duration_seconds",
			Help:    "Plugin RPC duration, by plugin name and method.",
			Buckets: prometheus.DefBuckets,
		}, []string{"name", "method"})
		PluginUp = prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "herold_plugin_up",
			Help: "1 if the plugin is in the healthy state, 0 otherwise.",
		}, []string{"name"})
		MustRegister(
			PluginCallsTotal,
			PluginCallDuration,
			PluginUp,
		)
	})
}

// Auth (directory) metrics. Label vocabulary:
//   - kind: "password" | "totp" | "oauth" | "apikey".
//   - outcome: "ok" | "fail" | "rate_limited".
var (
	authMetricsOnce sync.Once

	AuthAttemptsTotal *prometheus.CounterVec
)

// RegisterAuthMetrics registers the auth collector set; idempotent.
func RegisterAuthMetrics() {
	authMetricsOnce.Do(func() {
		AuthAttemptsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_auth_attempts_total",
			Help: "Total authentication attempts, by credential kind and outcome.",
		}, []string{"kind", "outcome"})
		MustRegister(AuthAttemptsTotal)
	})
}

// Runtime + process collectors. Registered against Registry so /metrics
// surfaces Go runtime memory / GC / goroutine counts and the standard
// process collector (RSS, CPU). Idempotent on repeated calls.
var runtimeMetricsOnce sync.Once

// RegisterRuntimeCollectors adds the standard Go runtime + process
// collectors to Registry. Called from StartServer; tests that drive
// /metrics directly call it through the same path.
func RegisterRuntimeCollectors() {
	runtimeMetricsOnce.Do(func() {
		MustRegister(
			collectors.NewGoCollector(),
			collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		)
	})
}

// ObserveStoreOp is the shared timing-helper protocol-handlers call to
// record a Metadata op latency under a bounded op label. Wrapping a call
// site looks like:
//
//	t := observe.StartStoreOp("insert_message")
//	_, _, err := store.Meta().InsertMessage(ctx, msg)
//	t.Done()
//
// The Done method is a no-op when collectors have not been registered,
// so packages can call it without first checking.
type StoreOpTimer struct {
	op    string
	start time.Time
}

// StartStoreOp begins timing a Metadata op. Returns a timer whose Done
// method records the elapsed seconds against the
// herold_store_metadata_op_duration_seconds histogram. Safe to call
// before RegisterStoreMetrics — Done becomes a no-op.
func StartStoreOp(op string) *StoreOpTimer {
	return &StoreOpTimer{op: op, start: time.Now()}
}

// Done records the elapsed time. Calling Done twice records twice; the
// caller is expected to defer the call.
func (t *StoreOpTimer) Done() {
	if t == nil {
		return
	}
	if StoreMetadataOpDuration == nil {
		return
	}
	StoreMetadataOpDuration.WithLabelValues(t.op).Observe(time.Since(t.start).Seconds())
}
