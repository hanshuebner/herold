package protojmap

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

	"github.com/hanshuebner/herold/internal/auth"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/store"
	heroldtls "github.com/hanshuebner/herold/internal/tls"
)

// Default sizing constants applied when Options leaves a field zero.
// The chosen values mirror the JMAP-Core advisory limits in RFC 8620
// §2 ("server limits") plus REQ-PROTO-40..48 from
// docs/design/requirements/01-protocols.md.
const (
	defaultMaxSizeUpload         int64 = 50 * 1024 * 1024
	defaultMaxSizeRequest        int64 = 10 * 1024 * 1024
	defaultMaxConcurrentRequests       = 256
	defaultMaxObjectsInGet             = 500
	defaultMaxObjectsInSet             = 500
	defaultMaxCallsInRequest           = 16
	defaultPushPingInterval            = 5 * time.Minute
	defaultPushCoalesceWindow          = 200 * time.Millisecond
	defaultDownloadRatePerSec    int64 = 5 * 1024 * 1024
	defaultDownloadBurstBytes    int64 = 20 * 1024 * 1024
)

// ListenerMode selects the TLS policy of a JMAP listener; mirrors
// protoadmin.ListenerMode so the wiring layer reuses the same enum
// shape across protocols.
type ListenerMode int

const (
	// ListenerModePlain serves HTTP without TLS (loopback / behind a
	// trusted reverse proxy only).
	ListenerModePlain ListenerMode = iota
	// ListenerModeImplicit wraps the listener in TLS.
	ListenerModeImplicit
)

// APIKeyLookup resolves a presented API key hash to its stored row.
// The Server uses the same scheme as protoadmin (Authorization: Bearer
// hk_...) so a key issued under the admin surface authenticates JMAP
// too.
type APIKeyLookup func(ctx context.Context, hash string) (store.APIKey, error)

// SessionResolver resolves a suite-session cookie on an inbound HTTP
// request into the authenticated principal ID and its scope set.
// Implementations are provided by protoui.Server.ResolveSessionWithScope.
// A nil resolver disables cookie-based auth (Bearer + Basic only).
// When non-nil it is called only when no Authorization header is
// present; Bearer / Basic always take precedence.
type SessionResolver func(r *http.Request) (store.PrincipalID, auth.ScopeSet, bool)

// Options configures a Server. Zero values pick conservative defaults
// per RFC 8620 §2 (server limits).
type Options struct {
	// TLSStore supplies certificates for ListenerModeImplicit.
	TLSStore *heroldtls.Store
	// BaseURL is the externally-reachable origin of this JMAP server.
	// The session descriptor's apiUrl / downloadUrl / uploadUrl /
	// eventSourceUrl fields are formed by joining this with the
	// well-known relative paths. Operator-supplied; falls back to the
	// request's Host header when empty.
	BaseURL string
	// MaxSizeUpload caps a single /jmap/upload request body. Default
	// 50 MiB.
	MaxSizeUpload int64
	// MaxSizeRequest caps the JSON body of POST /jmap. Default 10 MiB.
	MaxSizeRequest int64
	// MaxConcurrentRequests caps in-flight POST /jmap bodies across
	// every listener. Default 256.
	MaxConcurrentRequests int
	// MaxObjectsInGet bounds Foo/get's "ids" array. Default 500. The
	// Core dispatcher publishes the limit through the session
	// descriptor; per-method handlers enforce it.
	MaxObjectsInGet int
	// MaxObjectsInSet bounds the union of create/update/destroy keys.
	// Default 500.
	MaxObjectsInSet int
	// MaxCallsInRequest caps the methodCalls array length. Default 16.
	MaxCallsInRequest int
	// APIKeyLookup overrides the default store-backed lookup. Tests
	// inject a fake; production wiring leaves it nil.
	APIKeyLookup APIKeyLookup
	// SessionResolver, when non-nil, enables cookie-based authentication
	// on the public listener. The JMAP auth middleware calls it when no
	// Authorization header is present on the request. Production wiring
	// supplies protoui.Server.ResolveSessionWithScope here so a browser
	// with a valid suite-session cookie can call JMAP endpoints without
	// a separate Bearer credential. The resolver must return false for
	// expired or invalid sessions; a true result is trusted without
	// additional store round-trips (the protoui layer already validates
	// the cookie signature and checks the disabled flag).
	SessionResolver SessionResolver
	// PushPingInterval is the interval at which idle EventSource
	// streams emit a ": ping" comment. Default 5 minutes; clients
	// supply a smaller value with the ?ping= query parameter.
	PushPingInterval time.Duration
	// PushCoalesceWindow is the per-stream window the push goroutine
	// holds the latest StateChange before flushing. Default 200 ms.
	// Rationale: avoids spamming clients on rapid-fire writes (a JMAP
	// Email/set with N create entries produces N change-feed entries
	// in the same transaction); the 200 ms window collapses them into
	// one StateChange event without hurting interactivity.
	PushCoalesceWindow time.Duration
	// DownloadRatePerSec / DownloadBurstBytes feed the per-principal
	// token bucket on /jmap/download (REQ-STORE-20..25). Defaults 5
	// MiB/s and 20 MiB burst. Set DownloadRatePerSec to a negative
	// value to disable throttling entirely (tests).
	DownloadRatePerSec int64
	DownloadBurstBytes int64
	// ReadTimeout / WriteTimeout bound the per-request HTTP timeouts.
	// EventSource holds the response open indefinitely so WriteTimeout
	// is intentionally zero on that handler; the bounds here apply to
	// the rest. Defaults 30s.
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	// ShutdownDrain bounds the graceful-shutdown window. Defaults 10s.
	ShutdownDrain time.Duration
}

