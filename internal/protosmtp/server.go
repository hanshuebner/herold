package protosmtp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hanshuebner/herold/internal/categorise"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/mailarc"
	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/maildkim"
	"github.com/hanshuebner/herold/internal/maildmarc"
	"github.com/hanshuebner/herold/internal/mailspf"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/sasl"
	"github.com/hanshuebner/herold/internal/sieve"
	"github.com/hanshuebner/herold/internal/spam"
	"github.com/hanshuebner/herold/internal/store"
	heroldtls "github.com/hanshuebner/herold/internal/tls"
)

// ListenerMode distinguishes the three SMTP listener shapes the Server
// accepts. It drives EHLO advertisement (STARTTLS only for RelayIn +
// SubmissionSTARTTLS), whether AUTH is offered (submission only), and
// whether the listener wraps the conn in TLS at accept time
// (SubmissionImplicitTLS).
type ListenerMode int

const (
	// RelayIn is a port-25 relay-in listener. No AUTH offered, STARTTLS
	// advertised. Mail-auth (DKIM/SPF/DMARC/ARC) is evaluated after DATA.
	RelayIn ListenerMode = iota
	// SubmissionSTARTTLS is a port-587 submission listener. AUTH + STARTTLS
	// advertised; AUTH plain-text mechanisms refuse to run until the
	// session has upgraded to TLS.
	SubmissionSTARTTLS
	// SubmissionImplicitTLS is a port-465 submission listener. TLS is
	// negotiated immediately on accept; AUTH is advertised with all SASL
	// mechanisms enabled.
	SubmissionImplicitTLS
)

// String returns the lower-case identifier used in logs / metrics
// labels.
func (m ListenerMode) String() string {
	switch m {
	case RelayIn:
		return "relay_in"
	case SubmissionSTARTTLS:
		return "submission_starttls"
	case SubmissionImplicitTLS:
		return "submission_implicit_tls"
	default:
		return "unknown"
	}
}

// Options controls per-listener limits and behaviour. Zero values are
// replaced with the documented defaults below. The same Options value
// is shared across all listener modes served by one Server; per-listener
// overrides are not wired in Wave 2 (the struct is small and immutable,
// so passing different Options to different listeners is a Phase 2+
// addition).
type Options struct {
	// MaxConcurrentConnections bounds the per-Server goroutine count.
	// Default 1024. Must be positive; zero is replaced with the default.
	MaxConcurrentConnections int
	// MaxConcurrentPerIP bounds the simultaneous active connections from
	// one source IP. Default 16.
	MaxConcurrentPerIP int
	// MaxCommandsPerSession caps the total protocol commands one session
	// may issue before the server closes it with 421. Default 1000.
	MaxCommandsPerSession int
	// MaxRecipientsPerMessage caps RCPT TO count. Default 100.
	MaxRecipientsPerMessage int
	// MaxMessageSize is the SIZE advertisement value and the byte budget
	// the DATA / BDAT readers enforce before accepting. Default
	// 50 * 1024 * 1024 (50 MiB), matching mailparse's default.
	MaxMessageSize int64
	// ReadTimeout is the per-command read deadline. Default 30s.
	ReadTimeout time.Duration
	// WriteTimeout is the per-reply write deadline. Default 30s.
	WriteTimeout time.Duration
	// DataTimeout is the deadline for DATA / BDAT body bytes, separate
	// from ReadTimeout because bodies are potentially large. Default 5m.
	DataTimeout time.Duration
	// ShutdownGrace bounds how long Close waits for sessions to drain
	// before force-closing. Default 10s.
	ShutdownGrace time.Duration
	// Hostname is the value the server announces in greetings and
	// Received headers. Required; no default.
	Hostname string
	// AuthservID is the authserv-id emitted in Authentication-Results
	// per RFC 8601 §2.2. Falls back to Hostname when empty.
	AuthservID string
	// Greylist toggles greylist handling at RCPT time (REQ-PROTO-14).
	// Phase 1 exposes the toggle and defers the actual matcher to
	// Phase 2: when true and no GreylistHook has been set, the flag is
	// ignored. Default false.
	Greylist bool
	// GreylistHook is the per-envelope greylist matcher. Nil disables
	// greylisting regardless of the Greylist toggle. Phase 1 ships nil
	// by default; operators wire a real matcher in Phase 2.
	GreylistHook func(ctx context.Context, remoteIP, mailFrom, rcpt string) (defer_ bool)
	// RBLHook is the per-connection RBL lookup (REQ-PROTO-15). Nil
	// disables RBL checks. Phase 1 ships no hook so no RBLs are
	// configured out of the box; the seam keeps Phase 2 additive.
	RBLHook func(ctx context.Context, remoteIP string) (reject bool, reason string)
}

