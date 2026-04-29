package session_test

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/protosmtp/session"
)

// dialFakeSMTP creates an in-process fake SMTP server. handler is called
// with the server-side connection. It returns a *session.Session connected
// to the server.
func dialFakeSMTP(t *testing.T, handler func(srv net.Conn)) *session.Session {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		handler(conn)
	}()
	clientConn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		clientConn.Close()
		ln.Close()
		wg.Wait()
	})
	return session.New(clientConn)
}

func srvWriteln(w *bufio.Writer, s string) {
	_, _ = w.WriteString(s + "\r\n")
	_ = w.Flush()
}

func srvReadline(r *bufio.Reader) string {
	line, _ := r.ReadString('\n')
	return strings.TrimRight(line, "\r\n")
}

// TestEhlo_HappyPath verifies EHLO parsing for a multi-extension server.
func TestEhlo_HappyPath(t *testing.T) {
	sess := dialFakeSMTP(t, func(srv net.Conn) {
		r := bufio.NewReader(srv)
		w := bufio.NewWriter(srv)
		srvWriteln(w, "220 smtp.test ESMTP test")
		srvReadline(r) // EHLO
		srvWriteln(w, "250-smtp.test")
		srvWriteln(w, "250-STARTTLS")
		srvWriteln(w, "250-ENHANCEDSTATUSCODES")
		srvWriteln(w, "250-AUTH PLAIN LOGIN XOAUTH2")
		srvWriteln(w, "250 8BITMIME")
	})
	gr, err := sess.ReadGreeting()
	if err != nil {
		t.Fatalf("ReadGreeting: %v", err)
	}
	if gr.Code != 220 {
		t.Fatalf("greeting code = %d", gr.Code)
	}
	er, err := sess.Ehlo("client.test")
	if err != nil {
		t.Fatalf("Ehlo: %v", err)
	}
	if er.Code != 250 {
		t.Fatalf("ehlo code = %d", er.Code)
	}
	if !sess.HasExtension("STARTTLS") {
		t.Error("expected STARTTLS extension")
	}
	if !sess.HasExtension("AUTH") {
		t.Error("expected AUTH extension")
	}
	if !sess.HasExtension("8BITMIME") {
		t.Error("expected 8BITMIME extension")
	}
}

// TestMultilineReply verifies that a multi-line reply is parsed correctly.
func TestMultilineReply(t *testing.T) {
	sess := dialFakeSMTP(t, func(srv net.Conn) {
		r := bufio.NewReader(srv)
		w := bufio.NewWriter(srv)
		srvWriteln(w, "220 smtp.test ESMTP test")
		srvReadline(r) // EHLO
		srvWriteln(w, "250-FOO")
		srvWriteln(w, "250-BAR")
		srvWriteln(w, "250 BAZ")
	})
	if _, err := sess.ReadGreeting(); err != nil {
		t.Fatal(err)
	}
	r, err := sess.Ehlo("client.test")
	if err != nil {
		t.Fatalf("Ehlo: %v", err)
	}
	if r.Code != 250 {
		t.Fatalf("code = %d", r.Code)
	}
	if !strings.Contains(r.Text, "FOO") {
		t.Errorf("expected FOO in text, got %q", r.Text)
	}
	if !strings.Contains(r.Text, "BAR") {
		t.Errorf("expected BAR in text, got %q", r.Text)
	}
	if !strings.Contains(r.Text, "BAZ") {
		t.Errorf("expected BAZ in text, got %q", r.Text)
	}
}

// TestAuthPlain_InitialResponse verifies AUTH PLAIN sends the IR inline.
func TestAuthPlain_InitialResponse(t *testing.T) {
	var receivedAUTH string
	sess := dialFakeSMTP(t, func(srv net.Conn) {
		r := bufio.NewReader(srv)
		w := bufio.NewWriter(srv)
		srvWriteln(w, "220 smtp.test")
		srvReadline(r) // EHLO
		srvWriteln(w, "250 smtp.test")
		receivedAUTH = srvReadline(r) // AUTH PLAIN <ir>
		srvWriteln(w, "235 2.7.0 ok")
	})
	if _, err := sess.ReadGreeting(); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Ehlo("client.test"); err != nil {
		t.Fatal(err)
	}
	if err := sess.AuthPlain("alice@example.com", "secret"); err != nil {
		t.Fatalf("AuthPlain: %v", err)
	}
	if !strings.HasPrefix(receivedAUTH, "AUTH PLAIN ") {
		t.Errorf("expected AUTH PLAIN ..., got %q", receivedAUTH)
	}
	// Decode and verify the IR content.
	parts := strings.SplitN(receivedAUTH, " ", 3)
	if len(parts) != 3 {
		t.Fatalf("expected 3 parts in AUTH PLAIN line, got %d", len(parts))
	}
	decoded, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode IR: %v", err)
	}
	// Format: \x00 + user + \x00 + pass
	fields := strings.Split(string(decoded), "\x00")
	if len(fields) != 3 {
		t.Fatalf("expected 3 NUL-separated fields, got %d: %q", len(fields), decoded)
	}
	if fields[1] != "alice@example.com" {
		t.Errorf("user = %q; want alice@example.com", fields[1])
	}
	if fields[2] != "secret" {
		t.Errorf("pass = %q; want secret", fields[2])
	}
}

