package testharness

// AttachAdmin is the post-Start half of the harness's two-phase attach
// pattern for the REST admin surface. The mechanics mirror AttachSMTP
// and AttachIMAP: stop the default accept loop, hand the listener to
// srv.Serve in a managed goroutine joined by the harness waitgroup,
// and refuse to attach twice to the same listener.

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/hanshuebner/herold/internal/protoadmin"
)

// AttachAdmin binds a protoadmin.Server to the named listener. The
// listener must have Protocol == "admin". Calling AttachAdmin twice on
// the same listener panics.
//
// Returns an error when the listener is unknown or already attached.
// Tests typically t.Fatalf on a non-nil return because a wiring bug
// here always indicates a test bug, not a condition under test.
func (s *Server) AttachAdmin(name string, srv *protoadmin.Server, mode protoadmin.ListenerMode) error {
	if srv == nil {
		return fmt.Errorf("testharness: AttachAdmin nil server")
	}
	s.mu.Lock()
	st, ok := s.listeners[name]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("testharness: AttachAdmin: no listener %q", name)
	}
	if st.managed != nil {
		s.mu.Unlock()
		return fmt.Errorf("testharness: AttachAdmin: listener %q already attached", name)
	}
	st.managed = make(chan struct{})
	stopCh := st.stopDefault
	doneCh := st.defaultDone
	ln := st.ln
	s.mu.Unlock()
	close(stopCh)
	<-doneCh

	// Clear any deadline the default accept loop set on the listener so
	// http.Server's Accept can block indefinitely.
	if tcp, ok := ln.(*net.TCPListener); ok {
		_ = tcp.SetDeadline(time.Time{})
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer close(st.managed)
		_ = srv.Serve(s.ctx, ln, mode)
	}()
	return nil
}

// DialAdminByName returns an *http.Client that dials the named admin
// listener and the base URL for it. When the listener has not been
// attached, returns an http.Client whose transport fails every request
// with ErrListenerHasNoHandler so the test sees a typed error.
func (s *Server) DialAdminByName(ctx context.Context, name string) (*http.Client, string) {
	s.mu.Lock()
	st, ok := s.listeners[name]
	s.mu.Unlock()
	if !ok || (st.managed == nil && st.handler == nil) {
		return &http.Client{Transport: &errTransport{err: ErrListenerHasNoHandler}}, ""
	}
	addr := st.addr.String()
	base := "http://" + addr
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}
	return &http.Client{Transport: tr}, base
}

// DialAdminTLSByName is the implicit-TLS variant. Callers supply a
// tls.Config (normally one wired from a harness fixture CA).
func (s *Server) DialAdminTLSByName(ctx context.Context, name string, cfg *tls.Config) (*http.Client, string) {
	s.mu.Lock()
	st, ok := s.listeners[name]
	s.mu.Unlock()
	if !ok || (st.managed == nil && st.handler == nil) {
		return &http.Client{Transport: &errTransport{err: ErrListenerHasNoHandler}}, ""
	}
	addr := st.addr.String()
	base := "https://" + addr
	tr := &http.Transport{
		DialTLSContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			raw, err := d.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			tc := tls.Client(raw, cfg)
			if err := tc.HandshakeContext(ctx); err != nil {
				_ = tc.Close()
				return nil, err
			}
			return tc, nil
		},
	}
	return &http.Client{Transport: tr}, base
}
