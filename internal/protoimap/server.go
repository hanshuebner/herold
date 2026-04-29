package protoimap

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/sasl"
	"github.com/hanshuebner/herold/internal/store"
	heroldtls "github.com/hanshuebner/herold/internal/tls"
)

// ListenerMode selects the TLS policy of a listener.
type ListenerMode int

const (
	// ListenerModeImplicit993 wraps the accepted TCP connection in TLS
	// immediately. Used for port 993.
	ListenerModeImplicit993 ListenerMode = iota
	// ListenerModeSTARTTLS serves plaintext until the client issues
	// STARTTLS. Used for port 143.
	ListenerModeSTARTTLS
)

// Options is the server's runtime configuration.
type Options struct {
	// MaxConnections caps simultaneous sessions per listener. Zero
	// resolves to the default (1024). Negative explicitly disables
	// the cap; operators choosing this MUST front the listener with
	// an external limiter (REQ-PROTO-13/14, STANDARDS §9).
	MaxConnections int
	// MaxConnectionsPerIP caps simultaneous sessions from a single
	// remote IP. Zero resolves to the default (32). Negative
	// disables the per-IP cap; operators choosing this MUST front
	// the listener with an external limiter.
	MaxConnectionsPerIP int
	// MaxCommandsPerSession is the per-session command budget; once
	// exhausted the session is closed with BYE. Zero disables the cap.
	MaxCommandsPerSession int
	// DownloadBytesPerSecond is the per-session FETCH rate limit
	// (REQ-STORE-20..25). Zero disables throttling.
	DownloadBytesPerSecond int64
	// DownloadBurstBytes is the token-bucket burst size. Defaults to
	// DownloadBytesPerSecond when zero.
	DownloadBurstBytes int64
	// IdleMaxDuration is the hard cap on a single IDLE block before the
	// server terminates it with BYE and the client must reconnect (RFC 2177
	// recommends ~29 minutes for NAT keepalive). Zero means 30 minutes.
	IdleMaxDuration time.Duration
	// AllowPlainLoginWithoutTLS, when true, permits the LOGIN command on
	// cleartext sockets. Default false (REQ-PROTO-12).
	AllowPlainLoginWithoutTLS bool
	// ServerName advertised in the greeting and ID response.
	ServerName string
}

// IMAP server caps. Defaults are chosen to match the REQ-PROTO-31
// 2k IDLE budget plus headroom for short-lived FETCH/STORE clients.
const (
	defaultMaxConnections      = 1024
	defaultMaxConnectionsPerIP = 32
)

// Server is the protoimap listener + session factory. One *Server handles
// any number of listeners (Serve) but carries a single set of Options and
// dependencies.
type Server struct {
	store     store.Store
	dir       *directory.Directory
	tlsStore  *heroldtls.Store
	clk       clock.Clock
	logger    *slog.Logger
	opts      Options
	passwords sasl.PasswordLookup
	tokens    sasl.TokenVerifier

	mu        sync.Mutex
	closed    bool
	listeners []net.Listener
	sessions  map[*session]struct{}
	perIP     map[string]int
	wg        sync.WaitGroup
}

