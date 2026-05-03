package protoadmin

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hanshuebner/herold/internal/authsession"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
	heroldtls "github.com/hanshuebner/herold/internal/tls"
)

// ListenerMode selects the TLS policy of a listener. Implicit wraps the
// accepted socket in TLS immediately; Plain serves HTTP and is only
// sensible behind a trusted reverse proxy or on a loopback socket during
// bootstrap.
type ListenerMode int

const (
	// ListenerModePlain serves HTTP directly (no TLS). Use only for
	// loopback bootstrap or behind a trusted reverse proxy.
	ListenerModePlain ListenerMode = iota
	// ListenerModeImplicit wraps the listener in TLS using the
	// configured TLS store (HTTPS).
	ListenerModeImplicit
)

// Routing decision. The ticket calls for go-chi/chi/v5. We use the Go
// 1.22+ stdlib ServeMux instead: it supports method routing and path
// parameters via the same {name} syntax chi uses, which keeps our
// go.mod free of an additional direct dependency (STANDARDS.md §3 —
// prefer stdlib over third-party). The two are API-compatible for the
// routing surface we need (GET / POST / PATCH / DELETE with path
// parameters and path prefix matching); if future requirements demand
// chi-specific middleware composition we can introduce it then under
// the same handler type.

// APIKeyLookup resolves a presented API key hash to its stored row.
// Returns store.ErrNotFound when the hash is unknown. Server.Options
// supplies a default that calls store.Metadata.GetAPIKeyByHash; tests
// override it to inject fakes.
type APIKeyLookup func(ctx context.Context, hash string) (store.APIKey, error)

// CertRenewer renews an ACME certificate for hostname synchronously.
// Implementations typically call acme.Client.EnsureCert. Nil disables
// the /api/v1/certs/{hostname}/renew endpoint (returns 501).
type CertRenewer interface {
	RenewCert(ctx context.Context, hostname string) error
}

// DNSVerifier reports drift between herold-published DNS records and
// the live state for domain. Nil disables /api/v1/diag/dns-check/...
// (returns 501).
type DNSVerifier interface {
	VerifyDomain(ctx context.Context, domain string) (DNSVerifyReport, error)
}

// DNSVerifyReport is the wire-form structured output of DNSVerifier.
// Mirrors autodns.VerifyReport without importing autodns.
type DNSVerifyReport struct {
	Domain  string            `json:"domain"`
	OK      bool              `json:"ok"`
	Records []DNSVerifyRecord `json:"records"`
}

// DNSVerifyRecord is one reconciliation row in a DNSVerifyReport.
type DNSVerifyRecord struct {
	Name     string `json:"name"`
	Expected string `json:"expected"`
	Actual   string `json:"actual,omitempty"`
	State    string `json:"state"` // "match" | "drift" | "missing"
}

// SpamPolicy is the active spam policy snapshot. The store holds it
// in-memory (no schema yet); persistence lands in a later phase.
type SpamPolicy struct {
	PluginName string  `json:"plugin_name"`
	Threshold  float64 `json:"threshold"`
	Model      string  `json:"model,omitempty"`
	// SystemPromptOverride is the user-visible system prompt for the spam
	// classifier (REQ-FILT-22). Returned by the LLM-transparency endpoint
	// (REQ-FILT-65 / REQ-FILT-67). Default empty = use the plugin's built-in
	// default prompt.
	SystemPromptOverride string `json:"system_prompt_override,omitempty"`
	// Guardrail is the operator-only prefix prepended to the system prompt
	// at plugin-call time (REQ-FILT-67). NEVER returned by transparency
	// endpoints. Mutable only via admin REST. Default empty.
	Guardrail string `json:"guardrail,omitempty"`
}

// SpamPolicyStore reads + writes the live spam policy.
type SpamPolicyStore interface {
	GetSpamPolicy() SpamPolicy
	SetSpamPolicy(SpamPolicy)
}

