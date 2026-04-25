package protomanagesieve

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
	"github.com/hanshuebner/herold/internal/sasl"
	"github.com/hanshuebner/herold/internal/store"
	heroldtls "github.com/hanshuebner/herold/internal/tls"
)

// Options configures the ManageSieve listener. Zero values fall back
// to safe defaults; AllowPlainAuthWithoutTLS is intentionally never
// honoured by this listener — RFC 5804 §1.5 makes STARTTLS mandatory
// before AUTHENTICATE on the cleartext port.
type Options struct {
	// MaxConnections caps simultaneous sessions. Zero resolves to 256.
	// A negative value disables the cap (operator must front the
	// listener with an external limiter).
	MaxConnections int
	// MaxConnectionsPerIP caps simultaneous sessions per remote IP.
	// Zero resolves to 16. Negative disables the cap.
	MaxConnectionsPerIP int
	// MaxScriptBytes caps a single PUTSCRIPT / CHECKSCRIPT literal in
	// bytes. Zero resolves to 256 KiB; the RFC has no fixed bound but
	// the script we accept must round-trip through internal/sieve's
	// validator, which has its own per-script ceilings.
	MaxScriptBytes int64
	// IdleTimeout bounds reads between commands. Zero resolves to 5
	// minutes (RFC 5804 §1.5 leaves the value to the server).
	IdleTimeout time.Duration
	// ServerName is advertised in IMPLEMENTATION + greetings.
	ServerName string
}

// Defaults.
const (
	defaultMaxConnections      = 256
	defaultMaxConnectionsPerIP = 16
	defaultMaxScriptBytes      = 256 * 1024
	defaultIdleTimeout         = 5 * time.Minute
)

// Server is the ManageSieve listener. One *Server instance handles
// any number of listeners (Serve) but shares one Options + dependency
// set.
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

// NewServer constructs a Server. passwords / tokens may be nil, in
// which case the matching SASL mechanism is refused at runtime.
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
	if opts.MaxConnections == 0 {
		opts.MaxConnections = defaultMaxConnections
	}
	if opts.MaxConnectionsPerIP == 0 {
		opts.MaxConnectionsPerIP = defaultMaxConnectionsPerIP
	}
	if opts.MaxScriptBytes <= 0 {
		opts.MaxScriptBytes = defaultMaxScriptBytes
	}
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = defaultIdleTimeout
	}
	if opts.ServerName == "" {
		opts.ServerName = "herold"
	}
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

// Serve accepts connections from ln until ctx is cancelled or ln is
// closed. The ManageSieve listener is plaintext-on-accept; STARTTLS is
// the per-session TLS upgrade.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	if tcp, ok := ln.(*net.TCPListener); ok {
		_ = tcp.SetDeadline(time.Time{})
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("protomanagesieve: server closed")
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
			return fmt.Errorf("protomanagesieve: accept: %w", err)
		case c, ok := <-connCh:
			if !ok {
				return net.ErrClosed
			}
			rip := remoteIPOf(c)
			if !s.admitIP(rip) {
				_, _ = c.Write([]byte(`BYE "Too many connections from your IP"` + "\r\n"))
				_ = c.Close()
				continue
			}
			if useSem {
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					s.releaseIP(rip)
					_ = c.Close()
					_ = ln.Close()
					return ctx.Err()
				default:
					s.releaseIP(rip)
					_, _ = c.Write([]byte(`BYE "Too many connections"` + "\r\n"))
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
				s.handle(ctx, c)
			}(c, rip)
		}
	}
}

// handle wraps the per-session lifecycle with logging + perIP cleanup.
func (s *Server) handle(ctx context.Context, c net.Conn) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("protomanagesieve: session panic", "err", r)
		}
		_ = c.Close()
	}()
	ses := newSession(s, c, false)
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

// Close stops the listener and drains active sessions within a bounded
// window. Subsequent calls are no-ops.
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

// admitIP / releaseIP mirror the IMAP listener's per-IP bookkeeping.
func (s *Server) admitIP(remoteIP string) bool {
	if s.opts.MaxConnectionsPerIP < 0 {
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

// remoteIPOf returns the IP portion of c.RemoteAddr or "-" when
// unknown.
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

// upgradeTLS wraps c with a server TLS handshake using the configured
// store. Returns the upgraded connection, the captured leaf
// certificate (for tls-server-end-point binding), and any error.
func (s *Server) upgradeTLS(ctx context.Context, c net.Conn) (net.Conn, *x509.Certificate, error) {
	cap := sasl.NewCapturingTLSConfig(heroldtls.TLSConfig(s.tlsStore, heroldtls.Intermediate, nil))
	tlsConn := tls.Server(c, cap.Config())
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return nil, nil, err
	}
	return tlsConn, cap.Leaf(), nil
}
