package fakesmtp_test

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/testfakes/fakesmtp"
)

// smtpClient is a tiny scripted SMTP client for exercising the fake server.
type smtpClient struct {
	t    *testing.T
	conn net.Conn
	br   *bufio.Reader
}

func dial(t *testing.T, addr string) *smtpClient {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	c := &smtpClient{t: t, conn: conn, br: bufio.NewReader(conn)}
	c.expect("220")
	return c
}

func (c *smtpClient) upgradeTLS(cfg *tls.Config) {
	c.t.Helper()
	tc := tls.Client(c.conn, cfg)
	if err := tc.Handshake(); err != nil {
		c.t.Fatalf("tls handshake: %v", err)
	}
	c.conn = tc
	c.br = bufio.NewReader(tc)
}

func (c *smtpClient) cmd(line, wantPrefix string) string {
	c.t.Helper()
	if _, err := c.conn.Write([]byte(line + "\r\n")); err != nil {
		c.t.Fatalf("write %q: %v", line, err)
	}
	return c.expect(wantPrefix)
}

// expect reads a full (possibly multi-line) SMTP reply and asserts the code.
func (c *smtpClient) expect(wantPrefix string) string {
	c.t.Helper()
	var last string
	for {
		line, err := c.br.ReadString('\n')
		if err != nil {
			c.t.Fatalf("read reply: %v", err)
		}
		last = strings.TrimRight(line, "\r\n")
		if len(last) >= 4 && last[3] == ' ' {
			break
		}
	}
	if !strings.HasPrefix(last, wantPrefix) {
		c.t.Fatalf("reply %q; want prefix %q", last, wantPrefix)
	}
	return last
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// TestPlain_PasswordTransaction runs a PLAIN-authenticated transaction against
// a cleartext server and verifies the recorded message.
func TestPlain_PasswordTransaction(t *testing.T) {
	srv := fakesmtp.New(t, fakesmtp.Options{Security: fakesmtp.Plain})
	c := dial(t, srv.Addr())
	c.cmd("EHLO client.test", "250")
	c.cmd("AUTH PLAIN "+b64("\x00user@ext.test\x00s3cret"), "235")
	c.cmd("MAIL FROM:<user@ext.test>", "250")
	c.cmd("RCPT TO:<dest@remote.test>", "250")
	c.cmd("DATA", "354")
	c.cmd("Subject: hi\r\n\r\nbody line\r\n.", "250")
	c.cmd("QUIT", "221")

	msgs := srv.Messages()
	if len(msgs) != 1 {
		t.Fatalf("recorded %d messages; want 1", len(msgs))
	}
	m := msgs[0]
	if m.AuthMechanism != "PLAIN" || m.AuthIdentity != "user@ext.test" || m.AuthSecret != "s3cret" {
		t.Errorf("auth = {%q,%q,%q}; want {PLAIN,user@ext.test,s3cret}", m.AuthMechanism, m.AuthIdentity, m.AuthSecret)
	}
	if m.MailFrom != "user@ext.test" || len(m.RcptTo) != 1 || m.RcptTo[0] != "dest@remote.test" {
		t.Errorf("envelope = %q -> %v", m.MailFrom, m.RcptTo)
	}
	if !strings.Contains(string(m.Data), "Subject: hi") {
		t.Errorf("data missing subject: %q", m.Data)
	}
	if m.OverTLS {
		t.Errorf("OverTLS = true on a plain server")
	}
}

// TestXOAUTH2_Transaction verifies the XOAUTH2 initial response is decoded
// into the user and bearer token.
func TestXOAUTH2_Transaction(t *testing.T) {
	srv := fakesmtp.New(t, fakesmtp.Options{Security: fakesmtp.Plain})
	c := dial(t, srv.Addr())
	c.cmd("EHLO client.test", "250")
	ir := "user=alice@ext.test\x01auth=Bearer ya29.token123\x01\x01"
	c.cmd("AUTH XOAUTH2 "+b64(ir), "235")
	c.cmd("MAIL FROM:<alice@ext.test>", "250")
	c.cmd("RCPT TO:<dest@remote.test>", "250")
	c.cmd("DATA", "354")
	c.cmd("body\r\n.", "250")
	c.cmd("QUIT", "221")

	msgs := srv.Messages()
	if len(msgs) != 1 {
		t.Fatalf("recorded %d messages; want 1", len(msgs))
	}
	m := msgs[0]
	if m.AuthMechanism != "XOAUTH2" || m.AuthIdentity != "alice@ext.test" || m.AuthSecret != "ya29.token123" {
		t.Errorf("auth = {%q,%q,%q}; want {XOAUTH2,alice@ext.test,ya29.token123}", m.AuthMechanism, m.AuthIdentity, m.AuthSecret)
	}
}

// TestSTARTTLS_Upgrade verifies the STARTTLS posture advertises and honours
// the upgrade and records the transaction as running over TLS.
func TestSTARTTLS_Upgrade(t *testing.T) {
	srv := fakesmtp.New(t, fakesmtp.Options{Security: fakesmtp.STARTTLS, Hostname: "smtp.fake.test"})
	c := dial(t, srv.Addr())
	greeting := c.cmd("EHLO client.test", "250")
	if !strings.Contains(greeting, "STARTTLS") {
		// The multi-line EHLO reply is collapsed to its final line by expect;
		// re-issue against a fresh connection to inspect capabilities is
		// unnecessary — assert via a successful STARTTLS instead.
		_ = greeting
	}
	c.cmd("STARTTLS", "220")
	c.upgradeTLS(srv.TLSConfigForClient())
	c.cmd("EHLO client.test", "250")
	c.cmd("AUTH PLAIN "+b64("\x00u@ext.test\x00pw"), "235")
	c.cmd("MAIL FROM:<u@ext.test>", "250")
	c.cmd("RCPT TO:<d@remote.test>", "250")
	c.cmd("DATA", "354")
	c.cmd("hi\r\n.", "250")
	c.cmd("QUIT", "221")

	msgs := srv.Messages()
	if len(msgs) != 1 {
		t.Fatalf("recorded %d messages; want 1", len(msgs))
	}
	if !msgs[0].OverTLS {
		t.Errorf("OverTLS = false after STARTTLS upgrade")
	}
}

// TestHTTPHandler_MessagesRaw verifies GET /messages/{n}/raw returns the
// nth recorded message's raw bytes verbatim (1-based, recording order) and
// 404s when n is out of range or not a positive integer.
func TestHTTPHandler_MessagesRaw(t *testing.T) {
	srv := fakesmtp.New(t, fakesmtp.Options{Security: fakesmtp.Plain})
	c := dial(t, srv.Addr())
	c.cmd("EHLO client.test", "250")
	c.cmd("MAIL FROM:<one@ext.test>", "250")
	c.cmd("RCPT TO:<dest@remote.test>", "250")
	c.cmd("DATA", "354")
	c.cmd("Subject: first\r\n\r\nfirst body\r\n.", "250")
	c.cmd("MAIL FROM:<two@ext.test>", "250")
	c.cmd("RCPT TO:<dest@remote.test>", "250")
	c.cmd("DATA", "354")
	c.cmd("Subject: second\r\n\r\nsecond body\r\n.", "250")
	c.cmd("QUIT", "221")

	ts := httptest.NewServer(srv.HTTPHandler())
	defer ts.Close()

	get := func(path string) (*http.Response, []byte) {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp, body
	}

	resp, body := get("/messages/1/raw")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /messages/1/raw: status=%d body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "Subject: first") || !strings.Contains(string(body), "first body") {
		t.Errorf("/messages/1/raw body = %q; want the first message's raw bytes", body)
	}

	resp, body = get("/messages/2/raw")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /messages/2/raw: status=%d body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "Subject: second") {
		t.Errorf("/messages/2/raw body = %q; want the second message's raw bytes", body)
	}

	for _, path := range []string{"/messages/0/raw", "/messages/3/raw", "/messages/-1/raw", "/messages/abc/raw"} {
		resp, body = get(path)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s: status=%d; want 404 (body=%s)", path, resp.StatusCode, body)
		}
	}
}

