package testharness

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"time"

	"github.com/hanshuebner/herold/internal/protosmtp"
)

// AttachSMTP binds srv to the listener with the given Name. The harness
// hands the listener off to srv.Serve (which runs its own accept loop
// in a goroutine managed by the harness's waitgroup), and subsequent
// DialSMTP / DialSMTPByName calls connect through the real server.
//
// Only one SMTP server may be attached per listener name; calling
// AttachSMTP twice for the same name is a test bug and panics.
//
// Design note. The ticket's two options were (a) an Options.SMTPServer
// field filled at Start time, and (b) a post-Start AttachSMTP method.
// We pick (b): the protosmtp.Server construction depends on the harness
// Store/Clock/DNS (which Start provides), so the attach step is
// strictly after Start. An Options.SMTPServer field would force callers
// to build the server twice (once to register, once to wire). The
// smaller, more obvious change is to attach after Start.
func (s *Server) AttachSMTP(name string, srv *protosmtp.Server, mode protosmtp.ListenerMode) {
	if srv == nil {
		panic("testharness: AttachSMTP nil server")
	}
	s.mu.Lock()
	st, ok := s.listeners[name]
	if !ok {
		s.mu.Unlock()
		panic(fmt.Sprintf("testharness: AttachSMTP: no listener %q", name))
	}
	if st.managed != nil {
		s.mu.Unlock()
		panic(fmt.Sprintf("testharness: AttachSMTP: listener %q already attached", name))
	}
	st.managed = make(chan struct{})
	// Stop the default accept loop and wait for it to exit so there is
	// only one Accept caller on the listener at any given moment.
	stopCh := st.stopDefault
	doneCh := st.defaultDone
	handoffCh := st.handoff
	s.mu.Unlock()
	close(stopCh)
	<-doneCh

	// The default accept loop left a per-call deadline set on the
	// listener; clear it so the server's Accept blocks indefinitely as
	// expected.
	s.mu.Lock()
	ln := st.ln
	s.mu.Unlock()
	if tcp, ok := ln.(*net.TCPListener); ok {
		_ = tcp.SetDeadline(time.Time{})
	}

	// The default accept loop may have accepted one conn in the window
	// between close(stopCh) and the loop actually exiting Accept; it
	// parked that conn on handoffCh rather than closing it. Drain and
	// feed into the server so the client sees a real greeting.
	select {
	case pre := <-handoffCh:
		srv.HandleConn(pre, mode)
	default:
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer close(st.managed)
		_ = srv.Serve(s.ctx, st.ln, mode)
	}()
}

// DialSMTPByName connects to a named SMTP listener. When the listener
// has been attached via AttachSMTP this returns a live net.Conn on top
// of the server. For implicit-TLS listeners use DialSMTPSByName.
func (s *Server) DialSMTPByName(ctx context.Context, name string) (net.Conn, error) {
	s.mu.Lock()
	st, ok := s.listeners[name]
	s.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("no listener %q: %w", name, ErrListenerHasNoHandler)
	}
	if st.managed == nil && st.handler == nil {
		return nil, fmt.Errorf("listener %q has no handler: %w", name, ErrListenerHasNoHandler)
	}
	var d net.Dialer
	return d.DialContext(ctx, "tcp", st.addr.String())
}

// DialSMTPSByName is the implicit-TLS variant: the caller supplies the
// TLS config (typically one wired from the test's fixture CA). Returns
// a *tls.Conn after completing the handshake.
func (s *Server) DialSMTPSByName(ctx context.Context, name string, cfg *tls.Config) (*tls.Conn, error) {
	raw, err := s.DialSMTPByName(ctx, name)
	if err != nil {
		return nil, err
	}
	tc := tls.Client(raw, cfg)
	if err := tc.HandshakeContext(ctx); err != nil {
		_ = tc.Close()
		return nil, fmt.Errorf("testharness: smtps handshake: %w", err)
	}
	return tc, nil
}
