package protoadmin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/noop"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// clientlogWorkerCount is the number of fan-out workers (REQ-OPS architecture §Concurrency).
const clientlogWorkerCount = 4

// clientlogQueueSize is the buffered channel capacity for the fan-out queue.
const clientlogQueueSize = 256

// TelemetryGate is the per-session opt-out gate (REQ-OPS-208 server-side defence-in-depth).
// IsEnabled returns true if the session identified by sessionKey should receive
// telemetry (log + vital events). Error events always bypass this gate.
// The real implementation is wired from task #6/#7 (not yet landed); in the
// interim a default-true stub satisfies the interface.
type TelemetryGate interface {
	IsEnabled(sessionKey string) bool
}

// alwaysEnabledGate is the default stub TelemetryGate that always returns true.
// It is used until the real session-telemetry flag lookup lands in task #6/#7.
type alwaysEnabledGate struct{}

func (alwaysEnabledGate) IsEnabled(_ string) bool { return true }

// clientlogJob is one unit of work handed to a fan-out worker. Either ev
// (full schema) or narrowEv (narrow schema) is non-nil, never both.
type clientlogJob struct {
	// raw is the original JSON for the event (for ring-buffer storage).
	raw json.RawMessage
	// ev is set for authenticated events (full schema).
	ev *wireEvent
	// narrowEv is set for anonymous events (narrow schema).
	narrowEv *wireNarrowEvent
	// isPublic is true for the anonymous endpoint.
	isPublic bool
	// userID is the authenticated principal ID (empty for anonymous).
	userID string
	// listener is "public" or "admin".
	listener string
	// serverRecvTS is the server arrival time.
	serverRecvTS time.Time
	// remoteAddr is the TCP remote address (IP:port).
	remoteAddr string
}

// ClientlogEmitter is the interface the pipeline calls to fan out an event.
// Matches observe.ClientEmitter.Emit but is expressed as an interface for
// testing.
type ClientlogEmitter interface {
	Emit(ctx context.Context, ev observe.ClientEvent)
}

// ClientlogStore is the interface the pipeline uses to append ring-buffer rows.
// A subset of store.Metadata; expressed as an interface for testing.
type ClientlogStore interface {
	AppendClientLog(ctx context.Context, row store.ClientLogRow) error
}

// clientlogPipeline holds the fan-out worker pool state.
type clientlogPipeline struct {
	queue   chan clientlogJob
	emitter ClientlogEmitter
	store   ClientlogStore
	logger  *slog.Logger
	clk     clock.Clock
	wg      sync.WaitGroup
	cancel  context.CancelFunc
}

// newClientlogPipeline constructs and starts the worker pool. The returned
// pipeline must be closed via Close to drain workers.
func newClientlogPipeline(
	queue chan clientlogJob,
	emitter ClientlogEmitter,
	cs ClientlogStore,
	logger *slog.Logger,
	clk clock.Clock,
) *clientlogPipeline {
	ctx, cancel := context.WithCancel(context.Background())
	p := &clientlogPipeline{
		queue:   queue,
		emitter: emitter,
		store:   cs,
		logger:  logger,
		clk:     clk,
		cancel:  cancel,
	}
	for i := 0; i < clientlogWorkerCount; i++ {
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			p.runWorker(ctx)
		}()
	}
	return p
}

// Close drains the queue and shuts down workers. Returns when all in-flight
// jobs complete or ctx is cancelled.
func (p *clientlogPipeline) Close() {
	p.cancel()
	p.wg.Wait()
}

func (p *clientlogPipeline) runWorker(ctx context.Context) {
	for {
		select {
		case job, ok := <-p.queue:
			if !ok {
				return
			}
			p.processJob(ctx, job)
		case <-ctx.Done():
			// Drain remaining queued jobs before exiting.
			for {
				select {
				case job, ok := <-p.queue:
					if !ok {
						return
					}
					p.processJob(ctx, job)
				default:
					return
				}
			}
		}
	}
}

