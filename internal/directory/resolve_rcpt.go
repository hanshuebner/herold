package directory

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// ResolveRcptAction enumerates the four legal verdicts the plugin may
// return. The defer / synthesised-defer paths produced by the
// supervisor (timeout, transport error, breaker-open) collapse onto
// ResolveRcptDefer with code 4.4.3.
type ResolveRcptAction string

const (
	// ResolveRcptAccept is the 250 reply path.
	ResolveRcptAccept ResolveRcptAction = "accept"
	// ResolveRcptReject is the 5xx reply path.
	ResolveRcptReject ResolveRcptAction = "reject"
	// ResolveRcptDefer is the 4xx reply path.
	ResolveRcptDefer ResolveRcptAction = "defer"
	// ResolveRcptFallthrough hands back to the in-process resolution
	// chain (catch-all → 5.1.1).
	ResolveRcptFallthrough ResolveRcptAction = "fallthrough"
	// ResolveRcptRateLimited is a synthesised verdict the SMTP layer
	// maps to 4.7.1; not produced by the plugin.
	ResolveRcptRateLimited ResolveRcptAction = "ratelimited"
)

// ResolveRcptDecision is the resolver's per-RCPT verdict, ready for
// the SMTP layer to translate into a wire reply.
type ResolveRcptDecision struct {
	// Action governs the SMTP reply class.
	Action ResolveRcptAction
	// Code is the enhanced status the SMTP layer emits. Defaults
	// applied per REQ-DIR-RCPT-07 when the plugin returns nothing.
	Code string
	// Reason is the SMTP text payload.
	Reason string
	// PrincipalID is set on accept-with-principal; the SMTP layer must
	// resolve it to a principal row and downgrade to defer 4.3.0 on
	// miss (REQ-DIR-RCPT-08).
	PrincipalID *uint64
	// RouteTag is the opaque correlation token the plugin returned;
	// echoed into the synthetic-recipient hand-off and the audit log.
	RouteTag string
	// Synthetic is true when Action == accept and PrincipalID is nil.
	Synthetic bool
	// PluginName carries the plugin that produced the decision; "" for
	// internal-directory pre-empts and rate-limit refusals.
	PluginName string
	// Latency is the round-trip latency for plugin calls; zero for
	// synthesised verdicts.
	Latency time.Duration
}

// ResolveRcptInvoker is the supervisor surface ResolveRcpt consumes.
// It mirrors the (intentionally small) shape used by spam.PluginInvoker
// and acme.PluginInvoker so internal/plugin.Manager can satisfy it via
// a thin adapter. Tests substitute a fake.
type ResolveRcptInvoker interface {
	// InvokeResolveRcpt dispatches a directory.resolve_rcpt JSON-RPC
	// call to plugin. Implementations enforce the per-method timeout
	// (REQ-PLUG-32: 2s default, 5s hard cap) on top of any deadline
	// already on ctx; on overrun they MUST return an error wrapping
	// ErrResolveRcptTimeout. The same applies to plugin-disabled and
	// transport-error conditions, which return errors wrapping the
	// matching sentinel below so the resolver can attribute outcomes
	// to the right metric bucket.
	InvokeResolveRcpt(ctx context.Context, plugin string, req ResolveRcptRequest) (ResolveRcptResponse, error)
}

// Sentinel errors the invoker uses so the resolver can distinguish
// timeout / transport / disabled-plugin paths and attribute outcomes
// to the correct metric.
var (
	// ErrResolveRcptTimeout is returned when the plugin call exceeded
	// its per-method deadline. The resolver maps it to defer 4.4.3
	// (REQ-DIR-RCPT-04) and records herold_directory_resolve_rcpt_timeouts_total.
	ErrResolveRcptTimeout = errors.New("directory: resolve_rcpt timed out")
	// ErrResolveRcptUnavailable is returned for plugin-disabled,
	// breaker-open, transport-error, or any other transient supervisor
	// condition. The resolver maps it to defer 4.4.3.
	ErrResolveRcptUnavailable = errors.New("directory: resolve_rcpt unavailable")
)

