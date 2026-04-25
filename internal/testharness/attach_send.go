package testharness

// AttachSend is the post-Start half of the harness's two-phase attach
// pattern for the HTTP send API surface. The mechanics mirror
// AttachAdmin / AttachJMAP.

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/hanshuebner/herold/internal/protosend"
)

// AttachSend binds a protosend.Server to the named listener. The
// listener must have Protocol == "send". Returns an error when the
// listener is unknown or already attached.
func (s *Server) AttachSend(name string, srv *protosend.Server, mode protosend.ListenerMode) error {
	if srv == nil {
		return fmt.Errorf("testharness: AttachSend nil server")
	}
	s.mu.Lock()
	st, ok := s.listeners[name]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("testharness: AttachSend: no listener %q", name)
	}
	if st.managed != nil {
		s.mu.Unlock()
		return fmt.Errorf("testharness: AttachSend: listener %q already attached", name)
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

// DialSendByName returns an *http.Client that dials the named send
// listener and the base URL for it.
func (s *Server) DialSendByName(ctx context.Context, name string) (*http.Client, string) {
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

// DialSendTLSByName is the implicit-TLS variant.
func (s *Server) DialSendTLSByName(ctx context.Context, name string, cfg *tls.Config) (*http.Client, string) {
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