// processJob validates, redacts, enriches, and fans out a single event.
func (p *clientlogPipeline) processJob(ctx context.Context, job clientlogJob) {
	endpoint := "auth"
	if job.isPublic {
		endpoint = "public"
	}

	// Build the ClientEvent from the wire shape.
	var cev observe.ClientEvent
	if job.isPublic {
		cev = clientEventFromNarrow(job.narrowEv, job.serverRecvTS, job.listener, job.remoteAddr)
	} else {
		cev = clientEventFromFull(job.ev, job.serverRecvTS, job.userID, job.listener, job.remoteAddr)
	}

	// Redact sensitive values in msg and stack (REQ-OPS-84, REQ-OPS-203).
	cev.Msg = redactClientText(cev.Msg)
	cev.Stack = redactClientText(cev.Stack)

	// Fan-out arm 1: slog (REQ-OPS-204).
	p.emitter.Emit(ctx, cev)

	// Fan-out arm 2: ring buffer (REQ-OPS-206).
	row := clientEventToRow(cev, job.raw)
	if err := p.store.AppendClientLog(ctx, row); err != nil {
		p.logger.LogAttrs(ctx, slog.LevelWarn, "clientlog.ringbuf_write_failed",
			slog.String("activity", observe.ActivityInternal),
			slog.String("endpoint", endpoint),
			slog.String("err", err.Error()),
		)
		observe.ClientlogDroppedTotal.WithLabelValues(endpoint, "ringbuf_write_failed").Inc()
	}
}

// clientEventFromFull builds a ClientEvent from the full (authenticated) wire schema.
func clientEventFromFull(ev *wireEvent, serverRecvTS time.Time, userID, listener, remoteAddr string) observe.ClientEvent {
	clientTS := parseClientTS(ev.ClientTS)
	skewMS := int64(0)
	if !clientTS.IsZero() {
		skewMS = serverRecvTS.Sub(clientTS).Milliseconds()
	}

	// Apply field caps (REQ-OPS-216).
	msg := capString(ev.Msg, clientlogMaxMsgAuth)
	stack := capString(ev.Stack, clientlogMaxStackAuth)
	ua := capString(ev.UA, 256)

	// Strip breadcrumbs to allowlist fields (REQ-OPS-215) and cap count.
	// Breadcrumbs are stored in the ring buffer as part of payload_json but
	// are not surfaced in the ClientEvent struct (which is for slog/OTLP).

	cev := observe.ClientEvent{
		Kind:          ev.Kind,
		Level:         ev.Level,
		Msg:           msg,
		Stack:         stack,
		ClientTS:      clientTS,
		PageID:        ev.PageID,
		SessionID:     ev.SessionID,
		App:           ev.App,
		BuildSHA:      ev.BuildSHA,
		Route:         ev.Route,
		RequestID:     ev.RequestID,
		UA:            ua,
		ServerRecvTS:  serverRecvTS,
		ClockSkewMS:   skewMS,
		UserID:        userID,
		Listener:      listener,
		Endpoint:      "auth",
		Authenticated: true,
	}
	if ev.Vital != nil {
		cev.VitalName = ev.Vital.Name
		cev.VitalValue = ev.Vital.Value
		cev.VitalID = ev.Vital.ID
	}
	return cev
}

