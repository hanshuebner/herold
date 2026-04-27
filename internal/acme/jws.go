package acme

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// jwsSigner abstracts the asymmetric signing key used to authenticate
// every authenticated ACME request (RFC 8555 §6.2). The implementation
// is ECDSA P-256; the surface is kept narrow so adding RSA later is a
// matter of a second implementation.
type jwsSigner interface {
	// alg returns the JOSE "alg" header value (e.g. "ES256").
	alg() string
	// jwk returns the public-key JWK (RFC 7517) as a JSON map. The keys
	// are sorted lexicographically when serialised so the thumbprint is
	// stable.
	jwk() map[string]string
	// sign returns a raw JWS signature over signingInput per the
	// algorithm's encoding (P1363 for ECDSA).
	sign(signingInput []byte) ([]byte, error)
}

// ecdsaSigner is the P-256 implementation of jwsSigner. ACME defaults to
// ECDSA P-256 in modern deployments; we do not attempt to support
// negotiation of curves here.
type ecdsaSigner struct {
	key *ecdsa.PrivateKey
}

func (s *ecdsaSigner) alg() string { return "ES256" }

func (s *ecdsaSigner) jwk() map[string]string {
	x := s.key.X.Bytes()
	y := s.key.Y.Bytes()
	// Pad to 32 bytes for P-256 per RFC 7518 §6.2.1.2.
	xPad := make([]byte, 32)
	copy(xPad[32-len(x):], x)
	yPad := make([]byte, 32)
	copy(yPad[32-len(y):], y)
	return map[string]string{
		"crv": "P-256",
		"kty": "EC",
		"x":   base64.RawURLEncoding.EncodeToString(xPad),
		"y":   base64.RawURLEncoding.EncodeToString(yPad),
	}
}

func (s *ecdsaSigner) sign(signingInput []byte) ([]byte, error) {
	h := sha256.Sum256(signingInput)
	r, sig, err := ecdsa.Sign(rand.Reader, s.key, h[:])
	if err != nil {
		return nil, fmt.Errorf("acme: ecdsa sign: %w", err)
	}
	// JOSE wants R||S each padded to curve byte size (32 bytes for P-256).
	rb := r.Bytes()
	sb := sig.Bytes()
	out := make([]byte, 64)
	copy(out[32-len(rb):32], rb)
	copy(out[64-len(sb):], sb)
	return out, nil
}

// jwsThumbprint returns the RFC 7638 JWK thumbprint of pub, base64url
// encoded. Used for HTTP-01, TLS-ALPN-01 and DNS-01 key authorisation
// strings (RFC 8555 §8.1).
func jwsThumbprint(s jwsSigner) (string, error) {
	jwk := s.jwk()
	// RFC 7638 §3 requires the canonical JSON form: members in
	// lexicographic order, no whitespace.
	canonical, err := canonicalJSON(jwk)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

// canonicalJSON encodes m with members sorted by key. The map values
// here are always strings so a hand-rolled encoding suffices.
func canonicalJSON(m map[string]string) ([]byte, error) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// stdlib sort instead of bringing in slices to keep the package
	// stdlib-only.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	buf := []byte{'{'}
	for i, k := range keys {
		if i > 0 {
			buf = append(buf, ',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		buf = append(buf, kb...)
		buf = append(buf, ':')
		vb, err := json.Marshal(m[k])
		if err != nil {
			return nil, err
		}
		buf = append(buf, vb...)
	}
	buf = append(buf, '}')
	return buf, nil
}

// jwsHeader is the JOSE protected header used for one ACME request.
// Exactly one of JWK or KID is populated per RFC 8555 §6.2.
type jwsHeader struct {
	Alg   string         `json:"alg"`
	Nonce string         `json:"nonce"`
	URL   string         `json:"url"`
	JWK   map[string]any `json:"jwk,omitempty"`
	KID   string         `json:"kid,omitempty"`
}

// signedRequest is the wire shape of a JWS-authenticated ACME POST body
// (RFC 7515 flattened JSON serialisation).
type signedRequest struct {
	Protected string `json:"protected"`
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

// signRequest assembles a JWS over payload using either JWK (newAccount
// — RFC 8555 §6.2 lists newAccount as the one method that uses an
// embedded JWK) or KID (every other authenticated request). When
// payload is nil the request is a POST-as-GET (RFC 8555 §6.3); the JWS
// payload is the empty string per §6.3.
func signRequest(s jwsSigner, kid, url, nonce string, payload []byte) (*signedRequest, error) {
	hdr := jwsHeader{Alg: s.alg(), Nonce: nonce, URL: url}
	if kid != "" {
		hdr.KID = kid
	} else {
		jwk := s.jwk()
		m := make(map[string]any, len(jwk))
		for k, v := range jwk {
			m[k] = v
		}
		hdr.JWK = m
	}
	hdrBytes, err := json.Marshal(hdr)
	if err != nil {
		return nil, fmt.Errorf("acme: marshal jws header: %w", err)
	}
	protected := base64.RawURLEncoding.EncodeToString(hdrBytes)
	var payloadEncoded string
	if payload != nil {
		payloadEncoded = base64.RawURLEncoding.EncodeToString(payload)
	}
	signingInput := []byte(protected + "." + payloadEncoded)
	sig, err := s.sign(signingInput)
	if err != nil {
		return nil, err
	}
	return &signedRequest{
		Protected: protected,
		Payload:   payloadEncoded,
		Signature: base64.RawURLEncoding.EncodeToString(sig),
	}, nil
}

// generateECDSA returns a fresh P-256 ECDSA key.
func generateECDSA() (*ecdsa.PrivateKey, error) {
	return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
}
