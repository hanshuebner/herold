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

// SMTP outbound (queue worker → remote MX or smart host) metrics.
// REQ-FLOW-SMARTHOST-08 enumerates the closed label vocabulary:
//
//   - path: "smart_host" | "direct_mx".
//   - outcome: "success" | "transient" | "permanent" |
//     "connection_refused" | "tls_failed" | "auth_failed".
//   - from / to (fallback_total): the configured fallback policy and
//     the path the worker actually took on a fallback fire.
var (
	smtpOutboundMetricsOnce sync.Once

	SMTPOutboundTotal          *prometheus.CounterVec
	SMTPOutboundConnectSeconds *prometheus.HistogramVec
	SMTPSmartHostFallbackTotal *prometheus.CounterVec
)

// RegisterSMTPOutboundMetrics registers the smart-host / outbound
// collector set; idempotent so test fixtures that build many Clients
// against the process Registry stay race- and panic-free.
func RegisterSMTPOutboundMetrics() {
	smtpOutboundMetricsOnce.Do(func() {
		SMTPOutboundTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_smtp_outbound_total",
			Help: "Total outbound delivery attempts, by path and outcome.",
		}, []string{"path", "outcome"})
		SMTPOutboundConnectSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "herold_smtp_outbound_connect_seconds",
			Help:    "TCP / TLS connect latency for outbound delivery, by path.",
			Buckets: prometheus.DefBuckets,
		}, []string{"path"})
		SMTPSmartHostFallbackTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_smtp_smarthost_fallback_total",
			Help: "Smart-host fallback fires, by configured policy and the path actually taken.",
		}, []string{"from", "to"})
		MustRegister(
			SMTPOutboundTotal,
			SMTPOutboundConnectSeconds,
			SMTPSmartHostFallbackTotal,
		)
	})
}

// Inbound attachment-policy metrics (REQ-FLOW-ATTPOL-02). Label
// vocabulary is closed:
//   - recipient_domain: lowercased domain part of the local recipient.
//     Bounded by the operator's local-domain set, which is itself
//     finite and admin-managed.
//   - outcome: "passed" | "refused_at_data" | "refused_post_acceptance".
var (
	smtpAttPolMetricsOnce sync.Once

	SMTPInboundAttachmentPolicyTotal *prometheus.CounterVec
)

// RegisterSMTPAttachmentPolicyMetrics registers the inbound
// attachment-policy collector set; idempotent.
func RegisterSMTPAttachmentPolicyMetrics() {
	smtpAttPolMetricsOnce.Do(func() {
		SMTPInboundAttachmentPolicyTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_inbound_attachment_policy_total",
			Help: "Inbound attachment-policy outcomes, by recipient domain and outcome (REQ-FLOW-ATTPOL-02).",
		}, []string{"recipient_domain", "outcome"})
		MustRegister(SMTPInboundAttachmentPolicyTotal)
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

// Directory RCPT-time hook metrics (REQ-DIR-RCPT-10). Label vocabulary
// is closed by the plugin name (operator-supplied, bounded by
// system.toml) and the action enum:
//   - action (resolve_rcpt_total): "accept" | "reject" | "defer" |
//     "fallthrough" | "ratelimited" | "breaker_open".
var (
	directoryRcptMetricsOnce sync.Once

	DirectoryResolveRcptTotal          *prometheus.CounterVec
	DirectoryResolveRcptLatencySeconds *prometheus.HistogramVec
	DirectoryResolveRcptTimeoutsTotal  *prometheus.CounterVec
	DirectorySyntheticAcceptedTotal    *prometheus.CounterVec
	DirectoryPluginBreakerState        *prometheus.GaugeVec
)

// RegisterDirectoryRcptMetrics registers the directory.resolve_rcpt
// collector set; idempotent.
func RegisterDirectoryRcptMetrics() {
	directoryRcptMetricsOnce.Do(func() {
		DirectoryResolveRcptTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_directory_resolve_rcpt_total",
			Help: "Total directory.resolve_rcpt outcomes, by plugin and action.",
		}, []string{"plugin", "action"})
		DirectoryResolveRcptLatencySeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "herold_directory_resolve_rcpt_latency_seconds",
			Help:    "directory.resolve_rcpt round-trip latency, by plugin.",
			Buckets: prometheus.DefBuckets,
		}, []string{"plugin"})
		DirectoryResolveRcptTimeoutsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_directory_resolve_rcpt_timeouts_total",
			Help: "directory.resolve_rcpt invocations that exceeded the per-call deadline, by plugin.",
		}, []string{"plugin"})
		DirectorySyntheticAcceptedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_directory_synthetic_accepted_total",
			Help: "Synthetic recipients accepted via directory.resolve_rcpt (no principal_id), by plugin.",
		}, []string{"plugin"})
		DirectoryPluginBreakerState = prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "herold_directory_plugin_breaker_state",
			Help: "directory.resolve_rcpt circuit breaker state per plugin (0=closed, 1=half-open, 2=open).",
		}, []string{"plugin"})
		MustRegister(
			DirectoryResolveRcptTotal,
			DirectoryResolveRcptLatencySeconds,
			DirectoryResolveRcptTimeoutsTotal,
			DirectorySyntheticAcceptedTotal,
			DirectoryPluginBreakerState,
		)
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

