// Package smtppeer provides two in-process SMTP test utilities:
//
//   - Scripted: a pre-programmed SMTP responder that replays a canned
//     response sequence. Queue tests drive it with a stub SMTP client.
//   - InProcessServer: a TCP listener binding 127.0.0.1:0 that dispatches
//     accepted connections to a caller-supplied handler. Teardown closes
//     the listener and all accepted connections.
//
// Neither utility implements the full SMTP state machine; that lives in
// internal/protosmtp. The harness uses these peers as stand-ins for remote
// MX servers the queue dials out to.
package smtppeer

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
)

// Response is one pre-programmed reply in a scripted peer's queue. Code is
// the SMTP status code (e.g. 250); EnhancedCode is the optional enhanced
// status (RFC 2034, e.g. "2.1.0") emitted immediately after Code; Text is
// the human-readable remainder. Multi-line replies are produced by emitting
// one Response per physical line and relying on the driver to know when to
// stop — for the queue tests we write, single-line replies cover every
// case.
type Response struct {
	Code         int
	EnhancedCode string
	Text         string
}

// Format renders r as the wire line (without trailing CRLF). When
// EnhancedCode is empty, it is omitted from output.
func (r Response) Format() string {
	if r.EnhancedCode != "" {
		return fmt.Sprintf("%d %s %s", r.Code, r.EnhancedCode, r.Text)
	}
	return fmt.Sprintf("%d %s", r.Code, r.Text)
}

// Scripted is an in-memory SMTP responder that replays a pre-programmed
// sequence of replies to any command the client sends. The responder emits
// one scripted reply per command line read, in order; after the final
// reply it closes the connection.
//
// Scripted does not interpret commands; tests use it to verify client
// behaviour under a known server script. If the client issues more commands
// than the script has replies, Scripted returns io.EOF via a closed
// connection and recording stops.
type Scripted struct {
	mu       sync.Mutex
	sequence []Response
	received []string
	greeting Response
	hasGreet bool
}

// NewScripted constructs a scripted peer with the given response sequence.
// The first reply is sent immediately on Serve as the greeting (nominally
// 220); subsequent replies are one-per-command. If sequence is empty,
// Serve closes the connection without writing anything.
func NewScripted(sequence ...Response) *Scripted {
	s := &Scripted{}
	if len(sequence) > 0 {
		s.greeting = sequence[0]
		s.hasGreet = true
		if len(sequence) > 1 {
			s.sequence = append(s.sequence, sequence[1:]...)
		}
	}
	return s
}

// Serve runs the scripted peer against conn. It returns after conn closes
// or the script is exhausted. The peer does not own conn; callers (or the
// accept loop) close it. Serve honours ctx cancellation by closing conn.
func (s *Scripted) Serve(ctx context.Context, conn net.Conn) error {
	// A cancellation goroutine closes the conn on ctx.Done so blocked
	// reads unblock.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()

	w := bufio.NewWriter(conn)
	r := bufio.NewReader(conn)

	// Emit greeting first.
	s.mu.Lock()
	hasGreet := s.hasGreet
	greeting := s.greeting
	s.mu.Unlock()
	if hasGreet {
		if _, err := fmt.Fprintf(w, "%s\r\n", greeting.Format()); err != nil {
			return fmt.Errorf("smtppeer greet: %w", err)
		}
		if err := w.Flush(); err != nil {
			return fmt.Errorf("smtppeer greet flush: %w", err)
		}
	}

	for {
		line, err := r.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("smtppeer read: %w", err)
		}
		s.mu.Lock()
		s.received = append(s.received, trimCRLF(line))
		if len(s.sequence) == 0 {
			s.mu.Unlock()
			return nil
		}
		next := s.sequence[0]
		s.sequence = s.sequence[1:]
		s.mu.Unlock()
		if _, err := fmt.Fprintf(w, "%s\r\n", next.Format()); err != nil {
			return fmt.Errorf("smtppeer write: %w", err)
		}
		if err := w.Flush(); err != nil {
			return fmt.Errorf("smtppeer flush: %w", err)
		}
	}
}

// Received returns a copy of the command lines the client sent, in order.
func (s *Scripted) Received() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.received))
	copy(out, s.received)
	return out
}

// Remaining returns the number of scripted replies not yet consumed. Tests
// assert Remaining() == 0 to confirm the script played out.
func (s *Scripted) Remaining() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sequence)
}

func trimCRLF(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

// InProcessServer is a 127.0.0.1-bound TCP listener that dispatches accepted
// connections to a caller-supplied handler. It is used by queue tests as a
// stand-in remote MX: the test programs a Scripted peer and passes its
// Serve as the handler.
type InProcessServer struct {
	ln      net.Listener
	handler func(net.Conn)
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	mu      sync.Mutex
	conns   []net.Conn
	closed  bool
}

// NewInProcessServer binds 127.0.0.1:0 and starts an accept loop that
// dispatches each accepted connection to handler on its own goroutine. The
// handler returns when it is done; the server closes the conn afterwards.
// Close stops accepting new connections and waits for all handlers to
// return.
func NewInProcessServer(handler func(net.Conn)) (*InProcessServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("smtppeer listen: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &InProcessServer{
		ln:      ln,
		handler: handler,
		ctx:     ctx,
		cancel:  cancel,
	}
	s.wg.Add(1)
	go s.acceptLoop()
	return s, nil
}

// Addr returns the bound listener address; use .String() to obtain
// host:port for the queue's dial target.
func (s *InProcessServer) Addr() net.Addr {
	return s.ln.Addr()
}

// Close shuts the server down, closes every conn it is still tracking, and
// waits for the accept loop and handlers to exit. Idempotent.
func (s *InProcessServer) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.cancel()
	_ = s.ln.Close()
	for _, c := range s.conns {
		_ = c.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
	return nil
}

func (s *InProcessServer) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = conn.Close()
			return
		}
		s.conns = append(s.conns, conn)
		s.mu.Unlock()
		s.wg.Add(1)
		go func(c net.Conn) {
			defer s.wg.Done()
			defer c.Close()
			s.handler(c)
		}(conn)
	}
}