// Server is the JMAP Core handle. One *Server serves any number of
// listeners via Serve; Handler returns the mux every listener uses.
type Server struct {
	store store.Store
	dir   *directory.Directory
	tls   *heroldtls.Store
	clk   clock.Clock
	log   *slog.Logger
	reg   *CapabilityRegistry
	opts  Options

	apikeyLookup    APIKeyLookup
	sessionResolver SessionResolver

	// dlBuckets holds per-principal download token buckets. Bounded by
	// the registered principal count; entries persist for the life of
	// the Server (no GC — a malicious key cannot allocate a bucket per
	// request because the key resolves to a fixed PrincipalID).
	dlMu      sync.Mutex
	dlBuckets map[store.PrincipalID]*tokenBucket

	mu        sync.Mutex
	closed    bool
	servers   []*http.Server
	listeners []net.Listener
	wg        sync.WaitGroup
}

// NewServer constructs a Server bound to the given store / directory /
// TLS store / logger / clock. The CapabilityRegistry is created here
// and pre-loaded with the JMAP Core capability handler set ("Core/echo"
// per RFC 8620 §4); callers register additional capabilities (mail,
// submission, ...) via Registry().Register before serving.
func NewServer(
	st store.Store,
	dir *directory.Directory,
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
	if opts.MaxSizeUpload <= 0 {
		opts.MaxSizeUpload = defaultMaxSizeUpload
	}
	if opts.MaxSizeRequest <= 0 {
		opts.MaxSizeRequest = defaultMaxSizeRequest
	}
	if opts.MaxConcurrentRequests <= 0 {
		opts.MaxConcurrentRequests = defaultMaxConcurrentRequests
	}
	if opts.MaxObjectsInGet <= 0 {
		opts.MaxObjectsInGet = defaultMaxObjectsInGet
	}
	if opts.MaxObjectsInSet <= 0 {
		opts.MaxObjectsInSet = defaultMaxObjectsInSet
	}
	if opts.MaxCallsInRequest <= 0 {
		opts.MaxCallsInRequest = defaultMaxCallsInRequest
	}
	if opts.PushPingInterval <= 0 {
		opts.PushPingInterval = defaultPushPingInterval
	}
	if opts.PushCoalesceWindow <= 0 {
		opts.PushCoalesceWindow = defaultPushCoalesceWindow
	}
	if opts.DownloadRatePerSec == 0 {
		opts.DownloadRatePerSec = defaultDownloadRatePerSec
	}
	if opts.DownloadBurstBytes <= 0 {
		opts.DownloadBurstBytes = defaultDownloadBurstBytes
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
	s := &Server{
		store:     st,
		dir:       dir,
		tls:       tlsStore,
		clk:       clk,
		log:       logger,
		reg:       NewCapabilityRegistry(),
		opts:      opts,
		dlBuckets: make(map[store.PrincipalID]*tokenBucket),
	}
	if opts.APIKeyLookup != nil {
		s.apikeyLookup = opts.APIKeyLookup
	} else {
		s.apikeyLookup = func(ctx context.Context, hash string) (store.APIKey, error) {
			return st.Meta().GetAPIKeyByHash(ctx, hash)
		}
	}
	s.sessionResolver = opts.SessionResolver
	// Register the JMAP Core capability + the canonical Core/echo
	// method (RFC 8620 §4). Parallel agents register the Mail
	// capability + its handlers via Registry().
	s.reg.Register(CapabilityCore, coreEchoHandler{})
	s.reg.installCapabilityDescriptor(CapabilityCore, coreCapabilityDescriptor(opts))
	return s
}

// Registry returns the per-server capability registry. Callers register
// per-datatype handlers here at construction time, before Serve. The
// returned pointer is the same one the dispatcher consults; concurrent
// registration after Serve has been called is supported but is
// discouraged because the session descriptor caches its capabilities
// listing.
func (s *Server) Registry() *CapabilityRegistry { return s.reg }

// Handler returns the HTTP handler bound to all JMAP routes. Safe to
// mount under a parent mux (all routes are absolute paths).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	sem := make(chan struct{}, s.opts.MaxConcurrentRequests)
	return s.withConcurrencyLimit(sem, s.withPanicRecover(s.withRequestLog(mux)))
}