// CategoriseRecategoriser is the protoadmin-facing surface of the
// categorise.Categoriser. The ticket lifts only the
// RecategoriseRecent method through this seam so the admin server can
// stay independent of the categorise package's higher-level wiring
// (REQ-FILT-220). Nil leaves /api/v1/principals/{pid}/recategorise
// returning 501.
type CategoriseRecategoriser interface {
	// RecategoriseRecent re-runs the classifier on principal's last
	// limit messages in their inbox. Slow operation; the admin
	// handler runs it in a goroutine and reports progress through the
	// progress callback.
	RecategoriseRecent(ctx context.Context, principal store.PrincipalID, limit int, progress func(done, total int)) (int, error)
}

// CategoriseJobRegistry is the in-memory job-status map exposed by
// the categorise package. Decoupled here so protoadmin does not
// import categorise directly.
type CategoriseJobRegistry interface {
	Get(id string) (CategoriseJobStatus, bool)
	Put(now time.Time, s CategoriseJobStatus)
}

// CategoriseJobStatus is the wire-form snapshot of a recategorisation
// job. Mirrors categorise.JobStatus to keep the admin response stable
// even if the source struct grows fields.
type CategoriseJobStatus struct {
	ID    string `json:"id"`
	State string `json:"state"`
	Done  int    `json:"done"`
	Total int    `json:"total"`
	Err   string `json:"err,omitempty"`
}

// Options configures a Server.
type Options struct {
	// TLSStore holds certificates for Implicit-TLS listeners. Required
	// when any listener uses ListenerModeImplicit.
	TLSStore *heroldtls.Store
	// BaseURL is the externally-reachable origin of this server; used
	// when building Location headers. Defaults to an empty string
	// (handlers fall back to the request's Host header).
	BaseURL string
	// APIKeyLookup overrides the default store-backed lookup. Optional.
	APIKeyLookup APIKeyLookup
	// CertRenewer drives the /api/v1/certs/{hostname}/renew endpoint.
	// Nil leaves the endpoint returning 501 not_implemented.
	CertRenewer CertRenewer
	// DNSVerifier drives /api/v1/diag/dns-check/{domain}. Nil leaves the
	// endpoint returning 501 not_implemented.
	DNSVerifier DNSVerifier
	// DKIMKeyManager drives POST /api/v1/domains/{name}/dkim and
	// GET /api/v1/domains/{name}/dkim. Nil leaves those endpoints
	// returning 501 not_implemented. The concrete implementation is
	// keymgmt.Manager; tests supply a stub via this field.
	// REQ-ADM-11, REQ-OPS-60.
	DKIMKeyManager DKIMKeyManager
	// SpamPolicyStore drives /api/v1/spam/policy GET + PUT. Nil leaves
	// the endpoints returning 501.
	SpamPolicyStore SpamPolicyStore
	// Categoriser drives the per-principal "recategorise inbox" admin
	// action (REQ-FILT-220). Nil leaves the endpoints returning 501.
	Categoriser CategoriseRecategoriser
	// CategoriseJobs is the in-memory map the recategorise endpoints
	// use to report progress. Defaults to a fresh JobRegistry on
	// first use when Categoriser is set; supplied here for tests.
	CategoriseJobs CategoriseJobRegistry
	// Health tracks liveness / readiness for the /healthz endpoints.
	// Defaults to a fresh observe.Health() marked ready on first Serve.
	Health *observe.Health
	// RequestsPerMinutePerKey caps per-API-key request volume using a
	// sliding-window counter. Zero applies the default (100).
	RequestsPerMinutePerKey int
	// BootstrapPerWindow caps POST /api/v1/bootstrap volume per source
	// IP. Zero applies the default (1 per 5 minutes).
	BootstrapPerWindow int
	// BootstrapWindow is the bootstrap rate-limit window duration; zero
	// applies the default (5 minutes).
	BootstrapWindow time.Duration
	// MaxConcurrentRequests caps simultaneous in-flight requests across
	// all listeners. Zero applies the default (512).
	MaxConcurrentRequests int
	// ReadTimeout / WriteTimeout bound the per-request HTTP timeouts.
	// Zero applies the defaults (30 s each).
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	// ShutdownDrain bounds the graceful-shutdown window. Zero applies
	// the default (10 s).
	ShutdownDrain time.Duration
	// ServerVersion is returned by /api/v1/server/status. Defaults to
	// "dev" when empty.
	ServerVersion string
	// Session configures cookie-based authentication for the admin REST
	// surface (REQ-AUTH-SESSION-REST). When Session.SigningKey is non-nil
	// and at least 32 bytes long, authenticate() falls back to reading
	// the named session cookie if no Authorization: Bearer header is
	// present. Mutating requests authenticated via cookie also require an
	// X-CSRF-Token header that matches the CSRF cookie value
	// (REQ-AUTH-CSRF). Nil / unset signing key disables cookie auth so
	// existing deployments that wire only Bearer keys are unaffected.
	Session authsession.SessionConfig
	// ExternalSubmissionDataKey is the 32-byte data key used to seal and
	// open per-identity submission credentials (REQ-AUTH-EXT-SUBMIT-02).
	// Sourced from sysconfig.SecretsConfig.DataKeyRef at server boot.
	// When nil the submission endpoints return 503.
	ExternalSubmissionDataKey []byte
	// ExternalProbe is the function called by PUT
	// /api/v1/identities/{id}/submission to validate credentials before
	// persisting. Defaults to a no-op that always succeeds when nil and
	// ExternalSubmissionDataKey is also nil (the 503 fires first). When
	// ExternalSubmissionDataKey is set and ExternalProbe is nil, a
	// default no-op probe is used. Tests inject a fake via this field.
	// The production wiring passes defaultProbeFromSubmitter(s) where s
	// is a configured *extsubmit.Submitter.
	ExternalProbe ExternalProbe
	// OAuthProviders is the operator-configured map of OAuth provider
	// names to client credentials, sourced from sysconfig at boot
	// (REQ-AUTH-EXT-SUBMIT-03). Used by the OAuth start/callback endpoints.
	OAuthProviders map[string]OAuthProviderOptions

	// Clientlog overrides rate-limits, queue size, and the telemetry gate
	// used by the clientlog ingest endpoints. Zero values retain the defaults
	// from REQ-OPS-216. Intended for use in tests and sysconfig wiring.
	Clientlog ClientlogOptions
	// ListenerTag is the value stamped onto request contexts via
	// ctxKeyListener so handlers (notably clientlog ingest) can record the
	// originating listener in the ring buffer and metrics. Empty defaults
	// to "admin"; the public listener wiring passes "public".
	ListenerTag string
}

