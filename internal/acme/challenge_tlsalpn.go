package acme

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"
)

// acmeTLS1Protocol is the ALPN token defined by RFC 8737 §6.1.
const acmeTLS1Protocol = "acme-tls/1"

// idPeAcmeIdentifier is the ASN.1 OID 1.3.6.1.5.5.7.1.31 — the
// id-pe-acmeIdentifier extension defined by RFC 8737 §3.
var idPeAcmeIdentifier = asn1.ObjectIdentifier{1, 3, 6, 1, 5, 5, 7, 1, 31}

// TLSALPNChallenger answers ACME tls-alpn-01 challenges (RFC 8737).
// During a challenge, a self-signed leaf certificate carrying the
// SHA-256 of the key authorisation in the id-pe-acmeIdentifier
// extension is registered for the host's SNI; the production TLS
// listener selects it whenever the peer offers ALPN "acme-tls/1".
//
// Lookup is performed via Get, which the listener wires into a
// tls.Config.GetCertificate fronted before the production cert store —
// the production cert is used when ALPN is empty or differs.
type TLSALPNChallenger struct {
	mu    sync.RWMutex
	certs map[string]*tls.Certificate
}

// NewTLSALPNChallenger returns an empty challenger.
func NewTLSALPNChallenger() *TLSALPNChallenger {
	return &TLSALPNChallenger{certs: make(map[string]*tls.Certificate)}
}

// Provision builds and registers the per-host challenge certificate.
// Repeated calls for the same host replace the prior cert.
func (t *TLSALPNChallenger) Provision(host, keyAuth string) error {
	if host == "" {
		return errors.New("acme: tls-alpn-01 host empty")
	}
	cert, err := buildTLSALPNCert(host, keyAuth)
	if err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.certs[strings.ToLower(host)] = cert
	return nil
}

// Cleanup removes the registered challenge certificate for host. Safe
// to call for unknown hosts.
func (t *TLSALPNChallenger) Cleanup(host string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.certs, strings.ToLower(host))
}

// Get returns the registered challenge cert for hello when the peer
// offered the acme-tls/1 ALPN token, or (nil, nil) if no challenge is
// active for the host. The TLS listener falls back to the production
// cert store on (nil, nil); a non-nil error is surfaced as a TLS
// handshake failure.
func (t *TLSALPNChallenger) Get(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	if hello == nil {
		return nil, nil
	}
	hasACMEALPN := false
	for _, p := range hello.SupportedProtos {
		if p == acmeTLS1Protocol {
			hasACMEALPN = true
			break
		}
	}
	if !hasACMEALPN {
		return nil, nil
	}
	host := strings.ToLower(hello.ServerName)
	t.mu.RLock()
	defer t.mu.RUnlock()
	if c, ok := t.certs[host]; ok {
		return c, nil
	}
	return nil, fmt.Errorf("acme: no tls-alpn-01 cert registered for %q", host)
}

// buildTLSALPNCert constructs a self-signed cert per RFC 8737 §3.
func buildTLSALPNCert(host, keyAuth string) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("acme: tls-alpn cert key: %w", err)
	}
	hash := sha256.Sum256([]byte(keyAuth))
	extValue, err := asn1.Marshal(hash[:])
	if err != nil {
		return nil, fmt.Errorf("acme: marshal acmeIdentifier: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("acme: serial: %w", err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(7 * 24 * time.Hour),
		ExtraExtensions: []pkix.Extension{
			{
				Id:       idPeAcmeIdentifier,
				Critical: true,
				Value:    extValue,
			},
		},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("acme: create tls-alpn cert: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("acme: parse tls-alpn cert: %w", err)
	}
	return &tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  key,
		Leaf:        leaf,
	}, nil
}
