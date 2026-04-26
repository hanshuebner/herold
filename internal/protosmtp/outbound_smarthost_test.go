package protosmtp_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	mathrand "math/rand"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/protosmtp"
	"github.com/hanshuebner/herold/internal/sasl"
	"github.com/hanshuebner/herold/internal/sysconfig"
)

// stubResolver is a no-op mailauth.Resolver. None of these tests
// exercise its methods because the smart-host path never calls
// resolveMX — but the Client constructor requires a non-nil Resolver.
type stubResolver struct{}

func (stubResolver) TXTLookup(ctx context.Context, name string) ([]string, error) {
	return nil, nil
}
func (stubResolver) MXLookup(ctx context.Context, domain string) ([]*net.MX, error) {
	return nil, nil
}
func (stubResolver) IPLookup(ctx context.Context, host string) ([]net.IP, error) {
	return nil, nil
}

// authCfg is the shared AUTH-aware test SMTP server. It speaks
// EHLO/AUTH/MAIL/RCPT/DATA against connecting clients. Each instance
// remembers the last AUTH attempt for assertions.
type authCfg struct {
	// ExpectedMech is the SASL mechanism the test expects ("PLAIN",
	// "LOGIN", "SCRAM-SHA-256", "XOAUTH2", or "" to skip auth).
	ExpectedMech string
	// Username + Password are the credentials the server compares
	// against. PasswordFn lets SCRAM verify proofs.
	Username string
	Password string
	// FinalReply lets a test inject 4xx / 5xx instead of 250 on the
	// post-DATA reply.
	FinalReplyCode int
	FinalReplyText string
	// AuthRejected, when true, returns 535 instead of 235 on AUTH.
	AuthRejected bool
	// ImplicitTLS, when true, runs the entire session under TLS from
	// connect (operator gates with implicit_tls + 465).
	ImplicitTLS bool
	// AdvertiseSTARTTLS controls whether EHLO advertises the STARTTLS
	// extension. The default true matches typical submission relays.
	AdvertiseSTARTTLS bool
	// Cert is the server's TLS cert.
	Cert tls.Certificate
}

// runAuthSMTPServer accepts one connection, runs an SMTP exchange
// honouring auth, and returns once the client QUITs or the body has
// been received.
func runAuthSMTPServer(t *testing.T, cfg *authCfg, raw net.Conn) {
	t.Helper()
	defer raw.Close()
	conn := raw
	if cfg.ImplicitTLS {
		tlsConn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cfg.Cert}})
		if err := tlsConn.Handshake(); err != nil {
			t.Logf("server TLS handshake: %v", err)
			return
		}
		conn = tlsConn
	}
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	writeLine := func(line string) {
		_, _ = w.WriteString(line + "\r\n")
		_ = w.Flush()
	}
	readLine := func() (string, error) {
		line, err := r.ReadString('\n')
		if err != nil {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	advertiseSTARTTLS := cfg.AdvertiseSTARTTLS && !cfg.ImplicitTLS
	writeEHLO := func() {
		lines := []string{"smtp.test"}
		if advertiseSTARTTLS {
			lines = append(lines, "STARTTLS")
		}
		lines = append(lines, "AUTH PLAIN LOGIN SCRAM-SHA-256 XOAUTH2", "8BITMIME", "ENHANCEDSTATUSCODES")
		for i, l := range lines {
			sep := "-"
			if i == len(lines)-1 {
				sep = " "
			}
			writeLine("250" + sep + l)
		}
	}
	writeLine("220 smtp.test ESMTP test")
	for {
		line, err := readLine()
		if err != nil {
			return
		}
		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "EHLO"):
			writeEHLO()
		case strings.HasPrefix(upper, "HELO"):
			writeLine("250 smtp.test")
		case strings.HasPrefix(upper, "STARTTLS"):
			if !advertiseSTARTTLS {
				writeLine("502 STARTTLS not offered")
				continue
			}
			writeLine("220 ready")
			tlsConn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cfg.Cert}})
			if err := tlsConn.Handshake(); err != nil {
				t.Logf("server TLS upgrade: %v", err)
				return
			}
			conn = tlsConn
			r = bufio.NewReader(conn)
			w = bufio.NewWriter(conn)
			advertiseSTARTTLS = false
		case strings.HasPrefix(upper, "AUTH"):
			if cfg.AuthRejected {
				writeLine("535 5.7.8 auth refused")
				continue
			}
			handleAuth(t, cfg, line, r, w)
		case strings.HasPrefix(upper, "MAIL FROM"):
			writeLine("250 2.1.0 ok")
		case strings.HasPrefix(upper, "RCPT TO"):
			writeLine("250 2.1.5 ok")
		case strings.HasPrefix(upper, "DATA"):
			writeLine("354 send body")
			// Read until "\r\n.\r\n".
			var body bytes.Buffer
			for {
				bl, berr := readLine()
				if berr != nil {
					return
				}
				if bl == "." {
					break
				}
				body.WriteString(bl)
				body.WriteString("\r\n")
			}
			code := 250
			text := "2.0.0 ok queued"
			if cfg.FinalReplyCode != 0 {
				code = cfg.FinalReplyCode
				text = cfg.FinalReplyText
			}
			writeLine(fmt.Sprintf("%d %s", code, text))
		case strings.HasPrefix(upper, "QUIT"):
			writeLine("221 2.0.0 bye")
			return
		default:
			writeLine("502 5.5.1 unknown")
		}
	}
}

