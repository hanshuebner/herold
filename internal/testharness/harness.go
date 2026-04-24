// This file implements the core Server handle.
//
// Phase 1 Wave 0 reality check: the protocol subsystems (protosmtp,
// protoimap, protoadmin) have not been implemented yet. Start binds
// listener sockets and wires the store, clock, DNS, and plugin registry
// together, but DialSMTP / DialIMAP / DialAdmin return
// ErrListenerHasNoHandler until a later wave attaches the relevant
// protocol code. That keeps the harness landable in Wave 0 without
// depending on in-flight packages.
//
// Two-phase attach pattern (Options.Listeners + AttachX).
//
// The harness splits listener setup across two phases intentionally.
// Phase 1 (pre-Start) is declarative: Options.Listeners reserves
// each socket — the harness binds 127.0.0.1:0, records the allocated
// port, and runs a trivial accept loop that closes any inbound
// connection. Tests can assert the port is reachable at this stage,
// and Dial* helpers return ErrListenerHasNoHandler.
//
// Phase 2 (post-Start) is imperative: AttachSMTP, AttachIMAP (and
// future Attach* helpers) bind a real protocol handler to a reserved
// listener. The handler's construction depends on the harness's
// Store / Clock / DNS / Plugins — values that exist only after Start
// has run — so the handler cannot be supplied at Start() time. The
// alternative of an Options.XServer field would force callers to
// build two Server instances (one to register, one to wire); the
// attach-after-Start split keeps the construction linear.
//
// The attach step stops the default accept loop, drains any conn the
// loop happened to accept while shutting down (handed off on
// stopDefault via the handoff channel), and hands the bound listener
// to the protocol server's own Serve loop — which the harness's
// waitgroup then joins on Close. The result: one Accept caller per
// listener at any moment, zero dropped connections across the
// handoff, and a protocol handler wired against the same Store /
// Clock / DNS the test is asserting against.

package testharness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness/fakedns"
	"github.com/hanshuebner/herold/internal/testharness/fakeplugin"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
	"github.com/hanshuebner/herold/internal/testharness/smtppeer"
)

// ErrListenerHasNoHandler is returned by Dial* when the corresponding
// protocol subsystem has not yet been attached to the listener. It means
// the test tried to exercise a Wave 2+ path from a Wave 0 harness; the
// listener is bound and the port is allocated, but no goroutine is
// accepting on it with a real handler.
var ErrListenerHasNoHandler = errors.New("testharness: listener has no handler attached (protosmtp/protoimap/protoadmin lands in later waves)")

// fakeClockAnchor is the default anchor time for the injected FakeClock.
// It is deliberately in a predictable calendar position to aid debugging
// (2026-01-01T00:00:00Z).
var fakeClockAnchor = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// ListenerSpec declares a socket the harness should bind. The harness
// picks the address (127.0.0.1:0) and records the allocated port; real
// protocol handlers are attached in later waves via dedicated helpers.
type ListenerSpec struct {
	// Name is a stable identifier for the listener, used by callers to
	// reach specific sockets (e.g. two admin listeners on different auth
	// policies). Names must be unique within an Options.
	Name string
	// Protocol is one of "smtp", "smtp-submission", "imap", "imaps",
	// "admin". The harness does not currently branch on the value in
	// Wave 0; it is recorded so later waves can attach the right handler.
	Protocol string
}

