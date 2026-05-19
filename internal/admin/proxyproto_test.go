package admin

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// startRawProxyProtocolServer binds a loopback TCP listener wrapped exactly
// as a proxy_protocol mail listener is wired in bindOneAddress (issue #111).
// It accepts one connection, writes the conn.RemoteAddr() string to the
// client, then closes the connection. Returns the dial address and a stop
// func.
func startRawProxyProtocolServer(t *testing.T) (addr string, stop func()) {
	t.Helper()
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ln := proxyProtocolListener(base)
	dialAddr := base.Addr().String()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = fmt.Fprint(conn, conn.RemoteAddr().String())
	}()
	return dialAddr, func() {
		_ = ln.Close()
		<-done
	}
}

// startProxyProtocolServer binds a loopback listener wrapped exactly as a
// proxy_protocol = true admin listener is wired in bindOneAddress, and serves
// a handler that echoes the request's RemoteAddr and TLS state. It returns the
// dial address and a stop func.
func startProxyProtocolServer(t *testing.T) (addr string, stop func()) {
	t.Helper()
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	dialAddr := base.Addr().String()

	handler := forwardedHTTPSHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "remote=%s tls=%t", r.RemoteAddr, r.TLS != nil)
	}))
	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(proxyProtocolListener(base)) }()

	return dialAddr, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}
}

// TestProxyProtocolListener_DecodesClientAddr verifies that a connection
// carrying a PROXY v1 header surfaces the real client IP in
// http.Request.RemoteAddr, and that forwardedHTTPSHandler marks the request
// as TLS so scheme-deriving handlers emit https (issue #106).
func TestProxyProtocolListener_DecodesClientAddr(t *testing.T) {
	addr, stop := startProxyProtocolServer(t)
	defer stop()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	// PROXY v1 header announcing a client that is neither the loopback
	// dialer nor the destination, followed by an ordinary HTTP request.
	if _, err := io.WriteString(conn, "PROXY TCP4 198.51.100.23 10.0.0.5 51234 443\r\n"); err != nil {
		t.Fatalf("write proxy header: %v", err)
	}
	if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: example\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatalf("write request: %v", err)
	}

	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := string(body)
	if !strings.Contains(got, "remote=198.51.100.23:51234") {
		t.Errorf("RemoteAddr not taken from PROXY header: %q", got)
	}
	if !strings.Contains(got, "tls=true") {
		t.Errorf("request not marked as TLS behind the proxy: %q", got)
	}
}

// TestProxyProtocolListener_RejectsMissingHeader verifies the REQUIRE policy:
// a connection that does not send a PROXY header is rejected rather than
// served, so the loopback listener cannot be reached by a spoofing client
// that bypasses the proxy (issue #106).
func TestProxyProtocolListener_RejectsMissingHeader(t *testing.T) {
	addr, stop := startProxyProtocolServer(t)
	defer stop()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	// A plain HTTP request with no PROXY header.
	if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: example\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatalf("write request: %v", err)
	}
	if resp, err := http.ReadResponse(bufio.NewReader(conn), nil); err == nil {
		resp.Body.Close()
		t.Fatalf("connection without a PROXY header was served (status %d); want rejected", resp.StatusCode)
	}
}

// TestProxyProtocolListener_MailProtocol_DecodesClientAddr verifies that
// proxyProtocolListener works for raw (non-HTTP) mail protocol connections:
// the accepted conn.RemoteAddr() returns the address from the PROXY header
// rather than the loopback dialer address. This mirrors the HTTP test above
// but exercises the path used by SMTP, IMAP, and ManageSieve listeners
// (issue #111).
func TestProxyProtocolListener_MailProtocol_DecodesClientAddr(t *testing.T) {
	addr, stop := startRawProxyProtocolServer(t)
	defer stop()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	// PROXY v1 header announcing a real client address, as an upstream
	// L4 load balancer would inject for a mail protocol connection.
	if _, err := io.WriteString(conn, "PROXY TCP4 203.0.113.42 10.0.0.1 12345 25\r\n"); err != nil {
		t.Fatalf("write proxy header: %v", err)
	}

	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read remote addr: %v", err)
	}
	if !strings.Contains(string(got), "203.0.113.42:12345") {
		t.Errorf("RemoteAddr not taken from PROXY header: %q", string(got))
	}
}
