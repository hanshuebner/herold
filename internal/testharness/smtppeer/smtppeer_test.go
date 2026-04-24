package smtppeer_test

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/testharness/smtppeer"
)

func TestScriptedReplaysSequence(t *testing.T) {
	peer := smtppeer.NewScripted(
		smtppeer.Response{Code: 220, Text: "mx.test ESMTP"},
		smtppeer.Response{Code: 250, Text: "mx.test"},
		smtppeer.Response{Code: 250, EnhancedCode: "2.1.0", Text: "ok"},
		smtppeer.Response{Code: 250, EnhancedCode: "2.1.5", Text: "ok"},
	)

	// pair of pipe conns simulate a socket without going through TCP.
	client, server := net.Pipe()
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go func() {
		_ = peer.Serve(ctx, server)
		_ = server.Close()
	}()

	// client reads greeting
	r := bufio.NewReader(client)
	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("read greet: %v", err)
	}
	if !strings.HasPrefix(line, "220 ") {
		t.Fatalf("expected 220 greeting, got %q", line)
	}

	// client sends commands and reads replies
	commands := []string{"EHLO host.test\r\n", "MAIL FROM:<a@b>\r\n", "RCPT TO:<c@d>\r\n"}
	expected := []string{"250 mx.test", "250 2.1.0 ok", "250 2.1.5 ok"}
	for i, cmd := range commands {
		if _, err := client.Write([]byte(cmd)); err != nil {
			t.Fatalf("write cmd %d: %v", i, err)
		}
		reply, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read reply %d: %v", i, err)
		}
		if !strings.HasPrefix(reply, expected[i]) {
			t.Fatalf("reply %d: got %q want prefix %q", i, reply, expected[i])
		}
	}

	if peer.Remaining() != 0 {
		t.Fatalf("expected script consumed, remaining=%d", peer.Remaining())
	}
	got := peer.Received()
	if len(got) != 3 {
		t.Fatalf("expected 3 received lines, got %d: %v", len(got), got)
	}
}

func TestInProcessServerDispatches(t *testing.T) {
	called := make(chan string, 1)
	srv, err := smtppeer.NewInProcessServer(func(c net.Conn) {
		defer c.Close()
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 64)
		n, _ := c.Read(buf)
		called <- string(buf[:n])
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer srv.Close()

	conn, err := net.Dial("tcp", srv.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	select {
	case got := <-called:
		if got != "hello" {
			t.Fatalf("handler got %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("handler did not run")
	}
}

func TestInProcessServerCloseIdempotent(t *testing.T) {
	srv, err := smtppeer.NewInProcessServer(func(c net.Conn) { c.Close() })
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("close1: %v", err)
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("close2: %v", err)
	}
}
