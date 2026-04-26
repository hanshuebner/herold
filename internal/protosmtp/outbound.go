package protosmtp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/sysconfig"
)

// Client is the outbound SMTP client — Phase 2's queue.Deliverer. One
// Client is shared across all worker goroutines; Deliver is safe for
// concurrent use because every call builds its own dial / session state
// on the stack and the only shared mutable surface (the MTA-STS cache)
// is provided by the operator and is itself documented as concurrency-safe.
//
// The Client does not own the message bytes, the queue row, or any
// retry / DSN policy: those live in package queue. It also does not
// DKIM-sign; the queue's Signer runs before bytes reach Deliver.
type Client struct {
	hostName       string
	resolver       mailauth.Resolver
	tlsaResolver   mailauth.TLSAResolver
	log            *slog.Logger
	clk            clock.Clock
	dialTimeout    time.Duration
	sessionTimeout time.Duration
	stsCache       MTASTSCache
	dane           bool
	tlsRPT         TLSRPTReporter
	dialer         *net.Dialer
	// dialFunc is overridable for tests; nil means use dialer.DialContext.
	dialFunc func(ctx context.Context, network, address string) (net.Conn, error)

	// smartHost carries the operator's [smart_host] block. When
	// smartHost.Enabled is false (the default) Deliver uses the
	// direct-MX path unchanged. Per-domain overrides ride on
	// smartHost.PerDomain.
	smartHost sysconfig.SmartHostConfig
	// passwordResolver is invoked at delivery time to materialise the
	// SASL secret from env / file. Nil when smartHost.AuthMethod is
	// "none" or smart-host is disabled.
	passwordResolver func() (string, error)
	// smartHostFailureMu / smartHostFailureSince guard the in-process
	// sustained-outage timer used by FallbackPolicy =
	// "smart_host_then_mx". Keyed by upstream "host:port" so different
	// per-domain relays do not steal each other's outage budget.
	smartHostFailureMu    sync.Mutex
	smartHostFailureSince map[string]time.Time
}

// ClientOptions configures a Client. Required fields: HostName, Resolver.
// Logger and Clock fall back to slog.Default / clock.NewReal when nil.
type ClientOptions struct {
	// HostName is the EHLO / HELO name the Client presents to remote MXs.
	// Should be the operator's outbound hostname (matching forward + reverse
	// DNS). Required.
	HostName string
	// Resolver is the DNS abstraction. Required. The Client never calls
	// net.Lookup* directly; tests inject a fakedns adapter satisfying
	// mailauth.Resolver.
	Resolver mailauth.Resolver
	// TLSAResolver is the optional DANE TLSA lookup surface. When nil
	// or absent, DANE is not attempted regardless of the DANE flag
	// (RFC 7672 requires DNSSEC validation, which the stdlib resolver
	// cannot supply). The Client probes Resolver for the TLSAResolver
	// interface when this field is nil.
	TLSAResolver mailauth.TLSAResolver
	// Logger receives structured per-attempt records. Defaults to
	// slog.Default.
	Logger *slog.Logger
	// Clock is the time source used for AttemptedAt and TLS-RPT
	// timestamps. Defaults to clock.NewReal.
	Clock clock.Clock
	// DialTimeout caps each TCP dial. Default 30s.
	DialTimeout time.Duration
	// SessionTimeout caps the entire SMTP exchange after the dial
	// succeeds. Default 5 minutes.
	SessionTimeout time.Duration
	// MTASTSCache is the policy cache the Client consults per recipient
	// domain. Optional; when nil MTA-STS is skipped (opportunistic TLS
	// only, with policy precedence collapsed to DANE > opportunistic).
	MTASTSCache MTASTSCache
	// DANE toggles RFC 7672 enforcement. Default true. When TLSA records
	// are absent or DNSSEC validation is unavailable, DANE silently does
	// not apply; only TLSA-required-but-validation-failed counts as a
	// hard fail.
	DANE bool
	// TLSRPTReporter receives per-failure TLS-RPT entries. Optional.
	TLSRPTReporter TLSRPTReporter
	// DialFunc, when non-nil, replaces the default TCP dialer. The
	// address passed is "host:25" with host as the resolved MX name.
	// Production callers leave this nil; tests inject a router that
	// maps MX hostnames to in-process listeners.
	DialFunc func(ctx context.Context, network, address string) (net.Conn, error)
	// SmartHost is the operator's [smart_host] block (REQ-FLOW-
	// SMARTHOST-01..08). Defaults to the disabled zero shape, in
	// which case Deliver uses the direct-MX path unchanged. When
	// SmartHost.Enabled is true (or when a per-domain override
	// applies for the recipient), Deliver routes through the
	// configured submission endpoint.
	SmartHost sysconfig.SmartHostConfig
	// PasswordResolver, when non-nil, is invoked at delivery time to
	// materialise the smart-host SASL secret. Production callers
	// build this from sysconfig.ResolveSecret over SmartHost.PasswordEnv
	// / SmartHost.PasswordFile so that secrets do not live on the
	// Client struct. Required when SmartHost.AuthMethod != "none".
	PasswordResolver func() (string, error)
}

