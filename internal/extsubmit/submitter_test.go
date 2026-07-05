package extsubmit_test

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/extsubmit"
	"github.com/hanshuebner/herold/internal/secrets"
	"github.com/hanshuebner/herold/internal/store"
)

// testDataKey is a 32-byte AEAD key used across tests.
var testDataKey = make([]byte, 32)

func init() {
	for i := range testDataKey {
		testDataKey[i] = byte(i + 1)
	}
}

// sealSecret is a test helper that seals plaintext with testDataKey.
func sealSecret(t *testing.T, plaintext string) []byte {
	t.Helper()
	ct, err := secrets.Seal(testDataKey, []byte(plaintext))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	return ct
}

// smtpServer is a scripted in-process SMTP server for tests.
type smtpServer struct {
	ln      net.Listener
	handler func(conn net.Conn)
	wg      sync.WaitGroup
}

func newSMTPServer(t *testing.T, handler func(conn net.Conn)) *smtpServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &smtpServer{ln: ln, handler: handler}
	srv.wg.Add(1)
	go func() {
		defer srv.wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go handler(conn)
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		srv.wg.Wait()
	})
	return srv
}

func (s *smtpServer) addr() string { return s.ln.Addr().String() }

// srvWrite writes a CRLF-terminated line to a bufio.Writer.
func srvWrite(w *bufio.Writer, s string) {
	_, _ = w.WriteString(s + "\r\n")
	_ = w.Flush()
}

// srvRead reads one CRLF-terminated line.
func srvRead(r *bufio.Reader) string {
	line, _ := r.ReadString('\n')
	return strings.TrimRight(line, "\r\n")
}

// happyPathHandler is a scripted SMTP server handler for a successful
// password AUTH PLAIN + MAIL FROM + RCPT TO + DATA exchange.
func happyPathHandler(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)

	srvWrite(w, "220 smtp.test ESMTP test")
	srvRead(r) // EHLO
	srvWrite(w, "250-smtp.test")
	srvWrite(w, "250-ENHANCEDSTATUSCODES")
	srvWrite(w, "250 AUTH PLAIN LOGIN")

	// AUTH PLAIN
	line := srvRead(r)
	if strings.HasPrefix(line, "AUTH PLAIN ") {
		srvWrite(w, "235 2.7.0 ok")
	} else {
		srvWrite(w, "535 5.7.8 bad auth")
		return
	}

	srvRead(r) // MAIL FROM
	srvWrite(w, "250 2.1.0 ok")
	srvRead(r) // RCPT TO
	srvWrite(w, "250 2.1.5 ok")
	srvRead(r) // DATA
	srvWrite(w, "354 send")

	for {
		line := srvRead(r)
		if line == "." {
			break
		}
	}
	srvWrite(w, "250 2.0.0 queued as TEST-ID-001")
	srvRead(r) // QUIT
}

// TestSubmit_PlainAuth_HappyPath verifies full SMTP exchange with AUTH PLAIN.
func TestSubmit_PlainAuth_HappyPath(t *testing.T) {
	srv := newSMTPServer(t, happyPathHandler)

	s := &extsubmit.Submitter{
		DataKey:  testDataKey,
		HostName: "client.test",
	}
	// Inject a dial function that bypasses TLS and connects to our test server.
	s.SetDialFn(func(ctx context.Context, network, addr string) (net.Conn, error) {
		return net.Dial("tcp", srv.addr())
	})

	sub := store.IdentitySubmission{
		IdentityID:       "identity-1",
		SubmitHost:       "smtp.test",
		SubmitPort:       587,
		SubmitSecurity:   "none",
		SubmitAuthMethod: "password",
		PasswordCT:       sealSecret(t, "mypassword"),
	}
	env := extsubmit.Envelope{
		MailFrom:      "alice@example.com",
		RcptTo:        []string{"bob@example.com"},
		Body:          strings.NewReader("Subject: test\r\n\r\nHello\r\n"),
		CorrelationID: "corr-001",
	}

	out := s.Submit(context.Background(), sub, env)
	if out.State != extsubmit.OutcomeOK {
		t.Fatalf("state = %q; want %q; diagnostic: %s", out.State, extsubmit.OutcomeOK, out.Diagnostic)
	}
	if !strings.Contains(out.MTAID, "TEST-ID-001") {
		t.Errorf("MTAID = %q; want to contain TEST-ID-001", out.MTAID)
	}
	if out.CorrelationID != "corr-001" {
		t.Errorf("CorrelationID = %q; want corr-001", out.CorrelationID)
	}
}

