package protochat

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/hanshuebner/herold/internal/auth"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// Options configures a Server. Fields with zero values fall through
// to the defaults documented per-field.
type Options struct {
	Store           store.Store
	Logger          *slog.Logger
	Clock           clock.Clock
	SessionResolver func(r *http.Request) (store.PrincipalID, bool)
	// Broadcaster is the in-process pub-sub the connection registers
	// with. Production wiring constructs one per process and passes
	// the same instance to track D's video-call package.
	Broadcaster *Broadcaster
	// Membership / Members callbacks plug in the chat store. Track B
	// owns the production implementations; tests inject closures.
	// Nil membership rejects every conversation-scoped frame with
	// not_a_member; nil members makes EmitToConversation a no-op.
	Membership MembershipResolver

	MaxConnections  int           // default 4096
	PerPrincipalCap int           // default 8
	WriteQueueSize  int           // default 256
	PingInterval    time.Duration // default 30s
	PongTimeout     time.Duration // default 60s
	WriteTimeout    time.Duration // default 10s
	MaxFrameBytes   int           // default 65536 (64 KiB)

	// TypingAutoStop is how long a typing.start lasts before the
	// server emits an implicit typing.stop on its behalf. Default
	// 10s. Reset by a fresh typing.start from the same connection.
	TypingAutoStop time.Duration

	// PresenceGrace is the disconnect-grace window before a
	// principal's presence transitions to offline. Default 30s.
	PresenceGrace time.Duration

	// AllowedOrigins is the closed set of Origin header values the
	// /chat/ws upgrade accepts. Each entry MUST be a full origin
	// including scheme (e.g. "https://mail.example.com"). An empty
	// list (the default) is interpreted as "same-origin only": the
	// server compares the Origin header's host (case-insensitively)
	// against the Request.Host. Mismatches return 403 + RFC 7807
	// problem detail before the WebSocket hijack.
	AllowedOrigins []string

	// AllowEmptyOrigin lets non-browser clients connect without an
	// Origin header. Default false: every Origin-less request is
	// rejected with 403 to mirror browser fetch policy.
	AllowEmptyOrigin bool

	// PeersResolver returns the set of principal ids that share at
	// least one Conversation membership with the publishing
	// principal. Used to scope presence.set fanout so a user's
	// presence is only delivered to people they actually chat with.
	// Nil resolver makes every fanout target the empty set, which
	// effectively disables presence broadcast — fail-closed if the
	// chat-store path is not wired.
	PeersResolver PeersResolver
}

// Server is the GET /chat/ws upgrade handler plus its inner
// connection bookkeeping. One *Server backs the whole chat
// ephemeral surface; the parent (internal/admin) constructs one and
// mounts Handler() under /chat/ws.
type Server struct {
	store       store.Store
	logger      *slog.Logger
	clk         clock.Clock
	resolve     func(r *http.Request) (store.PrincipalID, bool)
	broadcaster *Broadcaster
	membership  MembershipResolver
	presence    *PresenceTracker
	rateLimiter *rateLimiter

	maxConnections  int
	perPrincipalCap int
	writeQueueSize  int
	pingInterval    time.Duration
	pongTimeout     time.Duration
	writeTimeout    time.Duration
	maxFrameBytes   int
	typingAutoStop  time.Duration

	allowedOrigins   []string
	allowEmptyOrigin bool
	peers            PeersResolver

	connsMu     sync.Mutex
	conns       map[*chatConn]struct{}
	perPrinc    map[store.PrincipalID]int
	totalActive atomic.Int64

	handlersMu sync.RWMutex
	handlers   map[string]FrameHandler

	// ctx is the server-level lifecycle context; Shutdown cancels it
	// and every per-connection / per-presence-grace goroutine watches
	// for it. The cancel is captured so Shutdown can fire it
	// idempotently. STANDARDS §5: every async boundary observes a
	// context.
	ctx          context.Context
	cancel       context.CancelFunc
	shutdownMu   sync.Mutex
	shuttingDown bool

	// connWG tracks every in-flight chatConn.run() so Shutdown can
	// wait for connection drain after cancelling ctx.
	connWG sync.WaitGroup
}

// FrameHandler is the signature external packages register for a
// specific inbound frame type. The handler runs after rate-limit
// gating and JSON decoding have already succeeded; it owns its own
// payload validation, membership check, and response fanout.
//
// Track D's video-call package registers HandleSignal under the
// "call.signal" type so call-lifecycle bookkeeping (call.started /
// call.ended system messages) lives outside the chat ephemeral
// surface.
type FrameHandler func(ctx context.Context, fromPrincipal store.PrincipalID, frame ClientFrame)