// NewClient constructs a Client with the supplied options applied.
// Panics on missing required fields are intentional: the Client is wired
// once at process start, and a misconfiguration there is a programmer
// error rather than a recoverable runtime condition.
func NewClient(opts ClientOptions) *Client {
	if opts.HostName == "" {
		panic("protosmtp: ClientOptions.HostName required")
	}
	if opts.Resolver == nil {
		panic("protosmtp: ClientOptions.Resolver required")
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	clk := opts.Clock
	if clk == nil {
		clk = clock.NewReal()
	}
	dialT := opts.DialTimeout
	if dialT <= 0 {
		dialT = 30 * time.Second
	}
	sessT := opts.SessionTimeout
	if sessT <= 0 {
		sessT = 5 * time.Minute
	}
	tlsaR := opts.TLSAResolver
	if tlsaR == nil {
		// Probe the Resolver: if it also satisfies TLSAResolver, use
		// it. Otherwise leave nil — DANE is then a no-op.
		if r, ok := opts.Resolver.(mailauth.TLSAResolver); ok {
			tlsaR = r
		}
	}
	observe.RegisterSMTPOutboundMetrics()
	return &Client{
		hostName:              opts.HostName,
		resolver:              opts.Resolver,
		tlsaResolver:          tlsaR,
		log:                   log,
		clk:                   clk,
		dialTimeout:           dialT,
		sessionTimeout:        sessT,
		stsCache:              opts.MTASTSCache,
		dane:                  opts.DANE,
		tlsRPT:                opts.TLSRPTReporter,
		dialer:                &net.Dialer{Timeout: dialT},
		dialFunc:              opts.DialFunc,
		smartHost:             opts.SmartHost,
		passwordResolver:      opts.PasswordResolver,
		smartHostFailureSince: make(map[string]time.Time),
	}
}

// Deliver implements queue.Deliverer for one recipient. The contract is:
//
//   - On a 2xx final reply after DATA, Deliver returns Outcome{Status:
//     DeliverySuccess} and a nil error.
//   - On a 4xx reply or a transient network / DNS / TLS failure on every
//     candidate MX, Deliver returns Outcome{Status: DeliveryTransient}
//     with a nil error; the queue reschedules.
//   - On a 5xx reply or a hard policy gate (MTA-STS enforce mismatch,
//     DANE TLSA mismatch, REQUIRETLS unsatisfied), Deliver returns
//     Outcome{Status: DeliveryPermanent} with a nil error; the queue
//     emits a failure DSN.
//
// A non-nil error is reserved for programmer-bug surfaces (nil context,
// invalid request shape) — never used for protocol outcomes.
func (c *Client) Deliver(ctx context.Context, req DeliveryRequest) (DeliveryOutcome, error) {
	if ctx == nil {
		return DeliveryOutcome{}, errors.New("protosmtp: nil context")
	}
	if req.RcptTo == "" {
		return DeliveryOutcome{}, errors.New("protosmtp: DeliveryRequest.RcptTo required")
	}
	if len(req.Message) == 0 {
		return DeliveryOutcome{}, errors.New("protosmtp: DeliveryRequest.Message required")
	}
	startedAt := c.clk.Now()
	domain, ok := domainOfAddress(req.RcptTo)
	if !ok {
		return DeliveryOutcome{
			Status:      DeliveryPermanent,
			SMTPCode:    550,
			Diagnostic:  fmt.Sprintf("invalid recipient address %q", req.RcptTo),
			AttemptedAt: startedAt,
		}, nil
	}

	// REQ-FLOW-SMARTHOST-01..06: route through the smart host when
	// the operator has wired one and the recipient domain matches.
	// Per-domain overrides take precedence over the global block; an
	// override is treated as Enabled=true even if the global block is
	// disabled (operator intent: "this domain ALWAYS rides the
	// override relay").
	if effective, override, hasSmartHost := c.effectiveSmartHost(domain); hasSmartHost {
		_ = override
		out := c.dispatchSmartHost(ctx, req, domain, effective, startedAt)
		return out, nil
	}

	// MTA-STS lookup is per-domain, not per-MX, so we resolve it once.
	var policy *MTASTSPolicy
	if c.stsCache != nil {
		p, perr := c.stsCache.Lookup(ctx, domain)
		if perr != nil {
			c.log.WarnContext(ctx, "outbound: mta-sts lookup failed; treating as no-policy",
				slog.String("domain", domain), slog.String("err", perr.Error()))
		} else {
			policy = p
		}
	}

	// Resolve MX candidates.
	candidates, mxErr := c.resolveMX(ctx, domain)
	if mxErr != nil {
		c.log.WarnContext(ctx, "outbound: mx resolution failed",
			slog.String("domain", domain), slog.String("err", mxErr.Error()))
		// Treat permanent / NXDOMAIN-shaped errors as Permanent; everything
		// else as Transient. mailauth.IsTemporary captures the temporary
		// shapes the stdlib + our adapters expose.
		if mailauth.IsTemporary(mxErr) {
			return DeliveryOutcome{
				Status:      DeliveryTransient,
				Diagnostic:  fmt.Sprintf("mx resolution: %s", mxErr.Error()),
				AttemptedAt: startedAt,
			}, nil
		}
		if errors.Is(mxErr, mailauth.ErrNoRecords) || strings.Contains(mxErr.Error(), "no such host") {
			return DeliveryOutcome{
				Status:      DeliveryPermanent,
				SMTPCode:    550,
				Diagnostic:  fmt.Sprintf("no MX or A/AAAA for %s", domain),
				AttemptedAt: startedAt,
			}, nil
		}
		return DeliveryOutcome{
			Status:      DeliveryTransient,
			Diagnostic:  fmt.Sprintf("mx resolution: %s", mxErr.Error()),
			AttemptedAt: startedAt,
		}, nil
	}

	// Iterate candidates in preference order.
	var lastOutcome DeliveryOutcome
	lastOutcome.AttemptedAt = startedAt
	for i, cand := range candidates {
		// MTA-STS enforce gate before we even dial: if the policy is in
		// enforce mode and the candidate is not in the MX whitelist,
		// skip it (and on the last candidate report Permanent).
		if policy != nil && policy.Mode == MTASTSModeEnforce && !policy.Match(cand.host) {
			c.log.InfoContext(ctx, "outbound: mta-sts enforce skip",
				slog.String("domain", domain),
				slog.String("mx", cand.host))
			c.recordTLSRPT(ctx, domain, cand.host, store.TLSRPTFailureMTASTS,
				"mta-sts-mx-mismatch", fmt.Sprintf("MX %s not in MTA-STS policy", cand.host))
			lastOutcome = DeliveryOutcome{
				Status:       DeliveryPermanent,
				SMTPCode:     550,
				EnhancedCode: "5.7.1",
				Diagnostic:   fmt.Sprintf("MX %s not in MTA-STS enforce policy for %s", cand.host, domain),
				MXHost:       cand.host,
				AttemptedAt:  startedAt,
			}
			continue
		}

		outcome := c.deliverToMX(ctx, req, domain, cand, policy, startedAt)
		observe.SMTPOutboundTotal.WithLabelValues("direct_mx", outboundOutcomeLabel(outcome)).Inc()
		switch outcome.Status {
		case DeliverySuccess, DeliveryPermanent:
			return outcome, nil
		case DeliveryTransient:
			lastOutcome = outcome
			c.log.InfoContext(ctx, "outbound: candidate transient; trying next",
				slog.String("domain", domain),
				slog.String("mx", cand.host),
				slog.Int("smtp_code", outcome.SMTPCode),
				slog.String("diag", outcome.Diagnostic),
				slog.Int("candidate_index", i))
			continue
		}
	}
	if lastOutcome.Status == DeliveryUnknown {
		lastOutcome.Status = DeliveryTransient
		lastOutcome.Diagnostic = "no MX candidates produced an outcome"
	}
	return lastOutcome, nil
}

// recordTLSRPT calls the reporter when one is configured AND the
// recipient domain publishes _smtp._tls TXT. Errors from the reporter
// are logged but never propagate; the rule is that a TLS-RPT plumbing
// failure must not break delivery decisions.
func (c *Client) recordTLSRPT(ctx context.Context, domain, mxHost string, ft store.TLSRPTFailureType, code, detail string) {
	if c.tlsRPT == nil {
		return
	}
	if !c.domainPublishesTLSRPT(ctx, domain) {
		return
	}
	f := store.TLSRPTFailure{
		RecordedAt:           c.clk.Now(),
		PolicyDomain:         domain,
		ReceivingMTAHostname: mxHost,
		FailureType:          ft,
		FailureCode:          code,
		FailureDetailJSON:    fmt.Sprintf(`{"detail":%q}`, detail),
	}
	if err := c.tlsRPT.Append(ctx, f); err != nil {
		c.log.WarnContext(ctx, "outbound: tls-rpt append failed",
			slog.String("domain", domain),
			slog.String("err", err.Error()))
	}
}

// domainPublishesTLSRPT looks up _smtp._tls.<domain> and returns true if
// any TXT record begins with "v=TLSRPTv1". Misses (NXDOMAIN-shaped) and
// transient errors both fall through to false; we only emit when the
// signal is unambiguous.
func (c *Client) domainPublishesTLSRPT(ctx context.Context, domain string) bool {
	txts, err := c.resolver.TXTLookup(ctx, "_smtp._tls."+domain)
	if err != nil {
		return false
	}
	for _, t := range txts {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(t)), "v=tlsrptv1") {
			return true
		}
	}
	return false
}