// Options configure Start. All fields are optional; zero values produce a
// sensible default (in-memory store, fake clock anchored at 2026-01-01,
// empty DNS, empty plugin registry, no listeners).
type Options struct {
	// Store is the store.Store backing the harness. Defaults to
	// fakestore.New.
	Store store.Store
	// Clock is the clock injected into subsystems. Defaults to
	// clock.NewFake(fakeClockAnchor).
	Clock clock.Clock
	// RandSeed is the deterministic seed passed to corpus generators that
	// consume it. Defaults to 42.
	RandSeed int64
	// Logger is the structured logger the harness writes through.
	// Defaults to a slog.Logger wired to t.Logf.
	Logger *slog.Logger
	// DNS is the fake resolver. Defaults to fakedns.New (empty answer set).
	DNS *fakedns.Resolver
	// Plugins is the fake plugin registry. Defaults to
	// fakeplugin.NewRegistry (empty).
	Plugins *fakeplugin.Registry
	// SMTPPeers is a map of remote MX hostname -> scripted peer, used by
	// queue tests to stand in for remote servers. Optional.
	SMTPPeers map[string]*smtppeer.Scripted
	// Listeners declares the sockets to bind. Each spec binds
	// 127.0.0.1:0; the allocated port is recorded and retrievable via
	// Server.ListenerAddr(name).
	Listeners []ListenerSpec
}

// Server is the harness handle. It owns the Store, Clock, DNS, Plugins,
// and any listener sockets it bound. All fields are safe for concurrent
// use by tests.
type Server struct {
	Store   store.Store
	Clock   clock.Clock
	Logger  *slog.Logger
	DNS     *fakedns.Resolver
	Plugins *fakeplugin.Registry

	// Options snapshot, retained for Dial* to know what is configured.
	opts Options

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu        sync.Mutex
	listeners map[string]*listenerState
	closed    bool
}

type listenerState struct {
	spec    ListenerSpec
	ln      net.Listener
	addr    net.Addr
	handler func(net.Conn) // nil in Wave 0
	// stopDefault, when closed, tells the default acceptLoop to exit
	// because an attach call is about to take the listener over.
	stopDefault chan struct{}
	// defaultDone is closed when the default accept loop has exited.
	// AttachSMTP blocks on it before launching the managed server so
	// the listener has exactly one accept caller at a time.
	defaultDone chan struct{}
	// managed is non-nil after AttachSMTP has wired a server; the
	// harness waitgroup tracks the managed goroutine and uses this
	// channel to join on shutdown.
	managed chan struct{}
	// handoff carries at most one connection the default accept loop
	// happened to accept after stopDefault was signalled. The attach
	// path drains it before starting the real server.
	handoff chan net.Conn
}