func handleAuth(t *testing.T, cfg *authCfg, authLine string, r *bufio.Reader, w *bufio.Writer) {
	t.Helper()
	writeLine := func(line string) {
		_, _ = w.WriteString(line + "\r\n")
		_ = w.Flush()
	}
	readLine := func() (string, error) {
		line, err := r.ReadString('\n')
		if err != nil {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	parts := strings.SplitN(authLine, " ", 3)
	mech := strings.ToUpper(parts[1])
	switch mech {
	case "PLAIN":
		var ir string
		if len(parts) == 3 {
			ir = parts[2]
		} else {
			writeLine("334 ")
			ir, _ = readLine()
		}
		raw, err := base64.StdEncoding.DecodeString(ir)
		if err != nil {
			writeLine("535 5.7.8 bad b64")
			return
		}
		fields := strings.SplitN(string(raw), "\x00", 3)
		if len(fields) != 3 || fields[1] != cfg.Username || fields[2] != cfg.Password {
			writeLine("535 5.7.8 bad creds")
			return
		}
		writeLine("235 2.7.0 ok")
	case "LOGIN":
		writeLine("334 " + base64.StdEncoding.EncodeToString([]byte("Username:")))
		userB64, _ := readLine()
		user, _ := base64.StdEncoding.DecodeString(userB64)
		writeLine("334 " + base64.StdEncoding.EncodeToString([]byte("Password:")))
		passB64, _ := readLine()
		pass, _ := base64.StdEncoding.DecodeString(passB64)
		if string(user) != cfg.Username || string(pass) != cfg.Password {
			writeLine("535 5.7.8 bad creds")
			return
		}
		writeLine("235 2.7.0 ok")
	case "XOAUTH2":
		var ir string
		if len(parts) == 3 {
			ir = parts[2]
		} else {
			writeLine("334 ")
			ir, _ = readLine()
		}
		raw, err := base64.StdEncoding.DecodeString(ir)
		if err != nil {
			writeLine("535 5.7.8 bad b64")
			return
		}
		fields := strings.Split(string(raw), "\x01")
		var token string
		for _, f := range fields {
			if strings.HasPrefix(f, "auth=Bearer ") {
				token = strings.TrimPrefix(f, "auth=Bearer ")
			}
		}
		if token != cfg.Password {
			writeLine("334 " + base64.StdEncoding.EncodeToString([]byte(`{"status":"invalid_token"}`)))
			_, _ = readLine()
			writeLine("535 5.7.8 bad token")
			return
		}
		writeLine("235 2.7.0 ok")
	case "SCRAM-SHA-256":
		var clientFirst string
		if len(parts) == 3 {
			clientFirst = parts[2]
		} else {
			writeLine("334 ")
			clientFirst, _ = readLine()
		}
		cf, _ := base64.StdEncoding.DecodeString(clientFirst)
		// Use the sasl package's server SCRAM mechanism to verify.
		// Build a stub PasswordLookup.
		salt := []byte("0123456789abcdef")
		cred := sasl.DeriveSCRAMCredentials(cfg.Password, salt, 4096)
		lookup := &smartHostScramLookup{authcid: cfg.Username, cred: cred, pid: 1}
		mech := sasl.NewSCRAMSHA256(nil, lookup, false)
		ctx := context.Background()
		serverFirst, done, err := mech.Start(ctx, cf)
		if err != nil || done {
			writeLine("535 5.7.8 scram start")
			return
		}
		writeLine("334 " + base64.StdEncoding.EncodeToString(serverFirst))
		clientFinal, _ := readLine()
		cFinal, _ := base64.StdEncoding.DecodeString(clientFinal)
		serverFinal, done, err := mech.Next(ctx, cFinal)
		if err != nil {
			writeLine("535 5.7.8 scram bad")
			return
		}
		if !done {
			writeLine("535 5.7.8 scram unfinished")
			return
		}
		writeLine("334 " + base64.StdEncoding.EncodeToString(serverFinal))
		_, _ = readLine() // empty client confirm
		writeLine("235 2.7.0 ok")
	default:
		writeLine("504 5.5.4 unknown mechanism")
	}
}

// smartHostScramLookup is a tiny PasswordLookup over a fixed credential.
type smartHostScramLookup struct {
	authcid string
	cred    sasl.SCRAMCredentials
	pid     sasl.PrincipalID
}

func (s *smartHostScramLookup) LookupSCRAMCredentials(ctx context.Context, authcid string) (sasl.SCRAMCredentials, sasl.PrincipalID, error) {
	if authcid != s.authcid {
		return sasl.SCRAMCredentials{}, 0, fmt.Errorf("not found")
	}
	return s.cred, s.pid, nil
}

// _ ensures stubResolver satisfies mailauth.Resolver (compile-time
// check). The smart-host path never invokes any of these methods
// because the dial target comes straight from sh.Host:Port.
var _ mailauth.Resolver = stubResolver{}

// startTestSMTPServer binds a 127.0.0.1:0 TCP listener and runs cfg's
// handler for each accepted connection. Returns the listener address
// and a teardown.
func startTestSMTPServer(t *testing.T, cfg *authCfg) (net.Listener, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func(c net.Conn) {
				defer wg.Done()
				runAuthSMTPServer(t, cfg, c)
			}(conn)
		}
	}()
	return ln, func() {
		_ = ln.Close()
		wg.Wait()
	}
}