// domainOfAddress extracts the domain part of an email address. Empty
// addresses (the null reverse-path) and malformed inputs return ok=false.
func domainOfAddress(addr string) (string, bool) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", false
	}
	at := strings.LastIndex(addr, "@")
	if at <= 0 || at == len(addr)-1 {
		return "", false
	}
	return strings.ToLower(addr[at+1:]), true
}

// canonicaliseHost lower-cases host and trims a trailing dot. Used for
// MTA-STS pattern matching.
func canonicaliseHost(h string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
}

// mtaSTSMatch implements the RFC 8461 §4.1 wildcard rule: a "*."-prefixed
// pattern matches one DNS label at the corresponding position; otherwise
// patterns are matched literally.
func mtaSTSMatch(pattern, host string) bool {
	if pattern == "" {
		return false
	}
	if !strings.HasPrefix(pattern, "*.") {
		return pattern == host
	}
	suffix := pattern[1:] // ".example.net"
	if !strings.HasSuffix(host, suffix) {
		return false
	}
	prefix := host[:len(host)-len(suffix)]
	if prefix == "" {
		return false
	}
	// "*." matches exactly one label — no further dots allowed.
	return !strings.Contains(prefix, ".")
}

// staticMTASTSCache is a trivial MTASTSCache used in tests and as a sane
// no-op default. It keys policies by domain via sync.Map and never expires
// them on its own (callers re-Set when policies change). Production
// implementations live in the autodns / queue agents and consult HTTPS.
type staticMTASTSCache struct {
	m sync.Map
}

// NewStaticMTASTSCache returns an in-memory MTASTSCache pre-loaded with
// the given policies. It exists primarily to make tests and small-scale
// deployments self-contained; large operators inject a cache backed by
// the autodns subsystem instead.
func NewStaticMTASTSCache(policies map[string]*MTASTSPolicy) MTASTSCache {
	c := &staticMTASTSCache{}
	for d, p := range policies {
		c.m.Store(canonicaliseHost(d), p)
	}
	return c
}

// Lookup implements MTASTSCache.
func (c *staticMTASTSCache) Lookup(_ context.Context, domain string) (*MTASTSPolicy, error) {
	v, ok := c.m.Load(canonicaliseHost(domain))
	if !ok {
		return nil, nil
	}
	return v.(*MTASTSPolicy), nil
}

// Set installs or replaces a policy entry. Exposed on the concrete type;
// callers needing mutability hold a *staticMTASTSCache directly.
func (c *staticMTASTSCache) Set(domain string, p *MTASTSPolicy) {
	c.m.Store(canonicaliseHost(domain), p)
}