// TestSubmit_AuthFailed maps a 535 AUTH response to OutcomeAuthFailed.
func TestSubmit_AuthFailed(t *testing.T) {
	srv := newSMTPServer(t, func(conn net.Conn) {
		defer conn.Close()
		r := bufio.NewReader(conn)
		w := bufio.NewWriter(conn)
		srvWrite(w, "220 smtp.test ESMTP")
		srvRead(r) // EHLO
		srvWrite(w, "250-smtp.test")
		srvWrite(w, "250 AUTH PLAIN")
		srvRead(r) // AUTH PLAIN
		srvWrite(w, "535 5.7.8 authentication failed")
	})

	s := &extsubmit.Submitter{DataKey: testDataKey, HostName: "client.test"}
	s.SetDialFn(func(ctx context.Context, network, addr string) (net.Conn, error) {
		return net.Dial("tcp", srv.addr())
	})

	sub := store.IdentitySubmission{
		IdentityID:       "identity-auth-fail",
		SubmitHost:       "smtp.test",
		SubmitPort:       587,
		SubmitSecurity:   "none",
		SubmitAuthMethod: "password",
		PasswordCT:       sealSecret(t, "wrongpassword"),
	}
	env := extsubmit.Envelope{
		MailFrom: "alice@example.com",
		RcptTo:   []string{"bob@example.com"},
		Body:     strings.NewReader(""),
	}

	out := s.Submit(context.Background(), sub, env)
	if out.State != extsubmit.OutcomeAuthFailed {
		t.Fatalf("state = %q; want auth-failed; diagnostic: %s", out.State, out.Diagnostic)
	}
}

// TestSubmit_UnreachableHost maps a dial failure to OutcomeUnreachable.
func TestSubmit_UnreachableHost(t *testing.T) {
	s := &extsubmit.Submitter{DataKey: testDataKey, HostName: "client.test"}
	s.SetDialFn(func(ctx context.Context, network, addr string) (net.Conn, error) {
		return nil, &net.OpError{Op: "dial", Err: &net.AddrError{Err: "connection refused", Addr: addr}}
	})

	sub := store.IdentitySubmission{
		IdentityID:       "identity-unreachable",
		SubmitHost:       "smtp.test",
		SubmitPort:       587,
		SubmitSecurity:   "none",
		SubmitAuthMethod: "password",
		PasswordCT:       sealSecret(t, "pw"),
	}
	env := extsubmit.Envelope{
		MailFrom: "alice@example.com",
		RcptTo:   []string{"bob@example.com"},
		Body:     strings.NewReader(""),
	}

	out := s.Submit(context.Background(), sub, env)
	if out.State != extsubmit.OutcomeUnreachable {
		t.Fatalf("state = %q; want unreachable; diagnostic: %s", out.State, out.Diagnostic)
	}
}

// TestSubmit_Permanent5xx maps a 5xx MAIL FROM rejection to OutcomePermanent.
func TestSubmit_Permanent5xx(t *testing.T) {
	srv := newSMTPServer(t, func(conn net.Conn) {
		defer conn.Close()
		r := bufio.NewReader(conn)
		w := bufio.NewWriter(conn)
		srvWrite(w, "220 smtp.test ESMTP")
		srvRead(r) // EHLO
		srvWrite(w, "250-smtp.test")
		srvWrite(w, "250 AUTH PLAIN")
		srvRead(r) // AUTH PLAIN
		srvWrite(w, "235 2.7.0 ok")
		srvRead(r) // MAIL FROM
		srvWrite(w, "550 5.1.1 user unknown")
	})

	s := &extsubmit.Submitter{DataKey: testDataKey, HostName: "client.test"}
	s.SetDialFn(func(ctx context.Context, network, addr string) (net.Conn, error) {
		return net.Dial("tcp", srv.addr())
	})

	sub := store.IdentitySubmission{
		IdentityID:       "identity-perm",
		SubmitHost:       "smtp.test",
		SubmitPort:       587,
		SubmitSecurity:   "none",
		SubmitAuthMethod: "password",
		PasswordCT:       sealSecret(t, "pw"),
	}
	env := extsubmit.Envelope{
		MailFrom: "alice@example.com",
		RcptTo:   []string{"bob@example.com"},
		Body:     strings.NewReader(""),
	}

	out := s.Submit(context.Background(), sub, env)
	if out.State != extsubmit.OutcomePermanent {
		t.Fatalf("state = %q; want permanent; diagnostic: %s", out.State, out.Diagnostic)
	}
}

