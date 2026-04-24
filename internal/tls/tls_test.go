package tls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// generateCert writes a self-signed ECDSA certificate + key to dir and
// returns the cert+key filepaths. The cert's SANs cover dnsNames; the CN is
// dnsNames[0].
func generateCert(t *testing.T, dir string, dnsNames []string) (certPath, keyPath string) {
	t.Helper()
	if len(dnsNames) == 0 {
		t.Fatalf("generateCert: at least one dnsName required")
	}
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: dnsNames[0]},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dnsNames,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")

	certPEM, err := os.Create(certPath)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	if err := pem.Encode(certPEM, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		t.Fatalf("pem encode cert: %v", err)
	}
	certPEM.Close()

	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM, err := os.Create(keyPath)
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	if err := pem.Encode(keyPEM, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}); err != nil {
		t.Fatalf("pem encode key: %v", err)
	}
	keyPEM.Close()
	return certPath, keyPath
}

func TestLoadFromFile(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := generateCert(t, dir, []string{"mail.example.test"})

	cert, err := LoadFromFile(certPath, keyPath)
	if err != nil {
		t.Fatalf("LoadFromFile: %v", err)
	}
	if cert.Leaf == nil {
		t.Fatalf("Leaf not populated")
	}
	if got := cert.Leaf.Subject.CommonName; got != "mail.example.test" {
		t.Fatalf("CN = %q, want mail.example.test", got)
	}
}

func TestLoadFromFile_MissingFileErrors(t *testing.T) {
	_, err := LoadFromFile("/does/not/exist.pem", "/does/not/exist.key")
	if err == nil {
		t.Fatalf("expected error for missing paths")
	}
}

func TestStoreSNI(t *testing.T) {
	dir := t.TempDir()
	certA, keyA := generateCert(t, filepath.Join(t.TempDir()), []string{"mail.a.test"})
	certB, keyB := generateCert(t, filepath.Join(t.TempDir()), []string{"mail.b.test"})
	_ = dir

	a, err := LoadFromFile(certA, keyA)
	if err != nil {
		t.Fatalf("load a: %v", err)
	}
	b, err := LoadFromFile(certB, keyB)
	if err != nil {
		t.Fatalf("load b: %v", err)
	}

	s := NewStore()
	s.Add("mail.a.test", a)
	s.Add("mail.b.test", b)

	got, err := s.Get(&tls.ClientHelloInfo{ServerName: "mail.a.test"})
	if err != nil {
		t.Fatalf("Get a: %v", err)
	}
	if got != a {
		t.Fatalf("Get returned wrong cert for mail.a.test")
	}

	// Case-insensitive match.
	got, err = s.Get(&tls.ClientHelloInfo{ServerName: "MAIL.B.TEST"})
	if err != nil {
		t.Fatalf("Get B mixed case: %v", err)
	}
	if got != b {
		t.Fatalf("Get returned wrong cert for MAIL.B.TEST")
	}

	// Unknown hostname, no default: error.
	if _, err := s.Get(&tls.ClientHelloInfo{ServerName: "mail.c.test"}); err == nil {
		t.Fatalf("expected error for unknown host without default")
	} else if !errors.Is(err, ErrNoCertificate) {
		t.Fatalf("expected ErrNoCertificate, got %v", err)
	}

	// Default fallback.
	s.SetDefault(a)
	got, err = s.Get(&tls.ClientHelloInfo{ServerName: "mail.c.test"})
	if err != nil {
		t.Fatalf("Get with default: %v", err)
	}
	if got != a {
		t.Fatalf("default fallback returned wrong cert")
	}

	// Add with empty hostname also sets the default.
	s2 := NewStore()
	s2.Add("", b)
	got, err = s2.Get(&tls.ClientHelloInfo{ServerName: "anything.test"})
	if err != nil {
		t.Fatalf("Get with Add(\"\", ...): %v", err)
	}
	if got != b {
		t.Fatalf("Add(\"\", cert) did not set fallback")
	}
}

func TestTLSConfig_Intermediate(t *testing.T) {
	s := NewStore()
	cfg := TLSConfig(s, Intermediate, []string{"smtp", "h2"})
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("MinVersion = %x, want TLS1.2", cfg.MinVersion)
	}
	if cfg.MaxVersion != tls.VersionTLS13 {
		t.Fatalf("MaxVersion = %x, want TLS1.3", cfg.MaxVersion)
	}
	if len(cfg.CipherSuites) == 0 {
		t.Fatalf("Intermediate CipherSuites empty")
	}
	if len(cfg.NextProtos) != 2 || cfg.NextProtos[0] != "smtp" || cfg.NextProtos[1] != "h2" {
		t.Fatalf("NextProtos = %v, want [smtp h2]", cfg.NextProtos)
	}
	if cfg.GetCertificate == nil {
		t.Fatalf("GetCertificate not wired to store")
	}
}

func TestTLSConfig_Modern(t *testing.T) {
	s := NewStore()
	cfg := TLSConfig(s, Modern, nil)
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Fatalf("Modern MinVersion = %x, want TLS1.3", cfg.MinVersion)
	}
	if cfg.MaxVersion != tls.VersionTLS13 {
		t.Fatalf("Modern MaxVersion = %x, want TLS1.3", cfg.MaxVersion)
	}
	tls13 := map[uint16]bool{
		tls.TLS_AES_128_GCM_SHA256:       true,
		tls.TLS_AES_256_GCM_SHA384:       true,
		tls.TLS_CHACHA20_POLY1305_SHA256: true,
	}
	if len(cfg.CipherSuites) == 0 {
		t.Fatalf("Modern CipherSuites empty")
	}
	for _, cs := range cfg.CipherSuites {
		if !tls13[cs] {
			t.Fatalf("Modern CipherSuites contains non-TLS1.3 suite %#x", cs)
		}
	}
}