// ClientlogOptions configures the client-log ingest pipeline parameters.
// All zero values are replaced with the defaults from REQ-OPS-216 at Server
// construction time.
type ClientlogOptions struct {
	// AuthRateLimit is the maximum events per AuthRateWindow per session.
	// Defaults to clientlogRateAuthLimit (1000).
	AuthRateLimit int
	// AuthRateWindow is the sliding-window duration for auth rate limiting.
	// Defaults to clientlogRateAuthWindow (5 min).
	AuthRateWindow time.Duration
	// PublicRateLimit is the maximum events per PublicRateWindow per IP.
	// Defaults to clientlogRatePublicLimit (20).
	PublicRateLimit int
	// PublicRateWindow is the sliding-window duration for IP rate limiting.
	// Defaults to clientlogRatePublicWindow (1 min).
	PublicRateWindow time.Duration
	// QueueSize is the buffered channel capacity. Defaults to
	// clientlogQueueSize (256). Set to a value > 0 to override; the value
	// 0 uses the default. Tests that need instant backpressure can set this
	// to a very small positive value via Options.Clientlog.
	QueueSize int
	// TelemetryGate is the per-session opt-out gate (REQ-OPS-208). Nil
	// defaults to the always-enabled stub. Replace with a real gate in task
	// #6/#7 once the principal-flag store is available.
	TelemetryGate TelemetryGate
	// Emitter is the fan-out emitter for slog + OTLP. Nil defaults to the
	// noop emitter. The production wiring passes a real ClientEmitter.
	Emitter ClientlogEmitter
	// LivetailDefaultDuration is the default lifetime for a live-tail
	// session enabled via POST /api/v1/admin/clientlog/livetail
	// (REQ-OPS-211, REQ-OPS-219). Zero value uses 15 minutes.
	LivetailDefaultDuration time.Duration
	// LivetailMaxDuration is the upper bound applied to caller-supplied
	// durations on live-tail enable (REQ-OPS-211, REQ-OPS-219). Zero value
	// uses 60 minutes.
	LivetailMaxDuration time.Duration
}