// TestSubmit_Transient4xx maps a 4xx RCPT TO response to OutcomeTransient.
func TestSubmit_Transient4xx(t *testing.T) {
	srv := newSMTPServer(t, func(conn net.Conn) {
		defer conn.Close()
		r := bufio.NewReader(conn)
		w := bufio.NewWriter(conn)
		srvWrite(w, "220 smtp.test ESMTP")
		srvRead(r) // EHLO
		srvWrite(w, "250-smtp.test")
		srvWrite(w, "250 AUTH PLAIN")
		srvRead(r) // AUTH PLAIN
		srvWrite(w, "235 2.7.0 ok")
		srvRead(r) // MAIL FROM
		srvWrite(w, "250 ok")
		srvRead(r) // RCPT TO
		srvWrite(w, "451 4.4.1 try again later")
	})

	s := &extsubmit.Submitter{DataKey: testDataKey, HostName: "client.test"}
	s.SetDialFn(func(ctx context.Context, network, addr string) (net.Conn, error) {
		return net.Dial("tcp", srv.addr())
	})

	sub := store.IdentitySubmission{
		IdentityID:       "identity-trans",
		SubmitHost:       "smtp.test",
		SubmitPort:       587,
		SubmitSecurity:   "none",
		SubmitAuthMethod: "password",
		PasswordCT:       sealSecret(t, "pw"),
	}
	env := extsubmit.Envelope{
		MailFrom: "alice@example.com",
		RcptTo:   []string{"bob@example.com"},
		Body:     strings.NewReader(""),
	}

	out := s.Submit(context.Background(), sub, env)
	if out.State != extsubmit.OutcomeTransient {
		t.Fatalf("state = %q; want transient; diagnostic: %s", out.State, out.Diagnostic)
	}
}

// TestSubmit_XOAUTH2_HappyPath verifies full exchange with AUTH XOAUTH2.
func TestSubmit_XOAUTH2_HappyPath(t *testing.T) {
	var receivedIR []byte
	srv := newSMTPServer(t, func(conn net.Conn) {
		defer conn.Close()
		r := bufio.NewReader(conn)
		w := bufio.NewWriter(conn)
		srvWrite(w, "220 smtp.gmail.com ESMTP")
		srvRead(r) // EHLO
		srvWrite(w, "250-smtp.gmail.com")
		srvWrite(w, "250 AUTH XOAUTH2")
		line := srvRead(r) // AUTH XOAUTH2 <ir>
		parts := strings.SplitN(line, " ", 3)
		if len(parts) == 3 {
			b, _ := base64.StdEncoding.DecodeString(parts[2])
			receivedIR = b
		}
		srvWrite(w, "235 2.7.0 ok")
		srvRead(r) // MAIL FROM
		srvWrite(w, "250 ok")
		srvRead(r) // RCPT TO
		srvWrite(w, "250 ok")
		srvRead(r) // DATA
		srvWrite(w, "354 send")
		for {
			l := srvRead(r)
			if l == "." {
				break
			}
		}
		srvWrite(w, "250 2.0.0 queued-xoauth2")
		srvRead(r) // QUIT
	})

	s := &extsubmit.Submitter{DataKey: testDataKey, HostName: "client.test"}
	s.SetDialFn(func(ctx context.Context, network, addr string) (net.Conn, error) {
		return net.Dial("tcp", srv.addr())
	})

	sub := store.IdentitySubmission{
		IdentityID:       "identity-xoauth2",
		SubmitHost:       "smtp.gmail.com",
		SubmitPort:       587,
		SubmitSecurity:   "none",
		SubmitAuthMethod: "oauth2",
		OAuthAccessCT:    sealSecret(t, "ya29.testtoken"),
		OAuthClientID:    "user@gmail.com",
	}
	env := extsubmit.Envelope{
		MailFrom: "user@gmail.com",
		RcptTo:   []string{"recipient@example.com"},
		Body:     strings.NewReader("Subject: xoauth2\r\n\r\nBody\r\n"),
	}

	out := s.Submit(context.Background(), sub, env)
	if out.State != extsubmit.OutcomeOK {
		t.Fatalf("state = %q; want ok; diagnostic: %s", out.State, out.Diagnostic)
	}
	// Verify XOAUTH2 IR format: user=<email>\x01auth=Bearer <token>\x01\x01
	want := "user=user@gmail.com\x01auth=Bearer ya29.testtoken\x01\x01"
	if string(receivedIR) != want {
		t.Errorf("XOAUTH2 IR = %q; want %q", string(receivedIR), want)
	}
}