// ResolveRcptRequest re-exports the SDK shape so callers do not have
// to import plugins/sdk for the common case. Field tags match the
// JSON-RPC wire format.
type ResolveRcptRequest struct {
	Envelope  ResolveRcptEnvelope `json:"envelope"`
	Recipient string              `json:"recipient"`
	Context   ResolveRcptContext  `json:"context"`
}

// ResolveRcptEnvelope is the in-flight envelope state passed to the
// plugin at RCPT TO time.
type ResolveRcptEnvelope struct {
	MailFrom    string `json:"mail_from"`
	HeloDomain  string `json:"helo_domain,omitempty"`
	SourceIP    string `json:"source_ip"`
	Listener    string `json:"listener"`
	AuthResults string `json:"auth_results,omitempty"`
}

// ResolveRcptContext is the opaque correlation context the supervisor
// hands to the plugin.
type ResolveRcptContext struct {
	PluginName string `json:"plugin_name"`
	RequestID  string `json:"request_id"`
}

// ResolveRcptResponse is the plugin's per-RCPT verdict.
type ResolveRcptResponse struct {
	Action      string  `json:"action"`
	Reason      string  `json:"reason,omitempty"`
	Code        string  `json:"code,omitempty"`
	PrincipalID *uint64 `json:"principal_id,omitempty"`
	RouteTag    string  `json:"route_tag,omitempty"`
}

// RcptResolver is the per-server orchestrator that runs the
// directory.resolve_rcpt path with breaker, rate limit, audit, and
// metrics applied. Construct one via NewRcptResolver and reuse across
// all RCPT TOs.
type RcptResolver struct {
	invoker  ResolveRcptInvoker
	breakers *ResolveRcptBreakers
	rl       *ResolveRcptRateLimiter
	clk      clock.Clock
	logger   *slog.Logger
	store    store.Metadata
}

// RcptResolverConfig bundles the per-server dependencies. invoker may
// be nil during construction order for tests that wire it later via
// SetInvoker; in production the manager supplies it before the SMTP
// listener accepts a connection.
type RcptResolverConfig struct {
	Invoker  ResolveRcptInvoker
	Breakers *ResolveRcptBreakers
	Limiter  *ResolveRcptRateLimiter
	Clock    clock.Clock
	Logger   *slog.Logger
	// Metadata is the store handle the resolver writes audit entries
	// against. Required.
	Metadata store.Metadata
}

