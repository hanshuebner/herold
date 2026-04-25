package testharness

// AttachUI is the post-Start half of the harness's two-phase attach
// pattern for the protoui web UI. The mechanics mirror AttachAdmin.
// Tests typically attach a UI server onto an "admin"- or "ui"-protocol
// listener; the harness does not branch on the listener's protocol
// label, so any reserved listener works.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/hanshuebner/herold/internal/protoui"
)

// AttachUI binds a protoui.Server to the named listener. Calling
// AttachUI twice on the same listener returns an error.
func (s *Server) AttachUI(name string, srv *protoui.Server) error {
	if srv == nil {
		return fmt.Errorf("testharness: AttachUI nil server")
	}
	s.mu.Lock()
	st, ok := s.listeners[name]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("testharness: AttachUI: no listener %q", name)
	}
	if st.managed != nil {
		s.mu.Unlock()
		return fmt.Errorf("testharness: AttachUI: listener %q already attached", name)
	}
	st.managed = make(chan struct{})
	stopCh := st.stopDefault
	doneCh := st.defaultDone
	ln := st.ln
	s.mu.Unlock()
	close(stopCh)
	<-doneCh

	// Clear any deadline set by the default accept loop.
	if tcp, ok := ln.(*net.TCPListener); ok {
		_ = tcp.SetDeadline(time.Time{})
	}

	httpSrv := &http.Server{
		Handler:     srv.Handler(),
		BaseContext: func(net.Listener) context.Context { return s.ctx },
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer close(st.managed)
		// Watch the harness ctx for shutdown; on cancel we Shutdown
		// the http.Server so Serve returns.
		shutdownDone := make(chan struct{})
		go func() {
			defer close(shutdownDone)
			<-s.ctx.Done()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = httpSrv.Shutdown(shutdownCtx)
		}()
		_ = httpSrv.Serve(ln)
		<-shutdownDone
	}()
	return nil
}

// DialUIByName returns an *http.Client wired to the named listener and
// the base URL it serves on. When the listener is unattached, returns
// a client whose transport returns ErrListenerHasNoHandler so tests
// see a typed error.
func (s *Server) DialUIByName(ctx context.Context, name string) (*http.Client, string) {
	s.mu.Lock()
	st, ok := s.listeners[name]
	s.mu.Unlock()
	if !ok || st.managed == nil {
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
	// We deliberately turn off automatic redirect-following so tests
	// can assert on 303 See Other Location headers from form
	// submissions. The protoui flow is redirect-heavy (POST /login ->
	// 303 -> GET /dashboard) and tests need to verify the redirect
	// target, not just observe the followed result.
	c := &http.Client{
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return c, base
}
