package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

// natsStub is a minimal NATS protocol server suitable for testing
// connect + publish flows. It implements just enough of the wire
// protocol (INFO/CONNECT, PING/PONG, PUB) for the plugin's use cases.
// It is NOT a general NATS implementation: SUB, JetStream, queue
// groups, headers and authentication beyond user/pass acks are out
// of scope.
type natsStub struct {
	ln       net.Listener
	addr     string
	mu       sync.Mutex
	pubs     []stubPub
	connects []map[string]string // CONNECT JSON-as-map for each session
	tlsCfg   *tls.Config
	stop     chan struct{}
	wg       sync.WaitGroup
}

// stubPub is one captured PUB.
type stubPub struct {
	Subject string
	Reply   string
	Payload []byte
}

// newNATSStub starts an in-process NATS-protocol responder on a
// random localhost port. Caller must Close it after the test.
func newNATSStub(t testingT) *natsStub {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &natsStub{
		ln:   ln,
		addr: ln.Addr().String(),
		stop: make(chan struct{}),
	}
	s.wg.Add(1)
	go s.acceptLoop()
	return s
}

// newNATSStubTLS starts a TLS-enabled stub with the supplied server
// cert. The stub advertises TLS in INFO and upgrades incoming
// connections via a TLS handshake before reading wire frames.
func newNATSStubTLS(t testingT, cfg *tls.Config) *natsStub {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tls: %v", err)
	}
	s := &natsStub{
		ln:     ln,
		addr:   ln.Addr().String(),
		stop:   make(chan struct{}),
		tlsCfg: cfg,
	}
	s.wg.Add(1)
	go s.acceptLoop()
	return s
}

// URL returns the nats://host:port URL clients should dial.
func (s *natsStub) URL() string {
	if s.tlsCfg != nil {
		return "tls://" + s.addr
	}
	return "nats://" + s.addr
}

// Close shuts the listener down and waits for the accept loop.
func (s *natsStub) Close() {
	close(s.stop)
	_ = s.ln.Close()
	s.wg.Wait()
}

// Pubs returns a snapshot of the captured PUBs in arrival order.
func (s *natsStub) Pubs() []stubPub {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]stubPub, len(s.pubs))
	copy(out, s.pubs)
	return out
}

// WaitForPub blocks until at least n PUBs have been captured or ctx
// fires, returning the snapshot at that point.
func (s *natsStub) WaitForPub(ctx context.Context, n int) ([]stubPub, error) {
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		s.mu.Lock()
		got := len(s.pubs)
		s.mu.Unlock()
		if got >= n {
			return s.Pubs(), nil
		}
		select {
		case <-ctx.Done():
			return s.Pubs(), ctx.Err()
		case <-tick.C:
		}
	}
}

// acceptLoop accepts incoming connections and spawns a session per
// connection.
func (s *natsStub) acceptLoop() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			select {
			case <-s.stop:
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		s.wg.Add(1)
		go s.handleConn(c)
	}
}