// Serve accepts connections from ln until ctx is cancelled or ln is
// closed. The lifecycle mirrors protoadmin.Server.Serve so wiring code
// (internal/admin) treats both surfaces uniformly.
func (s *Server) Serve(ctx context.Context, ln net.Listener, mode ListenerMode) error {
	if tcp, ok := ln.(*net.TCPListener); ok {
		_ = tcp.SetDeadline(time.Time{})
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("protojmap: server closed")
	}
	srv := &http.Server{
		Handler:     s.Handler(),
		ReadTimeout: s.opts.ReadTimeout,
		// WriteTimeout intentionally omitted at the http.Server level;
		// the EventSource handler streams indefinitely. Per-handler
		// SetWriteDeadline calls cover the bounded handlers.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	if mode == ListenerModeImplicit {
		if s.tls == nil {
			s.mu.Unlock()
			return errors.New("protojmap: TLS listener requested but no TLSStore configured")
		}
		srv.TLSConfig = heroldtls.TLSConfig(s.tls, heroldtls.Intermediate, []string{"h2", "http/1.1"})
	}
	s.servers = append(s.servers, srv)
	s.listeners = append(s.listeners, ln)
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
		return fmt.Errorf("protojmap: serve: %w", err)
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

// registerRoutes wires the JMAP HTTP surface onto mux. Every route
// requires authentication; the session endpoint and dispatcher consume
// the registry via the common handler chain.
func (s *Server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/jmap", s.requireAuth(s.handleSession))
	mux.HandleFunc("POST /jmap", s.requireAuth(s.handleAPI))
	mux.HandleFunc("GET /jmap/eventsource", s.requireAuth(s.handleEventSource))
	mux.HandleFunc("POST /jmap/upload/{accountId}", s.requireAuth(s.handleUpload))
	mux.HandleFunc("GET /jmap/download/{accountId}/{blobId}/{type}/{name}", s.requireAuth(s.handleDownload))
}

// withConcurrencyLimit caps simultaneous in-flight requests with a
// non-blocking channel send. Excess requests get 503 immediately.
// EventSource sessions long-poll under the same cap; operators tune
// MaxConcurrentRequests upward when push usage is high.
func (s *Server) withConcurrencyLimit(sem chan struct{}, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
		default:
			WriteJMAPError(w, http.StatusServiceUnavailable,
				"serverUnavailable", "server is at its concurrency limit")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// dlBucket returns the per-principal download token bucket, creating
// one on first access.
func (s *Server) dlBucket(pid store.PrincipalID) *tokenBucket {
	if s.opts.DownloadRatePerSec < 0 {
		return nil
	}
	s.dlMu.Lock()
	defer s.dlMu.Unlock()
	if b, ok := s.dlBuckets[pid]; ok {
		return b
	}
	b := newTokenBucket(s.clk, s.opts.DownloadRatePerSec, s.opts.DownloadBurstBytes)
	s.dlBuckets[pid] = b
	return b
}
