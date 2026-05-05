package observe

import (
	"context"
	"log/slog"
	"strings"
	"time"

	otellog "go.opentelemetry.io/otel/log"
)

// ClientEvent is the post-enrichment internal shape of a client-log event
// (REQ-OPS-203). It is produced by the ingest handler after validation,
// redaction, and enrichment, then handed to ClientEmitter.Emit.
//
// Fields map directly to the wire schema (REQ-OPS-202/207) enriched with
// server-side additions. Do NOT add wire-schema parsing here — that lives in
// internal/protoadmin.
type ClientEvent struct {
	// --- from wire ---

	// Kind is "error", "log", or "vital".
	Kind string
	// Level is the client-reported severity: "trace","debug","info","warn","error".
	// For Kind=="error" the effective level is always ERROR regardless of this field.
	Level string
	// Msg is the human-readable summary, already truncated and redacted.
	Msg string
	// Stack is the raw, unsymbolicated stack trace (Kind=="error" only).
	Stack string
	// ClientTS is the browser wall clock at capture.
	ClientTS time.Time
	// PageID is the per-page-load opaque identifier.
	PageID string
	// SessionID is the per-browser-session opaque identifier (empty for anonymous).
	SessionID string
	// App is "suite" or "admin".
	App string
	// BuildSHA is the SPA build identifier.
	BuildSHA string
	// Route is the current client route, e.g. "/mail/inbox".
	Route string
	// RequestID is the correlated server X-Request-Id when known (optional).
	RequestID string
	// UA is the User-Agent string, already capped.
	UA string

	// VitalName is the Web Vital metric name (e.g. "LCP", "FCP", "TTFB").
	// Non-empty only when Kind=="vital".
	VitalName string
	// VitalValue is the measured Web Vital value in milliseconds (or the unit
	// defined by the metric). Non-zero only when Kind=="vital".
	VitalValue float64
	// VitalID is the opaque attribution identifier for the vital measurement.
	// Non-empty only when Kind=="vital".
	VitalID string

	// --- enrichment (added server-side, REQ-OPS-203) ---

	// ServerRecvTS is the server wall clock at receipt.
	ServerRecvTS time.Time
	// ClockSkewMS is (ServerRecvTS - ClientTS) in milliseconds.
	ClockSkewMS int64
	// UserID is the authenticated principal ID (empty for anonymous).
	UserID string
	// Listener is "public" or "admin".
	Listener string
	// Endpoint is "auth" or "public".
	Endpoint string

	// Authenticated reports whether the event arrived on the authenticated
	// endpoint. Used by the activity mapper (REQ-OPS-204).
	Authenticated bool
}

// ClientEmitterConfig holds constructor parameters for ClientEmitter.
type ClientEmitterConfig struct {
	// Logger is the slog logger to write slog records into. Required.
	Logger *slog.Logger
	// LogProvider is the OTLP log provider. Required (may be a noop).
	LogProvider otellog.LoggerProvider
	// PublicOTLPEgress controls whether anonymous (endpoint=public) events
	// are forwarded to OTLP. Supplied by the caller from config
	// clientlog.public.otlp_egress (REQ-OPS-205). Default false.
	PublicOTLPEgress bool
}

// ClientEmitter re-emits an enriched ClientEvent into slog and OTLP
// (REQ-OPS-204, REQ-OPS-205). It is safe for concurrent use.
type ClientEmitter struct {
	log              *slog.Logger
	lp               otellog.LoggerProvider
	publicOTLPEgress bool
}

// NewClientEmitter constructs a ClientEmitter from cfg.
func NewClientEmitter(cfg ClientEmitterConfig) *ClientEmitter {
	return &ClientEmitter{
		log:              cfg.Logger,
		lp:               cfg.LogProvider,
		publicOTLPEgress: cfg.PublicOTLPEgress,
	}
}

// Emit fans event into (a) one slog record and (b) one OTLP log record when
// egress is enabled (REQ-OPS-204, REQ-OPS-205). Best-effort: a failure in
// OTLP emission does not block or surface an error to the caller.
func (e *ClientEmitter) Emit(ctx context.Context, ev ClientEvent) {
	e.emitSlog(ctx, ev)
	e.emitOTLP(ctx, ev)
}

// --- slog emission (REQ-OPS-204) ---

func (e *ClientEmitter) emitSlog(ctx context.Context, ev ClientEvent) {
	lvl := clientSlogLevel(ev)
	activity := clientActivity(ev)

	attrs := []slog.Attr{
		slog.String("source", "client"),
		slog.String("app", ev.App),
		slog.String("kind", ev.Kind),
		slog.String("listener", ev.Listener),
		slog.String("route", ev.Route),
		slog.String("build", ev.BuildSHA),
		slog.String("client_ts", ev.ClientTS.Format(time.RFC3339Nano)),
		slog.Int64("clock_skew_ms", ev.ClockSkewMS),
		slog.String("activity", activity),
	}
	if ev.SessionID != "" {
		attrs = append(attrs, slog.String("client_session", ev.SessionID))
	}
	if ev.RequestID != "" {
		attrs = append(attrs, slog.String("request_id", ev.RequestID))
	}
	if ev.Kind == "vital" && ev.VitalName != "" {
		attrs = append(attrs,
			slog.String("vital_name", ev.VitalName),
			slog.Float64("value", ev.VitalValue),
			slog.String("vital_id", ev.VitalID),
		)
	}

	e.log.LogAttrs(ctx, lvl, ev.Msg, attrs...)
}

