package protoadmin

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

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
	// slog -> mux. Concurrency limit is outermost so rejected requests
	// never allocate a request-scoped slog group.
	sem := make(chan struct{}, s.opts.MaxConcurrentRequests)
	return s.withConcurrencyLimit(sem,
		s.withPanicRecover(
			s.withRequestLog(mux)))
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