// OAuthProviderOptions carries the resolved (decrypted) OAuth 2.0 client
// credentials for one provider entry from [server.oauth_providers.<name>].
// The ClientSecret is resolved from the config reference at boot; it must
// never appear in logs or API responses.
type OAuthProviderOptions struct {
	// ClientID is the OAuth 2.0 client identifier.
	ClientID string
	// ClientSecret is the resolved plaintext client secret. It is zeroed
	// out of this struct on server Close.
	ClientSecret string
	// AuthURL is the provider's authorisation endpoint.
	AuthURL string
	// TokenURL is the provider's token endpoint.
	TokenURL string
	// Scopes is the set of OAuth scopes requested.
	Scopes []string
}

// Server is the protoadmin REST handle. One *Server serves any number
// of listeners via Serve; Handler returns the mux used by all listeners.
type Server struct {
	store  store.Store
	dir    *directory.Directory
	rp     *directoryoidc.RP
	clk    clock.Clock
	logger *slog.Logger
	opts   Options

	startedAt time.Time

	rl          *rateLimiter
	bootstrapRL *rateLimiter

	apikeyLookup APIKeyLookup

	// clientlog ingest fields (REQ-OPS-200..207, REQ-OPS-215..218, REQ-OPS-220).
	clientlogAuthRL        *rateLimiter
	clientlogPublicRL      *rateLimiter
	clientlogQueue         chan clientlogJob
	clientlogPipeline      *clientlogPipeline
	clientlogTelemetryGate TelemetryGate
	// clientlog admin-surface fields (REQ-ADM-232).
	clientlogLivetailDefault time.Duration
	clientlogLivetailMax     time.Duration

	mu        sync.Mutex
	closed    bool
	servers   []*http.Server
	listeners []net.Listener
	wg        sync.WaitGroup
}