// NewRcptResolver constructs a resolver. Any nil sub-component is
// replaced with the conservative default: an empty breaker registry,
// a default rate limiter, real clock, slog.Default. Metadata is
// required and may not be nil.
func NewRcptResolver(cfg RcptResolverConfig) (*RcptResolver, error) {
	if cfg.Metadata == nil {
		return nil, errors.New("directory: NewRcptResolver: nil Metadata")
	}
	if cfg.Clock == nil {
		cfg.Clock = clock.NewReal()
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Breakers == nil {
		cfg.Breakers = NewResolveRcptBreakers(cfg.Clock)
	}
	if cfg.Limiter == nil {
		cfg.Limiter = NewResolveRcptRateLimiter(cfg.Clock, 0)
	}
	observe.RegisterDirectoryRcptMetrics()
	return &RcptResolver{
		invoker:  cfg.Invoker,
		breakers: cfg.Breakers,
		rl:       cfg.Limiter,
		clk:      cfg.Clock,
		logger:   cfg.Logger,
		store:    cfg.Metadata,
	}, nil
}

// SetInvoker installs (or swaps) the plugin invoker. Goroutine-safe
// only at startup; wire it before the SMTP listener accepts.
func (r *RcptResolver) SetInvoker(inv ResolveRcptInvoker) { r.invoker = inv }

// Resolve runs the resolve_rcpt path for one recipient. plugin is the
// configured plugin name; an empty plugin yields ResolveRcptFallthrough
// without invoking anything. The decision is the verdict the SMTP layer
// turns into a wire reply.
func (r *RcptResolver) Resolve(ctx context.Context, plugin string, req ResolveRcptRequest) ResolveRcptDecision {
	if plugin == "" {
		return ResolveRcptDecision{Action: ResolveRcptFallthrough}
	}
	// REQ-DIR-RCPT-06: per-source-IP rate limit BEFORE the breaker
	// check; rate-limited RCPTs do not contribute to breaker error
	// rates.
	if !r.rl.Allow(req.Envelope.SourceIP) {
		dec := ResolveRcptDecision{
			Action:     ResolveRcptRateLimited,
			Code:       "4.7.1",
			Reason:     "try again later",
			PluginName: plugin,
		}
		if observe.DirectoryResolveRcptTotal != nil {
			observe.DirectoryResolveRcptTotal.WithLabelValues(plugin, "ratelimited").Inc()
		}
		r.audit(ctx, plugin, req, dec, 0)
		return dec
	}
	// REQ-DIR-RCPT-05: open breaker short-circuits with defer 4.4.3.
	br := r.breakers.Get(plugin)
	if !br.allow() {
		dec := ResolveRcptDecision{
			Action:     ResolveRcptDefer,
			Code:       "4.4.3",
			Reason:     "directory unreachable",
			PluginName: plugin,
		}
		if observe.DirectoryResolveRcptTotal != nil {
			observe.DirectoryResolveRcptTotal.WithLabelValues(plugin, "breaker_open").Inc()
		}
		r.audit(ctx, plugin, req, dec, 0)
		return dec
	}
	if r.invoker == nil {
		// Treat as transport error: no invoker wired up.
		br.record(true)
		dec := ResolveRcptDecision{
			Action:     ResolveRcptDefer,
			Code:       "4.4.3",
			Reason:     "directory unreachable",
			PluginName: plugin,
		}
		if observe.DirectoryResolveRcptTotal != nil {
			observe.DirectoryResolveRcptTotal.WithLabelValues(plugin, "defer").Inc()
		}
		r.audit(ctx, plugin, req, dec, 0)
		return dec
	}
	start := r.clk.Now()
	resp, err := r.invoker.InvokeResolveRcpt(ctx, plugin, req)
	latency := r.clk.Now().Sub(start)
	if observe.DirectoryResolveRcptLatencySeconds != nil {
		observe.DirectoryResolveRcptLatencySeconds.WithLabelValues(plugin).Observe(latency.Seconds())
	}
	if err != nil {
		br.record(true)
		isTimeout := errors.Is(err, ErrResolveRcptTimeout)
		if isTimeout && observe.DirectoryResolveRcptTimeoutsTotal != nil {
			observe.DirectoryResolveRcptTimeoutsTotal.WithLabelValues(plugin).Inc()
		}
		r.logger.WarnContext(ctx, "directory.resolve_rcpt failed",
			slog.String("plugin", plugin),
			slog.String("recipient", req.Recipient),
			slog.String("err", err.Error()),
			slog.Bool("timeout", isTimeout))
		dec := ResolveRcptDecision{
			Action:     ResolveRcptDefer,
			Code:       "4.4.3",
			Reason:     "directory unreachable",
			PluginName: plugin,
			Latency:    latency,
		}
		if observe.DirectoryResolveRcptTotal != nil {
			observe.DirectoryResolveRcptTotal.WithLabelValues(plugin, "defer").Inc()
		}
		r.audit(ctx, plugin, req, dec, latency)
		return dec
	}
	// Successful round-trip; classify the action.
	br.record(false)
	dec := r.classify(plugin, resp, latency)
	if observe.DirectoryResolveRcptTotal != nil {
		observe.DirectoryResolveRcptTotal.WithLabelValues(plugin, string(dec.Action)).Inc()
	}
	if dec.Synthetic && observe.DirectorySyntheticAcceptedTotal != nil {
		observe.DirectorySyntheticAcceptedTotal.WithLabelValues(plugin).Inc()
	}
	r.audit(ctx, plugin, req, dec, latency)
	return dec
}

// classify maps a successful plugin response onto a ResolveRcptDecision,
// applying the REQ-DIR-RCPT-07 defaults when fields are omitted.
func (r *RcptResolver) classify(plugin string, resp ResolveRcptResponse, latency time.Duration) ResolveRcptDecision {
	dec := ResolveRcptDecision{
		PluginName:  plugin,
		Latency:     latency,
		PrincipalID: resp.PrincipalID,
		RouteTag:    resp.RouteTag,
		Reason:      resp.Reason,
		Code:        resp.Code,
	}
	switch strings.ToLower(strings.TrimSpace(resp.Action)) {
	case "accept":
		dec.Action = ResolveRcptAccept
		dec.Synthetic = resp.PrincipalID == nil
	case "reject":
		dec.Action = ResolveRcptReject
		if dec.Code == "" || !strings.HasPrefix(dec.Code, "5.") {
			dec.Code = "5.1.1"
		}
		if dec.Reason == "" {
			dec.Reason = "no such recipient"
		}
	case "defer":
		dec.Action = ResolveRcptDefer
		if dec.Code == "" || !strings.HasPrefix(dec.Code, "4.") {
			dec.Code = "4.5.1"
		}
		if dec.Reason == "" {
			dec.Reason = "try again later"
		}
	case "fallthrough", "":
		dec.Action = ResolveRcptFallthrough
	default:
		// Unknown action — be conservative: defer 4.5.1 so the message
		// retries rather than silently accepting an unparseable verdict.
		dec.Action = ResolveRcptDefer
		dec.Code = "4.5.1"
		if dec.Reason == "" {
			dec.Reason = fmt.Sprintf("plugin returned unrecognised action %q", resp.Action)
		}
	}
	return dec
}

// audit appends one row to the audit log per REQ-DIR-RCPT-09. The
// row's metadata carries the structured fields the operator filters
// on (recipient, action, code, latency_ms, route_tag); the SMTP
// session's connection-level log line carries the rcpt_decision_source
// tag separately.
func (r *RcptResolver) audit(ctx context.Context, plugin string, req ResolveRcptRequest, dec ResolveRcptDecision, latency time.Duration) {
	if r.store == nil {
		return
	}
	meta := map[string]string{
		"plugin":     plugin,
		"recipient":  req.Recipient,
		"action":     string(dec.Action),
		"latency_ms": fmt.Sprintf("%d", latency.Milliseconds()),
	}
	if dec.Code != "" {
		meta["code"] = dec.Code
	}
	if dec.RouteTag != "" {
		meta["route_tag"] = dec.RouteTag
	}
	if dec.PrincipalID != nil {
		meta["principal_id"] = fmt.Sprintf("%d", *dec.PrincipalID)
	}
	outcome := store.OutcomeSuccess
	if dec.Action == ResolveRcptDefer || dec.Action == ResolveRcptReject || dec.Action == ResolveRcptRateLimited {
		outcome = store.OutcomeFailure
	}
	entry := store.AuditLogEntry{
		At:         r.clk.Now(),
		ActorKind:  store.ActorSystem,
		ActorID:    "smtp",
		Action:     "smtp.rcpt.resolve",
		Subject:    "rcpt:" + req.Recipient,
		RemoteAddr: req.Envelope.SourceIP,
		Outcome:    outcome,
		Message:    fmt.Sprintf("plugin=%s action=%s code=%s", plugin, dec.Action, dec.Code),
		Metadata:   meta,
	}
	if err := r.store.AppendAuditLog(ctx, entry); err != nil {
		// Audit failure must not break the SMTP path; log at WARN so
		// the operator notices a silently-dropping audit pipeline.
		r.logger.WarnContext(ctx, "directory.resolve_rcpt audit append failed",
			slog.String("plugin", plugin),
			slog.String("recipient", req.Recipient),
			slog.String("err", err.Error()))
	}
}
