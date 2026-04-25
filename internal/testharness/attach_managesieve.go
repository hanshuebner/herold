package testharness

// AttachManageSieve mirrors AttachIMAP / AttachAdmin: stop the default
// accept loop on the named listener and hand the bound socket to a
// managed protomanagesieve.Server. The protocol == "managesieve" or
// "sieve" — both are accepted so test fixtures may choose whichever
// reads naturally.

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"

	"github.com/hanshuebner/herold/internal/protomanagesieve"
)

// AttachManageSieve binds a protomanagesieve.Server to the named
// listener. Panics on the wiring errors that always indicate a test
// bug (unknown listener, double-attach, nil server) — matching the
// IMAP attach helper.
func (s *Server) AttachManageSieve(name string, srv *protomanagesieve.Server) {
	if srv == nil {
		panic("testharness: AttachManageSieve nil server")
	}
	s.mu.Lock()
	st, ok := s.listeners[name]
	if !ok {
		s.mu.Unlock()
		panic(fmt.Sprintf("testharness: AttachManageSieve: no listener %q", name))
	}
	if st.managed != nil {
		s.mu.Unlock()
		panic(fmt.Sprintf("testharness: AttachManageSieve: listener %q already attached", name))
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
		_ = srv.Serve(s.ctx, st.ln)
	}()
}

// DialManageSieveByName dials the named ManageSieve listener as a raw
// TCP connection (the listener serves plaintext-on-accept; STARTTLS is
// the per-session TLS upgrade).
func (s *Server) DialManageSieveByName(ctx context.Context, name string) (net.Conn, error) {
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

// DialManageSieveTLSByName dials the listener and immediately drives
// the STARTTLS upgrade with the supplied client config. The returned
// *tls.Conn is post-handshake and ready for AUTHENTICATE.
//
// The helper is here rather than in the test file so test fixtures
// across packages can share it.
func (s *Server) DialManageSieveTLSByName(ctx context.Context, name string, cfg *tls.Config) (*tls.Conn, error) {
	raw, err := s.DialManageSieveByName(ctx, name)
	if err != nil {
		return nil, err
	}
	tc := tls.Client(raw, cfg)
	if err := tc.HandshakeContext(ctx); err != nil {
		_ = tc.Close()
		return nil, fmt.Errorf("testharness: managesieve TLS handshake: %w", err)
	}
	return tc, nil
}
