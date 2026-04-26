package vapid

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestGenerateAndParseRoundTrip(t *testing.T) {
	kp, err := Generate(rand.Reader)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if kp.Private == nil {
		t.Fatalf("nil Private")
	}
	if len(kp.PublicKeyUncompressed) != 65 || kp.PublicKeyUncompressed[0] != 0x04 {
		t.Fatalf("uncompressed pub key shape: len=%d byte0=%#x", len(kp.PublicKeyUncompressed), kp.PublicKeyUncompressed[0])
	}
	// PublicKeyB64URL must base64url-decode to PublicKeyUncompressed.
	decoded, err := base64.RawURLEncoding.DecodeString(kp.PublicKeyB64URL)
	if err != nil {
		t.Fatalf("decode b64url: %v", err)
	}
	if string(decoded) != string(kp.PublicKeyUncompressed) {
		t.Fatalf("b64 round-trip mismatch")
	}
	pemStr, err := EncodePrivatePEM(kp.Private)
	if err != nil {
		t.Fatalf("EncodePrivatePEM: %v", err)
	}
	if !strings.Contains(pemStr, "BEGIN PRIVATE KEY") {
		t.Fatalf("PEM does not contain expected header: %s", pemStr)
	}
	parsed, err := ParseKeyPair([]byte(pemStr))
	if err != nil {
		t.Fatalf("ParseKeyPair: %v", err)
	}
	if parsed.PublicKeyB64URL != kp.PublicKeyB64URL {
		t.Fatalf("derived public key mismatch")
	}
}

func TestParseKeyPairRejectsMalformed(t *testing.T) {
	if _, err := ParseKeyPair([]byte("not pem")); err == nil {
		t.Fatalf("ParseKeyPair on garbage: err = nil, want non-nil")
	}
	if _, err := ParseKeyPair([]byte(`-----BEGIN RSA PRIVATE KEY-----
MIIBOgIBAAJB
-----END RSA PRIVATE KEY-----
`)); err == nil {
		t.Fatalf("ParseKeyPair on wrong-type PEM: err = nil, want non-nil")
	}
}

func TestParseKeyPairRejectsNonP256(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey P-384: %v", err)
	}
	pemStr, err := EncodePrivatePEM(priv)
	if err != nil {
		t.Fatalf("EncodePrivatePEM: %v", err)
	}
	_, err = ParseKeyPair([]byte(pemStr))
	if err == nil {
		t.Fatalf("ParseKeyPair on P-384: err = nil, want curve-mismatch")
	}
	if !strings.Contains(err.Error(), "P-256") {
		t.Fatalf("error did not mention curve: %v", err)
	}
}

func TestManagerUnconfigured(t *testing.T) {
	m := New()
	if m.Configured() {
		t.Fatalf("Configured = true for fresh Manager")
	}
	if _, err := m.PublicKeyB64URL(); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("PublicKeyB64URL: err = %v, want ErrNotConfigured", err)
	}
	if _, err := m.KeyPair(); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("KeyPair: err = %v, want ErrNotConfigured", err)
	}
}

func TestManagerLoad(t *testing.T) {
	kp, err := Generate(rand.Reader)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	pemStr, err := EncodePrivatePEM(kp.Private)
	if err != nil {
		t.Fatalf("EncodePrivatePEM: %v", err)
	}
	m := New()
	if err := m.Load([]byte(pemStr)); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !m.Configured() {
		t.Fatalf("Configured = false after Load")
	}
	pub, err := m.PublicKeyB64URL()
	if err != nil {
		t.Fatalf("PublicKeyB64URL: %v", err)
	}
	if pub != kp.PublicKeyB64URL {
		t.Fatalf("public key roundtrip mismatch")
	}
}