// TestAuthLogin_ChallengeResponse verifies the two-turn AUTH LOGIN exchange.
func TestAuthLogin_ChallengeResponse(t *testing.T) {
	var userReceived, passReceived string
	sess := dialFakeSMTP(t, func(srv net.Conn) {
		r := bufio.NewReader(srv)
		w := bufio.NewWriter(srv)
		srvWriteln(w, "220 smtp.test")
		srvReadline(r) // EHLO
		srvWriteln(w, "250 smtp.test")
		srvReadline(r) // AUTH LOGIN
		srvWriteln(w, "334 "+base64.StdEncoding.EncodeToString([]byte("Username:")))
		u := srvReadline(r)
		ub, _ := base64.StdEncoding.DecodeString(u)
		userReceived = string(ub)
		srvWriteln(w, "334 "+base64.StdEncoding.EncodeToString([]byte("Password:")))
		p := srvReadline(r)
		pb, _ := base64.StdEncoding.DecodeString(p)
		passReceived = string(pb)
		srvWriteln(w, "235 2.7.0 ok")
	})
	if _, err := sess.ReadGreeting(); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Ehlo("client.test"); err != nil {
		t.Fatal(err)
	}
	if err := sess.AuthLogin("user@example.com", "mypass"); err != nil {
		t.Fatalf("AuthLogin: %v", err)
	}
	if userReceived != "user@example.com" {
		t.Errorf("user = %q; want user@example.com", userReceived)
	}
	if passReceived != "mypass" {
		t.Errorf("pass = %q; want mypass", passReceived)
	}
}

// TestAuthXOAUTH2_InitialResponse verifies the XOAUTH2 SASL string format.
// The format is per RFC 7628: user=<email>\x01auth=Bearer <token>\x01\x01
func TestAuthXOAUTH2_InitialResponse(t *testing.T) {
	var receivedIR []byte
	sess := dialFakeSMTP(t, func(srv net.Conn) {
		r := bufio.NewReader(srv)
		w := bufio.NewWriter(srv)
		srvWriteln(w, "220 smtp.test")
		srvReadline(r) // EHLO
		srvWriteln(w, "250 smtp.test")
		authLine := srvReadline(r)
		// Extract the base64 IR from "AUTH XOAUTH2 <ir>"
		parts := strings.SplitN(authLine, " ", 3)
		if len(parts) == 3 {
			b, _ := base64.StdEncoding.DecodeString(parts[2])
			receivedIR = b
		}
		srvWriteln(w, "235 2.7.0 ok")
	})
	if _, err := sess.ReadGreeting(); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Ehlo("client.test"); err != nil {
		t.Fatal(err)
	}
	if err := sess.AuthXOAUTH2("alice@example.com", "ya29.token123"); err != nil {
		t.Fatalf("AuthXOAUTH2: %v", err)
	}
	// Expected per RFC 7628: "user=alice@example.com\x01auth=Bearer ya29.token123\x01\x01"
	expected := "user=alice@example.com\x01auth=Bearer ya29.token123\x01\x01"
	if string(receivedIR) != expected {
		t.Errorf("XOAUTH2 IR = %q; want %q", string(receivedIR), expected)
	}
}

// TestAuthXOAUTH2_Rejection verifies that a 334 JSON error challenge followed
// by 535 is returned as an error.
func TestAuthXOAUTH2_Rejection(t *testing.T) {
	sess := dialFakeSMTP(t, func(srv net.Conn) {
		r := bufio.NewReader(srv)
		w := bufio.NewWriter(srv)
		srvWriteln(w, "220 smtp.test")
		srvReadline(r) // EHLO
		srvWriteln(w, "250 smtp.test")
		srvReadline(r) // AUTH XOAUTH2 <ir>
		// Send JSON error challenge (base64({"status":"invalid_token"}))
		srvWriteln(w, "334 "+base64.StdEncoding.EncodeToString([]byte(`{"status":"invalid_token"}`)))
		srvReadline(r) // empty response
		srvWriteln(w, "535 5.7.8 bad token")
	})
	if _, err := sess.ReadGreeting(); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Ehlo("client.test"); err != nil {
		t.Fatal(err)
	}
	err := sess.AuthXOAUTH2("alice@example.com", "bad-token")
	if err == nil {
		t.Fatal("expected error on 535, got nil")
	}
	if !strings.Contains(err.Error(), "535") {
		t.Errorf("expected 535 in error, got %q", err.Error())
	}
}

