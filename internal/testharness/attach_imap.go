package testharness

// AttachIMAP is the post-Start half of the harness's two-phase attach
// pattern (the pre-Start half is Options.Listeners, which reserves
// sockets). See the package comment on harness.go for the full
// rationale; in short, a protoimap.Server must be constructed against
// the harness's Store / Clock / DNS / Plugins — values that exist
// only after Start — so the handler cannot be supplied at Start()
// time. The reserved socket returns ErrListenerHasNoHandler from
// Dial* helpers until AttachIMAP runs.

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"

	"github.com/hanshuebner/herold/internal/protoimap"
)

// AttachIMAP binds a protoimap.Server to the named listener. The mechanics
// mirror AttachSMTP: stop the default accept loop, hand the listener to
// srv.Serve in a managed goroutine joined by the harness waitgroup, and
// refuse to attach twice to the same listener.
func (s *Server) AttachIMAP(name string, srv *protoimap.Server, mode protoimap.ListenerMode) {
	if srv == nil {
		panic("testharness: AttachIMAP nil server")
	}
	s.mu.Lock()
	st, ok := s.listeners[name]
	if !ok {
		s.mu.Unlock()
		panic(fmt.Sprintf("testharness: AttachIMAP: no listener %q", name))
	}
	if st.managed != nil {
		s.mu.Unlock()
		panic(fmt.Sprintf("testharness: AttachIMAP: listener %q already attached", name))
	}
	st.managed = make(chan struct{})
	stopCh := st.stopDefault
	doneCh := st.defaultDone
	s.mu.Unlock()
	close(stopCh)
	<-doneCh

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer close(st.managed)
		_ = srv.Serve(s.ctx, st.ln, mode)
	}()
}

// DialIMAPByName connects to a named IMAP listener. For implicit-TLS
// listeners use DialIMAPSByName.
func (s *Server) DialIMAPByName(ctx context.Context, name string) (net.Conn, error) {
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

// DialIMAPSByName is the implicit-TLS variant of DialIMAPByName.
func (s *Server) DialIMAPSByName(ctx context.Context, name string, cfg *tls.Config) (*tls.Conn, error) {
	raw, err := s.DialIMAPByName(ctx, name)
	if err != nil {
		return nil, err
	}
	tc := tls.Client(raw, cfg)
	if err := tc.HandshakeContext(ctx); err != nil {
		_ = tc.Close()
		return nil, fmt.Errorf("testharness: imaps handshake: %w", err)
	}
	return tc, nil
}