// Start spins up an in-process herold server for tests. The returned
// Server binds any declared listener sockets and wires the in-memory
// store, clock, DNS, and plugin registry into one place.
//
// Start registers its teardown via t.Cleanup. The returned func() is
// redundant; callers that want to assert "no leaked goroutines before
// return" can invoke it explicitly before the test returns.
func Start(t testing.TB, opts Options) (*Server, func()) {
	t.Helper()
	filled, err := fillDefaults(t, opts)
	if err != nil {
		t.Fatalf("testharness: defaults: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{
		Store:     filled.Store,
		Clock:     filled.Clock,
		Logger:    filled.Logger,
		DNS:       filled.DNS,
		Plugins:   filled.Plugins,
		opts:      filled,
		ctx:       ctx,
		cancel:    cancel,
		listeners: make(map[string]*listenerState),
	}

	for _, spec := range filled.Listeners {
		if _, exists := s.listeners[spec.Name]; exists {
			_ = s.closeLocked()
			t.Fatalf("testharness: duplicate listener name %q", spec.Name)
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			_ = s.closeLocked()
			t.Fatalf("testharness: listen %s: %v", spec.Name, err)
		}
		st := &listenerState{
			spec:        spec,
			ln:          ln,
			addr:        ln.Addr(),
			stopDefault: make(chan struct{}),
			defaultDone: make(chan struct{}),
			handoff:     make(chan net.Conn, 1),
		}
		s.listeners[spec.Name] = st
		// Accept loop: drain connections so the socket remains a valid
		// bindable port and the port survives; with no handler, we
		// immediately close each accepted conn. This keeps tests that
		// probe ports (admin health checks) from hanging.
		s.wg.Add(1)
		go s.acceptLoop(st)
	}

	cleanup := func() { _ = s.Close() }
	t.Cleanup(cleanup)
	return s, cleanup
}

func fillDefaults(t testing.TB, o Options) (Options, error) {
	if o.Clock == nil {
		o.Clock = clock.NewFake(fakeClockAnchor)
	}
	if o.RandSeed == 0 {
		o.RandSeed = 42
	}
	if o.Logger == nil {
		o.Logger = newTestLogger(t)
	}
	if o.DNS == nil {
		o.DNS = fakedns.New()
	}
	if o.Plugins == nil {
		o.Plugins = fakeplugin.NewRegistry()
	}
	if o.Store == nil {
		fs, err := fakestore.New(fakestore.Options{
			Clock:   o.Clock,
			BlobDir: t.TempDir(),
		})
		if err != nil {
			return Options{}, fmt.Errorf("fakestore: %w", err)
		}
		o.Store = fs
	}
	return o, nil
}

// newTestLogger wires a slog.Logger to t.Logf via a redirecting writer.
// The writer is line-buffered; observe.NewLoggerTo attaches the
// secret-redaction handler for parity with production.
func newTestLogger(t testing.TB) *slog.Logger {
	w := &tLogWriter{t: t}
	return observe.NewLoggerTo(w, observe.ObservabilityConfig{
		LogFormat: "text",
		LogLevel:  "debug",
	})
}

// tLogWriter relays slog output to testing.TB.Logf. Each complete line
// becomes one t.Logf call so the test output groups cleanly.
type tLogWriter struct {
	t   testing.TB
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *tLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	for {
		i := bytes.IndexByte(w.buf.Bytes(), '\n')
		if i < 0 {
			break
		}
		line := w.buf.Next(i + 1)
		w.t.Logf("%s", bytes.TrimRight(line, "\n"))
	}
	return len(p), nil
}

// acceptLoop runs the Wave 0 stand-in accept loop for a listener that
// has no real handler attached. Each accepted conn is closed
// immediately. The loop exits when either the listener closes or
// stopDefault is closed (the latter happens when an attach path is
// about to take the listener over). When stopDefault fires with an
// accepted-but-not-yet-handled connection in-flight, the connection is
// handed off to the attach path via the handoff channel so no client
// sees a silent close.
func (s *Server) acceptLoop(st *listenerState) {
	defer s.wg.Done()
	defer close(st.defaultDone)
	for {
		// Non-blocking check for an attach-stop request.
		select {
		case <-st.stopDefault:
			return
		default:
		}
		// Short deadline so the loop notices stopDefault quickly.
		if tcp, ok := st.ln.(*net.TCPListener); ok {
			_ = tcp.SetDeadline(time.Now().Add(100 * time.Millisecond))
		}
		conn, err := st.ln.Accept()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return
		}
		// If an attach has been requested, route the conn to the
		// handoff channel instead of closing it.
		select {
		case <-st.stopDefault:
			select {
			case st.handoff <- conn:
			default:
				_ = conn.Close()
			}
			return
		default:
		}
		s.mu.Lock()
		handler := st.handler
		s.mu.Unlock()
		if handler == nil {
			_ = conn.Close()
			continue
		}
		s.wg.Add(1)
		go func(c net.Conn) {
			defer s.wg.Done()
			defer c.Close()
			handler(c)
		}(conn)
	}
}

// ListenerAddr returns the bound address for a named listener and ok==true,
// or the zero Addr and ok==false if no such listener exists.
func (s *Server) ListenerAddr(name string) (net.Addr, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.listeners[name]
	if !ok {
		return nil, false
	}
	return st.addr, true
}

// DialSMTP connects to the harness's SMTP listener. In Wave 0 this returns
// ErrListenerHasNoHandler; a later wave attaches protosmtp and the conn
// will carry a real SMTP session.
func (s *Server) DialSMTP(ctx context.Context) (net.Conn, error) {
	return s.dialByProtocol(ctx, "smtp")
}