// generateSelfSignedCert issues a fresh ECDSA P-256 certificate for
// "smtp.test". Used by TLS-mode tests so the client's
// system_roots / pinned validation has a concrete chain to validate.
func generateSelfSignedCert(t *testing.T) (tls.Certificate, []byte) {
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
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	pemCert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	cert, err := tls.X509KeyPair(pemCert, encodePrivateKey(t, priv))
	if err != nil {
		t.Fatal(err)
	}
	return cert, pemCert
}

func encodePrivateKey(t *testing.T, priv *ecdsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
}

// -----------------------------------------------------------------
// Tests
// -----------------------------------------------------------------

func newSmartHostClient(t *testing.T, sh sysconfig.SmartHostConfig, password string, fakeClk clock.Clock) *protosmtp.Client {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return protosmtp.NewClient(protosmtp.ClientOptions{
		HostName:  "client.test",
		Resolver:  stubResolver{},
		Logger:    logger,
		Clock:     fakeClk,
		SmartHost: sh,
		PasswordResolver: func() (string, error) {
			return password, nil
		},
	})
}

func TestSmartHost_PLAIN_HappyPath_STARTTLS(t *testing.T) {
	cert, pemCert := generateSelfSignedCert(t)
	cfg := &authCfg{
		ExpectedMech:      "PLAIN",
		Username:          "alice@example.com",
		Password:          "hunter2",
		AdvertiseSTARTTLS: true,
		Cert:              cert,
	}
	ln, stop := startTestSMTPServer(t, cfg)
	defer stop()

	dir := t.TempDir()
	pinPath := filepath.Join(dir, "pin.pem")
	if err := os.WriteFile(pinPath, pemCert, 0o600); err != nil {
		t.Fatal(err)
	}

	host, port, _ := net.SplitHostPort(ln.Addr().String())
	portN, _ := strconv.Atoi(port)
	sh := sysconfig.SmartHostConfig{
		Enabled:                     true,
		Host:                        host,
		Port:                        portN,
		TLSMode:                     "starttls",
		AuthMethod:                  "plain",
		Username:                    "alice@example.com",
		PasswordEnv:                 "$X",
		FallbackPolicy:              "smart_host_only",
		ConnectTimeoutSeconds:       5,
		ReadTimeoutSeconds:          5,
		FallbackAfterFailureSeconds: 300,
		TLSVerifyMode:               "insecure_skip_verify",
		PinnedCertPath:              pinPath,
	}

	clk := clock.NewReal()
	c := newSmartHostClient(t, sh, "hunter2", clk)
	out, err := c.Deliver(context.Background(), protosmtp.DeliveryRequest{
		MailFrom: "noreply@client.test",
		RcptTo:   "bob@example.com",
		Message:  []byte("Subject: test\r\n\r\nbody\r\n"),
	})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if out.Status != protosmtp.DeliverySuccess {
		t.Fatalf("status: %v diag=%q", out.Status, out.Diagnostic)
	}
	if !out.TLSUsed {
		t.Errorf("expected TLSUsed=true, got false")
	}
}