// Phase 2 Wave 2.9.7 metric taxonomy. The eight subsystems shipped in
// Waves 2.5-2.9.6 (protocall, protochat, protoimg, categorise, snooze,
// chatretention, and the protojmap chat / calendars / contacts JMAP
// surfaces) all register here for the same reasons the Phase 1 sets do:
// closed-vocabulary labels, sync.Once guard so test fixtures that build
// many servers against the process Registry stay idempotent, and
// collectors exposed as package vars so callers can increment / observe
// directly without going through the registry.

// protocall metrics. Label vocabulary:
//   - result (credentials_minted_total): "ok" | "blocked" | "ratelimited".
//   - disposition (calls_ended_total): "completed" | "missed" |
//     "declined" | "timeout".
//   - reason (busy_emitted_total): "offerer_in_call" | "recipient_in_call".
var (
	protocallMetricsOnce sync.Once

	ProtocallCredentialsMintedTotal *prometheus.CounterVec
	ProtocallCallsStartedTotal      prometheus.Counter
	ProtocallCallsEndedTotal        *prometheus.CounterVec
	ProtocallCallsInflight          prometheus.Gauge
	ProtocallBusyEmittedTotal       *prometheus.CounterVec
	ProtocallRingTimeoutsTotal      prometheus.Counter
)

// RegisterProtocallMetrics registers the protocall collector set;
// idempotent.
func RegisterProtocallMetrics() {
	protocallMetricsOnce.Do(func() {
		ProtocallCredentialsMintedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_protocall_credentials_minted_total",
			Help: "TURN credential mint attempts, by outcome.",
		}, []string{"result"})
		ProtocallCallsStartedTotal = prometheus.NewCounter(prometheus.CounterOpts{
			Name: "herold_protocall_calls_started_total",
			Help: "Total call sessions started (call.started system message written).",
		})
		ProtocallCallsEndedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_protocall_calls_ended_total",
			Help: "Total call sessions ended, by disposition.",
		}, []string{"disposition"})
		ProtocallCallsInflight = prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "herold_protocall_calls_inflight",
			Help: "In-flight call sessions tracked by the lifecycle map.",
		})
		ProtocallBusyEmittedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_protocall_busy_emitted_total",
			Help: "Synthetic kind=\"busy\" call.signal emissions, by side already in a call.",
		}, []string{"reason"})
		ProtocallRingTimeoutsTotal = prometheus.NewCounter(prometheus.CounterOpts{
			Name: "herold_protocall_ring_timeouts_total",
			Help: "Ring-timer expirations producing a synthetic kind=\"timeout\" signal.",
		})
		MustRegister(
			ProtocallCredentialsMintedTotal,
			ProtocallCallsStartedTotal,
			ProtocallCallsEndedTotal,
			ProtocallCallsInflight,
			ProtocallBusyEmittedTotal,
			ProtocallRingTimeoutsTotal,
		)
	})
}

// protochat metrics. Label vocabulary is closed by the wire protocol's
// frame-type vocabulary (see internal/protochat/protocol.go) and the
// RFC 6455 close-code subset herold emits.
//   - frame_type (frames_in_total / frames_out_total /
//     ratelimit_drops_total): the closed enum below; unknown / malformed
//     types collapse to "unknown" so cardinality stays bounded.
//   - code (close_total): the numeric RFC 6455 close code as a string.
var (
	protochatMetricsOnce sync.Once

	ProtochatConnectionsCurrent     prometheus.Gauge
	ProtochatConnectionsTotal       prometheus.Counter
	ProtochatFramesInTotal          *prometheus.CounterVec
	ProtochatFramesOutTotal         *prometheus.CounterVec
	ProtochatRatelimitDropsTotal    *prometheus.CounterVec
	ProtochatBackpressureDropsTotal prometheus.Counter
	ProtochatCloseTotal             *prometheus.CounterVec
)

