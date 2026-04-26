package tls

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// LoadFromFile reads a PEM-encoded certificate chain and private key from
// disk and returns a parsed *tls.Certificate. The returned certificate's
// Leaf field is populated so callers can inspect it without re-parsing.
func LoadFromFile(certFile, keyFile string) (*tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("tls: load %q / %q: %w", certFile, keyFile, err)
	}
	if len(cert.Certificate) > 0 && cert.Leaf == nil {
		leaf, err := x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			return nil, fmt.Errorf("tls: parse leaf: %w", err)
		}
		cert.Leaf = leaf
	}
	return &cert, nil
}

// ALPNChallengeProvider is the minimal interface the Store needs to
// dispatch tls-alpn-01 challenges during ACME validation (RFC 8737).
// The production implementation is *acme.TLSALPNChallenger; tests may
// substitute a stub.
type ALPNChallengeProvider interface {
	// Get returns the challenge cert for hello when the peer offered the
	// "acme-tls/1" ALPN token, or (nil, nil) if no challenge is active
	// for the host. A non-nil error is surfaced as a handshake failure.
	Get(hello *tls.ClientHelloInfo) (*tls.Certificate, error)
}

// Store is a goroutine-safe SNI-indexed certificate registry (REQ-PROTO-72,
// REQ-OPS-41). Certificates are keyed by hostname (case-insensitive). A
// per-store default certificate is returned when no SNI entry matches.
//
// An optional ALPNChallengeProvider is checked first when the client
// handshake includes the "acme-tls/1" ALPN token; when the provider
// returns (nil, nil) the normal SNI lookup proceeds.
type Store struct {
	mu       sync.RWMutex
	byHost   map[string]*tls.Certificate
	fallback *tls.Certificate

	alpnMu       sync.RWMutex
	alpnProvider ALPNChallengeProvider
}

// NewStore returns an empty certificate store.
func NewStore() *Store {
	return &Store{byHost: make(map[string]*tls.Certificate)}
}

// Add registers cert under hostname. If hostname is empty, cert becomes the
// store-wide fallback used when no SNI entry matches. Calling Add with the
// same hostname replaces the previous entry (supports live rotation per
// REQ-OPS-72).
func (s *Store) Add(hostname string, cert *tls.Certificate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if hostname == "" {
		s.fallback = cert
		return
	}
	s.byHost[strings.ToLower(hostname)] = cert
}

// SetDefault sets (or replaces) the fallback certificate.
func (s *Store) SetDefault(cert *tls.Certificate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fallback = cert
}

// SetALPNChallenger registers the provider consulted for tls-alpn-01
// challenges (RFC 8737). When set, Get checks the provider first for any
// ClientHello that includes "acme-tls/1" in SupportedProtos; if the
// provider returns (nil, nil) the normal SNI path is used. Safe to call
// concurrently with Get.
func (s *Store) SetALPNChallenger(p ALPNChallengeProvider) {
	s.alpnMu.Lock()
	defer s.alpnMu.Unlock()
	s.alpnProvider = p
}

// Get implements tls.Config.GetCertificate: it returns the certificate whose
// hostname matches hello.ServerName (case-insensitive), falling back to the
// store default. Returns an error when neither is available.
//
// If an ALPNChallenger is registered and the ClientHello includes the
// "acme-tls/1" protocol, the challenger is checked first (RFC 8737).
func (s *Store) Get(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	// ACME tls-alpn-01 side-channel: check before production cert lookup.
	if hello != nil {
		s.alpnMu.RLock()
		p := s.alpnProvider
		s.alpnMu.RUnlock()
		if p != nil {
			for _, proto := range hello.SupportedProtos {
				if proto == "acme-tls/1" {
					if cert, err := p.Get(hello); cert != nil || err != nil {
						return cert, err
					}
					break
				}
			}
		}
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	if hello != nil && hello.ServerName != "" {
		if c, ok := s.byHost[strings.ToLower(hello.ServerName)]; ok {
			return c, nil
		}
	}
	if s.fallback != nil {
		return s.fallback, nil
	}
	sn := ""
	if hello != nil {
		sn = hello.ServerName
	}
	return nil, fmt.Errorf("%w: server name %q", ErrNoCertificate, sn)
}

// MozillaProfile selects a cipher suite / protocol profile per Mozilla's
// ssl-config guidelines (https://ssl-config.mozilla.org/).
type MozillaProfile int

const (
	// Intermediate is the default profile: TLS 1.2 and TLS 1.3 with the
	// 2024 Mozilla intermediate cipher suites.
	Intermediate MozillaProfile = iota
	// Modern restricts negotiation to TLS 1.3 only.
	Modern
)

// intermediateCipherSuites is Mozilla's "Intermediate" TLS 1.2 cipher-suite
// set, filtered to the AEAD suites Go supports. TLS 1.3 suites are negotiated
// separately by the stdlib and do not appear here.
//
// Reference: https://ssl-config.mozilla.org/guidelines/5.7-openssl-3.0-current.json
var intermediateCipherSuites = []uint16{
	tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
	tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
}

// modernTLS13Suites are the TLS 1.3 cipher suites Go enables. Set on the
// config for documentation; Go's TLS 1.3 implementation does not honour
// CipherSuites for 1.3 but readers can still see our intent.
var modernTLS13Suites = []uint16{
	tls.TLS_AES_128_GCM_SHA256,
	tls.TLS_AES_256_GCM_SHA384,
	tls.TLS_CHACHA20_POLY1305_SHA256,
}

// TLSConfig returns a *tls.Config wired to the store for SNI lookup, with
// cipher suites and version bounds set per profile. alpn may be nil.
func TLSConfig(s *Store, profile MozillaProfile, alpn []string) *tls.Config {
	cfg := &tls.Config{
		MinVersion:     tls.VersionTLS12,
		MaxVersion:     tls.VersionTLS13,
		GetCertificate: s.Get,
		NextProtos:     append([]string(nil), alpn...),
	}
	switch profile {
	case Modern:
		cfg.MinVersion = tls.VersionTLS13
		cfg.CipherSuites = append([]uint16(nil), modernTLS13Suites...)
	case Intermediate:
		fallthrough
	default:
		cfg.CipherSuites = append([]uint16(nil), intermediateCipherSuites...)
	}
	return cfg
}

// ErrNoCertificate is returned by Get when no SNI match is available and no
// fallback has been configured. Callers may use errors.Is to distinguish
// this from other configuration failures.
var ErrNoCertificate = errors.New("tls: no certificate available")