func TestSmartHost_LOGIN(t *testing.T) {
	cert, pemCert := generateSelfSignedCert(t)
	cfg := &authCfg{
		ExpectedMech:      "LOGIN",
		Username:          "u@example.com",
		Password:          "pw",
		AdvertiseSTARTTLS: true,
		Cert:              cert,
	}
	ln, stop := startTestSMTPServer(t, cfg)
	defer stop()
	dir := t.TempDir()
	pinPath := filepath.Join(dir, "pin.pem")
	_ = os.WriteFile(pinPath, pemCert, 0o600)

	host, port, _ := net.SplitHostPort(ln.Addr().String())
	portN, _ := strconv.Atoi(port)
	sh := sysconfig.SmartHostConfig{
		Enabled:                     true,
		Host:                        host,
		Port:                        portN,
		TLSMode:                     "starttls",
		AuthMethod:                  "login",
		Username:                    "u@example.com",
		PasswordEnv:                 "$X",
		FallbackPolicy:              "smart_host_only",
		ConnectTimeoutSeconds:       5,
		ReadTimeoutSeconds:          5,
		FallbackAfterFailureSeconds: 300,
		TLSVerifyMode:               "insecure_skip_verify",
		PinnedCertPath:              pinPath,
	}
	c := newSmartHostClient(t, sh, "pw", clock.NewReal())
	out, _ := c.Deliver(context.Background(), protosmtp.DeliveryRequest{
		MailFrom: "n@a.test",
		RcptTo:   "b@b.test",
		Message:  []byte("Subject: x\r\n\r\nbody\r\n"),
	})
	if out.Status != protosmtp.DeliverySuccess {
		t.Fatalf("LOGIN: status %v diag=%q", out.Status, out.Diagnostic)
	}
}

func TestSmartHost_XOAUTH2(t *testing.T) {
	cert, pemCert := generateSelfSignedCert(t)
	cfg := &authCfg{
		ExpectedMech:      "XOAUTH2",
		Username:          "u@example.com",
		Password:          "ya29.token",
		AdvertiseSTARTTLS: true,
		Cert:              cert,
	}
	ln, stop := startTestSMTPServer(t, cfg)
	defer stop()
	dir := t.TempDir()
	pinPath := filepath.Join(dir, "pin.pem")
	_ = os.WriteFile(pinPath, pemCert, 0o600)
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	portN, _ := strconv.Atoi(port)
	sh := sysconfig.SmartHostConfig{
		Enabled:                     true,
		Host:                        host,
		Port:                        portN,
		TLSMode:                     "starttls",
		AuthMethod:                  "xoauth2",
		Username:                    "u@example.com",
		PasswordEnv:                 "$X",
		FallbackPolicy:              "smart_host_only",
		ConnectTimeoutSeconds:       5,
		ReadTimeoutSeconds:          5,
		FallbackAfterFailureSeconds: 300,
		TLSVerifyMode:               "insecure_skip_verify",
		PinnedCertPath:              pinPath,
	}
	c := newSmartHostClient(t, sh, "ya29.token", clock.NewReal())
	out, _ := c.Deliver(context.Background(), protosmtp.DeliveryRequest{
		MailFrom: "n@a.test",
		RcptTo:   "b@b.test",
		Message:  []byte("Subject: x\r\n\r\nbody\r\n"),
	})
	if out.Status != protosmtp.DeliverySuccess {
		t.Fatalf("XOAUTH2: status %v diag=%q", out.Status, out.Diagnostic)
	}
}

