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

	"github.com/hanshuebner/herold/internal/clock"
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
	MaxFrameBytes   int           // default 65536 (64 KiB)

	// TypingAutoStop is how long a typing.start lasts before the
	// server emits an implicit typing.stop on its behalf. Default
	// 10s. Reset by a fresh typing.start from the same connection.
	TypingAutoStop time.Duration

	// PresenceGrace is the disconnect-grace window before a
	// principal's presence transitions to offline. Default 30s.
	PresenceGrace time.Duration
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
	maxFrameBytes   int
	typingAutoStop  time.Duration

	connsMu     sync.Mutex
	conns       map[*chatConn]struct{}
	perPrinc    map[store.PrincipalID]int
	totalActive atomic.Int64

	handlersMu sync.RWMutex
	handlers   map[string]FrameHandler
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
	if opts.MaxFrameBytes <= 0 {
		opts.MaxFrameBytes = 64 * 1024
	}
	if opts.TypingAutoStop <= 0 {
		opts.TypingAutoStop = 10 * time.Second
	}
	if opts.PresenceGrace <= 0 {
		opts.PresenceGrace = 30 * time.Second
	}
	return &Server{
		store:           opts.Store,
		logger:          logger,
		clk:             clk,
		resolve:         opts.SessionResolver,
		broadcaster:     opts.Broadcaster,
		membership:      opts.Membership,
		presence:        NewPresenceTracker(clk, opts.PresenceGrace),
		rateLimiter:     newRateLimiter(clk, 60, 120),
		maxConnections:  opts.MaxConnections,
		perPrincipalCap: opts.PerPrincipalCap,
		writeQueueSize:  opts.WriteQueueSize,
		pingInterval:    opts.PingInterval,
		pongTimeout:     opts.PongTimeout,
		maxFrameBytes:   opts.MaxFrameBytes,
		typingAutoStop:  opts.TypingAutoStop,
		conns:           make(map[*chatConn]struct{}),
		perPrinc:        make(map[store.PrincipalID]int),
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

	if !validateUpgradeHeaders(r) {
		http.Error(w, "bad websocket upgrade", http.StatusBadRequest)
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

	// Write the 101 handshake. brw is the buffered writer the http
	// server handed us; we flush after writing to ensure the
	// response lands before the codec starts.
	accept := computeAcceptKey(strings.TrimSpace(key))
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := brw.WriteString(resp); err != nil {
		s.releaseReservation(pid)
		_ = netConn.Close()
		return
	}
	if err := brw.Flush(); err != nil {
		s.releaseReservation(pid)
		_ = netConn.Close()
		return
	}

	cc := s.newChatConn(pid, netConn, brw.Reader)
	s.connsMu.Lock()
	s.conns[cc] = struct{}{}
	s.connsMu.Unlock()
	cc.id = s.broadcaster.Register(pid, cc)
	s.presence.CancelOffline(pid)

	// Run the connection. r.Context() is the request context — it
	// fires when the underlying http.Server is shutting down; we
	// derive cc.ctx from it so a server shutdown drains every
	// connection.
	cc.run(r.Context())

	s.broadcaster.Unregister(cc.id)
	s.connsMu.Lock()
	delete(s.conns, cc)
	s.connsMu.Unlock()
	s.releaseReservation(pid)

	// Disconnect-grace: if this was the last connection for pid,
	// schedule a transition to offline; a reconnect within the
	// grace window cancels it via Set / CancelOffline above.
	if !s.broadcaster.HasConnection(pid) {
		s.presence.ScheduleOffline(pid, func(now time.Time) {
			s.emitPresence(pid, "offline", now)
		})
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

// emitPresence fans out a presence-update frame to every connection
// subscribed to a conversation containing pid. Also stamps the
// internal presence tracker.
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
	// Presence fans out to every connection that has registered
	// interest. Subscribers are the lightweight routing primitive
	// here — clients explicitly subscribe to peers' conversations
	// and so see presence transitions for the people they chat
	// with.
	s.broadcaster.mu.RLock()
	out := make([]Sender, 0)
	for _, sub := range s.broadcaster.byID {
		// A connection is interested in pid's presence if it is
		// itself a subscriber to any conversation that pid is in.
		// Without a membership lookup we cannot resolve that here;
		// instead we deliver to every connection that has at least
		// one active subscription, and rely on the client to filter
		// on principal id. This is a simplification; track B can
		// tighten it when the chat-store ListConversationsForPair
		// path is available.
		sub.subsMu.RLock()
		hasAny := len(sub.subs) > 0
		sub.subsMu.RUnlock()
		if hasAny {
			out = append(out, sub.sender)
		}
	}
	s.broadcaster.mu.RUnlock()
	for _, sndr := range out {
		_ = sndr.Send(frame)
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
	// on a fresh typing.start; deleted on typing.stop.
	typingMu    sync.Mutex
	typingStops map[string]chan struct{}
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
		typingStops: make(map[string]chan struct{}),
	}
}

// Send implements Sender. Non-blocking: returns ErrFull if the
// queue is saturated; in that case the caller (the broadcaster)
// drops the frame and logs.
func (c *chatConn) Send(f ServerFrame) error {
	select {
	case c.writeQ <- f:
		return nil
	default:
		// Queue full — close the connection so the client
		// reconnects rather than receiving a partial frame stream.
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
			if err := writeFrame(c.netConn, frame{
				fin:     true,
				opcode:  opText,
				payload: body,
			}); err != nil {
				c.shutdown(closeInternalError, "write failed")
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
			if err := writeFrame(c.netConn, frame{
				fin:     true,
				opcode:  opPing,
				payload: nil,
			}); err != nil {
				c.shutdown(closeInternalError, "ping write failed")
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
			// Network error or peer closed; just exit.
			return
		}
		switch f.opcode {
		case opPing:
			// Reply with a pong carrying the same payload (RFC 6455 §5.5.3).
			_ = writeFrame(c.netConn, frame{fin: true, opcode: opPong, payload: f.payload})
		case opPong:
			c.lastPong.Store(c.srv.clk.Now().UnixNano())
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
	var inFrame ClientFrame
	if err := json.Unmarshal(body, &inFrame); err != nil {
		c.send(makeError(ErrCodeBadFrame, "invalid JSON envelope", ""))
		return
	}
	if !c.srv.rateLimiter.allow(c.pid, frameWeight(inFrame.Type)) {
		c.send(makeError(ErrCodeRateLimited, "rate limit exceeded", inFrame.ClientID))
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
	c.srv.broadcaster.EmitToConversation(c.ctx, p.ConversationID, c.pid, ServerFrame{
		Type:    ServerTypeTyping,
		Payload: body,
	})
	if start {
		c.scheduleTypingAutoStop(p.ConversationID)
	} else {
		c.cancelTypingAutoStop(p.ConversationID)
	}
	if in.ClientID != "" {
		c.send(makeAck(in.ClientID))
	}
}

// scheduleTypingAutoStop replaces any existing auto-stop timer for
// conv with a fresh one. If the timer fires before another
// typing.start lands, the server emits a server-generated
// typing.stop on behalf of the connection.
func (c *chatConn) scheduleTypingAutoStop(conv string) {
	c.typingMu.Lock()
	if cancel, ok := c.typingStops[conv]; ok {
		close(cancel)
	}
	cancel := make(chan struct{})
	c.typingStops[conv] = cancel
	c.typingMu.Unlock()

	go func() {
		select {
		case <-c.srv.clk.After(c.srv.typingAutoStop):
			body, err := json.Marshal(outboundTyping{
				ConversationID:    conv,
				SenderPrincipalID: c.pid,
				State:             "stop",
			})
			if err != nil {
				return
			}
			c.srv.broadcaster.EmitToConversation(c.ctx, conv, c.pid, ServerFrame{
				Type:    ServerTypeTyping,
				Payload: body,
			})
			c.typingMu.Lock()
			if cur, ok := c.typingStops[conv]; ok && cur == cancel {
				delete(c.typingStops, conv)
			}
			c.typingMu.Unlock()
		case <-cancel:
		case <-c.ctx.Done():
		}
	}()
}

// cancelTypingAutoStop drops a pending auto-stop for conv, if any.
func (c *chatConn) cancelTypingAutoStop(conv string) {
	c.typingMu.Lock()
	defer c.typingMu.Unlock()
	if cancel, ok := c.typingStops[conv]; ok {
		close(cancel)
		delete(c.typingStops, conv)
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