// NewServer constructs a Server. store, dir, and rp are required. logger
// defaults to slog.Default. clk defaults to clock.NewReal.
func NewServer(
	st store.Store,
	dir *directory.Directory,
	rp *directoryoidc.RP,
	logger *slog.Logger,
	clk clock.Clock,
	opts Options,
) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	if clk == nil {
		clk = clock.NewReal()
	}
	if opts.Health == nil {
		opts.Health = observe.NewHealth()
	}
	if opts.RequestsPerMinutePerKey <= 0 {
		opts.RequestsPerMinutePerKey = 100
	}
	if opts.BootstrapPerWindow <= 0 {
		opts.BootstrapPerWindow = 1
	}
	if opts.BootstrapWindow <= 0 {
		opts.BootstrapWindow = 5 * time.Minute
	}
	if opts.MaxConcurrentRequests <= 0 {
		opts.MaxConcurrentRequests = 512
	}
	if opts.ReadTimeout <= 0 {
		opts.ReadTimeout = 30 * time.Second
	}
	if opts.WriteTimeout <= 0 {
		opts.WriteTimeout = 30 * time.Second
	}
	if opts.ShutdownDrain <= 0 {
		opts.ShutdownDrain = 10 * time.Second
	}
	if opts.ServerVersion == "" {
		opts.ServerVersion = "dev"
	}
	// Default ExternalProbe: when no probe is injected and no data key is
	// configured, the 503 fires before the probe is reached. When a data
	// key IS set but no probe is provided (test fixtures that seal but
	// don't want a real probe), fall back to noopProbe.
	if opts.ExternalProbe == nil {
		opts.ExternalProbe = noopProbe
	}
	// Register the admin REST collector set on Server construction.
	// Idempotent across multiple instances sharing a process Registry.
	observe.RegisterAdminMetrics()
	observe.RegisterStoreMetrics()
	observe.RegisterAuthMetrics()
	observe.RegisterClientlogMetrics()

	// Apply clientlog option defaults.
	clo := opts.Clientlog
	if clo.AuthRateLimit <= 0 {
		clo.AuthRateLimit = clientlogRateAuthLimit
	}
	if clo.AuthRateWindow <= 0 {
		clo.AuthRateWindow = clientlogRateAuthWindow
	}
	if clo.PublicRateLimit <= 0 {
		clo.PublicRateLimit = clientlogRatePublicLimit
	}
	if clo.PublicRateWindow <= 0 {
		clo.PublicRateWindow = clientlogRatePublicWindow
	}
	queueSize := clo.QueueSize
	if queueSize <= 0 {
		queueSize = clientlogQueueSize
	}
	var emitter ClientlogEmitter
	if clo.Emitter != nil {
		emitter = clo.Emitter
	} else {
		emitter = newNoopEmitter()
	}
	var gate TelemetryGate
	if clo.TelemetryGate != nil {
		gate = clo.TelemetryGate
	} else {
		gate = alwaysEnabledGate{}
	}

	clQueue := make(chan clientlogJob, queueSize)
	pipeline := newClientlogPipeline(clQueue, emitter, st.Meta(), logger, clk)

	s := &Server{
		store:       st,
		dir:         dir,
		rp:          rp,
		clk:         clk,
		logger:      logger,
		opts:        opts,
		startedAt:   clk.Now(),
		rl:          newRateLimiter(clk, opts.RequestsPerMinutePerKey, time.Minute),
		bootstrapRL: newRateLimiter(clk, opts.BootstrapPerWindow, opts.BootstrapWindow),

		clientlogAuthRL:          newRateLimiter(clk, clo.AuthRateLimit, clo.AuthRateWindow),
		clientlogPublicRL:        newRateLimiter(clk, clo.PublicRateLimit, clo.PublicRateWindow),
		clientlogQueue:           clQueue,
		clientlogPipeline:        pipeline,
		clientlogTelemetryGate:   gate,
		clientlogLivetailDefault: clo.LivetailDefaultDuration,
		clientlogLivetailMax:     clo.LivetailMaxDuration,
	}
	if opts.APIKeyLookup != nil {
		s.apikeyLookup = opts.APIKeyLookup
	} else {
		s.apikeyLookup = func(ctx context.Context, hash string) (store.APIKey, error) {
			return st.Meta().GetAPIKeyByHash(ctx, hash)
		}
	}
	return s
}

// Handler returns the REST mux wrapped with server-wide middleware. It
// is safe to mount under a prefix in a parent mux (the handler registers
// absolute /api/v1/... paths internally).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	// Outer chain: concurrency limit -> panic recover -> request id +
	// slog -> metrics -> mux. Concurrency limit is outermost so rejected
	// requests never allocate a request-scoped slog group. The metrics
	// middleware sits closest to the mux so it can read the route
	// pattern set by mux.findHandler on the request.
	sem := make(chan struct{}, s.opts.MaxConcurrentRequests)
	return s.withConcurrencyLimit(sem,
		s.withPanicRecover(
			s.withRequestLog(
				s.withListenerTag(
					s.withMetrics(mux)))))
}

