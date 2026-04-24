package protoimap

import (
	"context"
	"crypto/tls"
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
	// MaxConnections caps simultaneous sessions per listener. Zero means
	// unlimited.
	MaxConnections int
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
	wg        sync.WaitGroup
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

	sem := make(chan struct{}, s.opts.MaxConnections)
	useSem := s.opts.MaxConnections > 0
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
			if useSem {
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					_ = c.Close()
					_ = ln.Close()
					return ctx.Err()
				default:
					// Over cap; reject politely.
					_, _ = c.Write([]byte("* BYE Too many connections\r\n"))
					_ = c.Close()
					continue
				}
			}
			s.wg.Add(1)
			go func(c net.Conn) {
				defer s.wg.Done()
				defer func() {
					if useSem {
						<-sem
					}
				}()
				s.handle(ctx, c, mode)
			}(c)
		}
	}
}

func (s *Server) handle(ctx context.Context, c net.Conn, mode ListenerMode) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("protoimap: session panic", "err", r)
		}
		_ = c.Close()
	}()
	if mode == ListenerModeImplicit993 {
		cfg := heroldtls.TLSConfig(s.tlsStore, heroldtls.Intermediate, nil)
		tlsConn := tls.Server(c, cfg)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			s.logger.Warn("protoimap: TLS handshake", "err", err, "remote", c.RemoteAddr().String())
			return
		}
		c = tlsConn
	}
	ses := newSession(s, c, mode == ListenerModeImplicit993)
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
