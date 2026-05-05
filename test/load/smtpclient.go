package load

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

// smtpClient is a minimal line-oriented SMTP client for load tests.
// It is not thread-safe; each goroutine owns its own instance.
type smtpClient struct {
	conn net.Conn
	r    *bufio.Reader
}

// dialSMTP opens a TCP connection to addr and reads the greeting. If the
// server greets with 421 (e.g. per-IP connection cap reached) the dial
// is retried with a small backoff up to 30 s — this matches the
// production reality where load comes from many IPs but the harness's
// simulated load originates from a single 127.0.0.1.
func dialSMTP(ctx context.Context, addr string) (*smtpClient, error) {
	deadline := time.Now().Add(30 * time.Second)
	backoff := 25 * time.Millisecond
	for {
		var d net.Dialer
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			if ctx.Err() != nil || time.Now().After(deadline) {
				return nil, fmt.Errorf("dial smtp %s: %w", addr, err)
			}
			time.Sleep(backoff)
			if backoff < 500*time.Millisecond {
				backoff *= 2
			}
			continue
		}
		c := &smtpClient{conn: conn, r: bufio.NewReader(conn)}
		code, err := c.readReply()
		if err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("smtp greeting: %w", err)
		}
		if code == 220 {
			return c, nil
		}
		_ = conn.Close()
		// 421 is the documented "service not available, closing channel"
		// reply that the per-IP cap emits. Treat any 4xx greeting as
		// transient and retry until the deadline.
		if code/100 != 4 {
			return nil, fmt.Errorf("smtp greeting: unexpected code %d", code)
		}
		if ctx.Err() != nil || time.Now().After(deadline) {
			return nil, fmt.Errorf("smtp greeting: %d (transient, deadline reached)", code)
		}
		time.Sleep(backoff)
		if backoff < 500*time.Millisecond {
			backoff *= 2
		}
	}
}

// close tears down the connection.
func (c *smtpClient) close() { _ = c.conn.Close() }

// send writes a CRLF-terminated command.
func (c *smtpClient) send(line string) error {
	_ = c.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	_, err := fmt.Fprintf(c.conn, "%s\r\n", line)
	return err
}

// sendBytes writes b verbatim.
func (c *smtpClient) sendBytes(b []byte) error {
	_ = c.conn.SetWriteDeadline(time.Now().Add(60 * time.Second))
	_, err := c.conn.Write(b)
	return err
}

// readReply reads a (possibly multi-line) SMTP reply and returns the
// numeric code + joined text.
func (c *smtpClient) readReply() (int, error) {
	_ = c.conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	var code int
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			return 0, fmt.Errorf("read reply: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if len(line) < 3 {
			return 0, fmt.Errorf("short reply: %q", line)
		}
		if _, err := fmt.Sscanf(line[:3], "%d", &code); err != nil {
			return 0, fmt.Errorf("parse code in %q: %w", line, err)
		}
		if len(line) < 4 || line[3] == ' ' {
			return code, nil
		}
	}
}

// expect reads a reply and returns an error when the code is not want.
func (c *smtpClient) expect(want int) error {
	got, err := c.readReply()
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("expected %d, got %d", want, got)
	}
	return nil
}

// deliverMessage sends one message from sender to recipient using a
// minimal EHLO / MAIL / RCPT / DATA transaction.  It does NOT close
// the connection so the caller can reuse it for the next message.
func (c *smtpClient) deliverMessage(sender, recipient, body string) error {
	if err := c.send("EHLO load.test"); err != nil {
		return err
	}
	if err := c.expect(250); err != nil {
		return fmt.Errorf("EHLO: %w", err)
	}
	if err := c.send("MAIL FROM:<" + sender + ">"); err != nil {
		return err
	}
	if err := c.expect(250); err != nil {
		return fmt.Errorf("MAIL FROM: %w", err)
	}
	if err := c.send("RCPT TO:<" + recipient + ">"); err != nil {
		return err
	}
	if err := c.expect(250); err != nil {
		return fmt.Errorf("RCPT TO: %w", err)
	}
	if err := c.send("DATA"); err != nil {
		return err
	}
	if err := c.expect(354); err != nil {
		return fmt.Errorf("DATA: %w", err)
	}
	if !strings.HasSuffix(body, "\r\n") {
		body += "\r\n"
	}
	if err := c.sendBytes([]byte(body + ".\r\n")); err != nil {
		return fmt.Errorf("DATA body: %w", err)
	}
	_ = c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	if err := c.expect(250); err != nil {
		return fmt.Errorf("DATA accepted: %w", err)
	}
	return nil
}

// quit sends QUIT and closes the connection.
func (c *smtpClient) quit() {
	_ = c.send("QUIT")
	_, _ = c.readReply()
	c.close()
}