// New constructs a Server. opts.Broadcaster MUST be non-nil; the
// rest fall through to defaults. The supplied Clock is shared with
// the presence tracker and the rate limiter so a FakeClock drives
// every time-dependent path in tests.
func New(opts Options) *Server {
	observe.RegisterProtochatMetrics()
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	clk := opts.Clock
	if clk == nil {
		clk = clock.NewReal()
	}
	if opts.MaxConnections <= 0 {
		opts.MaxConnections = 4096
	}
	if opts.PerPrincipalCap <= 0 {
		opts.PerPrincipalCap = 8
	}
	if opts.WriteQueueSize <= 0 {
		opts.WriteQueueSize = 256
	}
	if opts.PingInterval <= 0 {
		opts.PingInterval = 30 * time.Second
	}
	if opts.PongTimeout <= 0 {
		opts.PongTimeout = 60 * time.Second
	}
	if opts.WriteTimeout <= 0 {
		opts.WriteTimeout = 10 * time.Second
	}
	if opts.MaxFrameBytes <= 0 {
		opts.MaxFrameBytes = 64 * 1024
	}
	if opts.TypingAutoStop <= 0 {
		opts.TypingAutoStop = 10 * time.Second
	}
	if opts.PresenceGrace <= 0 {
		opts.PresenceGrace = 30 * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{
		store:            opts.Store,
		logger:           logger,
		clk:              clk,
		resolve:          opts.SessionResolver,
		broadcaster:      opts.Broadcaster,
		membership:       opts.Membership,
		presence:         NewPresenceTracker(clk, opts.PresenceGrace),
		rateLimiter:      newRateLimiter(clk, 60, 120),
		maxConnections:   opts.MaxConnections,
		perPrincipalCap:  opts.PerPrincipalCap,
		writeQueueSize:   opts.WriteQueueSize,
		pingInterval:     opts.PingInterval,
		pongTimeout:      opts.PongTimeout,
		writeTimeout:     opts.WriteTimeout,
		maxFrameBytes:    opts.MaxFrameBytes,
		typingAutoStop:   opts.TypingAutoStop,
		allowedOrigins:   normaliseOrigins(opts.AllowedOrigins),
		allowEmptyOrigin: opts.AllowEmptyOrigin,
		peers:            opts.PeersResolver,
		conns:            make(map[*chatConn]struct{}),
		perPrinc:         make(map[store.PrincipalID]int),
		ctx:              ctx,
		cancel:           cancel,
	}
}

// Shutdown drains the chat server: it cancels the server-level
// context (so every in-flight connection's read/write pumps,
// presence-grace timers, and typing-auto-stop goroutines unblock),
// then waits up to the supplied ctx's deadline for the per-connection
// run() goroutines and the presence-tracker's grace-period goroutines
// to exit. Returns ctx.Err() if the deadline expires before drain.
//
// Idempotent: a second call returns immediately with nil.
func (s *Server) Shutdown(ctx context.Context) error {
	s.shutdownMu.Lock()
	if s.shuttingDown {
		s.shutdownMu.Unlock()
		return nil
	}
	s.shuttingDown = true
	s.shutdownMu.Unlock()

	if s.cancel != nil {
		s.cancel()
	}
	// Pull every connection's read/write pump down. shutdown() is
	// idempotent so a connection already on its way out is unaffected.
	s.connsMu.Lock()
	conns := make([]*chatConn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.connsMu.Unlock()
	for _, c := range conns {
		c.shutdown(closeGoingAway, "server shutdown")
	}

	// Stop every pending presence-grace timer so Wait() doesn't block
	// on real-clock waiters that would otherwise only fire at graceWindow.
	s.presence.Drain()

	done := make(chan struct{})
	go func() {
		s.connWG.Wait()
		s.presence.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Presence returns the server's presence tracker. Exposed for tests
// and for the (future) admin-facing presence inspector.
func (s *Server) Presence() *PresenceTracker { return s.presence }

// RegisterHandler installs h as the handler for inbound frames of the
// given type, replacing the built-in handler (if any). Re-registering
// the same type overwrites the previous handler.
//
// Cross-package use: protocall registers HandleSignal under
// "call.signal" so the chat dispatcher's built-in call.signal
// forwarder is replaced with the lifecycle-aware version that writes
// call.started / call.ended system messages.
func (s *Server) RegisterHandler(frameType string, h FrameHandler) error {
	if frameType == "" {
		return errors.New("protochat: empty frameType")
	}
	if h == nil {
		return errors.New("protochat: nil FrameHandler")
	}
	s.handlersMu.Lock()
	if s.handlers == nil {
		s.handlers = make(map[string]FrameHandler)
	}
	s.handlers[frameType] = h
	s.handlersMu.Unlock()
	return nil
}

// lookupHandler returns the externally-registered handler for the
// given frame type, or nil if none is registered.
func (s *Server) lookupHandler(frameType string) FrameHandler {
	s.handlersMu.RLock()
	defer s.handlersMu.RUnlock()
	if s.handlers == nil {
		return nil
	}
	return s.handlers[frameType]
}

// Handler returns the http.Handler that performs the GET /chat/ws
// upgrade. Mount it on the parent mux under the "/chat/ws" path.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.handleUpgrade)
}

// websocketGUID is the protocol-mandated suffix for the
// Sec-WebSocket-Accept hash (RFC 6455 §1.3).
const websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// computeAcceptKey derives the Sec-WebSocket-Accept value the
// server returns for a given Sec-WebSocket-Key. SHA-1 here is
// protocol-mandated (not a security primitive).
func computeAcceptKey(key string) string {
	h := sha1.New()
	h.Write([]byte(key))
	h.Write([]byte(websocketGUID))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// handleUpgrade resolves the suite session, validates the WebSocket
// upgrade headers, hijacks the connection, and hands off to the
// per-connection read/write pumps.
func (s *Server) handleUpgrade(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	pid := store.PrincipalID(0)
	if s.resolve != nil {
		var ok bool
		pid, ok = s.resolve(r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	} else {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// REQ-AUTH-SCOPE-02: chat.read scope is required for the
	// WebSocket upgrade. write happens within the WebSocket via
	// per-frame action authorisation; the upgrade gate here keeps
	// admin-only cookies from opening a chat connection.
	if actx := auth.FromContext(r.Context()); actx != nil {
		if err := auth.RequireScope(r.Context(), auth.ScopeChatRead); err != nil {
			http.Error(w, "forbidden: "+err.Error(), http.StatusForbidden)
			return
		}
	}

	if !validateUpgradeHeaders(r) {
		http.Error(w, "bad websocket upgrade", http.StatusBadRequest)
		return
	}
	// Origin check (CSWSH defence). Runs after the upgrade-header
	// shape check so a client that issued a plain GET is rejected
	// with 400 first; an actual upgrade with a hostile Origin gets
	// 403 + RFC 7807 problem detail before we hijack.
	if reason, ok := s.checkOrigin(r); !ok {
		writeOriginProblem(w, reason)
		return
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		http.Error(w, "missing Sec-WebSocket-Key", http.StatusBadRequest)
		return
	}

	// Connection caps. Enforced before the hijack so the rejection
	// goes back as an HTTP 503; once hijacked we'd have to close on
	// a websocket close frame, which clients dislike on the very
	// first byte.
	if !s.tryReserve(pid) {
		http.Error(w, "too many connections", http.StatusServiceUnavailable)
		return
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		s.releaseReservation(pid)
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	netConn, brw, err := hj.Hijack()
	if err != nil {
		s.releaseReservation(pid)
		http.Error(w, "hijack failed", http.StatusInternalServerError)
		return
	}

	// Register the connection with the broadcaster BEFORE flushing the
	// 101 handshake. A client that observes the handshake response and
	// immediately starts sending frames (or whose peer expects to
	// receive a fanout to it the instant it connects) must not race
	// with broadcaster registration: any frame addressed to pid that
	// arrives between the client unblocking and Register completing
	// would be silently dropped because byPrinc[pid] would still be
	// empty. Registering first makes h.connect synchronization tight.
	cc := s.newChatConn(pid, netConn, brw.Reader)
	s.connsMu.Lock()
	s.conns[cc] = struct{}{}
	s.connsMu.Unlock()
	observe.ProtochatConnectionsTotal.Inc()
	observe.ProtochatConnectionsCurrent.Inc()
	cc.id = s.broadcaster.Register(pid, cc)
	s.presence.CancelOffline(pid)

	// Write the 101 handshake. brw is the buffered writer the http
	// server handed us; we flush after writing to ensure the
	// response lands before the codec starts. If the handshake fails
	// to flush, undo the broadcaster registration so a half-open
	// connection does not appear active.
	accept := computeAcceptKey(strings.TrimSpace(key))
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := brw.WriteString(resp); err != nil {
		s.unregisterChatConn(cc)
		s.releaseReservation(pid)
		_ = netConn.Close()
		return
	}
	if err := brw.Flush(); err != nil {
		s.unregisterChatConn(cc)
		s.releaseReservation(pid)
		_ = netConn.Close()
		return
	}

	// Track the connection's run() lifetime on the server-level
	// WaitGroup so Shutdown can wait for it to drain. The Add must
	// happen before run() to be observable by Shutdown's wait. We
	// derive cc.ctx from the merged (server, request) context so a
	// server-wide cancel drains every connection independently of the
	// http.Server's own shutdown.
	s.connWG.Add(1)
	defer s.connWG.Done()

	connCtx, connCancel := mergedContext(s.ctx, r.Context())
	defer connCancel()
	cc.run(connCtx)

	s.unregisterChatConn(cc)
	s.releaseReservation(pid)

	// Disconnect-grace: if this was the last connection for pid,
	// schedule a transition to offline; a reconnect within the
	// grace window cancels it via Set / CancelOffline above. The
	// server-level ctx scopes the grace goroutine so Shutdown drains
	// it.
	if !s.broadcaster.HasConnection(pid) {
		s.presence.ScheduleOffline(s.ctx, pid, func(now time.Time) {
			s.emitPresence(pid, "offline", now)
		})
	}
}

// unregisterChatConn rolls back the broadcaster + connection-tracking
// state set up in handleUpgrade. Used both on the failure path before
// run() starts (handshake flush failure) and in the normal teardown
// after run() returns. Idempotent because broadcaster.Unregister is.
func (s *Server) unregisterChatConn(cc *chatConn) {
	s.broadcaster.Unregister(cc.id)
	s.connsMu.Lock()
	delete(s.conns, cc)
	s.connsMu.Unlock()
	observe.ProtochatConnectionsCurrent.Dec()
}

// mergedContext returns a context that cancels when either parent
// cancels. The returned cancel must be called to release the merging
// goroutine. Used to fold the server-level shutdown ctx into the
// per-request ctx without forcing run() to know about both.
func mergedContext(a, b context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(a)
	stop := make(chan struct{})
	go func() {
		select {
		case <-b.Done():
			cancel()
		case <-stop:
		}
	}()
	return ctx, func() {
		close(stop)
		cancel()
	}
}

// validateUpgradeHeaders enforces the inbound-handshake half of RFC
// 6455 §4.1. We are strict: missing or non-matching values get
// rejected with 400 rather than papered over.
func validateUpgradeHeaders(r *http.Request) bool {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return false
	}
	conn := r.Header.Get("Connection")
	if !headerContains(conn, "upgrade") {
		return false
	}
	if r.Header.Get("Sec-WebSocket-Version") != "13" {
		return false
	}
	return true
}

// headerContains performs a case-insensitive token search on a
// comma-separated header value. The Connection header may carry
// multiple tokens (e.g. "Upgrade, keep-alive"); we accept any
// ordering.
func headerContains(value, token string) bool {
	for _, part := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(part), token) {
			return true
		}
	}
	return false
}

// tryReserve atomically increments the connection counters if both
// the global and per-principal caps still admit one more. Returns
// false if either cap would be exceeded.
func (s *Server) tryReserve(pid store.PrincipalID) bool {
	s.connsMu.Lock()
	defer s.connsMu.Unlock()
	if int(s.totalActive.Load())+1 > s.maxConnections {
		return false
	}
	if s.perPrinc[pid]+1 > s.perPrincipalCap {
		return false
	}
	s.perPrinc[pid]++
	s.totalActive.Add(1)
	return true
}

// releaseReservation undoes a tryReserve.
func (s *Server) releaseReservation(pid store.PrincipalID) {
	s.connsMu.Lock()
	defer s.connsMu.Unlock()
	if s.perPrinc[pid] > 0 {
		s.perPrinc[pid]--
	}
	if s.perPrinc[pid] == 0 {
		delete(s.perPrinc, pid)
	}
	s.totalActive.Add(-1)
}

// emitPresence fans out a presence-update frame to the set of
// principals that share at least one Conversation membership with the
// publisher. A nil PeersResolver collapses the target set to empty —
// fail-closed when the chat-store path is not wired.
func (s *Server) emitPresence(pid store.PrincipalID, state string, now time.Time) {
	s.presence.Set(pid, state, now)
	payload, err := json.Marshal(outboundPresence{
		PrincipalID: pid,
		State:       state,
		LastSeenAt:  now.Unix(),
	})
	if err != nil {
		s.logger.Warn("protochat.presence.marshal_failed", "err", err.Error())
		return
	}
	frame := ServerFrame{
		Type:    ServerTypePresence,
		Payload: payload,
	}
	if s.peers == nil {
		// No PeersResolver: refuse to broadcast rather than leaking
		// presence to every connected principal.
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	peers, perr := s.peers(ctx, pid)
	if perr != nil {
		s.logger.Warn("protochat.presence.peers_lookup_failed",
			"pid", uint64(pid),
			"err", perr.Error())
		return
	}
	for _, peer := range peers {
		if peer == pid {
			// Don't bounce the publisher's own presence back at them.
			continue
		}
		s.broadcaster.Emit(peer, frame)
	}
}

// chatConn is a single WebSocket connection. The read pump consumes
// frames off netConn and dispatches to the handler; the write pump
// drains writeQ to netConn. Both run under cc.ctx so a parent
// cancellation drains the connection.
type chatConn struct {
	srv       *Server
	id        ConnID
	pid       store.PrincipalID
	netConn   net.Conn
	reader    *bufio.Reader
	writeQ    chan ServerFrame
	closeOnce sync.Once
	closeCh   chan struct{}

	ctx    context.Context
	cancel context.CancelFunc

	// last pong instant — read-pump updates on every pong frame,
	// write-pump consults to police the heartbeat timeout. Stored
	// as Unix nanoseconds so atomic.Int64 suffices.
	lastPong atomic.Int64

	// per-conversation typing-stop timers. Cancelled and replaced
	// on a fresh typing.start; deleted on typing.stop. The timers
	// are scheduled via Clock.AfterFunc so the registration is
	// synchronous with handleTyping — a test that drives the
	// FakeClock can rely on the timer being known to the clock the
	// instant the typing.start fanout becomes observable.
	typingMu    sync.Mutex
	typingStops map[string]clock.Timer
}

// newChatConn constructs a chatConn. The returned conn is not yet
// running; the caller must call run(ctx).
func (s *Server) newChatConn(pid store.PrincipalID, netConn net.Conn, reader *bufio.Reader) *chatConn {
	return &chatConn{
		srv:         s,
		pid:         pid,
		netConn:     netConn,
		reader:      reader,
		writeQ:      make(chan ServerFrame, s.writeQueueSize),
		closeCh:     make(chan struct{}),
		typingStops: make(map[string]clock.Timer),
	}
}

// Send implements Sender. Non-blocking: returns ErrFull if the
// queue is saturated; in that case the caller (the broadcaster)
// drops the frame and logs.
func (c *chatConn) Send(f ServerFrame) error {
	select {
	case c.writeQ <- f:
		observe.ProtochatFramesOutTotal.WithLabelValues(metricFrameTypeOut(f.Type)).Inc()
		return nil
	default:
		// Queue full — close the connection so the client
		// reconnects rather than receiving a partial frame stream.
		observe.ProtochatBackpressureDropsTotal.Inc()
		c.shutdown(closeMessageTooBig, "send queue full")
		return ErrFull
	}
}

// Principal implements Sender.
func (c *chatConn) Principal() store.PrincipalID { return c.pid }

// run blocks until the connection terminates. The ctx parameter is
// the request context (ties to the http.Server lifetime); cc.ctx is
// derived so an internal shutdown also fires through cancel().
func (c *chatConn) run(ctx context.Context) {
	c.ctx, c.cancel = context.WithCancel(ctx)
	defer c.cancel()
	c.lastPong.Store(c.srv.clk.Now().UnixNano())

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		c.readPump()
	}()
	go func() {
		defer wg.Done()
		c.writePump()
	}()
	wg.Wait()
	_ = c.netConn.Close()
}

// shutdown closes the connection with the given code/reason. Safe
// to call from any goroutine; idempotent.
func (c *chatConn) shutdown(code closeCode, reason string) {
	c.closeOnce.Do(func() {
		observe.ProtochatCloseTotal.WithLabelValues(metricCloseCode(code)).Inc()
		// Best-effort close frame; if the peer is gone the write
		// fails and we proceed. We deliberately don't queue this
		// through writeQ — backpressure-driven shutdowns may have
		// a full queue, and the close frame must reach the peer.
		_ = writeCloseFrame(c.netConn, code, reason)
		close(c.closeCh)
		if c.cancel != nil {
			c.cancel()
		}
	})
}

// writePump drains writeQ to the wire. Heartbeat ping is interleaved
// here so a slow client (no pong) is detected without a third
// goroutine.
func (c *chatConn) writePump() {
	pingTimer := c.srv.clk.After(c.srv.pingInterval)
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-c.closeCh:
			return
		case f := <-c.writeQ:
			body, err := json.Marshal(f)
			if err != nil {
				c.srv.logger.Warn("protochat.write.marshal_failed",
					"err", err.Error(),
					"type", f.Type)
				continue
			}
			// Write deadline policed via SetWriteDeadline: a slow
			// peer that stops draining its TCP receive buffer must
			// not pin the writer indefinitely. Deadline derives from
			// the (real wall-clock) time.Now because SetDeadline on a
			// net.Conn rides the system clock; the FakeClock only
			// drives the application-layer ping/pong cadence.
			_ = c.netConn.SetWriteDeadline(time.Now().Add(c.srv.writeTimeout))
			if err := writeFrame(c.netConn, frame{
				fin:     true,
				opcode:  opText,
				payload: body,
			}); err != nil {
				c.shutdown(closePolicyViolation, "write failed")
				return
			}
		case <-pingTimer:
			pingTimer = c.srv.clk.After(c.srv.pingInterval)
			now := c.srv.clk.Now()
			lastPongNs := c.lastPong.Load()
			if lastPongNs > 0 {
				lastPong := time.Unix(0, lastPongNs)
				if now.Sub(lastPong) > c.srv.pongTimeout {
					c.srv.logger.Info("protochat.heartbeat.timeout",
						"pid", uint64(c.pid))
					c.shutdown(closePolicyViolation, "pong timeout")
					return
				}
			}
			_ = c.netConn.SetWriteDeadline(time.Now().Add(c.srv.writeTimeout))
			if err := writeFrame(c.netConn, frame{
				fin:     true,
				opcode:  opPing,
				payload: nil,
			}); err != nil {
				c.shutdown(closePolicyViolation, "ping write failed")
				return
			}
		}
	}
}

// readPump consumes frames from the wire until the connection
// closes. Application frames are dispatched through dispatchClient;
// control frames are handled inline.
func (c *chatConn) readPump() {
	defer c.shutdown(closeNormalClosure, "")
	var fragBuf []byte
	var fragOp byte
	// Read deadline policed via SetReadDeadline: a silent peer that
	// stops sending pongs must be evicted within pongTimeout regardless
	// of TCP keepalive. The deadline is reset on every successful frame
	// read AND inside the pong handler when the heartbeat extends.
	_ = c.netConn.SetReadDeadline(time.Now().Add(c.srv.pongTimeout))
	for {
		f, err := readFrame(c.reader, true, c.srv.maxFrameBytes)
		if err != nil {
			if errors.Is(err, errFrameUnmaskedClient) ||
				errors.Is(err, errFrameRSVBitsSet) ||
				errors.Is(err, errFrameBadOpcode) ||
				errors.Is(err, errFrameBadControl) {
				c.shutdown(closeProtocolError, err.Error())
				return
			}
			if errors.Is(err, errFrameTooLarge) {
				c.shutdown(closeMessageTooBig, "frame too large")
				return
			}
			// Read-deadline timeout: classify as a policy violation
			// (RFC 6455 §7.4 1008) so the client knows to back off
			// rather than reconnecting tight.
			var nerr net.Error
			if errors.As(err, &nerr) && nerr.Timeout() {
				c.shutdown(closePolicyViolation, "read deadline exceeded")
				return
			}
			// Network error or peer closed; just exit.
			return
		}
		// Successful frame read: extend the read deadline so a slow
		// but live peer is not killed mid-stream.
		_ = c.netConn.SetReadDeadline(time.Now().Add(c.srv.pongTimeout))
		switch f.opcode {
		case opPing:
			// Reply with a pong carrying the same payload (RFC 6455 §5.5.3).
			_ = c.netConn.SetWriteDeadline(time.Now().Add(c.srv.writeTimeout))
			_ = writeFrame(c.netConn, frame{fin: true, opcode: opPong, payload: f.payload})
		case opPong:
			c.lastPong.Store(c.srv.clk.Now().UnixNano())
			_ = c.netConn.SetReadDeadline(time.Now().Add(c.srv.pongTimeout))
		case opClose:
			code, reason := decodeCloseFrame(f.payload)
			c.srv.logger.Debug("protochat.close",
				"pid", uint64(c.pid),
				"code", uint16(code),
				"reason", reason)
			return
		case opText, opContinuation, opBinary:
			// Reassemble fragmented messages. fragOp is set on the
			// first fragment and cleared when fin lands.
			if f.opcode != opContinuation {
				if fragOp != 0 {
					c.shutdown(closeProtocolError, "unexpected opcode mid-fragment")
					return
				}
				fragOp = f.opcode
				fragBuf = nil
			}
			fragBuf = append(fragBuf, f.payload...)
			if !f.fin {
				continue
			}
			if fragOp == opBinary {
				c.shutdown(closeUnsupportedData, "binary frames not supported")
				return
			}
			c.dispatchText(fragBuf)
			fragBuf = nil
			fragOp = 0
		}
	}
}

// dispatchText handles one fully-reassembled text message: rate-
// limit gate, JSON decode, and dispatch to the type-specific
// handler. Errors from the handler turn into "error" frames sent
// back to the client; the connection survives.
func (c *chatConn) dispatchText(body []byte) {
	// RFC 6455 §8.1: text-frame payload MUST be valid UTF-8. The
	// server MUST close with code 1007 on receipt of invalid bytes.
	if !utf8.Valid(body) {
		c.shutdown(closeInvalidPayload, "invalid utf-8 in text frame")
		return
	}
	var inFrame ClientFrame
	if err := json.Unmarshal(body, &inFrame); err != nil {
		c.send(makeError(ErrCodeBadFrame, "invalid JSON envelope", ""))
		return
	}
	observe.ProtochatFramesInTotal.WithLabelValues(metricFrameTypeIn(inFrame.Type)).Inc()
	if !c.srv.rateLimiter.allow(c.pid, frameWeight(inFrame.Type)) {
		c.send(makeError(ErrCodeRateLimited, "rate limit exceeded", inFrame.ClientID))
		observe.ProtochatRatelimitDropsTotal.WithLabelValues(metricFrameTypeIn(inFrame.Type)).Inc()
		return
	}
	if h := c.srv.lookupHandler(inFrame.Type); h != nil {
		h(c.ctx, c.pid, inFrame)
		return
	}
	switch inFrame.Type {
	case clientTypeTypingStart:
		c.handleTyping(inFrame, true)
	case clientTypeTypingStop:
		c.handleTyping(inFrame, false)
	case clientTypePresenceSet:
		c.handlePresence(inFrame)
	case clientTypeSubscribe:
		c.handleSubscribe(inFrame, true)
	case clientTypeUnsubscribe:
		c.handleSubscribe(inFrame, false)
	case clientTypeCallSignal:
		c.handleCallSignal(inFrame)
	case clientTypePing:
		c.send(ServerFrame{Type: ServerTypePong, Ack: inFrame.ClientID})
	default:
		c.send(makeError(ErrCodeUnknownType,
			fmt.Sprintf("unknown frame type %q", inFrame.Type),
			inFrame.ClientID))
	}
}

// send is a convenience wrapper around Send that swallows ErrFull
// (the conn is already shutting down).
func (c *chatConn) send(f ServerFrame) {
	_ = c.Send(f)
}

// handleTyping fans out a typing.start / typing.stop signal to the
// other members of the conversation. start=true emits "start" with a
// 10s auto-stop timer; start=false emits "stop" and cancels any
// pending auto-stop.
func (c *chatConn) handleTyping(in ClientFrame, start bool) {
	var p typingPayload
	if err := json.Unmarshal(in.Payload, &p); err != nil || p.ConversationID == "" {
		c.send(makeError(ErrCodeInvalid, "missing conversationId", in.ClientID))
		return
	}
	if !c.checkMembership(p.ConversationID) {
		c.send(makeError(ErrCodeNotMember, "not a member of conversation", in.ClientID))
		return
	}
	state := "start"
	if !start {
		state = "stop"
	}
	body, err := json.Marshal(outboundTyping{
		ConversationID:    p.ConversationID,
		SenderPrincipalID: c.pid,
		State:             state,
	})
	if err != nil {
		c.send(makeError(ErrCodeInvalid, "marshal", in.ClientID))
		return
	}
	// Schedule the auto-stop BEFORE fanning the typing.start out so
	// the timer is registered with the clock by the time observers can
	// see the start frame. Tests that drive the FakeClock can then
	// Advance past the auto-stop window without racing against an
	// unregistered waiter.
	if start {
		c.scheduleTypingAutoStop(p.ConversationID)
	} else {
		c.cancelTypingAutoStop(p.ConversationID)
	}
	c.srv.broadcaster.EmitToConversation(c.ctx, p.ConversationID, c.pid, ServerFrame{
		Type:    ServerTypeTyping,
		Payload: body,
	})
	if in.ClientID != "" {
		c.send(makeAck(in.ClientID))
	}
}

// scheduleTypingAutoStop replaces any existing auto-stop timer for
// conv with a fresh one. If the timer fires before another
// typing.start lands, the server emits a server-generated
// typing.stop on behalf of the connection. Registration is
// synchronous (Clock.AfterFunc) so the timer is observable by the
// clock the instant this returns.
func (c *chatConn) scheduleTypingAutoStop(conv string) {
	c.typingMu.Lock()
	if existing, ok := c.typingStops[conv]; ok {
		existing.Stop()
	}
	pid := c.pid
	clk := c.srv.clk
	bcast := c.srv.broadcaster
	connCtx := c.ctx
	var thisTimer clock.Timer
	thisTimer = clk.AfterFunc(c.srv.typingAutoStop, func() {
		// If the connection has shut down, drop the fanout; the
		// peer has already seen typing stop via the close.
		if connCtx != nil && connCtx.Err() != nil {
			c.typingMu.Lock()
			if cur, ok := c.typingStops[conv]; ok && cur == thisTimer {
				delete(c.typingStops, conv)
			}
			c.typingMu.Unlock()
			return
		}
		body, err := json.Marshal(outboundTyping{
			ConversationID:    conv,
			SenderPrincipalID: pid,
			State:             "stop",
		})
		if err != nil {
			return
		}
		bcast.EmitToConversation(connCtx, conv, pid, ServerFrame{
			Type:    ServerTypeTyping,
			Payload: body,
		})
		c.typingMu.Lock()
		if cur, ok := c.typingStops[conv]; ok && cur == thisTimer {
			delete(c.typingStops, conv)
		}
		c.typingMu.Unlock()
	})
	c.typingStops[conv] = thisTimer
	c.typingMu.Unlock()
}

// cancelTypingAutoStop drops a pending auto-stop for conv, if any.
func (c *chatConn) cancelTypingAutoStop(conv string) {
	c.typingMu.Lock()
	timer, ok := c.typingStops[conv]
	if ok {
		delete(c.typingStops, conv)
	}
	c.typingMu.Unlock()
	if ok {
		timer.Stop()
	}
}

// handlePresence records the new presence state and fans out a
// presence frame to every subscriber.
func (c *chatConn) handlePresence(in ClientFrame) {
	var p presencePayload
	if err := json.Unmarshal(in.Payload, &p); err != nil {
		c.send(makeError(ErrCodeInvalid, "missing state", in.ClientID))
		return
	}
	if _, ok := validPresenceStates[p.State]; !ok {
		c.send(makeError(ErrCodeInvalid,
			fmt.Sprintf("invalid presence state %q", p.State),
			in.ClientID))
		return
	}
	now := c.srv.clk.Now()
	c.srv.emitPresence(c.pid, p.State, now)
	if in.ClientID != "" {
		c.send(makeAck(in.ClientID))
	}
}

// handleSubscribe registers / removes the connection's interest in
// the supplied conversation ids. Membership is validated per id;
// non-member ids are dropped silently from the subscribe set and
// included in the error frame for the client to surface.
func (c *chatConn) handleSubscribe(in ClientFrame, add bool) {
	var p subscribePayload
	if err := json.Unmarshal(in.Payload, &p); err != nil {
		c.send(makeError(ErrCodeInvalid, "missing conversationIds", in.ClientID))
		return
	}
	allowed := make([]string, 0, len(p.ConversationIDs))
	denied := make([]string, 0)
	for _, conv := range p.ConversationIDs {
		if c.checkMembership(conv) {
			allowed = append(allowed, conv)
		} else {
			denied = append(denied, conv)
		}
	}
	if add {
		c.srv.broadcaster.addSubscriptions(c.id, allowed)
	} else {
		c.srv.broadcaster.removeSubscriptions(c.id, allowed)
	}
	if len(denied) > 0 {
		c.send(makeError(ErrCodeNotMember,
			fmt.Sprintf("not a member of: %s", strings.Join(denied, ",")),
			in.ClientID))
		return
	}
	if in.ClientID != "" {
		c.send(makeAck(in.ClientID))
	}
}

// handleCallSignal forwards a WebRTC signalling envelope to the
// targeted principal's connections, after validating that the
// originator is a member of the conversation.
func (c *chatConn) handleCallSignal(in ClientFrame) {
	var p callSignalPayload
	if err := json.Unmarshal(in.Payload, &p); err != nil || p.ConversationID == "" || p.Kind == "" {
		c.send(makeError(ErrCodeInvalid, "malformed call.signal", in.ClientID))
		return
	}
	if !c.checkMembership(p.ConversationID) {
		c.send(makeError(ErrCodeNotMember, "not a member of conversation", in.ClientID))
		return
	}
	body, err := json.Marshal(outboundCallSignal{
		ConversationID:  p.ConversationID,
		Kind:            p.Kind,
		Payload:         p.Payload,
		FromPrincipalID: c.pid,
	})
	if err != nil {
		c.send(makeError(ErrCodeInvalid, "marshal", in.ClientID))
		return
	}
	out := ServerFrame{Type: ServerTypeCallSignal, Payload: body}
	if p.TargetID != 0 {
		c.srv.broadcaster.Emit(p.TargetID, out)
	} else {
		c.srv.broadcaster.EmitToConversation(c.ctx, p.ConversationID, c.pid, out)
	}
	if in.ClientID != "" {
		c.send(makeAck(in.ClientID))
	}
}

// checkMembership consults the configured MembershipResolver. A nil
// resolver fails closed. Errors are logged and treated as "not a
// member" so a transient lookup failure cannot leak content.
func (c *chatConn) checkMembership(conv string) bool {
	if c.srv.membership == nil {
		return false
	}
	ok, err := c.srv.membership(c.ctx, conv, c.pid)
	if err != nil {
		c.srv.logger.Warn("protochat.membership.lookup_failed",
			"conv", conv,
			"pid", uint64(c.pid),
			"err", err.Error())
		return false
	}
	return ok
}

// checkOrigin validates the inbound Origin header against the
// operator-configured allowlist. Returns (reason, true) on accept and
// (reason, false) on reject; the reason string lands in the RFC 7807
// problem detail.
//
// Empty AllowedOrigins is "same-origin only": the Origin's host must
// match Request.Host (case-insensitively, port-sensitive). An empty
// Origin header is rejected unless AllowEmptyOrigin is true.
func (s *Server) checkOrigin(r *http.Request) (string, bool) {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		if s.allowEmptyOrigin {
			return "", true
		}
		return "missing Origin header", false
	}
	// Origin syntax per RFC 6454 §6.1: scheme "://" host [":" port].
	// We accept any URL parseable target; lowercasing host gives us
	// the case-insensitive comparison the spec requires.
	parsed, err := parseOrigin(origin)
	if err != nil {
		return "malformed Origin header", false
	}
	if len(s.allowedOrigins) == 0 {
		// Same-origin policy: Origin host must match Request.Host.
		want := strings.ToLower(strings.TrimSpace(r.Host))
		if want == "" {
			return "request missing Host header", false
		}
		if !strings.EqualFold(parsed, want) {
			return fmt.Sprintf("Origin %q does not match Host %q", origin, r.Host), false
		}
		return "", true
	}
	want := strings.ToLower(origin)
	for _, allow := range s.allowedOrigins {
		if allow == want {
			return "", true
		}
	}
	return fmt.Sprintf("Origin %q is not in the allowed list", origin), false
}

// parseOrigin extracts the host[:port] component of an Origin header
// for the same-origin comparison. Returns an error for malformed
// values; the spec requires the value carry a scheme and a host.
func parseOrigin(origin string) (string, error) {
	idx := strings.Index(origin, "://")
	if idx < 0 {
		return "", errors.New("origin missing scheme separator")
	}
	host := strings.ToLower(origin[idx+3:])
	if host == "" {
		return "", errors.New("origin missing host")
	}
	// Strip an optional path component; some clients send the page URL
	// instead of the bare origin (the spec allows it but we are strict
	// on the host part).
	if i := strings.IndexByte(host, '/'); i >= 0 {
		host = host[:i]
	}
	if host == "" {
		return "", errors.New("origin host is empty")
	}
	return host, nil
}

// metricFrameTypeIn maps a client frame Type to the closed-enum label
// value used by the protochat metrics. Unknown types collapse to
// "unknown" so the counter's cardinality stays bounded by the protocol's
// vocabulary, not by hostile-client noise.
func metricFrameTypeIn(t string) string {
	switch t {
	case clientTypeTypingStart, clientTypeTypingStop,
		clientTypePresenceSet,
		clientTypeSubscribe, clientTypeUnsubscribe,
		clientTypeCallSignal, clientTypePing:
		return t
	default:
		return "unknown"
	}
}

// metricFrameTypeOut maps a server frame Type to the closed-enum label
// value used by the protochat outbound-frame metrics.
func metricFrameTypeOut(t string) string {
	switch t {
	case ServerTypeTyping, ServerTypePresence, ServerTypeRead,
		ServerTypeCallSignal, ServerTypeError, ServerTypeAck,
		ServerTypePong:
		return t
	default:
		return "unknown"
	}
}

// metricCloseCode renders an RFC 6455 close code as the closed-enum
// label string used by the close-totals counter.
func metricCloseCode(code closeCode) string {
	switch code {
	case closeNormalClosure:
		return "1000"
	case closeGoingAway:
		return "1001"
	case closeProtocolError:
		return "1002"
	case closeUnsupportedData:
		return "1003"
	case closeInvalidPayload:
		return "1007"
	case closePolicyViolation:
		return "1008"
	case closeMessageTooBig:
		return "1009"
	case closeInternalError:
		return "1011"
	default:
		return "unknown"
	}
}

// normaliseOrigins lowercases each entry and drops empties so the
// allowlist comparison in checkOrigin is a straight string equality.
func normaliseOrigins(in []string) []string {
	out := make([]string, 0, len(in))
	for _, raw := range in {
		s := strings.ToLower(strings.TrimSpace(raw))
		if s == "" {
			continue
		}
		out = append(out, s)
	}
	return out
}

// writeOriginProblem emits an RFC 7807 problem detail describing the
// Origin rejection. We use a dedicated writer (not http.Error) so the
// content-type is application/problem+json per RFC 7807 §3.
func writeOriginProblem(w http.ResponseWriter, reason string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusForbidden)
	body, _ := json.Marshal(map[string]any{
		"type":   "https://errors.herold.example/chat/origin",
		"title":  "Forbidden Origin",
		"status": http.StatusForbidden,
		"detail": reason,
	})
	_, _ = w.Write(body)
}
