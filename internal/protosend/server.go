package protosend

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
	"github.com/hanshuebner/herold/internal/store"
	heroldtls "github.com/hanshuebner/herold/internal/tls"
)

// ListenerMode selects the TLS policy of a listener; mirrors
// protoadmin.ListenerMode so wiring can reuse the same enum value.
type ListenerMode int

const (
	// ListenerModePlain serves HTTP without TLS (loopback / behind a
	// trusted reverse proxy).
	ListenerModePlain ListenerMode = iota
	// ListenerModeImplicit wraps the listener in TLS using the
	// configured TLS store (HTTPS).
	ListenerModeImplicit
)

// Default option values.
const (
	defaultMaxBodySize           int64 = 30 * 1024 * 1024 // 30 MiB; matches SES SendRawEmail body limit.
	defaultMaxRecipients               = 100
	defaultMaxConcurrentRequests       = 256
	defaultRateLimitPerKey             = 60
	defaultIdleTimeout                 = 30 * time.Second
	defaultMaxBatchItems               = 50
	defaultReadTimeout                 = 30 * time.Second
	defaultWriteTimeout                = 60 * time.Second
	defaultShutdownDrain               = 10 * time.Second
)

// APIKeyLookup resolves a presented API key hash to its stored row.
// Defaults to store.Metadata.GetAPIKeyByHash; tests inject a fake.
type APIKeyLookup func(ctx context.Context, hash string) (store.APIKey, error)

// Options configures a Server.
type Options struct {
	// TLSStore holds certificates for Implicit-TLS listeners. Required
	// when any listener uses ListenerModeImplicit.
	TLSStore *heroldtls.Store
	// BaseURL is the externally-reachable origin of this server; used
	// when building Location headers. Defaults to empty (handlers
	// fall back to the request's Host header).
	BaseURL string
	// MaxBodySize caps the request body size in bytes. Zero applies
	// the default (30 MiB; matches SES SendRawEmail's limit).
	MaxBodySize int64
	// MaxRecipients caps the destination count for /send and the
	// per-item recipient count for /send-raw. Zero applies the
	// default (100).
	MaxRecipients int
	// MaxBatchItems caps the number of items in /send-batch. Zero
	// applies the default (50).
	MaxBatchItems int
	// MaxConcurrentRequests caps simultaneous in-flight requests
	// across all listeners. Zero applies the default (256).
	MaxConcurrentRequests int
	// RateLimitPerKey is the sliding-window per-API-key request cap
	// (per minute). Zero applies the default (60).
	RateLimitPerKey int
	// IdleTimeout is the idle-keepalive timeout for the http.Server.
	// Zero applies the default (30s).
	IdleTimeout time.Duration
	// ReadTimeout / WriteTimeout bound per-request timeouts.
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	// ShutdownDrain bounds the graceful-shutdown window.
	ShutdownDrain time.Duration
	// APIKeyLookup overrides the default store-backed lookup.
	APIKeyLookup APIKeyLookup
	// Hostname is the local hostname used to mint Message-IDs when the
	// caller-supplied message has none. Defaults to "localhost".
	Hostname string
}

// Server is the protosend HTTP send handle. One *Server serves any
// number of listeners via Serve; Handler returns the mux used by all
// listeners.
type Server struct {
	store  store.Store
	dir    *directory.Directory
	queue  Submitter
	clk    clock.Clock
	logger *slog.Logger
	opts   Options

	rl           *rateLimiter
	apikeyLookup APIKeyLookup

	// dailyMu guards dailyCounts. Each principal's per-day counter is
	// reset lazily when the calendar day rolls over.
	dailyMu     sync.Mutex
	dailyCounts map[store.PrincipalID]dailyCounter

	mu      sync.Mutex
	closed  bool
	servers []*http.Server
	wg      sync.WaitGroup
}

type dailyCounter struct {
	day   string // "2026-04-24"
	count int64
}