// TestData_HappyPath verifies the DATA exchange returns the MTA queue-id.
func TestData_HappyPath(t *testing.T) {
	sess := dialFakeSMTP(t, func(srv net.Conn) {
		r := bufio.NewReader(srv)
		w := bufio.NewWriter(srv)
		srvWriteln(w, "220 smtp.test")
		srvReadline(r) // EHLO
		srvWriteln(w, "250 smtp.test")
		srvReadline(r) // MAIL FROM
		srvWriteln(w, "250 ok")
		srvReadline(r) // RCPT TO
		srvWriteln(w, "250 ok")
		srvReadline(r) // DATA
		srvWriteln(w, "354 send body")
		for {
			line := srvReadline(r)
			if line == "." {
				break
			}
		}
		srvWriteln(w, "250 2.0.0 queued as abc123")
	})
	if _, err := sess.ReadGreeting(); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Ehlo("client.test"); err != nil {
		t.Fatal(err)
	}
	if r, err := sess.MailFrom("from@example.com"); err != nil || !r.IsSuccess() {
		t.Fatalf("MailFrom: err=%v code=%d", err, r.Code)
	}
	if r, err := sess.RcptTo("to@example.com"); err != nil || !r.IsSuccess() {
		t.Fatalf("RcptTo: err=%v code=%d", err, r.Code)
	}
	body := strings.NewReader("Subject: test\r\n\r\nHello world\r\n")
	mtaID, err := sess.Data(body)
	if err != nil {
		t.Fatalf("Data: %v", err)
	}
	if !strings.Contains(mtaID, "abc123") {
		t.Errorf("mtaID = %q; want to contain abc123", mtaID)
	}
}

// TestDotStuffing verifies that a message line starting with '.' is escaped.
func TestDotStuffing(t *testing.T) {
	var receivedLines []string
	sess := dialFakeSMTP(t, func(srv net.Conn) {
		r := bufio.NewReader(srv)
		w := bufio.NewWriter(srv)
		srvWriteln(w, "220 smtp.test")
		srvReadline(r) // EHLO
		srvWriteln(w, "250 smtp.test")
		srvReadline(r) // MAIL FROM
		srvWriteln(w, "250 ok")
		srvReadline(r) // RCPT TO
		srvWriteln(w, "250 ok")
		srvReadline(r) // DATA
		srvWriteln(w, "354 send")
		for {
			line := srvReadline(r)
			if line == "." {
				break
			}
			receivedLines = append(receivedLines, line)
		}
		srvWriteln(w, "250 2.0.0 ok")
	})
	if _, err := sess.ReadGreeting(); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Ehlo("client.test"); err != nil {
		t.Fatal(err)
	}
	sess.MailFrom("a@a.test")
	sess.RcptTo("b@b.test")
	// Body contains a dot-only line that must be escaped ("dot-stuffed").
	body := strings.NewReader("Subject: x\r\n\r\n.hidden\r\nnormal\r\n")
	if _, err := sess.Data(body); err != nil {
		t.Fatalf("Data: %v", err)
	}
	found := false
	for _, line := range receivedLines {
		if line == "..hidden" {
			found = true
		}
		if line == ".hidden" {
			t.Error("dot line was not escaped")
		}
	}
	if !found {
		t.Errorf("expected '..hidden' in received lines, got %v", receivedLines)
	}
}

// TestParseReply_EnhancedStatus verifies that enhanced status codes are
// extracted from reply text when enhancedCodes is true.
func TestParseReply_EnhancedStatus(t *testing.T) {
	input := "250 2.1.0 Sender ok\r\n"
	r := bufio.NewReader(strings.NewReader(input))
	reply, err := session.ParseReply(r, true)
	if err != nil {
		t.Fatalf("ParseReply: %v", err)
	}
	if reply.Code != 250 {
		t.Errorf("code = %d; want 250", reply.Code)
	}
	if reply.Enhanced != "2.1.0" {
		t.Errorf("enhanced = %q; want 2.1.0", reply.Enhanced)
	}
	if reply.Text != "Sender ok" {
		t.Errorf("text = %q; want 'Sender ok'", reply.Text)
	}
}