// TestSubmit_LoginFallback verifies that when AUTH list does not contain PLAIN,
// AUTH LOGIN is used.
func TestSubmit_LoginFallback(t *testing.T) {
	var authMethod string
	srv := newSMTPServer(t, func(conn net.Conn) {
		defer conn.Close()
		r := bufio.NewReader(conn)
		w := bufio.NewWriter(conn)
		srvWrite(w, "220 smtp.test ESMTP")
		srvRead(r) // EHLO
		srvWrite(w, "250-smtp.test")
		// Only LOGIN, no PLAIN.
		srvWrite(w, "250 AUTH LOGIN")
		line := srvRead(r) // AUTH LOGIN
		authMethod = strings.Fields(line)[0] + " " + strings.Fields(line)[1]
		// Username challenge.
		srvWrite(w, "334 "+base64.StdEncoding.EncodeToString([]byte("Username:")))
		srvRead(r) // user response
		srvWrite(w, "334 "+base64.StdEncoding.EncodeToString([]byte("Password:")))
		srvRead(r) // pass response
		srvWrite(w, "235 2.7.0 ok")
		srvRead(r) // MAIL FROM
		srvWrite(w, "250 ok")
		srvRead(r) // RCPT TO
		srvWrite(w, "250 ok")
		srvRead(r) // DATA
		srvWrite(w, "354 send")
		for {
			l := srvRead(r)
			if l == "." {
				break
			}
		}
		srvWrite(w, "250 2.0.0 ok")
		srvRead(r) // QUIT
	})

	s := &extsubmit.Submitter{DataKey: testDataKey, HostName: "client.test"}
	s.SetDialFn(func(ctx context.Context, network, addr string) (net.Conn, error) {
		return net.Dial("tcp", srv.addr())
	})

	sub := store.IdentitySubmission{
		IdentityID:       "identity-login",
		SubmitHost:       "smtp.test",
		SubmitPort:       587,
		SubmitSecurity:   "none",
		SubmitAuthMethod: "password",
		PasswordCT:       sealSecret(t, "pw"),
	}
	env := extsubmit.Envelope{
		MailFrom: "alice@example.com",
		RcptTo:   []string{"bob@example.com"},
		Body:     strings.NewReader("Subject: x\r\n\r\n.\r\n"),
	}

	out := s.Submit(context.Background(), sub, env)
	if out.State != extsubmit.OutcomeOK {
		t.Fatalf("state = %q; want ok; diagnostic: %s", out.State, out.Diagnostic)
	}
	if authMethod != "AUTH LOGIN" {
		t.Errorf("auth method = %q; want AUTH LOGIN", authMethod)
	}
}

// TestSubmit_STARTTLS_HappyPath verifies STARTTLS upgrade before AUTH.
func TestSubmit_STARTTLS_HappyPath(t *testing.T) {
	cert, certPEM := generateTestCert(t)
	serverTLS := &tls.Config{Certificates: []tls.Certificate{cert}}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(certPEM)

	srv := newSMTPServer(t, func(conn net.Conn) {
		defer conn.Close()
		r := bufio.NewReader(conn)
		w := bufio.NewWriter(conn)
		srvWrite(w, "220 smtp.test ESMTP")
		srvRead(r) // EHLO
		srvWrite(w, "250-smtp.test")
		srvWrite(w, "250 STARTTLS")
		srvRead(r) // STARTTLS
		srvWrite(w, "220 ready")
		tlsConn := tls.Server(conn, serverTLS)
		if err := tlsConn.Handshake(); err != nil {
			return
		}
		r = bufio.NewReader(tlsConn)
		w = bufio.NewWriter(tlsConn)
		srvRead(r) // EHLO post-TLS
		srvWrite(w, "250-smtp.test")
		srvWrite(w, "250 AUTH PLAIN")
		srvRead(r) // AUTH PLAIN
		srvWrite(w, "235 2.7.0 ok")
		srvRead(r) // MAIL FROM
		srvWrite(w, "250 ok")
		srvRead(r) // RCPT TO
		srvWrite(w, "250 ok")
		srvRead(r) // DATA
		srvWrite(w, "354 send")
		for {
			l := srvRead(r)
			if l == "." {
				break
			}
		}
		srvWrite(w, "250 2.0.0 ok-starttls")
		srvRead(r) // QUIT
	})

	s := &extsubmit.Submitter{DataKey: testDataKey, HostName: "client.test"}
	s.SetDialFn(func(ctx context.Context, network, addr string) (net.Conn, error) {
		return net.Dial("tcp", srv.addr())
	})
	s.SetTLSWrapFn(func(conn net.Conn, serverName string) (*tls.Conn, error) {
		cfg := &tls.Config{RootCAs: pool, ServerName: "smtp.test"}
		tlsConn := tls.Client(conn, cfg)
		return tlsConn, tlsConn.Handshake()
	})

	sub := store.IdentitySubmission{
		IdentityID:       "identity-starttls",
		SubmitHost:       "smtp.test",
		SubmitPort:       587,
		SubmitSecurity:   "starttls",
		SubmitAuthMethod: "password",
		PasswordCT:       sealSecret(t, "pw"),
	}
	env := extsubmit.Envelope{
		MailFrom: "alice@example.com",
		RcptTo:   []string{"bob@example.com"},
		Body:     strings.NewReader("Subject: x\r\n\r\nHi\r\n"),
	}

	out := s.Submit(context.Background(), sub, env)
	if out.State != extsubmit.OutcomeOK {
		t.Fatalf("state = %q; want ok; diagnostic: %s", out.State, out.Diagnostic)
	}
}