// withListenerTag stamps ctxKeyListener with the configured Options.ListenerTag
// (default "admin") so downstream handlers can record the originating listener.
func (s *Server) withListenerTag(next http.Handler) http.Handler {
	tag := s.opts.ListenerTag
	if tag == "" {
		tag = "admin"
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), ctxKeyListener, tag)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// withMetrics records every served admin request in the
// herold_admin_requests_total counter and the
// herold_admin_request_duration_seconds histogram, keyed on the route
// template (NOT the resolved path) so the per-pid principal endpoints
// stay at one cardinality bucket. Requests that did not match any route
// (404) are bucketed under the empty pattern "".
func (s *Server) withMetrics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := s.clk.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		// r.Pattern is populated by net/http.ServeMux on a successful
		// match. For unmatched requests (404), the pattern is empty.
		pattern := r.Pattern
		if pattern == "" {
			pattern = "unmatched"
		} else {
			// r.Pattern is "METHOD /path" — strip the leading method so
			// the same /path under different methods bucketed by the
			// dedicated method label.
			if i := strings.IndexByte(pattern, ' '); i >= 0 {
				pattern = pattern[i+1:]
			}
		}
		observe.AdminRequestsTotal.WithLabelValues(pattern, r.Method, fmt.Sprintf("%d", rec.status)).Inc()
		observe.AdminRequestDuration.WithLabelValues(pattern).Observe(s.clk.Now().Sub(start).Seconds())
	})
}

// Serve accepts connections from ln until ctx is cancelled or ln is
// closed. Each listener gets its own http.Server. The server flips the
// health gate to ready on first Serve.
func (s *Server) Serve(ctx context.Context, ln net.Listener, mode ListenerMode) error {
	// Clear any deadline set by the testharness stand-in accept loop.
	if tcp, ok := ln.(*net.TCPListener); ok {
		_ = tcp.SetDeadline(time.Time{})
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("protoadmin: server closed")
	}
	srv := &http.Server{
		Handler:      s.Handler(),
		ReadTimeout:  s.opts.ReadTimeout,
		WriteTimeout: s.opts.WriteTimeout,
		BaseContext:  func(net.Listener) context.Context { return ctx },
		ErrorLog:     nil, // stdlib http errors are reported through slog inside handlers
	}
	if mode == ListenerModeImplicit {
		if s.opts.TLSStore == nil {
			s.mu.Unlock()
			return errors.New("protoadmin: TLS listener requested but no TLSStore configured")
		}
		srv.TLSConfig = heroldtls.TLSConfig(s.opts.TLSStore, heroldtls.Intermediate, []string{"h2", "http/1.1"})
	}
	s.servers = append(s.servers, srv)
	s.listeners = append(s.listeners, ln)
	s.opts.Health.MarkReady()
	s.mu.Unlock()

	errCh := make(chan error, 1)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		var err error
		if mode == ListenerModeImplicit {
			tlsLn := tls.NewListener(ln, srv.TLSConfig)
			err = srv.Serve(tlsLn)
		} else {
			err = srv.Serve(ln)
		}
		errCh <- err
	}()

	select {
	case <-ctx.Done():
		drainCtx, cancel := context.WithTimeout(context.Background(), s.opts.ShutdownDrain)
		defer cancel()
		_ = srv.Shutdown(drainCtx)
		return ctx.Err()
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return http.ErrServerClosed
		}
		return fmt.Errorf("protoadmin: serve: %w", err)
	}
}

// Close shuts the server down. In-flight requests drain within
// Options.ShutdownDrain; subsequent calls are no-ops.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	servers := append([]*http.Server(nil), s.servers...)
	s.mu.Unlock()
	s.opts.Health.MarkNotReady()
	drainCtx, cancel := context.WithTimeout(context.Background(), s.opts.ShutdownDrain)
	defer cancel()
	var first error
	for _, srv := range servers {
		if err := srv.Shutdown(drainCtx); err != nil && first == nil {
			first = err
		}
	}
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
	// Drain the clientlog worker pool after HTTP listeners are shut down.
	if s.clientlogPipeline != nil {
		s.clientlogPipeline.Close()
	}
	return first
}

// withConcurrencyLimit rejects requests that exceed the server-wide
// request cap with 503 Service Unavailable. The check is non-blocking
// so a single slow handler cannot block fresh requests from getting a
// decisive response.
func (s *Server) withConcurrencyLimit(sem chan struct{}, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
		default:
			writeProblem(w, r, http.StatusServiceUnavailable,
				"server_busy", "server is at its concurrency limit", "")
			return
		}
		next.ServeHTTP(w, r)
	})
}