// Defaults returns a copy of o with zero fields filled in. Callers
// usually let the Server apply defaults internally by calling Fill.
func (o Options) Defaults() Options {
	if o.MaxConcurrentConnections <= 0 {
		o.MaxConcurrentConnections = 1024
	}
	if o.MaxConcurrentPerIP <= 0 {
		o.MaxConcurrentPerIP = 16
	}
	if o.MaxCommandsPerSession <= 0 {
		o.MaxCommandsPerSession = 1000
	}
	if o.MaxRecipientsPerMessage <= 0 {
		o.MaxRecipientsPerMessage = 100
	}
	if o.MaxMessageSize <= 0 {
		o.MaxMessageSize = 50 * 1024 * 1024
	}
	if o.ReadTimeout <= 0 {
		o.ReadTimeout = 30 * time.Second
	}
	if o.WriteTimeout <= 0 {
		o.WriteTimeout = 30 * time.Second
	}
	if o.DataTimeout <= 0 {
		o.DataTimeout = 5 * time.Minute
	}
	if o.ShutdownGrace <= 0 {
		o.ShutdownGrace = 10 * time.Second
	}
	if o.AuthservID == "" {
		o.AuthservID = o.Hostname
	}
	return o
}

// Server implements SMTP relay-in and submission on any of the three
// listener modes. One Server is shared across all listeners; Serve is
// called once per bound socket.
//
// Sieve scripts are loaded through store.Metadata.GetSieveScript on
// the Server's Store handle; the previous ScriptLoader seam was
// promoted onto the store.Metadata interface in Wave 3 so admin REST
// and ManageSieve converge on one storage surface.
type Server struct {
	store         store.Store
	dir           *directory.Directory
	dkim          *maildkim.Verifier
	spf           *mailspf.Verifier
	dmarc         *maildmarc.Evaluator
	arc           *mailarc.Verifier
	spam          *spam.Classifier
	sieve         *sieve.Interpreter
	categorise    *categorise.Categoriser
	tls           *heroldtls.Store
	resolver      mailauth.Resolver
	clk           clock.Clock
	log           *slog.Logger
	passLk        sasl.PasswordLookup
	opts          Options
	spamPlug      string
	rcptResolver  *directory.RcptResolver
	rcptPluginNm  string
	rcptPluginFor map[string]struct{} // domains where plugin runs first
	bouncePoster  BouncePoster
	subQueue      SubmissionQueue
	webhookDisp   WebhookDispatcher

	// lifecycle
	ctx       context.Context
	cancel    context.CancelFunc
	sessions  sync.WaitGroup
	connSem   chan struct{}
	mu        sync.Mutex
	perIP     map[string]int
	shutdown  atomic.Bool
	connCount atomic.Int64
	listeners []net.Listener
}

// Config bundles all dependencies required to construct a Server.
type Config struct {
	Store      store.Store
	Directory  *directory.Directory
	DKIM       *maildkim.Verifier
	SPF        *mailspf.Verifier
	DMARC      *maildmarc.Evaluator
	ARC        *mailarc.Verifier
	Spam       *spam.Classifier
	Sieve      *sieve.Interpreter
	Categorise *categorise.Categoriser
	TLS        *heroldtls.Store
	Resolver   mailauth.Resolver
	Clock      clock.Clock
	Logger     *slog.Logger
	// SCRAMLookup is the optional SCRAM credential source. When nil,
	// SCRAM mechanisms are absent from the advertised AUTH list.
	SCRAMLookup sasl.PasswordLookup
	// SpamPluginName is the configured plugin name passed to
	// Classifier.Classify. Defaults to "spam" when empty.
	SpamPluginName string
	// RcptResolver is the per-server RCPT-time directory resolver
	// (REQ-DIR-RCPT-01..12). Nil disables the path; the SMTP layer
	// then falls through to the in-process directory + catch-all
	// chain unchanged.
	RcptResolver *directory.RcptResolver
	// RcptPluginName is the configured plugin name. When empty the
	// resolve_rcpt path is disabled even if RcptResolver is non-nil.
	RcptPluginName string
	// RcptPluginFirstDomains is the lowercased set of recipient
	// domains for which the plugin is consulted BEFORE internal
	// directory lookup (REQ-DIR-RCPT-03 inversion). Domains outside
	// this set fall through to the standard "internal first, plugin
	// for non-local" flow.
	RcptPluginFirstDomains []string
	// BouncePoster enqueues a DSN bounce when the post-acceptance
	// attachment-policy walker (REQ-FLOW-ATTPOL-02) refuses a
	// recipient. Optional; nil collapses the post-acceptance refusal
	// to "drop, audit, no DSN" with a warn-level log line.
	BouncePoster BouncePoster
	Options      Options
}