// TestProbe_HappyPath verifies that Probe runs EHLO + AUTH + MAIL FROM +
// RCPT TO + RSET against the identity's own address and returns OutcomeOK
// without transmitting a body (REQ-AUTH-EXT-SUBMIT-11).
func TestProbe_HappyPath(t *testing.T) {
	var authSeen, mailFromSeen, rcptSeen, rsetSeen, dataSeen bool
	var mailFromArg, rcptArg string
	srv := newSMTPServer(t, func(conn net.Conn) {
		defer conn.Close()
		r := bufio.NewReader(conn)
		w := bufio.NewWriter(conn)
		srvWrite(w, "220 smtp.test ESMTP")
		srvRead(r) // EHLO
		srvWrite(w, "250-smtp.test")
		srvWrite(w, "250 AUTH PLAIN")
		for {
			line := srvRead(r)
			switch {
			case line == "":
				return
			case strings.HasPrefix(line, "AUTH"):
				authSeen = true
				srvWrite(w, "235 2.7.0 ok")
			case strings.HasPrefix(line, "MAIL"):
				mailFromSeen = true
				mailFromArg = line
				srvWrite(w, "250 2.1.0 ok")
			case strings.HasPrefix(line, "RCPT"):
				rcptSeen = true
				rcptArg = line
				srvWrite(w, "250 2.1.5 ok")
			case strings.HasPrefix(line, "RSET"):
				rsetSeen = true
				srvWrite(w, "250 2.0.0 reset")
			case strings.HasPrefix(line, "DATA"):
				dataSeen = true
				srvWrite(w, "354 send")
			case line == "QUIT":
				srvWrite(w, "221 bye")
				return
			default:
				srvWrite(w, "250 ok")
			}
		}
	})

	s := &extsubmit.Submitter{DataKey: testDataKey, HostName: "client.test"}
	s.SetDialFn(func(ctx context.Context, network, addr string) (net.Conn, error) {
		return net.Dial("tcp", srv.addr())
	})

	sub := store.IdentitySubmission{
		IdentityID:       "identity-probe",
		SubmitHost:       "smtp.test",
		SubmitPort:       587,
		SubmitSecurity:   "none",
		SubmitAuthMethod: "password",
		PasswordCT:       sealSecret(t, "pw"),
		OAuthClientID:    "alice@example.com",
	}

	out := s.Probe(context.Background(), sub, "alice@example.com")
	if out.State != extsubmit.OutcomeOK {
		t.Fatalf("state = %q; want ok; diagnostic: %s", out.State, out.Diagnostic)
	}
	if !authSeen {
		t.Error("expected AUTH to be sent")
	}
	if !mailFromSeen {
		t.Error("expected MAIL FROM to be sent during send-capability probe")
	}
	if !strings.Contains(mailFromArg, "alice@example.com") {
		t.Errorf("MAIL FROM arg = %q; want it to carry the identity address", mailFromArg)
	}
	if !rcptSeen {
		t.Error("expected RCPT TO to be sent during send-capability probe")
	}
	if !strings.Contains(rcptArg, "alice@example.com") {
		t.Errorf("RCPT TO arg = %q; want the identity's own address", rcptArg)
	}
	if !rsetSeen {
		t.Error("expected RSET to be sent to abort the probe transaction")
	}
	if dataSeen {
		t.Error("DATA must not be sent during Probe (no body is transmitted)")
	}
}