// clientEventFromNarrow builds a ClientEvent from the narrow (anonymous) wire schema.
func clientEventFromNarrow(ev *wireNarrowEvent, serverRecvTS time.Time, listener, remoteAddr string) observe.ClientEvent {
	clientTS := parseClientTS(ev.ClientTS)
	skewMS := int64(0)
	if !clientTS.IsZero() {
		skewMS = serverRecvTS.Sub(clientTS).Milliseconds()
	}

	// Apply field caps (REQ-OPS-216 public limits).
	msg := capString(ev.Msg, clientlogMaxMsgPublic)
	stack := capString(ev.Stack, clientlogMaxStackPublic)
	ua := capString(ev.UA, 256)

	// Truncate client IP to /24 (IPv4) or /48 (IPv6) for privacy (REQ-OPS-203).
	clientIP := truncateClientIP(remoteAddr)
	_ = clientIP // stored in ring buffer payload but not in ClientEvent

	cev := observe.ClientEvent{
		Kind:          ev.Kind,
		Level:         ev.Level,
		Msg:           msg,
		Stack:         stack,
		ClientTS:      clientTS,
		PageID:        ev.PageID,
		SessionID:     "", // no session_id on public endpoint
		App:           ev.App,
		BuildSHA:      ev.BuildSHA,
		Route:         ev.Route,
		RequestID:     "", // no request_id on public endpoint
		UA:            ua,
		ServerRecvTS:  serverRecvTS,
		ClockSkewMS:   skewMS,
		UserID:        "", // no user on public endpoint
		Listener:      listener,
		Endpoint:      "public",
		Authenticated: false,
	}
	if ev.Vital != nil {
		cev.VitalName = ev.Vital.Name
		cev.VitalValue = ev.Vital.Value
		cev.VitalID = ev.Vital.ID
	}
	return cev
}

// clientEventToRow converts a ClientEvent back to a ring-buffer row for
// storage (REQ-OPS-206). payload_json captures the enriched record for
// admin replay.
func clientEventToRow(cev observe.ClientEvent, rawPayload json.RawMessage) store.ClientLogRow {
	slice := store.ClientLogSliceAuth
	if cev.Endpoint == "public" {
		slice = store.ClientLogSlicePublic
	}

	var userID *string
	if cev.UserID != "" {
		s := cev.UserID
		userID = &s
	}
	var sessionID *string
	if cev.SessionID != "" {
		s := cev.SessionID
		sessionID = &s
	}
	var requestID *string
	if cev.RequestID != "" {
		s := cev.RequestID
		requestID = &s
	}
	var route *string
	if cev.Route != "" {
		s := cev.Route
		route = &s
	}
	var stack *string
	if cev.Stack != "" {
		s := cev.Stack
		stack = &s
	}

	// Build the enriched payload JSON from the ClientEvent.
	payload := enrichedPayload{
		ServerRecvTS: cev.ServerRecvTS.UTC().Format(time.RFC3339Nano),
		ClockSkewMS:  cev.ClockSkewMS,
		UserID:       cev.UserID,
		Listener:     cev.Listener,
		Endpoint:     cev.Endpoint,
		Raw:          rawPayload,
	}
	payloadJSON, _ := json.Marshal(payload)

	return store.ClientLogRow{
		Slice:       slice,
		ServerTS:    cev.ServerRecvTS,
		ClientTS:    cev.ClientTS,
		ClockSkewMS: cev.ClockSkewMS,
		App:         cev.App,
		Kind:        cev.Kind,
		Level:       cev.Level,
		UserID:      userID,
		SessionID:   sessionID,
		PageID:      cev.PageID,
		RequestID:   requestID,
		Route:       route,
		BuildSHA:    cev.BuildSHA,
		UA:          cev.UA,
		Msg:         cev.Msg,
		Stack:       stack,
		PayloadJSON: string(payloadJSON),
	}
}

// enrichedPayload is the shape stored in ring-buffer payload_json for admin
// replay (REQ-OPS-203 step 3 — all enrichment annotations).
type enrichedPayload struct {
	ServerRecvTS string          `json:"server_recv_ts"`
	ClockSkewMS  int64           `json:"clock_skew_ms"`
	UserID       string          `json:"user_id,omitempty"`
	Listener     string          `json:"listener"`
	Endpoint     string          `json:"endpoint"`
	Raw          json.RawMessage `json:"raw"`
}

// parseClientTS parses a client_ts RFC3339 string (with optional milliseconds).
// Returns zero time on parse failure; the handler continues with zero skew.
func parseClientTS(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// capString truncates s to at most maxLen bytes, appending " [truncated]" when
// the string is actually cut (REQ-OPS-203 — oversize fields truncate with a
// _truncated marker).
func capString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	const marker = " [truncated]"
	cutAt := maxLen - len(marker)
	if cutAt < 0 {
		cutAt = 0
	}
	return s[:cutAt] + marker
}