// RegisterProtochatMetrics registers the protochat collector set;
// idempotent.
func RegisterProtochatMetrics() {
	protochatMetricsOnce.Do(func() {
		ProtochatConnectionsCurrent = prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "herold_protochat_connections_current",
			Help: "Current number of live /chat/ws WebSocket connections.",
		})
		ProtochatConnectionsTotal = prometheus.NewCounter(prometheus.CounterOpts{
			Name: "herold_protochat_connections_total",
			Help: "Total accepted /chat/ws WebSocket upgrades.",
		})
		ProtochatFramesInTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_protochat_frames_in_total",
			Help: "Inbound chat frames dispatched, by client frame type.",
		}, []string{"type"})
		ProtochatFramesOutTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_protochat_frames_out_total",
			Help: "Outbound chat frames sent to clients, by server frame type.",
		}, []string{"type"})
		ProtochatRatelimitDropsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_protochat_ratelimit_drops_total",
			Help: "Inbound chat frames denied by the per-principal rate limiter, by frame type.",
		}, []string{"type"})
		ProtochatBackpressureDropsTotal = prometheus.NewCounter(prometheus.CounterOpts{
			Name: "herold_protochat_backpressure_drops_total",
			Help: "Connections shut down because their write queue overflowed (slow client).",
		})
		ProtochatCloseTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_protochat_close_total",
			Help: "WebSocket connection closures, by close code emitted by the server.",
		}, []string{"code"})
		MustRegister(
			ProtochatConnectionsCurrent,
			ProtochatConnectionsTotal,
			ProtochatFramesInTotal,
			ProtochatFramesOutTotal,
			ProtochatRatelimitDropsTotal,
			ProtochatBackpressureDropsTotal,
			ProtochatCloseTotal,
		)
	})
}

// protoimg metrics. Label vocabulary is closed by the proxy outcome set
// the handler classifies, plus the netguard SSRF reason vocabulary.
//   - outcome (requests_total): "hit" | "miss" | "blocked" |
//     "upstream_error" | "ratelimited" | "oversize" | "badurl".
//   - reason (ssrf_blocked_total): netguard.Reason values
//     ("loopback" | "link_local" | "private" | "cgnat" | "multicast" |
//     "unspecified" | "ipv6_ula").
var (
	protoimgMetricsOnce sync.Once

	ProtoimgRequestsTotal       *prometheus.CounterVec
	ProtoimgCacheHitsTotal      prometheus.Counter
	ProtoimgCacheMissesTotal    prometheus.Counter
	ProtoimgCacheEvictionsTotal prometheus.Counter
	ProtoimgBytesProxiedTotal   prometheus.Counter
	ProtoimgSSRFBlockedTotal    *prometheus.CounterVec
)

// RegisterProtoimgMetrics registers the protoimg collector set;
// idempotent.
func RegisterProtoimgMetrics() {
	protoimgMetricsOnce.Do(func() {
		ProtoimgRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_protoimg_requests_total",
			Help: "Image-proxy requests served, by outcome.",
		}, []string{"outcome"})
		ProtoimgCacheHitsTotal = prometheus.NewCounter(prometheus.CounterOpts{
			Name: "herold_protoimg_cache_hits_total",
			Help: "In-process image-proxy cache hits.",
		})
		ProtoimgCacheMissesTotal = prometheus.NewCounter(prometheus.CounterOpts{
			Name: "herold_protoimg_cache_misses_total",
			Help: "In-process image-proxy cache misses.",
		})
		ProtoimgCacheEvictionsTotal = prometheus.NewCounter(prometheus.CounterOpts{
			Name: "herold_protoimg_cache_evictions_total",
			Help: "Entries dropped by the image-proxy LRU cache (count or byte budget).",
		})
		ProtoimgBytesProxiedTotal = prometheus.NewCounter(prometheus.CounterOpts{
			Name: "herold_protoimg_bytes_proxied_total",
			Help: "Image bytes written to clients (cache hit or fresh upstream fetch).",
		})
		ProtoimgSSRFBlockedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_protoimg_ssrf_blocked_total",
			Help: "Upstream fetches refused by the SSRF guard, by netguard reason.",
		}, []string{"reason"})
		MustRegister(
			ProtoimgRequestsTotal,
			ProtoimgCacheHitsTotal,
			ProtoimgCacheMissesTotal,
			ProtoimgCacheEvictionsTotal,
			ProtoimgBytesProxiedTotal,
			ProtoimgSSRFBlockedTotal,
		)
	})
}