// TestProbe_AuthFailed verifies that Probe returns OutcomeAuthFailed on 535.
func TestProbe_AuthFailed(t *testing.T) {
	srv := newSMTPServer(t, func(conn net.Conn) {
		defer conn.Close()
		r := bufio.NewReader(conn)
		w := bufio.NewWriter(conn)
		srvWrite(w, "220 smtp.test ESMTP")
		srvRead(r) // EHLO
		srvWrite(w, "250-smtp.test")
		srvWrite(w, "250 AUTH PLAIN")
		srvRead(r) // AUTH PLAIN
		srvWrite(w, "535 5.7.8 authentication failed")
	})

	s := &extsubmit.Submitter{DataKey: testDataKey, HostName: "client.test"}
	s.SetDialFn(func(ctx context.Context, network, addr string) (net.Conn, error) {
		return net.Dial("tcp", srv.addr())
	})

	sub := store.IdentitySubmission{
		IdentityID:       "identity-probe-fail",
		SubmitHost:       "smtp.test",
		SubmitPort:       587,
		SubmitSecurity:   "none",
		SubmitAuthMethod: "password",
		PasswordCT:       sealSecret(t, "badpw"),
		OAuthClientID:    "alice@example.com",
	}

	out := s.Probe(context.Background(), sub, "alice@example.com")
	if out.State != extsubmit.OutcomeAuthFailed {
		t.Fatalf("state = %q; want auth-failed; diagnostic: %s", out.State, out.Diagnostic)
	}
}

// TestProbe_PermanentReject verifies that a 5xx on RCPT TO during the
// send-capability probe is mapped to OutcomePermanent and the config is
// not accepted (REQ-AUTH-EXT-SUBMIT-11 "permanent-reject").
func TestProbe_PermanentReject(t *testing.T) {
	srv := newSMTPServer(t, func(conn net.Conn) {
		defer conn.Close()
		r := bufio.NewReader(conn)
		w := bufio.NewWriter(conn)
		srvWrite(w, "220 smtp.test ESMTP")
		srvRead(r) // EHLO
		srvWrite(w, "250-smtp.test")
		srvWrite(w, "250 AUTH PLAIN")
		srvRead(r) // AUTH PLAIN
		srvWrite(w, "235 2.7.0 ok")
		srvRead(r) // MAIL FROM
		srvWrite(w, "250 2.1.0 ok")
		srvRead(r) // RCPT TO
		srvWrite(w, "550 5.7.1 sender address rejected: not allowed to send as this address")
	})

	s := &extsubmit.Submitter{DataKey: testDataKey, HostName: "client.test"}
	s.SetDialFn(func(ctx context.Context, network, addr string) (net.Conn, error) {
		return net.Dial("tcp", srv.addr())
	})

	sub := store.IdentitySubmission{
		IdentityID:       "identity-probe-perm",
		SubmitHost:       "smtp.test",
		SubmitPort:       587,
		SubmitSecurity:   "none",
		SubmitAuthMethod: "password",
		PasswordCT:       sealSecret(t, "pw"),
		OAuthClientID:    "alice@example.com",
	}

	out := s.Probe(context.Background(), sub, "alice@example.com")
	if out.State != extsubmit.OutcomePermanent {
		t.Fatalf("state = %q; want permanent; diagnostic: %s", out.State, out.Diagnostic)
	}
}

// TestSubmit_MultipleRcpt verifies that all RCPT TO addresses are sent.
func TestSubmit_MultipleRcpt(t *testing.T) {
	var rcpts []string
	srv := newSMTPServer(t, func(conn net.Conn) {
		defer conn.Close()
		r := bufio.NewReader(conn)
		w := bufio.NewWriter(conn)
		srvWrite(w, "220 smtp.test ESMTP")
		srvRead(r) // EHLO
		srvWrite(w, "250-smtp.test")
		srvWrite(w, "250 AUTH PLAIN")
		srvRead(r) // AUTH
		srvWrite(w, "235 2.7.0 ok")
		srvRead(r) // MAIL FROM
		srvWrite(w, "250 ok")
		// Accept all RCPT TOs until DATA.
		for {
			line := srvRead(r)
			if strings.HasPrefix(line, "RCPT") {
				rcpts = append(rcpts, line)
				srvWrite(w, "250 ok")
			} else if strings.HasPrefix(line, "DATA") {
				srvWrite(w, "354 send")
				break
			}
		}
		for {
			l := srvRead(r)
			if l == "." {
				break
			}
		}
		srvWrite(w, "250 2.0.0 ok")
		srvRead(r) // QUIT
	})

	s := &extsubmit.Submitter{DataKey: testDataKey, HostName: "client.test"}
	s.SetDialFn(func(ctx context.Context, network, addr string) (net.Conn, error) {
		return net.Dial("tcp", srv.addr())
	})

	sub := store.IdentitySubmission{
		IdentityID:       "identity-multi",
		SubmitHost:       "smtp.test",
		SubmitPort:       587,
		SubmitSecurity:   "none",
		SubmitAuthMethod: "password",
		PasswordCT:       sealSecret(t, "pw"),
	}
	env := extsubmit.Envelope{
		MailFrom: "alice@example.com",
		RcptTo:   []string{"to1@example.com", "to2@example.com", "bcc@example.com"},
		Body:     strings.NewReader("Subject: multi\r\n\r\nBody\r\n"),
	}

	out := s.Submit(context.Background(), sub, env)
	if out.State != extsubmit.OutcomeOK {
		t.Fatalf("state = %q; want ok; diagnostic: %s", out.State, out.Diagnostic)
	}
	if len(rcpts) != 3 {
		t.Errorf("got %d RCPT TO commands; want 3: %v", len(rcpts), rcpts)
	}
}

