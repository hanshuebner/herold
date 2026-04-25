package testharness

// AttachJMAP is the post-Start half of the harness's two-phase attach
// pattern for the JMAP Core surface. The mechanics mirror AttachAdmin
// / AttachIMAP / AttachSMTP.

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/hanshuebner/herold/internal/protojmap"
)

// AttachJMAP binds a protojmap.Server to the named listener. The
// listener must have Protocol == "jmap". Returns an error when the
// listener is unknown or already attached.
func (s *Server) AttachJMAP(name string, srv *protojmap.Server, mode protojmap.ListenerMode) error {
	if srv == nil {
		return fmt.Errorf("testharness: AttachJMAP nil server")
	}
	s.mu.Lock()
	st, ok := s.listeners[name]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("testharness: AttachJMAP: no listener %q", name)
	}
	if st.managed != nil {
		s.mu.Unlock()
		return fmt.Errorf("testharness: AttachJMAP: listener %q already attached", name)
	}
	st.managed = make(chan struct{})
	stopCh := st.stopDefault
	doneCh := st.defaultDone
	ln := st.ln
	s.mu.Unlock()
	close(stopCh)
	<-doneCh

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

// DialJMAPByName returns an *http.Client that dials the named JMAP
// listener and the base URL for it. When the listener has not been
// attached, returns an http.Client whose transport fails every
// request with ErrListenerHasNoHandler.
func (s *Server) DialJMAPByName(ctx context.Context, name string) (*http.Client, string) {
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

// DialJMAPTLSByName is the implicit-TLS variant. Callers supply a
// tls.Config (normally one wired from a harness fixture CA).
func (s *Server) DialJMAPTLSByName(ctx context.Context, name string, cfg *tls.Config) (*http.Client, string) {
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