// admitIP atomically reserves a slot for remoteIP under the per-IP
// cap. Returns false when the cap is exhausted; the caller must
// reject the connection.
func (s *Server) admitIP(remoteIP string) bool {
	if s.opts.MaxConnectionsPerIP < 0 {
		// Operator opted out; per-IP gating disabled.
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.perIP == nil {
		s.perIP = make(map[string]int)
	}
	if s.perIP[remoteIP] >= s.opts.MaxConnectionsPerIP {
		return false
	}
	s.perIP[remoteIP]++
	return true
}

// releaseIP decrements the per-IP counter at session teardown. The
// map entry is deleted at zero so a long-lived listener does not
// accumulate dead-IP entries.
func (s *Server) releaseIP(remoteIP string) {
	if s.opts.MaxConnectionsPerIP < 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.perIP == nil {
		return
	}
	s.perIP[remoteIP]--
	if s.perIP[remoteIP] <= 0 {
		delete(s.perIP, remoteIP)
	}
}

// remoteIPOf returns the IP portion of conn.RemoteAddr, or "-" when
// unknown. Used for per-IP bookkeeping and structured logging.
func remoteIPOf(c net.Conn) string {
	if c == nil {
		return "-"
	}
	addr := c.RemoteAddr()
	if addr == nil {
		return "-"
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return host
}

// NewServer constructs a Server. passwords / tokens may be nil, in which
// case the matching AUTH mechanism is refused at runtime. The server
// reaches the metadata layer through st.Meta() — Wave 3 promoted the
// previous local MailboxRepo seam onto store.Metadata.
func NewServer(
	st store.Store,
	dir *directory.Directory,
	tlsStore *heroldtls.Store,
	clk clock.Clock,
	logger *slog.Logger,
	passwords sasl.PasswordLookup,
	tokens sasl.TokenVerifier,
	opts Options,
) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	if clk == nil {
		clk = clock.NewReal()
	}
	if opts.IdleMaxDuration <= 0 {
		opts.IdleMaxDuration = 30 * time.Minute
	}
	if opts.DownloadBurstBytes <= 0 {
		opts.DownloadBurstBytes = opts.DownloadBytesPerSecond
	}
	if opts.ServerName == "" {
		opts.ServerName = "herold"
	}
	// REQ-PROTO-13/14, STANDARDS §9: bound MaxConnections by default.
	// A zero value means the operator did not configure the cap, so
	// fall back to a safe default. A negative value is the operator
	// explicitly opting out (front the listener with an external
	// limiter); leave it untouched.
	if opts.MaxConnections == 0 {
		opts.MaxConnections = defaultMaxConnections
	}
	if opts.MaxConnectionsPerIP == 0 {
		opts.MaxConnectionsPerIP = defaultMaxConnectionsPerIP
	}
	// Register the IMAP collector set on Server construction. Idempotent
	// across many Server instances sharing one process Registry (tests).
	observe.RegisterIMAPMetrics()
	observe.RegisterStoreMetrics()
	return &Server{
		store:     st,
		dir:       dir,
		tlsStore:  tlsStore,
		clk:       clk,
		logger:    logger,
		opts:      opts,
		passwords: passwords,
		tokens:    tokens,
		sessions:  make(map[*session]struct{}),
		perIP:     make(map[string]int),
	}
}

// Serve accepts connections from ln, spawning one goroutine per session,
// until ctx is canceled or ln.Close is called. Returns ctx.Err() on
// cancel; never returns nil.
func (s *Server) Serve(ctx context.Context, ln net.Listener, mode ListenerMode) error {
	// Clear any previously-installed accept deadline (the testharness sets
	// a short deadline on its stand-in accept loop before handing the
	// listener off to us).
	if tcp, ok := ln.(*net.TCPListener); ok {
		_ = tcp.SetDeadline(time.Time{})
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("protoimap: server closed")
	}
	s.listeners = append(s.listeners, ln)
	s.mu.Unlock()

	connCh := make(chan net.Conn)
	errCh := make(chan error, 1)
	go func() {
		defer close(connCh)
		for {
			c, err := ln.Accept()
			if err != nil {
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					continue
				}
				errCh <- err
				return
			}
			connCh <- c
		}
	}()

	useSem := s.opts.MaxConnections > 0
	var sem chan struct{}
	if useSem {
		sem = make(chan struct{}, s.opts.MaxConnections)
	}
	for {
		select {
		case <-ctx.Done():
			_ = ln.Close()
			return ctx.Err()
		case err := <-errCh:
			if errors.Is(err, net.ErrClosed) {
				return net.ErrClosed
			}
			return fmt.Errorf("protoimap: accept: %w", err)
		case c, ok := <-connCh:
			if !ok {
				return net.ErrClosed
			}
			remoteIP := remoteIPOf(c)
			// Per-IP cap. STANDARDS §9 + REQ-PROTO-13/14. Refuse
			// before reserving the global slot so a single attacker
			// cannot starve the global semaphore. IMAP has no
			// dedicated rate-limited greeting; a "* BYE" is the
			// closest spec-friendly close response.
			if !s.admitIP(remoteIP) {
				s.logger.Debug("protoimap: refusing connection (per-IP cap)",
					"activity", "access",
					"subsystem", "protoimap",
					"remote_addr", remoteIP,
				)
				_, _ = c.Write([]byte("* BYE Too many connections from your IP\r\n"))
				_ = c.Close()
				continue
			}
			if useSem {
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					s.releaseIP(remoteIP)
					_ = c.Close()
					_ = ln.Close()
					return ctx.Err()
				default:
					// Over global cap; reject politely.
					s.releaseIP(remoteIP)
					_, _ = c.Write([]byte("* BYE Too many connections\r\n"))
					_ = c.Close()
					continue
				}
			}
			s.wg.Add(1)
			go func(c net.Conn, rip string) {
				defer s.wg.Done()
				defer s.releaseIP(rip)
				defer func() {
					if useSem {
						<-sem
					}
				}()
				s.handle(ctx, c, mode)
			}(c, remoteIP)
		}
	}
}

func (s *Server) handle(ctx context.Context, c net.Conn, mode ListenerMode) {
	outcome := "ok"
	observe.IMAPSessionsActive.Inc()
	defer func() {
		if r := recover(); r != nil {
			outcome = "panic"
			s.logger.Error("protoimap: session panic",
				"activity", "internal",
				"subsystem", "protoimap",
				"remote_addr", remoteIPOf(c),
				"err", r,
			)
		}
		observe.IMAPSessionsTotal.WithLabelValues(outcome).Inc()
		observe.IMAPSessionsActive.Dec()
		_ = c.Close()
	}()
	var implicitLeaf *x509.Certificate
	if mode == ListenerModeImplicit993 {
		cap := sasl.NewCapturingTLSConfig(heroldtls.TLSConfig(s.tlsStore, heroldtls.Intermediate, nil))
		tlsConn := tls.Server(c, cap.Config())
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			outcome = "error"
			s.logger.Warn("protoimap: TLS handshake failed",
				"activity", "internal",
				"subsystem", "protoimap",
				"remote_addr", c.RemoteAddr().String(),
				"err", err,
			)
			return
		}
		c = tlsConn
		implicitLeaf = cap.Leaf()
	}
	ses := newSession(s, c, mode == ListenerModeImplicit993)
	if implicitLeaf != nil {
		if cb, err := sasl.TLSServerEndpoint(implicitLeaf); err == nil {
			ses.serverEndpoint = cb
		}
	}
	s.mu.Lock()
	s.sessions[ses] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.sessions, ses)
		s.mu.Unlock()
	}()
	ses.run(ctx)
}

// Close shuts the server down. Listeners close; in-flight sessions receive
// a cancelled context and drain within a bounded window.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	for _, ln := range s.listeners {
		_ = ln.Close()
	}
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
	return nil
}
