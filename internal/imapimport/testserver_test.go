package imapimport

// testserver_test.go provides a reusable in-process IMAP test server
// for all 3a-3e sub-step tests. It is in package imapimport (white-box
// test) so it shares the seam types without requiring exported wrappers.
//
// The server is backed by imapmemserver (an in-memory backend shipped
// with emersion/go-imap/v2). Both an implicit-TLS listener and a
// STARTTLS listener are started on random localhost ports.
// A self-signed certificate is generated per test run.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

// testIMAPServer is the reusable fixture for all sub-step tests.
// TLS is always available; both implicit and STARTTLS modes are exercised.
type testIMAPServer struct {
	// ImplicitAddr is the address of the TLS listener.
	ImplicitAddr string
	// StartTLSAddr is the address of the STARTTLS listener.
	StartTLSAddr string
	// ClientTLSConfig contains the self-signed CA pool for clients.
	ClientTLSConfig *tls.Config
	// Backend is the imapmemserver; tests add users via addUser().
	Backend *imapmemserver.Server

	implicitSrv *imapserver.Server
	starttlsSrv *imapserver.Server
}

// startTestIMAPServer starts two IMAP listeners (implicit TLS and
// STARTTLS) on random localhost ports. Returns a handle with the
// addresses and a client TLS config. Cleanup is registered with t.
func startTestIMAPServer(t *testing.T) *testIMAPServer {
	t.Helper()

	// Self-signed cert for 127.0.0.1.
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("testserver: gen key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("testserver: create cert: %v", err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}
	leaf, _ := x509.ParseCertificate(der)
	cert.Leaf = leaf
	serverTLS := &tls.Config{Certificates: []tls.Certificate{cert}}

	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	clientTLS := &tls.Config{RootCAs: pool, ServerName: "127.0.0.1"}

	backend := imapmemserver.New()

	// Implicit-TLS listener: wrap in TLS before imapserver.Serve.
	implicitLn, err := tls.Listen("tcp", "127.0.0.1:0", serverTLS)
	if err != nil {
		t.Fatalf("testserver: implicit TLS listen: %v", err)
	}
	implicitSrv := imapserver.New(&imapserver.Options{
		NewSession: func(_ *imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return backend.NewSession(), nil, nil
		},
		Caps: imap.CapSet{imap.CapIMAP4rev2: {}},
		// InsecureAuth: false because TLS is already active.
	})

	// STARTTLS listener: plaintext TCP, TLSConfig enables STARTTLS.
	starttlsLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		implicitLn.Close()
		t.Fatalf("testserver: STARTTLS listen: %v", err)
	}
	starttlsSrv := imapserver.New(&imapserver.Options{
		NewSession: func(_ *imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return backend.NewSession(), nil, nil
		},
		Caps:      imap.CapSet{imap.CapIMAP4rev2: {}},
		TLSConfig: serverTLS, // triggers STARTTLS advertisement
	})

	go func() { _ = implicitSrv.Serve(implicitLn) }()
	go func() { _ = starttlsSrv.Serve(starttlsLn) }()

	ts := &testIMAPServer{
		ImplicitAddr:    implicitLn.Addr().String(),
		StartTLSAddr:    starttlsLn.Addr().String(),
		ClientTLSConfig: clientTLS,
		Backend:         backend,
		implicitSrv:     implicitSrv,
		starttlsSrv:     starttlsSrv,
	}
	t.Cleanup(func() {
		ts.implicitSrv.Close()
		ts.starttlsSrv.Close()
	})
	return ts
}

// addUser creates a user in the in-process server with an INBOX.
// Returns the *imapmemserver.User so callers can add mailboxes/messages.
func (ts *testIMAPServer) addUser(username, password string) *imapmemserver.User {
	u := imapmemserver.NewUser(username, password)
	ts.Backend.AddUser(u)
	if err := u.Create("INBOX", nil); err != nil {
		panic("testserver.addUser: create INBOX: " + err.Error())
	}
	return u
}