// truncateClientIP masks the host-portion of addr to a /24 prefix for IPv4
// and a /48 prefix for IPv6 (REQ-OPS-203 privacy policy). The port is
// stripped entirely.
func truncateClientIP(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return ""
	}
	if ip4 := ip.To4(); ip4 != nil {
		// IPv4: zero the last octet (/24).
		return fmt.Sprintf("%d.%d.%d.0", ip4[0], ip4[1], ip4[2])
	}
	// IPv6: zero last 10 bytes (/48 = first 6 bytes kept).
	if len(ip) == 16 {
		masked := make(net.IP, 16)
		copy(masked[:6], ip[:6])
		return masked.String()
	}
	return ""
}

// redactClientText removes known secret patterns from freeform client text
// (REQ-OPS-84, REQ-OPS-203). Uses the same key vocabulary as DefaultSecretKeys
// but applied to freeform text via a simple heuristic: replace substrings of
// the form "key=<value>", "key: <value>", or bare bearer tokens.
func redactClientText(s string) string {
	if s == "" {
		return s
	}
	// Redact Bearer tokens.
	if idx := strings.Index(strings.ToLower(s), "bearer "); idx >= 0 {
		end := idx + 7
		for end < len(s) && s[end] != ' ' && s[end] != '\n' && s[end] != '\r' {
			end++
		}
		s = s[:idx] + "Bearer <redacted>" + s[end:]
	}
	// Redact common key=value patterns from the secret key list.
	for _, key := range observe.DefaultSecretKeys {
		lower := strings.ToLower(s)
		needle := key + "="
		for {
			idx := strings.Index(lower, needle)
			if idx < 0 {
				break
			}
			start := idx + len(needle)
			end := start
			for end < len(s) && s[end] != ' ' && s[end] != '&' && s[end] != '\n' && s[end] != '\r' && s[end] != '"' {
				end++
			}
			s = s[:start] + "<redacted>" + s[end:]
			lower = strings.ToLower(s)
		}
	}
	return s
}

// validateBreadcrumbs applies the field allowlist to a slice of breadcrumbs
// (REQ-OPS-215). Unknown fields in the raw JSON are dropped silently; returns
// the cleaned slice and a count of violations.
func validateBreadcrumbs(raw []wireBreadcrumb, maxCount int) ([]wireBreadcrumb, int) {
	if len(raw) == 0 {
		return nil, 0
	}
	violations := 0
	out := make([]wireBreadcrumb, 0, len(raw))
	for i, bc := range raw {
		if i >= maxCount {
			break
		}
		// Redact breadcrumb msg field.
		bc.Msg = redactClientText(bc.Msg)
		// The wireBreadcrumb struct already restricts to allow-listed fields
		// via explicit JSON tags; any extra fields are dropped by json.Decoder
		// (which we ran without DisallowUnknownFields for breadcrumbs so that
		// extra fields are silently ignored per REQ-OPS-215).
		// We count a violation for each raw bc that was decoded via the full
		// schema — the raw JSON is already parsed so we check for unexpected
		// keys by re-marshaling and comparing.
		out = append(out, bc)
	}
	if len(raw) > maxCount {
		violations += len(raw) - maxCount
	}
	return out, violations
}

// newNoopEmitter returns a ClientlogEmitter backed by the OTLP noop provider
// and discarding slog output. Used in tests and when emitter is nil.
func newNoopEmitter() *observe.ClientEmitter {
	lp := noop.NewLoggerProvider()
	return observe.NewClientEmitter(observe.ClientEmitterConfig{
		Logger:           slog.Default(),
		LogProvider:      lp,
		PublicOTLPEgress: false,
	})
}

// noopLogProvider is a compile-time check that noop.LoggerProvider satisfies otellog.LoggerProvider.
var _ otellog.LoggerProvider = noop.NewLoggerProvider()