// TestParseReply_MultiLineEnhanced verifies multi-line reply with enhanced codes.
func TestParseReply_MultiLineEnhanced(t *testing.T) {
	input := "250-2.0.0 First\r\n250-2.0.0 Second\r\n250 2.0.0 Third\r\n"
	r := bufio.NewReader(strings.NewReader(input))
	reply, err := session.ParseReply(r, true)
	if err != nil {
		t.Fatalf("ParseReply: %v", err)
	}
	if reply.Code != 250 {
		t.Errorf("code = %d; want 250", reply.Code)
	}
	// Enhanced is extracted from the first line only.
	if reply.Enhanced != "2.0.0" {
		t.Errorf("enhanced = %q; want 2.0.0", reply.Enhanced)
	}
}

// TestParseReply_NoEnhanced verifies that enhanced status is not extracted
// when enhancedCodes is false.
func TestParseReply_NoEnhanced(t *testing.T) {
	input := "250 2.1.0 Sender ok\r\n"
	r := bufio.NewReader(strings.NewReader(input))
	reply, err := session.ParseReply(r, false)
	if err != nil {
		t.Fatalf("ParseReply: %v", err)
	}
	if reply.Enhanced != "" {
		t.Errorf("enhanced = %q; want empty when enhancedCodes=false", reply.Enhanced)
	}
	// Full text including the enhanced prefix should be present.
	if !strings.Contains(reply.Text, "2.1.0") {
		t.Errorf("text = %q; want to contain 2.1.0", reply.Text)
	}
}

// TestParseReply_ShortLine verifies that a malformed short reply is rejected.
func TestParseReply_ShortLine(t *testing.T) {
	input := "25 short\r\n"
	r := bufio.NewReader(strings.NewReader(input))
	_, err := session.ParseReply(r, false)
	if err == nil {
		t.Fatal("expected error for short reply")
	}
}

// TestTLSUpgrade verifies that UpgradeConn replaces the connection and
// clears the extension map (forcing a re-EHLO).
func TestTLSUpgrade(t *testing.T) {
	cert, certPEM := generateTestCert(t)
	_ = certPEM
	serverTLS := &tls.Config{Certificates: []tls.Certificate{cert}}
	clientPool := x509.NewCertPool()
	clientPool.AppendCertsFromPEM(certPEM)
	clientTLS := &tls.Config{RootCAs: clientPool, ServerName: "smtp.test"}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		w := bufio.NewWriter(conn)
		srvWriteln(w, "220 smtp.test")
		srvReadline(r) // EHLO
		srvWriteln(w, "250-smtp.test")
		srvWriteln(w, "250 STARTTLS")
		srvReadline(r) // STARTTLS
		srvWriteln(w, "220 ready")
		// Upgrade to TLS.
		tlsConn := tls.Server(conn, serverTLS)
		if err := tlsConn.Handshake(); err != nil {
			return
		}
		r = bufio.NewReader(tlsConn)
		w = bufio.NewWriter(tlsConn)
		srvReadline(r) // EHLO post-TLS
		srvWriteln(w, "250-smtp.test")
		srvWriteln(w, "250 AUTH PLAIN")
	}()

	clientConn, _ := net.Dial("tcp", ln.Addr().String())
	t.Cleanup(func() {
		clientConn.Close()
		ln.Close()
		wg.Wait()
	})
	sess := session.New(clientConn)

	if _, err := sess.ReadGreeting(); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Ehlo("client.test"); err != nil {
		t.Fatal(err)
	}
	if !sess.HasExtension("STARTTLS") {
		t.Fatal("expected STARTTLS pre-upgrade")
	}

	// STARTTLS exchange.
	r, err := sess.Cmd("STARTTLS")
	if err != nil || r.Code != 220 {
		t.Fatalf("STARTTLS: err=%v code=%d", err, r.Code)
	}

	// Upgrade conn.
	tlsConn := tls.Client(sess.Conn(), clientTLS)
	if err := tlsConn.Handshake(); err != nil {
		t.Fatalf("TLS handshake: %v", err)
	}
	sess.UpgradeConn(tlsConn)

	// Extension map must be cleared after upgrade.
	if sess.HasExtension("STARTTLS") {
		t.Error("STARTTLS should be cleared after UpgradeConn")
	}

	// Re-EHLO.
	if _, err := sess.Ehlo("client.test"); err != nil {
		t.Fatal(err)
	}
	if !sess.HasExtension("AUTH") {
		t.Error("expected AUTH after re-EHLO")
	}
}

// generateTestCert issues a fresh ECDSA P-256 self-signed certificate for
// "smtp.test" and 127.0.0.1.
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
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
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

// silence unused import
var _ = fmt.Sprintf