// New constructs a Server. Logger and Clock default to slog.Default /
// clock.NewReal when nil; Hostname must be set on Options.
func New(cfg Config) (*Server, error) {
	if cfg.Store == nil {
		return nil, errors.New("protosmtp: nil Store")
	}
	if cfg.Directory == nil {
		return nil, errors.New("protosmtp: nil Directory")
	}
	if cfg.Options.Hostname == "" {
		return nil, errors.New("protosmtp: Options.Hostname required")
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	clk := cfg.Clock
	if clk == nil {
		clk = clock.NewReal()
	}
	plug := cfg.SpamPluginName
	if plug == "" {
		plug = "spam"
	}
	opts := cfg.Options.Defaults()
	// Register the SMTP collector set on Server construction. Idempotent
	// across many Server instances sharing one process Registry (tests).
	observe.RegisterSMTPMetrics()
	observe.RegisterStoreMetrics()
	ctx, cancel := context.WithCancel(context.Background())
	rcptFirst := make(map[string]struct{}, len(cfg.RcptPluginFirstDomains))
	for _, d := range cfg.RcptPluginFirstDomains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" {
			continue
		}
		rcptFirst[d] = struct{}{}
	}
	s := &Server{
		store:         cfg.Store,
		dir:           cfg.Directory,
		dkim:          cfg.DKIM,
		spf:           cfg.SPF,
		dmarc:         cfg.DMARC,
		arc:           cfg.ARC,
		spam:          cfg.Spam,
		sieve:         cfg.Sieve,
		categorise:    cfg.Categorise,
		tls:           cfg.TLS,
		resolver:      cfg.Resolver,
		clk:           clk,
		log:           log,
		passLk:        cfg.SCRAMLookup,
		spamPlug:      plug,
		opts:          opts,
		ctx:           ctx,
		cancel:        cancel,
		connSem:       make(chan struct{}, opts.MaxConcurrentConnections),
		perIP:         make(map[string]int),
		rcptResolver:  cfg.RcptResolver,
		rcptPluginNm:  cfg.RcptPluginName,
		rcptPluginFor: rcptFirst,
		bouncePoster:  cfg.BouncePoster,
	}
	// Register the inbound attachment-policy collector set; idempotent.
	observe.RegisterSMTPAttachmentPolicyMetrics()
	return s, nil
}

// SetBouncePoster installs the BouncePoster late, after Server
// construction. Used by the cmd/herold wiring where the outbound
// queue is built after the SMTP server (the queue depends on the
// store + resolver, the SMTP server only on the store, so the
// natural construction order is SMTP first). nil is permitted and
// clears any prior value.
//
// Concurrency: the BouncePoster pointer is read once per session
// after DATA accept, never inside a hot loop. Setting it post-
// construction is therefore safe under the standard Go memory model
// when no in-flight session is mid-DATA at the time of the call;
// production wiring sets it before the listener accepts its first
// connection.
func (s *Server) SetBouncePoster(b BouncePoster) {
	s.bouncePoster = b
}

// SetSubmissionQueue installs the outbound queue handle for the
// submission-listener path (Wave 3.1.6). When set, a non-local RCPT TO
// accepted on a SubmissionSTARTTLS / SubmissionImplicitTLS listener is
// enqueued via Submit at DATA-finish time so the outbound queue worker
// dials the smart-host / MX. nil is permitted and collapses the
// submission listener to "accept then drop" (the wave-3.1.6 pre-fix
// behaviour) — operators should set the queue before binding listeners.
//
// Concurrency: the handle is read once per session after DATA accept,
// never inside a hot loop. Setting it post-construction is safe under
// the same memory-model rules as SetBouncePoster.
func (s *Server) SetSubmissionQueue(q SubmissionQueue) {
	s.subQueue = q
}

// SetWebhookDispatcher installs the webhook dispatcher for synthetic-
// recipient deliveries (Wave 3.5c-Z, REQ-DIR-RCPT-07 + REQ-HOOK-02).
// When set, a synthetic RCPT accepted by the directory.resolve_rcpt
// plugin dispatches the parsed message to every matching subscription
// directly from the SMTP DATA-phase loop, bypassing the change-feed-
// driven principal-bound path. nil is permitted and collapses
// synthetic recipients to "accept and log" (the pre-3.5c-Z behaviour).
//
// Concurrency: same shape as SetBouncePoster; operators set this
// before listeners bind.
func (s *Server) SetWebhookDispatcher(d WebhookDispatcher) {
	s.webhookDisp = d
}