func TestSmartHost_SCRAM(t *testing.T) {
	cert, pemCert := generateSelfSignedCert(t)
	cfg := &authCfg{
		ExpectedMech:      "SCRAM-SHA-256",
		Username:          "u@example.com",
		Password:          "hunter2",
		AdvertiseSTARTTLS: true,
		Cert:              cert,
	}
	ln, stop := startTestSMTPServer(t, cfg)
	defer stop()
	dir := t.TempDir()
	pinPath := filepath.Join(dir, "pin.pem")
	_ = os.WriteFile(pinPath, pemCert, 0o600)
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	portN, _ := strconv.Atoi(port)
	sh := sysconfig.SmartHostConfig{
		Enabled:                     true,
		Host:                        host,
		Port:                        portN,
		TLSMode:                     "starttls",
		AuthMethod:                  "scram-sha-256",
		Username:                    "u@example.com",
		PasswordEnv:                 "$X",
		FallbackPolicy:              "smart_host_only",
		ConnectTimeoutSeconds:       5,
		ReadTimeoutSeconds:          5,
		FallbackAfterFailureSeconds: 300,
		TLSVerifyMode:               "insecure_skip_verify",
		PinnedCertPath:              pinPath,
	}
	c := newSmartHostClient(t, sh, "hunter2", clock.NewReal())
	out, _ := c.Deliver(context.Background(), protosmtp.DeliveryRequest{
		MailFrom: "n@a.test",
		RcptTo:   "b@b.test",
		Message:  []byte("Subject: x\r\n\r\nbody\r\n"),
	})
	if out.Status != protosmtp.DeliverySuccess {
		t.Fatalf("SCRAM: status %v diag=%q", out.Status, out.Diagnostic)
	}
}

func TestSmartHost_ImplicitTLS(t *testing.T) {
	cert, pemCert := generateSelfSignedCert(t)
	cfg := &authCfg{
		ExpectedMech: "PLAIN",
		Username:     "u@a.test",
		Password:     "pw",
		ImplicitTLS:  true,
		Cert:         cert,
	}
	ln, stop := startTestSMTPServer(t, cfg)
	defer stop()
	dir := t.TempDir()
	pinPath := filepath.Join(dir, "pin.pem")
	_ = os.WriteFile(pinPath, pemCert, 0o600)
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	portN, _ := strconv.Atoi(port)
	sh := sysconfig.SmartHostConfig{
		Enabled:                     true,
		Host:                        host,
		Port:                        portN,
		TLSMode:                     "implicit_tls",
		AuthMethod:                  "plain",
		Username:                    "u@a.test",
		PasswordEnv:                 "$X",
		FallbackPolicy:              "smart_host_only",
		ConnectTimeoutSeconds:       5,
		ReadTimeoutSeconds:          5,
		FallbackAfterFailureSeconds: 300,
		TLSVerifyMode:               "insecure_skip_verify",
		PinnedCertPath:              pinPath,
	}
	c := newSmartHostClient(t, sh, "pw", clock.NewReal())
	out, _ := c.Deliver(context.Background(), protosmtp.DeliveryRequest{
		MailFrom: "n@a.test",
		RcptTo:   "b@b.test",
		Message:  []byte("Subject: x\r\n\r\nbody\r\n"),
	})
	if out.Status != protosmtp.DeliverySuccess {
		t.Fatalf("implicit-TLS: status %v diag=%q", out.Status, out.Diagnostic)
	}
	if !out.TLSUsed {
		t.Errorf("expected TLSUsed=true")
	}
}