// TestSubmit_SubmitUsername_UsedForAuth verifies that Submit sends SubmitUsername
// as the SMTP AUTH username (not the MailFrom / identity email) when SubmitUsername
// is set on the IdentitySubmission (re #126).
func TestSubmit_SubmitUsername_UsedForAuth(t *testing.T) {
	var receivedAuthUser string
	srv := newSMTPServer(t, func(conn net.Conn) {
		defer conn.Close()
		r := bufio.NewReader(conn)
		w := bufio.NewWriter(conn)
		srvWrite(w, "220 smtp.test ESMTP")
		srvRead(r) // EHLO
		srvWrite(w, "250-smtp.test")
		srvWrite(w, "250 AUTH PLAIN")
		line := srvRead(r) // AUTH PLAIN <ir>
		if strings.HasPrefix(line, "AUTH PLAIN ") {
			ir := strings.TrimPrefix(line, "AUTH PLAIN ")
			raw, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(ir))
			parts := strings.Split(string(raw), "\x00")
			if len(parts) == 3 {
				receivedAuthUser = parts[1]
			}
		}
		srvWrite(w, "235 2.7.0 ok")
		srvRead(r) // MAIL FROM
		srvWrite(w, "250 ok")
		srvRead(r) // RCPT TO
		srvWrite(w, "250 ok")
		srvRead(r) // DATA
		srvWrite(w, "354 send")
		for {
			l := srvRead(r)
			if l == "." {
				break
			}
		}
		srvWrite(w, "250 2.0.0 ok")
		srvRead(r) // QUIT
	})

	s := &extsubmit.Submitter{DataKey: testDataKey, HostName: "client.test"}
	s.SetDialFn(func(ctx context.Context, network, addr string) (net.Conn, error) {
		return net.Dial("tcp", srv.addr())
	})

	sub := store.IdentitySubmission{
		IdentityID:       "identity-username",
		SubmitHost:       "smtp.test",
		SubmitPort:       587,
		SubmitSecurity:   "none",
		SubmitAuthMethod: "password",
		SubmitUsername:   "vorsitz",
		PasswordCT:       sealSecret(t, "secretpw"),
	}
	env := extsubmit.Envelope{
		MailFrom: "vorsitz@classic-computing.de",
		RcptTo:   []string{"bob@example.com"},
		Body:     strings.NewReader("Subject: test\r\n\r\nHello\r\n"),
	}

	out := s.Submit(context.Background(), sub, env)
	if out.State != extsubmit.OutcomeOK {
		t.Fatalf("state = %q; want ok; diagnostic: %s", out.State, out.Diagnostic)
	}
	// The AUTH PLAIN username must be the SubmitUsername, not the MailFrom.
	if receivedAuthUser != "vorsitz" {
		t.Errorf("AUTH PLAIN username = %q; want %q (SubmitUsername, not email)", receivedAuthUser, "vorsitz")
	}
}