// NewServer constructs a Server.
//
// store is required (audit log, principal lookup, queue listing).
// dir is currently unused on the request path but reserved for the
// configuration-set lookup wave.
// q is the outbound queue Submitter (pass *queue.Queue in production).
// tlsStore is required only when a listener uses ListenerModeImplicit.
// logger defaults to slog.Default. clk defaults to clock.NewReal.
func NewServer(
	st store.Store,
	dir *directory.Directory,
	q Submitter,
	tlsStore *heroldtls.Store,
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
	if opts.TLSStore == nil {
		opts.TLSStore = tlsStore
	}
	if opts.MaxBodySize <= 0 {
		opts.MaxBodySize = defaultMaxBodySize
	}
	if opts.MaxRecipients <= 0 {
		opts.MaxRecipients = defaultMaxRecipients
	}
	if opts.MaxBatchItems <= 0 {
		opts.MaxBatchItems = defaultMaxBatchItems
	}
	if opts.MaxConcurrentRequests <= 0 {
		opts.MaxConcurrentRequests = defaultMaxConcurrentRequests
	}
	if opts.RateLimitPerKey <= 0 {
		opts.RateLimitPerKey = defaultRateLimitPerKey
	}
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = defaultIdleTimeout
	}
	if opts.ReadTimeout <= 0 {
		opts.ReadTimeout = defaultReadTimeout
	}
	if opts.WriteTimeout <= 0 {
		opts.WriteTimeout = defaultWriteTimeout
	}
	if opts.ShutdownDrain <= 0 {
		opts.ShutdownDrain = defaultShutdownDrain
	}
	if opts.Hostname == "" {
		opts.Hostname = "localhost"
	}
	s := &Server{
		store:       st,
		dir:         dir,
		queue:       q,
		clk:         clk,
		logger:      logger,
		opts:        opts,
		rl:          newRateLimiter(clk, opts.RateLimitPerKey, time.Minute),
		dailyCounts: make(map[store.PrincipalID]dailyCounter),
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

// Handler returns the REST mux wrapped with server-wide middleware.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	sem := make(chan struct{}, s.opts.MaxConcurrentRequests)
	return s.withConcurrencyLimit(sem,
		s.withPanicRecover(
			s.withRequestLog(mux)))
}

// registerRoutes registers every /api/v1/mail/... endpoint.
func (s *Server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/mail/send", s.requireAuth(s.handleSend))
	mux.HandleFunc("POST /api/v1/mail/send-raw", s.requireAuth(s.handleSendRaw))
	mux.HandleFunc("POST /api/v1/mail/send-batch", s.requireAuth(s.handleSendBatch))
	mux.HandleFunc("GET /api/v1/mail/quota", s.requireAuth(s.handleQuota))
	mux.HandleFunc("GET /api/v1/mail/stats", s.requireAuth(s.handleStats))
}

// Serve accepts connections from ln until ctx is cancelled or ln is
// closed. Each listener gets its own http.Server. Mirrors the
// protoadmin/protojmap shape so the wiring layer treats all three
// HTTP surfaces uniformly.
func (s *Server) Serve(ctx context.Context, ln net.Listener, mode ListenerMode) error {
	if tcp, ok := ln.(*net.TCPListener); ok {
		_ = tcp.SetDeadline(time.Time{})
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("protosend: server closed")
	}
	srv := &http.Server{
		Handler:      s.Handler(),
		ReadTimeout:  s.opts.ReadTimeout,
		WriteTimeout: s.opts.WriteTimeout,
		IdleTimeout:  s.opts.IdleTimeout,
		BaseContext:  func(net.Listener) context.Context { return ctx },
	}
	if mode == ListenerModeImplicit {
		if s.opts.TLSStore == nil {
			s.mu.Unlock()
			return errors.New("protosend: TLS listener requested but no TLSStore configured")
		}
		srv.TLSConfig = heroldtls.TLSConfig(s.opts.TLSStore, heroldtls.Intermediate, []string{"h2", "http/1.1"})
	}
	s.servers = append(s.servers, srv)
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
		return fmt.Errorf("protosend: serve: %w", err)
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

// noteSubmitted records a successful send for the calling principal in
// the in-process per-day counter. Resets when the calendar day rolls.
// The counter is best-effort and process-local — it lives only to feed
// /quota; durable per-day rollups land in the audit / queue tables.
func (s *Server) noteSubmitted(pid store.PrincipalID, n int64) {
	day := s.clk.Now().UTC().Format("2006-01-02")
	s.dailyMu.Lock()
	defer s.dailyMu.Unlock()
	c := s.dailyCounts[pid]
	if c.day != day {
		c = dailyCounter{day: day}
	}
	c.count += n
	s.dailyCounts[pid] = c
}

// dailyUsed returns the per-day counter for pid (UTC calendar day).
func (s *Server) dailyUsed(pid store.PrincipalID) int64 {
	day := s.clk.Now().UTC().Format("2006-01-02")
	s.dailyMu.Lock()
	defer s.dailyMu.Unlock()
	c := s.dailyCounts[pid]
	if c.day != day {
		return 0
	}
	return c.count
}