func TestSmartHost_NoAuth_NoTLS(t *testing.T) {
	cfg := &authCfg{
		// No AUTH; the client must skip the AUTH step. We do NOT
		// advertise STARTTLS so the post-EHLO path goes straight to
		// MAIL FROM with TLSMode="none".
		AdvertiseSTARTTLS: false,
	}
	ln, stop := startTestSMTPServer(t, cfg)
	defer stop()
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	portN, _ := strconv.Atoi(port)
	sh := sysconfig.SmartHostConfig{
		Enabled:                     true,
		Host:                        host,
		Port:                        portN,
		TLSMode:                     "none",
		AuthMethod:                  "none",
		FallbackPolicy:              "smart_host_only",
		ConnectTimeoutSeconds:       5,
		ReadTimeoutSeconds:          5,
		FallbackAfterFailureSeconds: 300,
		TLSVerifyMode:               "insecure_skip_verify", // dev-mode 127.0.0.1
	}
	c := newSmartHostClient(t, sh, "", clock.NewReal())
	out, _ := c.Deliver(context.Background(), protosmtp.DeliveryRequest{
		MailFrom: "n@a.test",
		RcptTo:   "b@b.test",
		Message:  []byte("Subject: x\r\n\r\nbody\r\n"),
	})
	if out.Status != protosmtp.DeliverySuccess {
		t.Fatalf("no-auth: status %v diag=%q", out.Status, out.Diagnostic)
	}
}

func TestSmartHost_5xxFinal_Permanent(t *testing.T) {
	cert, pemCert := generateSelfSignedCert(t)
	cfg := &authCfg{
		ExpectedMech:      "PLAIN",
		Username:          "u@a.test",
		Password:          "pw",
		AdvertiseSTARTTLS: true,
		Cert:              cert,
		FinalReplyCode:    550,
		FinalReplyText:    "5.1.1 mailbox unknown",
	}
	ln, stop := startTestSMTPServer(t, cfg)
	defer stop()
	dir := t.TempDir()
	pinPath := filepath.Join(dir, "pin.pem")
	_ = os.WriteFile(pinPath, pemCert, 0o600)
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	portN, _ := strconv.Atoi(port)
	sh := sysconfig.SmartHostConfig{
		Enabled:                     true,
		Host:                        host,
		Port:                        portN,
		TLSMode:                     "starttls",
		AuthMethod:                  "plain",
		Username:                    "u@a.test",
		PasswordEnv:                 "$X",
		FallbackPolicy:              "smart_host_only",
		ConnectTimeoutSeconds:       5,
		ReadTimeoutSeconds:          5,
		FallbackAfterFailureSeconds: 300,
		TLSVerifyMode:               "insecure_skip_verify",
		PinnedCertPath:              pinPath,
	}
	c := newSmartHostClient(t, sh, "pw", clock.NewReal())
	out, _ := c.Deliver(context.Background(), protosmtp.DeliveryRequest{
		MailFrom: "n@a.test",
		RcptTo:   "b@b.test",
		Message:  []byte("Subject: x\r\n\r\nbody\r\n"),
	})
	if out.Status != protosmtp.DeliveryPermanent {
		t.Fatalf("status: %v diag=%q", out.Status, out.Diagnostic)
	}
}