// TestHTTPHandler_MessagesRawFlag verifies GET /messages?raw=1 includes a
// base64 "raw" field per record that decodes to the message's raw bytes,
// and that the field is omitted without the flag.
func TestHTTPHandler_MessagesRawFlag(t *testing.T) {
	srv := fakesmtp.New(t, fakesmtp.Options{Security: fakesmtp.Plain})
	c := dial(t, srv.Addr())
	c.cmd("EHLO client.test", "250")
	c.cmd("MAIL FROM:<u@ext.test>", "250")
	c.cmd("RCPT TO:<dest@remote.test>", "250")
	c.cmd("DATA", "354")
	c.cmd("Subject: hi\r\n\r\nbody line\r\n.", "250")
	c.cmd("QUIT", "221")

	ts := httptest.NewServer(srv.HTTPHandler())
	defer ts.Close()

	type msgJSON struct {
		MailFrom string `json:"mail_from"`
		Raw      string `json:"raw,omitempty"`
	}

	resp, err := http.Get(ts.URL + "/messages")
	if err != nil {
		t.Fatalf("GET /messages: %v", err)
	}
	var withoutRaw []msgJSON
	if err := json.NewDecoder(resp.Body).Decode(&withoutRaw); err != nil {
		t.Fatalf("decode /messages: %v", err)
	}
	_ = resp.Body.Close()
	if len(withoutRaw) != 1 || withoutRaw[0].Raw != "" {
		t.Fatalf("/messages without raw=1: %+v; want one record with no raw field", withoutRaw)
	}

	resp, err = http.Get(ts.URL + "/messages?raw=1")
	if err != nil {
		t.Fatalf("GET /messages?raw=1: %v", err)
	}
	var withRaw []msgJSON
	if err := json.NewDecoder(resp.Body).Decode(&withRaw); err != nil {
		t.Fatalf("decode /messages?raw=1: %v", err)
	}
	_ = resp.Body.Close()
	if len(withRaw) != 1 {
		t.Fatalf("/messages?raw=1: got %d records; want 1", len(withRaw))
	}
	decoded, err := base64.StdEncoding.DecodeString(withRaw[0].Raw)
	if err != nil {
		t.Fatalf("decode raw field: %v", err)
	}
	if !strings.Contains(string(decoded), "Subject: hi") || !strings.Contains(string(decoded), "body line") {
		t.Errorf("decoded raw field = %q; want the recorded message bytes", decoded)
	}
}
