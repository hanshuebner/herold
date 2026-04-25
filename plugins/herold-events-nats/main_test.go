package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanshuebner/herold/plugins/sdk"
)

// TestConnect_Plain drives OnConfigure against the in-process stub and
// confirms the plugin successfully connects.
func TestConnect_Plain(t *testing.T) {
	t.Parallel()
	stub := newNATSStub(t)
	defer stub.Close()

	h := newHandler()
	if err := h.OnConfigure(context.Background(), map[string]any{
		"url": stub.URL(),
	}); err != nil {
		t.Fatalf("OnConfigure: %v", err)
	}
	defer h.OnShutdown(context.Background())

	if err := h.OnHealth(context.Background()); err != nil {
		t.Fatalf("OnHealth: %v", err)
	}
}

// TestPublish_HappyPath confirms a published event lands at the stub
// with the expected subject + payload.
func TestPublish_HappyPath(t *testing.T) {
	t.Parallel()
	stub := newNATSStub(t)
	defer stub.Close()

	h := newHandler()
	if err := h.OnConfigure(context.Background(), map[string]any{
		"url":            stub.URL(),
		"subject_prefix": "herold",
	}); err != nil {
		t.Fatalf("OnConfigure: %v", err)
	}
	defer h.OnShutdown(context.Background())

	res, err := h.EventsPublish(context.Background(), sdk.EventsPublishParams{
		Event: map[string]any{
			"id":      "01ABCDEF",
			"kind":    "mail.received",
			"subject": "example.com",
			"payload": map[string]any{
				"message_id": "1",
				"sender":     "a@example.com",
			},
		},
	})
	if err != nil {
		t.Fatalf("EventsPublish: %v", err)
	}
	if !res.Ack {
		t.Fatalf("Ack=false")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pubs, err := stub.WaitForPub(ctx, 1)
	if err != nil {
		t.Fatalf("WaitForPub: %v", err)
	}
	if len(pubs) < 1 {
		t.Fatalf("no pubs captured")
	}
	want := "herold.mail.received.example.com"
	if pubs[0].Subject != want {
		t.Fatalf("subject: got %q want %q", pubs[0].Subject, want)
	}
	if len(pubs[0].Payload) == 0 {
		t.Fatalf("payload empty")
	}
}

// TestPublish_MissingKind reports an error rather than silently
// dropping the event.
func TestPublish_MissingKind(t *testing.T) {
	t.Parallel()
	stub := newNATSStub(t)
	defer stub.Close()

	h := newHandler()
	if err := h.OnConfigure(context.Background(), map[string]any{
		"url": stub.URL(),
	}); err != nil {
		t.Fatalf("OnConfigure: %v", err)
	}
	defer h.OnShutdown(context.Background())
	_, err := h.EventsPublish(context.Background(), sdk.EventsPublishParams{
		Event: map[string]any{"id": "x"},
	})
	if err == nil {
		t.Fatalf("expected error for missing kind")
	}
}

// TestSubject_SanitizesIllegalChars confirms operator subjects with
// illegal NATS chars are sanitized rather than rejected.
func TestSubject_SanitizesIllegalChars(t *testing.T) {
	t.Parallel()
	stub := newNATSStub(t)
	defer stub.Close()

	h := newHandler()
	if err := h.OnConfigure(context.Background(), map[string]any{"url": stub.URL()}); err != nil {
		t.Fatalf("OnConfigure: %v", err)
	}
	defer h.OnShutdown(context.Background())

	_, err := h.EventsPublish(context.Background(), sdk.EventsPublishParams{
		Event: map[string]any{
			"kind":    "auth.success",
			"subject": "user 1.>",
		},
	})
	if err != nil {
		t.Fatalf("EventsPublish: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pubs, err := stub.WaitForPub(ctx, 1)
	if err != nil {
		t.Fatalf("WaitForPub: %v", err)
	}
	got := pubs[0].Subject
	// Dots pass through (subject hierarchy); whitespace and ">" are
	// rewritten to "_".
	want := "herold.auth.success.user_1._"
	if got != want {
		t.Fatalf("subject: got %q want %q", got, want)
	}
}

// TestConnect_TLS exercises the mTLS / ca-rooted code path: the stub
// runs with a self-signed server cert; the plugin connects with that
// CA pinned.
func TestConnect_TLS(t *testing.T) {
	t.Parallel()
	caPath, certPath, keyPath, serverCfg := generateSelfSignedCert(t)
	stub := newNATSStubTLS(t, serverCfg)
	defer stub.Close()
	_ = certPath
	_ = keyPath

	h := newHandler()
	err := h.OnConfigure(context.Background(), map[string]any{
		"url":         stub.URL(),
		"tls_ca_file": caPath,
	})
	if err != nil {
		t.Fatalf("OnConfigure (TLS): %v", err)
	}
	defer h.OnShutdown(context.Background())
	if err := h.OnHealth(context.Background()); err != nil {
		t.Fatalf("OnHealth: %v", err)
	}
}

// TestShutdown_ClosesConnection confirms OnShutdown is idempotent and
// nils the conn so a follow-up OnHealth fails cleanly.
func TestShutdown_ClosesConnection(t *testing.T) {
	t.Parallel()
	stub := newNATSStub(t)
	defer stub.Close()
	h := newHandler()
	if err := h.OnConfigure(context.Background(), map[string]any{"url": stub.URL()}); err != nil {
		t.Fatalf("OnConfigure: %v", err)
	}
	if err := h.OnShutdown(context.Background()); err != nil {
		t.Fatalf("OnShutdown: %v", err)
	}
	// Second OnShutdown is a no-op.
	if err := h.OnShutdown(context.Background()); err != nil {
		t.Fatalf("OnShutdown(2nd): %v", err)
	}
	if err := h.OnHealth(context.Background()); err == nil {
		t.Fatalf("OnHealth after shutdown should fail")
	}
}

// generateSelfSignedCert produces a fresh ECDSA cert + key the test
// will write to disk for the plugin to consume, plus a tls.Config the
// stub server uses.
func generateSelfSignedCert(t *testing.T) (caPath, certPath, keyPath string, serverCfg *tls.Config) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	dir := t.TempDir()
	caPath = filepath.Join(dir, "ca.pem")
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	pemCert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(caPath, pemCert, 0o600); err != nil {
		t.Fatalf("write ca: %v", err)
	}
	if err := os.WriteFile(certPath, pemCert, 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	keyDer, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDer})
	if err := os.WriteFile(keyPath, pemKey, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	cert, err := tls.X509KeyPair(pemCert, pemKey)
	if err != nil {
		t.Fatalf("x509 keypair: %v", err)
	}
	serverCfg = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	return
}