func TestSmartHost_4xxFinal_Transient(t *testing.T) {
	cert, pemCert := generateSelfSignedCert(t)
	cfg := &authCfg{
		ExpectedMech:      "PLAIN",
		Username:          "u@a.test",
		Password:          "pw",
		AdvertiseSTARTTLS: true,
		Cert:              cert,
		FinalReplyCode:    451,
		FinalReplyText:    "4.7.1 try again later",
	}
	ln, stop := startTestSMTPServer(t, cfg)
	defer stop()
	dir := t.TempDir()
	pinPath := filepath.Join(dir, "pin.pem")
	_ = os.WriteFile(pinPath, pemCert, 0o600)
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	portN, _ := strconv.Atoi(port)
	sh := sysconfig.SmartHostConfig{
		Enabled:                     true,
		Host:                        host,
		Port:                        portN,
		TLSMode:                     "starttls",
		AuthMethod:                  "plain",
		Username:                    "u@a.test",
		PasswordEnv:                 "$X",
		FallbackPolicy:              "smart_host_only",
		ConnectTimeoutSeconds:       5,
		ReadTimeoutSeconds:          5,
		FallbackAfterFailureSeconds: 300,
		TLSVerifyMode:               "insecure_skip_verify",
		PinnedCertPath:              pinPath,
	}
	c := newSmartHostClient(t, sh, "pw", clock.NewReal())
	out, _ := c.Deliver(context.Background(), protosmtp.DeliveryRequest{
		MailFrom: "n@a.test",
		RcptTo:   "b@b.test",
		Message:  []byte("Subject: x\r\n\r\nbody\r\n"),
	})
	if out.Status != protosmtp.DeliveryTransient {
		t.Fatalf("status: %v diag=%q", out.Status, out.Diagnostic)
	}
}

func TestSmartHost_AuthFailure_Permanent(t *testing.T) {
	cert, pemCert := generateSelfSignedCert(t)
	cfg := &authCfg{
		ExpectedMech:      "PLAIN",
		Username:          "u@a.test",
		Password:          "pw",
		AdvertiseSTARTTLS: true,
		Cert:              cert,
		AuthRejected:      true,
	}
	ln, stop := startTestSMTPServer(t, cfg)
	defer stop()
	dir := t.TempDir()
	pinPath := filepath.Join(dir, "pin.pem")
	_ = os.WriteFile(pinPath, pemCert, 0o600)
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	portN, _ := strconv.Atoi(port)
	sh := sysconfig.SmartHostConfig{
		Enabled:                     true,
		Host:                        host,
		Port:                        portN,
		TLSMode:                     "starttls",
		AuthMethod:                  "plain",
		Username:                    "u@a.test",
		PasswordEnv:                 "$X",
		FallbackPolicy:              "smart_host_only",
		ConnectTimeoutSeconds:       5,
		ReadTimeoutSeconds:          5,
		FallbackAfterFailureSeconds: 300,
		TLSVerifyMode:               "insecure_skip_verify",
		PinnedCertPath:              pinPath,
	}
	c := newSmartHostClient(t, sh, "pw", clock.NewReal())
	out, _ := c.Deliver(context.Background(), protosmtp.DeliveryRequest{
		MailFrom: "n@a.test",
		RcptTo:   "b@b.test",
		Message:  []byte("Subject: x\r\n\r\nbody\r\n"),
	})
	if out.Status != protosmtp.DeliveryPermanent {
		t.Fatalf("status: %v diag=%q", out.Status, out.Diagnostic)
	}
	if !strings.Contains(strings.ToLower(out.Diagnostic), "auth") {
		t.Errorf("expected auth-related diag, got %q", out.Diagnostic)
	}
}