// TestSubmit_SubmitUsername_EmptyFallsBackToEmail verifies that when SubmitUsername
// is empty, Submit falls back to using the identity's email (env.MailFrom) as the
// AUTH username (re #126).
func TestSubmit_SubmitUsername_EmptyFallsBackToEmail(t *testing.T) {
	var receivedAuthUser string
	srv := newSMTPServer(t, func(conn net.Conn) {
		defer conn.Close()
		r := bufio.NewReader(conn)
		w := bufio.NewWriter(conn)
		srvWrite(w, "220 smtp.test ESMTP")
		srvRead(r) // EHLO
		srvWrite(w, "250-smtp.test")
		srvWrite(w, "250 AUTH PLAIN")
		line := srvRead(r) // AUTH PLAIN <ir>
		if strings.HasPrefix(line, "AUTH PLAIN ") {
			ir := strings.TrimPrefix(line, "AUTH PLAIN ")
			raw, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(ir))
			parts := strings.Split(string(raw), "\x00")
			if len(parts) == 3 {
				receivedAuthUser = parts[1]
			}
		}
		srvWrite(w, "235 2.7.0 ok")
		srvRead(r) // MAIL FROM
		srvWrite(w, "250 ok")
		srvRead(r) // RCPT TO
		srvWrite(w, "250 ok")
		srvRead(r) // DATA
		srvWrite(w, "354 send")
		for {
			l := srvRead(r)
			if l == "." {
				break
			}
		}
		srvWrite(w, "250 2.0.0 ok")
		srvRead(r) // QUIT
	})

	s := &extsubmit.Submitter{DataKey: testDataKey, HostName: "client.test"}
	s.SetDialFn(func(ctx context.Context, network, addr string) (net.Conn, error) {
		return net.Dial("tcp", srv.addr())
	})

	sub := store.IdentitySubmission{
		IdentityID:       "identity-no-username",
		SubmitHost:       "smtp.test",
		SubmitPort:       587,
		SubmitSecurity:   "none",
		SubmitAuthMethod: "password",
		SubmitUsername:   "", // empty -> fall back to MailFrom
		PasswordCT:       sealSecret(t, "secretpw"),
	}
	env := extsubmit.Envelope{
		MailFrom: "alice@example.com",
		RcptTo:   []string{"bob@example.com"},
		Body:     strings.NewReader("Subject: test\r\n\r\nHello\r\n"),
	}

	out := s.Submit(context.Background(), sub, env)
	if out.State != extsubmit.OutcomeOK {
		t.Fatalf("state = %q; want ok; diagnostic: %s", out.State, out.Diagnostic)
	}
	// SubmitUsername empty -> AUTH PLAIN must use MailFrom.
	if receivedAuthUser != "alice@example.com" {
		t.Errorf("AUTH PLAIN username = %q; want %q (email fallback)", receivedAuthUser, "alice@example.com")
	}
}

// TestProbe_SubmitUsername_UsedForAuth verifies that Probe sends SubmitUsername
// as the SMTP AUTH username when set, not the fromAddr email (re #126).
func TestProbe_SubmitUsername_UsedForAuth(t *testing.T) {
	var receivedAuthUser string
	srv := newSMTPServer(t, func(conn net.Conn) {
		defer conn.Close()
		r := bufio.NewReader(conn)
		w := bufio.NewWriter(conn)
		srvWrite(w, "220 smtp.test ESMTP")
		srvRead(r) // EHLO
		srvWrite(w, "250-smtp.test")
		srvWrite(w, "250 AUTH PLAIN")
		for {
			line := srvRead(r)
			if line == "" {
				return
			}
			switch {
			case strings.HasPrefix(line, "AUTH PLAIN "):
				ir := strings.TrimPrefix(line, "AUTH PLAIN ")
				raw, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(ir))
				parts := strings.Split(string(raw), "\x00")
				if len(parts) == 3 {
					receivedAuthUser = parts[1]
				}
				srvWrite(w, "235 2.7.0 ok")
			case strings.HasPrefix(line, "MAIL"):
				srvWrite(w, "250 ok")
			case strings.HasPrefix(line, "RCPT"):
				srvWrite(w, "250 ok")
			case strings.HasPrefix(line, "RSET"):
				srvWrite(w, "250 ok")
			case line == "QUIT":
				srvWrite(w, "221 bye")
				return
			default:
				srvWrite(w, "250 ok")
			}
		}
	})

	s := &extsubmit.Submitter{DataKey: testDataKey, HostName: "client.test"}
	s.SetDialFn(func(ctx context.Context, network, addr string) (net.Conn, error) {
		return net.Dial("tcp", srv.addr())
	})

	sub := store.IdentitySubmission{
		IdentityID:       "identity-probe-username",
		SubmitHost:       "smtp.test",
		SubmitPort:       587,
		SubmitSecurity:   "none",
		SubmitAuthMethod: "password",
		SubmitUsername:   "vorsitz",
		PasswordCT:       sealSecret(t, "secretpw"),
	}

	out := s.Probe(context.Background(), sub, "vorsitz@classic-computing.de")
	if out.State != extsubmit.OutcomeOK {
		t.Fatalf("state = %q; want ok; diagnostic: %s", out.State, out.Diagnostic)
	}
	// Probe must authenticate as SubmitUsername, not the identity email.
	if receivedAuthUser != "vorsitz" {
		t.Errorf("Probe AUTH username = %q; want %q (SubmitUsername)", receivedAuthUser, "vorsitz")
	}
}

// generateTestCert issues a self-signed ECDSA P-256 cert for "smtp.test".
func generateTestCert(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "smtp.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		DNSNames:     []string{"smtp.test"},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	privDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privDER})
	cert, err := tls.X509KeyPair(certPEM, privPEM)
	if err != nil {
		t.Fatal(err)
	}
	return cert, certPEM
}