// categorise metrics. Label vocabulary:
//   - outcome (calls_total): "categorised" | "none" | "unknown_category" |
//     "endpoint_rejected" | "http_error" | "timeout".
//   - category (categories_assigned_total): the bucketed category name.
//     CategorySet is operator-configurable (REQ-FILT-200..215) so the
//     metric labels every assigned category by name. Cardinality is
//     bounded by the operator's CategorySet rows; any model output not
//     in the configured set is rejected upstream and never reaches the
//     metric. The set is small in practice (the spec lists the six
//     gmail-style buckets as the canonical default) and operator-scoped,
//     not user-controlled, so the cardinality risk is bounded.
var (
	categoriseMetricsOnce sync.Once

	CategoriseCallsTotal              *prometheus.CounterVec
	CategoriseCallDurationSeconds     prometheus.Histogram
	CategoriseCategoriesAssignedTotal *prometheus.CounterVec
)

// RegisterCategoriseMetrics registers the categorise collector set;
// idempotent.
func RegisterCategoriseMetrics() {
	categoriseMetricsOnce.Do(func() {
		CategoriseCallsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_categorise_calls_total",
			Help: "Categoriser invocations, by outcome.",
		}, []string{"outcome"})
		CategoriseCallDurationSeconds = prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "herold_categorise_call_duration_seconds",
			Help:    "End-to-end categoriser invocation duration (LLM call included).",
			Buckets: prometheus.DefBuckets,
		})
		CategoriseCategoriesAssignedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_categorise_categories_assigned_total",
			Help: "Category names assigned to messages by the categoriser, bounded by operator-configured CategorySet.",
		}, []string{"category"})
		MustRegister(
			CategoriseCallsTotal,
			CategoriseCallDurationSeconds,
			CategoriseCategoriesAssignedTotal,
		)
	})
}

// snooze metrics. Counters track sweep work; the histogram surfaces
// per-sweep duration so backlogs are visible without log scraping.
var (
	snoozeMetricsOnce sync.Once

	SnoozeSweepsTotal          prometheus.Counter
	SnoozeMessagesWokenTotal   prometheus.Counter
	SnoozeSweepDurationSeconds prometheus.Histogram
)

// RegisterSnoozeMetrics registers the snooze collector set; idempotent.
func RegisterSnoozeMetrics() {
	snoozeMetricsOnce.Do(func() {
		SnoozeSweepsTotal = prometheus.NewCounter(prometheus.CounterOpts{
			Name: "herold_snooze_sweeps_total",
			Help: "Total snooze worker sweep ticks executed.",
		})
		SnoozeMessagesWokenTotal = prometheus.NewCounter(prometheus.CounterOpts{
			Name: "herold_snooze_messages_woken_total",
			Help: "Total snoozed messages released by the snooze worker.",
		})
		SnoozeSweepDurationSeconds = prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "herold_snooze_sweep_duration_seconds",
			Help:    "Snooze worker sweep duration.",
			Buckets: prometheus.DefBuckets,
		})
		MustRegister(
			SnoozeSweepsTotal,
			SnoozeMessagesWokenTotal,
			SnoozeSweepDurationSeconds,
		)
	})
}

// chatretention metrics. Same shape as snooze.
var (
	chatretentionMetricsOnce sync.Once

	ChatretentionSweepsTotal          prometheus.Counter
	ChatretentionMessagesDeletedTotal prometheus.Counter
	ChatretentionSweepDurationSeconds prometheus.Histogram
)

