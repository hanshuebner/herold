package sasl

import (
	"crypto/sha256"
	"crypto/sha512"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"hash"
)

// CapturingTLSConfig wraps a *tls.Config so the leaf certificate the
// handshake actually serves can be retrieved after HandshakeContext
// returns. Go's stdlib does not expose the local server's leaf via
// (*tls.Conn).ConnectionState(); this helper installs a GetCertificate
// wrapper that records the chosen leaf in c.Leaf for SCRAM-PLUS to
// derive the RFC 5929 channel binding from.
//
// Usage:
//
//	cap := sasl.NewCapturingTLSConfig(heroldtls.TLSConfig(...))
//	conn := tls.Server(rawConn, cap.Config())
//	if err := conn.HandshakeContext(ctx); err != nil { ... }
//	cb, _ := sasl.TLSServerEndpoint(cap.Leaf())
type CapturingTLSConfig struct {
	cfg  *tls.Config
	leaf *x509.Certificate
}

// NewCapturingTLSConfig clones src and substitutes a GetCertificate
// callback that captures the served leaf.
func NewCapturingTLSConfig(src *tls.Config) *CapturingTLSConfig {
	c := &CapturingTLSConfig{}
	clone := src.Clone()
	inner := clone.GetCertificate
	clone.GetCertificate = func(hi *tls.ClientHelloInfo) (*tls.Certificate, error) {
		var (
			cert *tls.Certificate
			err  error
		)
		if inner != nil {
			cert, err = inner(hi)
		} else if len(clone.Certificates) > 0 {
			cert = &clone.Certificates[0]
		}
		if err != nil {
			return nil, err
		}
		if cert != nil {
			leaf := cert.Leaf
			if leaf == nil && len(cert.Certificate) > 0 {
				if parsed, perr := x509.ParseCertificate(cert.Certificate[0]); perr == nil {
					leaf = parsed
				}
			}
			c.leaf = leaf
		}
		return cert, nil
	}
	c.cfg = clone
	return c
}

// Config returns the wrapped *tls.Config to hand to tls.Server.
func (c *CapturingTLSConfig) Config() *tls.Config { return c.cfg }

// Leaf returns the leaf certificate captured during handshake, or nil
// if the handshake has not run yet (or no GetCertificate callback was
// invoked, e.g. when src.Certificates is empty and src.GetCertificate
// is nil).
func (c *CapturingTLSConfig) Leaf() *x509.Certificate { return c.leaf }

// ErrNoTLSCertificate is returned by TLSServerEndpoint when the supplied
// connection state has no peer-visible server certificate from which to
// derive an RFC 5929 tls-server-end-point binding.
var ErrNoTLSCertificate = errors.New("sasl: connection has no server certificate")

// TLSServerEndpoint computes the RFC 5929 §4 tls-server-end-point
// channel binding value for a TLS connection. The binding is the digest
// of the server's leaf certificate, hashed with the same family as the
// certificate's signature algorithm, with two exceptions per RFC 5929 §4.1:
// MD5 and SHA-1 signatures are upgraded to SHA-256.
//
// state is the result of (*tls.Conn).ConnectionState on the server side
// of the TLS handshake. The leaf certificate is the first entry of
// PeerCertificates when the local end is the client, and (since Go does
// not expose the local-server cert via ConnectionState) the caller must
// reach the leaf via the same handshake context that produced state — see
// the tls-server-end-point helper used by protosmtp / protoimap.
//
// Returns ErrNoTLSCertificate when no leaf certificate is available.
func TLSServerEndpoint(leaf *x509.Certificate) ([]byte, error) {
	if leaf == nil {
		return nil, ErrNoTLSCertificate
	}
	h := hashForSignatureAlgorithm(leaf.SignatureAlgorithm)
	h.Write(leaf.Raw)
	return h.Sum(nil), nil
}

// TLSServerEndpointFromState extracts a server certificate from an
// active TLS connection state and returns the RFC 5929 binding.
//
// Server-side, Go's stdlib does not surface the local server's leaf
// certificate via ConnectionState, so callers usually pass a *tls.Conn
// they have wrapped with a custom GetCertificate callback that captured
// the leaf at handshake time. As a fallback this helper inspects
// PeerCertificates (populated only for mutual-TLS connections).
// It returns ErrNoTLSCertificate when no leaf is available.
func TLSServerEndpointFromState(state tls.ConnectionState) ([]byte, error) {
	if len(state.PeerCertificates) > 0 {
		return TLSServerEndpoint(state.PeerCertificates[0])
	}
	return nil, ErrNoTLSCertificate
}

// hashForSignatureAlgorithm picks the digest to use for an RFC 5929
// tls-server-end-point binding given the leaf certificate's signature
// algorithm. Anything weaker than SHA-256 (MD5, SHA-1) is upgraded to
// SHA-256 per §4.1; SHA-384 and SHA-512 carry through.
func hashForSignatureAlgorithm(alg x509.SignatureAlgorithm) hash.Hash {
	switch alg {
	case x509.SHA384WithRSA, x509.ECDSAWithSHA384, x509.SHA384WithRSAPSS:
		return sha512.New384()
	case x509.SHA512WithRSA, x509.ECDSAWithSHA512, x509.SHA512WithRSAPSS:
		return sha512.New()
	default:
		// MD5, SHA-1, SHA-256, ED25519, and unknown algorithms all map
		// to SHA-256: the first two via the RFC 5929 §4.1 upgrade rule;
		// SHA-256 because it is the matching family; ED25519 because
		// RFC 8410 does not define a separate tls-server-end-point
		// digest and SHA-256 is the safe default.
		return sha256.New()
	}
}