// DialIMAP connects to the harness's IMAP listener. Returns
// ErrListenerHasNoHandler in Wave 0.
func (s *Server) DialIMAP(ctx context.Context) (net.Conn, error) {
	return s.dialByProtocol(ctx, "imap")
}

// DialAdmin returns an *http.Client and the base URL for the harness's
// admin listener. The client has a tiny transport dialing the listener
// directly; callers issue requests against baseURL. Returns
// ErrListenerHasNoHandler in Wave 0.
func (s *Server) DialAdmin(ctx context.Context) (*http.Client, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range s.listeners {
		if st.spec.Protocol != "admin" {
			continue
		}
		if st.handler == nil {
			// Return a client that always errors with ErrListenerHasNoHandler.
			return &http.Client{Transport: &errTransport{err: ErrListenerHasNoHandler}}, ""
		}
		addr := st.addr.String()
		baseURL := "http://" + addr
		tr := &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, network, addr)
			},
		}
		return &http.Client{Transport: tr}, baseURL
	}
	return &http.Client{Transport: &errTransport{err: ErrListenerHasNoHandler}}, ""
}

// errTransport is an http.RoundTripper that always fails with err. It is
// returned by DialAdmin when no admin handler is attached so that test
// code observes a real error instead of a nil client.
type errTransport struct{ err error }

func (t *errTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, t.err
}

func (s *Server) dialByProtocol(ctx context.Context, protocol string) (net.Conn, error) {
	s.mu.Lock()
	var st *listenerState
	for _, cand := range s.listeners {
		if cand.spec.Protocol == protocol {
			st = cand
			break
		}
	}
	s.mu.Unlock()
	if st == nil {
		return nil, fmt.Errorf("no %s listener: %w", protocol, ErrListenerHasNoHandler)
	}
	if st.handler == nil && st.managed == nil {
		// Port is bound but accept loop just closes conns; surface a typed
		// error so tests can assert this explicitly.
		return nil, fmt.Errorf("%s listener has no handler: %w", protocol, ErrListenerHasNoHandler)
	}
	var d net.Dialer
	return d.DialContext(ctx, "tcp", st.addr.String())
}

// Advance advances the injected clock. If the underlying Clock is a
// *clock.FakeClock this fires any After waiters whose deadline has been
// crossed. If the Clock is not a FakeClock, Advance is a no-op (production
// clocks do not accept external advances).
func (s *Server) Advance(d time.Duration) {
	if fc, ok := s.Clock.(*clock.FakeClock); ok {
		fc.Advance(d)
	}
}

// AddDNSRecord forwards to the fake DNS resolver. See
// fakedns.Resolver.AddRecord.
func (s *Server) AddDNSRecord(name, rrtype, value string) {
	if err := s.DNS.AddRecord(name, rrtype, value); err != nil {
		s.Logger.Warn("harness: AddDNSRecord", "err", err, "name", name, "rrtype", rrtype, "value", value)
	}
}

// RegisterPlugin forwards to the fake plugin registry. The registry
// stores the pointer; the caller must not share the same *FakePlugin with
// another registry.
func (s *Server) RegisterPlugin(name string, p *fakeplugin.FakePlugin) {
	p.Name = name
	s.Plugins.Register(p)
}

// Close shuts the harness down: closes all listeners, cancels the root
// context, waits for the accept loops to exit, and calls Close on the
// store. Subsequent calls are no-ops.
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeLocked()
}

func (s *Server) closeLocked() error {
	if s.closed {
		return nil
	}
	s.closed = true
	s.cancel()
	for _, st := range s.listeners {
		_ = st.ln.Close()
	}
	// Give the accept loops a bounded window to exit so we do not deadlock
	// if a listener.Close races an Accept.
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		return fmt.Errorf("testharness: accept loops did not exit within 5s")
	}
	if s.Store != nil {
		if err := s.Store.Close(); err != nil {
			return fmt.Errorf("testharness: store close: %w", err)
		}
	}
	return nil
}