// RegisterChatretentionMetrics registers the chatretention collector
// set; idempotent.
func RegisterChatretentionMetrics() {
	chatretentionMetricsOnce.Do(func() {
		ChatretentionSweepsTotal = prometheus.NewCounter(prometheus.CounterOpts{
			Name: "herold_chatretention_sweeps_total",
			Help: "Total chat retention worker sweep ticks executed.",
		})
		ChatretentionMessagesDeletedTotal = prometheus.NewCounter(prometheus.CounterOpts{
			Name: "herold_chatretention_messages_deleted_total",
			Help: "Total chat messages hard-deleted by the chat retention worker.",
		})
		ChatretentionSweepDurationSeconds = prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "herold_chatretention_sweep_duration_seconds",
			Help:    "Chat retention worker sweep duration.",
			Buckets: prometheus.DefBuckets,
		})
		MustRegister(
			ChatretentionSweepsTotal,
			ChatretentionMessagesDeletedTotal,
			ChatretentionSweepDurationSeconds,
		)
	})
}

// protojmap chat / calendars / contacts metrics. Label vocabulary is
// closed by the registered method set in each datatype's Register*
// constructor; methods not registered cannot be invoked, so cardinality
// is bounded by code, not user input.
//   - method: the JMAP method verb ("Conversation/get", ...).
//   - code (method_errors_total): the JMAP error type token
//     ("forbidden" | "invalidProperties" | "notFound" | "serverError" |
//     "tooLarge" | ...). Open-ended in the JMAP spec but the per-package
//     handlers map to a closed subset; unknown types collapse to
//     "unknown" so the cardinality stays bounded.
var (
	protojmapChatMetricsOnce      sync.Once
	protojmapCalendarsMetricsOnce sync.Once
	protojmapContactsMetricsOnce  sync.Once

	ProtojmapChatMethodsTotal      *prometheus.CounterVec
	ProtojmapChatMethodErrorsTotal *prometheus.CounterVec

	ProtojmapCalendarsMethodsTotal      *prometheus.CounterVec
	ProtojmapCalendarsMethodErrorsTotal *prometheus.CounterVec

	ProtojmapContactsMethodsTotal      *prometheus.CounterVec
	ProtojmapContactsMethodErrorsTotal *prometheus.CounterVec
)

// RegisterProtojmapChatMetrics registers the chat-JMAP collector set;
// idempotent.
func RegisterProtojmapChatMetrics() {
	protojmapChatMetricsOnce.Do(func() {
		ProtojmapChatMethodsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_protojmap_chat_methods_total",
			Help: "JMAP Chat method invocations, by method.",
		}, []string{"method"})
		ProtojmapChatMethodErrorsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_protojmap_chat_method_errors_total",
			Help: "JMAP Chat method failures, by method and error code.",
		}, []string{"method", "code"})
		MustRegister(
			ProtojmapChatMethodsTotal,
			ProtojmapChatMethodErrorsTotal,
		)
	})
}

// RegisterProtojmapCalendarsMetrics registers the calendars-JMAP
// collector set; idempotent.
func RegisterProtojmapCalendarsMetrics() {
	protojmapCalendarsMetricsOnce.Do(func() {
		ProtojmapCalendarsMethodsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_protojmap_calendars_methods_total",
			Help: "JMAP Calendars method invocations, by method.",
		}, []string{"method"})
		ProtojmapCalendarsMethodErrorsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_protojmap_calendars_method_errors_total",
			Help: "JMAP Calendars method failures, by method and error code.",
		}, []string{"method", "code"})
		MustRegister(
			ProtojmapCalendarsMethodsTotal,
			ProtojmapCalendarsMethodErrorsTotal,
		)
	})
}

// Webhook (mail-arrival) dispatcher metrics. REQ-HOOK-40 enumerates
// the closed label vocabulary:
//
//   - name:   the operator-visible webhook name; bounded by the row
//     count in `webhooks` so we surface it as a label rather
//     than embedding it in the metric name.
//   - status: "2xx" | "4xx" | "5xx" | "timeout" | "network" |
//     "dropped_no_text"  (REQ-HOOK-EXTRACTED-03).
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

// RegisterProtojmapContactsMetrics registers the contacts-JMAP collector
// set; idempotent.
func RegisterProtojmapContactsMetrics() {
	protojmapContactsMetricsOnce.Do(func() {
		ProtojmapContactsMethodsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_protojmap_contacts_methods_total",
			Help: "JMAP Contacts method invocations, by method.",
		}, []string{"method"})
		ProtojmapContactsMethodErrorsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "herold_protojmap_contacts_method_errors_total",
			Help: "JMAP Contacts method failures, by method and error code.",
		}, []string{"method", "code"})
		MustRegister(
			ProtojmapContactsMethodsTotal,
			ProtojmapContactsMethodErrorsTotal,
		)
	})
}