// clientSlogLevel maps a ClientEvent to the slog.Level that should be used
// for the slog record (REQ-OPS-204). kind=error is always ERROR.
func clientSlogLevel(ev ClientEvent) slog.Level {
	if ev.Kind == "error" {
		return slog.LevelError
	}
	return parseSlogLevelFromClientLevel(ev.Level)
}

// parseSlogLevelFromClientLevel maps a client-reported level string to slog.Level.
func parseSlogLevelFromClientLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "trace":
		return LevelTrace
	case "debug":
		return slog.LevelDebug
	case "", "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// clientActivity maps a ClientEvent to the activity enum value (REQ-OPS-204).
//
//   - kind=error -> "audit"    (security-relevant: errors are seen by operators)
//   - kind=log, authenticated  -> "user"
//   - kind=vital               -> "internal"
//   - breadcrumb echo (kind="breadcrumb") -> "access"  (not in this emitter; handled upstream)
func clientActivity(ev ClientEvent) string {
	switch ev.Kind {
	case "error":
		return ActivityAudit
	case "vital":
		return ActivityInternal
	default:
		// kind=log from authenticated session -> user; otherwise internal
		if ev.Authenticated {
			return ActivityUser
		}
		return ActivityInternal
	}
}

// --- OTLP emission (REQ-OPS-205) ---

func (e *ClientEmitter) emitOTLP(ctx context.Context, ev ClientEvent) {
	// Gate: anonymous events are skipped unless publicOTLPEgress is enabled.
	if ev.Endpoint == "public" && !e.publicOTLPEgress {
		return
	}

	// service.name is per-event (herold-suite | herold-admin) so we obtain a
	// scoped logger keyed on app+buildSHA. This is cheap: the LoggerProvider
	// caches by instrumentation scope.
	serviceName := "herold-" + ev.App
	logger := LoggerForService(e.lp, serviceName, ev.BuildSHA)

	var rec otellog.Record
	rec.SetTimestamp(ev.ClientTS)
	rec.SetObservedTimestamp(ev.ServerRecvTS)
	rec.SetSeverity(clientOTLPSeverity(ev))
	rec.SetSeverityText(clientOTLPSeverityText(ev))
	rec.SetBody(otellog.StringValue(ev.Msg))

	// Per-record attributes (architecture §OTLP shape).
	kvs := []otellog.KeyValue{
		otellog.String("client.session_id", ev.SessionID),
		otellog.String("client.page_id", ev.PageID),
		otellog.String("client.route", ev.Route),
		otellog.String("client.ua", ev.UA),
		otellog.String("client.kind", ev.Kind),
		otellog.String("client.build_sha", ev.BuildSHA),
		otellog.String("client.client_ts", ev.ClientTS.Format(time.RFC3339Nano)),
		otellog.Int64("client.clock_skew_ms", ev.ClockSkewMS),
		otellog.String("client.endpoint", ev.Endpoint),
		otellog.String("client.listener", ev.Listener),
	}
	if ev.UserID != "" {
		kvs = append(kvs, otellog.String("user.id", ev.UserID))
	}
	if ev.RequestID != "" {
		kvs = append(kvs, otellog.String("request_id", ev.RequestID))
	}
	if ev.Kind == "error" {
		excType := parseExceptionType(ev.Msg)
		if excType != "" {
			kvs = append(kvs, otellog.String("exception.type", excType))
		}
		if ev.Stack != "" {
			kvs = append(kvs, otellog.String("exception.stacktrace", ev.Stack))
		}
	}
	if ev.Kind == "vital" && ev.VitalName != "" {
		kvs = append(kvs,
			otellog.String("vital.name", ev.VitalName),
			otellog.Float64("vital.value", ev.VitalValue),
			otellog.String("vital.id", ev.VitalID),
		)
	}
	rec.AddAttributes(kvs...)

	logger.Emit(ctx, rec)
}

// clientOTLPSeverity maps ClientEvent levels to OTLP severity numbers.
// kind=error is always SeverityError regardless of the input level.
func clientOTLPSeverity(ev ClientEvent) otellog.Severity {
	if ev.Kind == "error" {
		return otellog.SeverityError
	}
	switch strings.ToLower(ev.Level) {
	case "trace":
		return otellog.SeverityTrace
	case "debug":
		return otellog.SeverityDebug
	case "", "info":
		return otellog.SeverityInfo
	case "warn", "warning":
		return otellog.SeverityWarn
	case "error":
		return otellog.SeverityError
	default:
		return otellog.SeverityInfo
	}
}

// clientOTLPSeverityText returns the canonical severity text string.
func clientOTLPSeverityText(ev ClientEvent) string {
	if ev.Kind == "error" {
		return "ERROR"
	}
	switch strings.ToLower(ev.Level) {
	case "trace":
		return "TRACE"
	case "debug":
		return "DEBUG"
	case "", "info":
		return "INFO"
	case "warn", "warning":
		return "WARN"
	case "error":
		return "ERROR"
	default:
		return "INFO"
	}
}

// parseExceptionType extracts a best-effort exception type token from the
// message. It looks for common JavaScript/browser exception patterns:
// "TypeError: ...", "Error: ...", "RangeError: ...", etc.
// Returns empty string if no recognisable type is found.
func parseExceptionType(msg string) string {
	// Look for "SomeType: " at the start of the message.
	idx := strings.Index(msg, ":")
	if idx <= 0 {
		return ""
	}
	candidate := strings.TrimSpace(msg[:idx])
	// A type token must be all ASCII letters (no spaces, no digits).
	for _, c := range candidate {
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')) {
			return ""
		}
	}
	return candidate
}