// HandleConn drives one already-accepted connection through the SMTP
// state machine under the given listener mode. The Server takes
// ownership of conn: the caller must not read or write after calling.
// Intended for harnesses that pre-accept a TCP connection (e.g. when
// handing over from a shim accept loop to the real server). Subject
// to the same concurrency / IP caps as Serve.
func (s *Server) HandleConn(conn net.Conn, mode ListenerMode) {
	if conn == nil {
		return
	}
	remoteIP := remoteIPOf(conn)
	if !s.admitIP(remoteIP) {
		_ = writeGreetline(conn, "421 4.7.0 too many concurrent connections from your IP\r\n", s.opts.WriteTimeout)
		_ = conn.Close()
		return
	}
	select {
	case s.connSem <- struct{}{}:
	default:
		s.releaseIP(remoteIP)
		_ = writeGreetline(conn, "421 4.7.0 server too busy\r\n", s.opts.WriteTimeout)
		_ = conn.Close()
		return
	}
	s.sessions.Add(1)
	s.connCount.Add(1)
	listenerLabel := mode.String()
	observe.SMTPSessionsActive.WithLabelValues(listenerLabel).Inc()
	go func() {
		outcome := "ok"
		defer func() {
			if rcv := recover(); rcv != nil {
				outcome = "panic"
				// Re-panic so the session-level recover (if any) sees it,
				// or the goroutine terminates loud rather than silent.
				observe.SMTPSessionsTotal.WithLabelValues(listenerLabel, outcome).Inc()
				observe.SMTPSessionsActive.WithLabelValues(listenerLabel).Dec()
				<-s.connSem
				s.releaseIP(remoteIP)
				s.sessions.Done()
				s.connCount.Add(-1)
				panic(rcv)
			}
			observe.SMTPSessionsTotal.WithLabelValues(listenerLabel, outcome).Inc()
			observe.SMTPSessionsActive.WithLabelValues(listenerLabel).Dec()
			<-s.connSem
			s.releaseIP(remoteIP)
			s.sessions.Done()
			s.connCount.Add(-1)
		}()
		var implicit *tls.Conn
		var implicitLeaf *x509.Certificate
		if mode == SubmissionImplicitTLS && s.tls != nil {
			cap := sasl.NewCapturingTLSConfig(heroldtls.TLSConfig(s.tls, heroldtls.Intermediate, nil))
			tlsConn := tls.Server(conn, cap.Config())
			if err := tlsConn.HandshakeContext(s.ctx); err != nil {
				outcome = "error"
				_ = tlsConn.Close()
				return
			}
			implicit = tlsConn
			implicitLeaf = cap.Leaf()
		}
		s.runSession(conn, mode, remoteIP, implicit, implicitLeaf)
	}()
}