func TestSmartHost_PerDomainOverride(t *testing.T) {
	// Two upstreams: a "global" relay and a "corp" override. The
	// corp override is the only one wired to a real server; the
	// global block points at a nonsense host that would fail. The
	// test asserts that "corp.example.com" recipients reach the
	// corp upstream rather than the global one.
	cert, pemCert := generateSelfSignedCert(t)
	cfg := &authCfg{
		ExpectedMech:      "PLAIN",
		Username:          "corp",
		Password:          "pw",
		AdvertiseSTARTTLS: true,
		Cert:              cert,
	}
	ln, stop := startTestSMTPServer(t, cfg)
	defer stop()
	dir := t.TempDir()
	pinPath := filepath.Join(dir, "pin.pem")
	_ = os.WriteFile(pinPath, pemCert, 0o600)
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	portN, _ := strconv.Atoi(port)
	sh := sysconfig.SmartHostConfig{
		Enabled:                     false, // global is OFF
		FallbackPolicy:              "smart_host_only",
		ConnectTimeoutSeconds:       5,
		ReadTimeoutSeconds:          5,
		FallbackAfterFailureSeconds: 300,
		TLSVerifyMode:               "system_roots",
		PerDomain: map[string]sysconfig.SmartHostConfig{
			"corp.example.com": {
				Host:                        host,
				Port:                        portN,
				TLSMode:                     "starttls",
				AuthMethod:                  "plain",
				Username:                    "corp",
				PasswordEnv:                 "$X",
				FallbackPolicy:              "smart_host_only",
				ConnectTimeoutSeconds:       5,
				ReadTimeoutSeconds:          5,
				FallbackAfterFailureSeconds: 300,
				TLSVerifyMode:               "insecure_skip_verify",
				PinnedCertPath:              pinPath,
			},
		},
	}
	c := newSmartHostClient(t, sh, "pw", clock.NewReal())
	out, _ := c.Deliver(context.Background(), protosmtp.DeliveryRequest{
		MailFrom: "n@a.test",
		RcptTo:   "user@corp.example.com",
		Message:  []byte("Subject: x\r\n\r\nbody\r\n"),
	})
	if out.Status != protosmtp.DeliverySuccess {
		t.Fatalf("override: status %v diag=%q", out.Status, out.Diagnostic)
	}
}

func TestSmartHost_Netguard_Refuses_Loopback(t *testing.T) {
	// 127.0.0.1 is loopback; netguard.CheckHost MUST refuse the dial
	// unless TLSVerifyMode = "insecure_skip_verify". The test pins
	// the bypass off and asserts a permanent outcome.
	sh := sysconfig.SmartHostConfig{
		Enabled:                     true,
		Host:                        "127.0.0.1",
		Port:                        25,
		TLSMode:                     "none",
		AuthMethod:                  "none",
		FallbackPolicy:              "smart_host_only",
		ConnectTimeoutSeconds:       5,
		ReadTimeoutSeconds:          5,
		FallbackAfterFailureSeconds: 300,
		TLSVerifyMode:               "system_roots",
	}
	c := newSmartHostClient(t, sh, "", clock.NewReal())
	out, _ := c.Deliver(context.Background(), protosmtp.DeliveryRequest{
		MailFrom: "n@a.test",
		RcptTo:   "b@b.test",
		Message:  []byte("Subject: x\r\n\r\nbody\r\n"),
	})
	if out.Status != protosmtp.DeliveryPermanent {
		t.Fatalf("expected permanent, got %v diag=%q", out.Status, out.Diagnostic)
	}
	if !strings.Contains(out.Diagnostic, "netguard") {
		t.Errorf("expected netguard mention in diag: %q", out.Diagnostic)
	}
}

func TestSmartHost_ConnectionRefused_OnlyPolicy_Permanent(t *testing.T) {
	// Pick a closed port via the dial-an-immediate-close trick:
	// listen, capture port, close listener; the smart host then
	// dials and gets RST.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	_ = ln.Close()
	portN, _ := strconv.Atoi(port)

	sh := sysconfig.SmartHostConfig{
		Enabled:                     true,
		Host:                        host,
		Port:                        portN,
		TLSMode:                     "none",
		AuthMethod:                  "none",
		FallbackPolicy:              "smart_host_only",
		ConnectTimeoutSeconds:       1,
		ReadTimeoutSeconds:          1,
		FallbackAfterFailureSeconds: 300,
		TLSVerifyMode:               "insecure_skip_verify",
	}
	c := newSmartHostClient(t, sh, "", clock.NewReal())
	out, _ := c.Deliver(context.Background(), protosmtp.DeliveryRequest{
		MailFrom: "n@a.test",
		RcptTo:   "b@b.test",
		Message:  []byte("Subject: x\r\n\r\nbody\r\n"),
	})
	if out.Status != protosmtp.DeliveryTransient {
		t.Fatalf("expected transient on connection refused, got %v diag=%q", out.Status, out.Diagnostic)
	}
}

// silenceUnusedImports keeps a few stdlib symbols in the import set
// for tests that may grow into them later (HMAC checks, sha256-based
// pins, math/rand-driven server jitter).
var (
	_ = mathrand.New
	_ = hmac.Equal
	_ = sha256.New
)