// handleConn drives one session: send INFO, then loop reading frames
// (CONNECT, PUB, PING). Closes the connection on EOF or error.
func (s *natsStub) handleConn(c net.Conn) {
	defer s.wg.Done()
	defer c.Close()
	// Advertise INFO. tls_required matches our TLS-enabled mode.
	tlsRequired := s.tlsCfg != nil
	info := fmt.Sprintf(`{"server_id":"stub","server_name":"stub","version":"2.10.0","go":"go1.21","host":"127.0.0.1","port":4222,"max_payload":1048576,"proto":1,"tls_required":%t}`, tlsRequired)
	if _, err := fmt.Fprintf(c, "INFO %s\r\n", info); err != nil {
		return
	}
	if s.tlsCfg != nil {
		tlsConn := tls.Server(c, s.tlsCfg)
		if err := tlsConn.Handshake(); err != nil {
			return
		}
		c = tlsConn
	}
	br := bufio.NewReader(c)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			return
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(line, "CONNECT "):
			// Stash the CONNECT JSON for inspection. The client sets
			// verbose=false by default so we MUST NOT echo +OK; the
			// real NATS server only echoes +OK after CONNECT when the
			// client requests verbose mode.
			s.recordConnect(line[len("CONNECT "):])
		case strings.HasPrefix(line, "PING"):
			if _, err := fmt.Fprint(c, "PONG\r\n"); err != nil {
				return
			}
		case strings.HasPrefix(line, "PUB "):
			if err := s.handlePUB(c, br, strings.TrimPrefix(line, "PUB ")); err != nil {
				return
			}
		case strings.HasPrefix(line, "HPUB "):
			if err := s.handleHPUB(c, br, strings.TrimPrefix(line, "HPUB ")); err != nil {
				return
			}
		default:
			// SUB / UNSUB: acknowledge silently; we do not implement
			// delivery. PONG: client pong reply; no-op. Anything else
			// is an unknown command we also ignore.
		}
	}
}

// handlePUB consumes one PUB frame: header line "<subject> [reply] <size>"
// then a payload of <size> bytes followed by CRLF. Captures the result.
func (s *natsStub) handlePUB(_ net.Conn, br *bufio.Reader, header string) error {
	subject, reply, size, err := parsePubHeader(header)
	if err != nil {
		return err
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(br, body); err != nil {
		return err
	}
	// Consume trailing CRLF.
	_, _ = br.ReadString('\n')
	s.mu.Lock()
	s.pubs = append(s.pubs, stubPub{Subject: subject, Reply: reply, Payload: body})
	s.mu.Unlock()
	return nil
}

// handleHPUB is the headers-aware variant. Header line is
// "<subject> [reply] <hdr_size> <total_size>" and the payload begins
// with hdr_size bytes of headers followed by total_size-hdr_size of
// body. The stub strips the headers and records the body.
func (s *natsStub) handleHPUB(_ net.Conn, br *bufio.Reader, header string) error {
	parts := strings.Fields(header)
	if len(parts) < 3 {
		return fmt.Errorf("HPUB malformed: %q", header)
	}
	subject := parts[0]
	var reply string
	var hdrSize, totalSize int
	if len(parts) == 3 {
		fmt.Sscanf(parts[1], "%d", &hdrSize)
		fmt.Sscanf(parts[2], "%d", &totalSize)
	} else {
		reply = parts[1]
		fmt.Sscanf(parts[2], "%d", &hdrSize)
		fmt.Sscanf(parts[3], "%d", &totalSize)
	}
	if hdrSize < 0 || totalSize < hdrSize {
		return fmt.Errorf("HPUB sizes invalid")
	}
	full := make([]byte, totalSize)
	if _, err := io.ReadFull(br, full); err != nil {
		return err
	}
	_, _ = br.ReadString('\n')
	body := full[hdrSize:]
	s.mu.Lock()
	s.pubs = append(s.pubs, stubPub{Subject: subject, Reply: reply, Payload: body})
	s.mu.Unlock()
	return nil
}

func parsePubHeader(header string) (subject, reply string, size int, err error) {
	parts := strings.Fields(header)
	switch len(parts) {
	case 2:
		subject = parts[0]
		_, err = fmt.Sscanf(parts[1], "%d", &size)
	case 3:
		subject = parts[0]
		reply = parts[1]
		_, err = fmt.Sscanf(parts[2], "%d", &size)
	default:
		err = fmt.Errorf("PUB malformed: %q", header)
	}
	return
}

func (s *natsStub) recordConnect(jsonStr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Store raw JSON so tests can assert auth fields if they care.
	s.connects = append(s.connects, map[string]string{"raw": jsonStr})
}

// testingT is a small surface compatible with both *testing.T and
// *testing.B so the stub helpers can be reused across test kinds.
type testingT interface {
	Helper()
	Fatalf(format string, args ...any)
}