// Serve accepts connections on ln and dispatches each to a fresh
// session goroutine. One Server.Serve call per listener; Serve blocks
// until ln.Close or the Server's ctx cancel.
//
// Accepts are gated by the connSem (global cap) and the per-IP map
// (per-source cap); when either is exhausted the conn is closed
// immediately with a 421. Returns ln.Accept's terminal error (wrapped
// as net.ErrClosed when the listener is intentionally closed).
func (s *Server) Serve(ctx context.Context, ln net.Listener, mode ListenerMode) error {
	if ln == nil {
		return errors.New("protosmtp: nil listener")
	}
	s.mu.Lock()
	s.listeners = append(s.listeners, ln)
	s.mu.Unlock()

	if mode == SubmissionImplicitTLS && s.tls == nil {
		return errors.New("protosmtp: SubmissionImplicitTLS requires TLS store")
	}
	// Accept loop.
	for {
		conn, err := ln.Accept()
		if err != nil {
			if s.shutdown.Load() || errors.Is(err, net.ErrClosed) {
				return nil
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return fmt.Errorf("protosmtp: accept: %w", err)
		}

		remoteIP := remoteIPOf(conn)

		// Per-IP cap check.
		if !s.admitIP(remoteIP) {
			s.log.InfoContext(ctx, "smtp connection refused (per-IP cap)",
				slog.String("activity", observe.ActivityAccess),
				slog.String("remote_ip", remoteIP),
				slog.String("mode", mode.String()))
			// Best-effort 421 emission before close.
			_ = writeGreetline(conn, "421 4.7.0 too many concurrent connections from your IP\r\n", s.opts.WriteTimeout)
			_ = conn.Close()
			continue
		}

		// Global concurrency semaphore: non-blocking; reject if at cap.
		select {
		case s.connSem <- struct{}{}:
		default:
			s.releaseIP(remoteIP)
			s.log.InfoContext(ctx, "smtp connection refused (server cap)",
				slog.String("activity", observe.ActivityAccess),
				slog.String("remote_ip", remoteIP),
				slog.String("mode", mode.String()))
			_ = writeGreetline(conn, "421 4.7.0 server too busy\r\n", s.opts.WriteTimeout)
			_ = conn.Close()
			continue
		}

		s.sessions.Add(1)
		s.connCount.Add(1)
		listenerLabel := mode.String()
		observe.SMTPSessionsActive.WithLabelValues(listenerLabel).Inc()
		go func(c net.Conn, rip string) {
			outcome := "ok"
			defer func() {
				if rcv := recover(); rcv != nil {
					outcome = "panic"
					observe.SMTPSessionsTotal.WithLabelValues(listenerLabel, outcome).Inc()
					observe.SMTPSessionsActive.WithLabelValues(listenerLabel).Dec()
					<-s.connSem
					s.releaseIP(rip)
					s.sessions.Done()
					s.connCount.Add(-1)
					panic(rcv)
				}
				observe.SMTPSessionsTotal.WithLabelValues(listenerLabel, outcome).Inc()
				observe.SMTPSessionsActive.WithLabelValues(listenerLabel).Dec()
				<-s.connSem
				s.releaseIP(rip)
				s.sessions.Done()
				s.connCount.Add(-1)
			}()
			// Wrap implicit-TLS listeners now so the session's TLS
			// bookkeeping is identical to a STARTTLS upgrade later.
			var startTLS *tls.Conn
			var startTLSLeaf *x509.Certificate
			if mode == SubmissionImplicitTLS {
				cap := sasl.NewCapturingTLSConfig(heroldtls.TLSConfig(s.tls, heroldtls.Intermediate, nil))
				tlsConn := tls.Server(c, cap.Config())
				// Force handshake now so we know TLS state before greeting.
				if err := tlsConn.HandshakeContext(s.ctx); err != nil {
					outcome = "error"
					s.log.InfoContext(s.ctx, "smtp implicit-tls handshake failed",
						slog.String("activity", observe.ActivityAccess),
						slog.String("remote_ip", rip),
						slog.String("err", err.Error()))
					_ = tlsConn.Close()
					return
				}
				startTLS = tlsConn
				startTLSLeaf = cap.Leaf()
			}
			s.runSession(c, mode, rip, startTLS, startTLSLeaf)
		}(conn, remoteIP)
	}
}

// remoteIPOf returns the IP portion of conn.RemoteAddr, or "-" when
// unknown. Used for per-IP bookkeeping + structured logging.
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

// admitIP records a new connection from remoteIP when the per-IP cap
// still has headroom. It returns false when the cap is exhausted and
// the caller should reject the conn.
func (s *Server) admitIP(remoteIP string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.perIP[remoteIP] >= s.opts.MaxConcurrentPerIP {
		return false
	}
	s.perIP[remoteIP]++
	return true
}

// releaseIP decrements the per-IP counter at session teardown.
func (s *Server) releaseIP(remoteIP string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.perIP[remoteIP]--
	if s.perIP[remoteIP] <= 0 {
		delete(s.perIP, remoteIP)
	}
}

// ActiveSessions reports the number of in-flight session goroutines.
// Exposed for tests and operator dashboards; not load-bearing.
func (s *Server) ActiveSessions() int64 {
	return s.connCount.Load()
}

// Close cancels the server ctx, closes all tracked listeners, and waits
// up to Options.ShutdownGrace for in-flight sessions to exit. Subsequent
// calls are no-ops.
func (s *Server) Close(ctx context.Context) error {
	if !s.shutdown.CompareAndSwap(false, true) {
		return nil
	}
	s.cancel()
	s.mu.Lock()
	for _, ln := range s.listeners {
		_ = ln.Close()
	}
	s.mu.Unlock()
	done := make(chan struct{})
	go func() {
		s.sessions.Wait()
		close(done)
	}()
	grace := s.opts.ShutdownGrace
	timer := s.clk.After(grace)
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer:
		return fmt.Errorf("protosmtp: %w: sessions did not drain in %s", context.DeadlineExceeded, grace)
	}
}

// writeGreetline writes a single-line reply with a write deadline. Used
// at accept-refusal time where we do not yet have a session to reach.
func writeGreetline(c net.Conn, line string, deadline time.Duration) error {
	_ = c.SetWriteDeadline(time.Now().Add(deadline))
	_, err := c.Write([]byte(line))
	_ = c.SetWriteDeadline(time.Time{})
	return err
}
