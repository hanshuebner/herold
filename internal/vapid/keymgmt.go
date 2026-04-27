package vapid

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
)

// PrivateKeyPEMType is the PEM block type the loader accepts for the
// VAPID private key. PKCS#8 is the canonical encoding for an ECDSA
// P-256 key inside herold's secrets store.
const PrivateKeyPEMType = "PRIVATE KEY"

// ErrNotConfigured is returned by Manager.PublicKey / .PrivateKey
// when no key pair has been loaded. Callers translate this into "Web
// Push is unavailable" — it is not a fatal error at startup.
var ErrNotConfigured = errors.New("vapid: key pair not configured")

// KeyPair carries the loaded VAPID key pair plus the cached wire
// representations the JMAP session descriptor and (in 3.8b) the JWT
// signer reuse without re-deriving on every request.
type KeyPair struct {
	// Private is the parsed P-256 ECDSA private key. The dispatcher
	// in 3.8b uses Private.Sign to mint the VAPID JWT.
	Private *ecdsa.PrivateKey
	// PublicKeyB64URL is the RFC 8292-required base64url (no padding)
	// encoding of the uncompressed SEC1 public key. the suite reads this
	// from the JMAP session capability descriptor and passes it to
	// pushManager.subscribe({applicationServerKey: ...}).
	PublicKeyB64URL string
	// PublicKeyUncompressed is the 65-byte uncompressed SEC1 form
	// (0x04 || X || Y). Cached so the dispatcher and the capability
	// renderer share one byte slice.
	PublicKeyUncompressed []byte
}

// Manager owns the deployment's VAPID key pair. One Manager instance
// is constructed at server startup; it caches the parsed key in
// memory. Concurrent access is safe — the loaded KeyPair is immutable
// after Load.
//
// When no operator-supplied key is configured the Manager is
// "unconfigured": every accessor returns ErrNotConfigured and the
// JMAP capability descriptor omits applicationServerKey. Manager
// itself does NOT generate keys at startup; key generation is
// operator-driven via the herold vapid generate CLI subcommand.
type Manager struct {
	// kp is non-nil exactly when Load has been called with a valid PEM
	// PKCS#8 P-256 key. Tests can also build a Manager around a
	// pre-parsed key via NewWithKey.
	kp *KeyPair
}

// New returns an unconfigured Manager. Callers Load() the secret
// reference resolved by sysconfig before serving traffic.
func New() *Manager {
	return &Manager{}
}

// NewWithKey returns a Manager pre-populated with kp. Tests use this
// to bypass the file/env secret resolution path; production code
// always goes through Load.
func NewWithKey(kp *KeyPair) *Manager {
	return &Manager{kp: kp}
}

// Configured reports whether Load (or NewWithKey) has installed a key
// pair. The capability advertiser uses this to decide whether to
// include applicationServerKey in the session descriptor.
func (m *Manager) Configured() bool {
	return m != nil && m.kp != nil
}

// Load parses pemBytes as a PKCS#8 PEM-encoded P-256 ECDSA private
// key and caches the resulting KeyPair on m. Returns an error on
// malformed input; the caller is expected to surface that to the
// operator at config validate time.
func (m *Manager) Load(pemBytes []byte) error {
	kp, err := ParseKeyPair(pemBytes)
	if err != nil {
		return err
	}
	m.kp = kp
	return nil
}

// KeyPair returns the loaded key pair, or ErrNotConfigured when no
// key has been loaded.
func (m *Manager) KeyPair() (*KeyPair, error) {
	if m == nil || m.kp == nil {
		return nil, ErrNotConfigured
	}
	return m.kp, nil
}

// PublicKeyB64URL returns the base64url-encoded VAPID public key the
// JMAP capability descriptor advertises as applicationServerKey.
// Returns "" + ErrNotConfigured when no key is loaded.
func (m *Manager) PublicKeyB64URL() (string, error) {
	if m == nil || m.kp == nil {
		return "", ErrNotConfigured
	}
	return m.kp.PublicKeyB64URL, nil
}

// ParseKeyPair decodes pemBytes as a PEM PKCS#8-wrapped P-256 ECDSA
// private key, derives the public-key bytes, and returns the loaded
// pair. Used by Manager.Load and by the herold vapid CLI subcommand
// (validation path before the operator wires the secret reference).
func ParseKeyPair(pemBytes []byte) (*KeyPair, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("vapid: no PEM block found")
	}
	if block.Type != PrivateKeyPEMType {
		return nil, fmt.Errorf("vapid: unexpected PEM type %q (want %q)", block.Type, PrivateKeyPEMType)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("vapid: parse PKCS8: %w", err)
	}
	priv, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("vapid: parsed key is not ECDSA (%T)", parsed)
	}
	if priv.Curve != elliptic.P256() {
		return nil, fmt.Errorf("vapid: private key is not P-256 (%s)", priv.Curve.Params().Name)
	}
	uncomp := MarshalUncompressed(&priv.PublicKey)
	return &KeyPair{
		Private:               priv,
		PublicKeyUncompressed: uncomp,
		PublicKeyB64URL:       base64.RawURLEncoding.EncodeToString(uncomp),
	}, nil
}

// Generate produces a fresh P-256 ECDSA key pair using r as the
// entropy source (nil falls back to crypto/rand.Reader). Used by the
// herold vapid generate CLI subcommand to print the operator a
// PEM private key + the matching base64url public key.
func Generate(r io.Reader) (*KeyPair, error) {
	if r == nil {
		r = rand.Reader
	}
	priv, err := ecdsa.GenerateKey(elliptic.P256(), r)
	if err != nil {
		return nil, fmt.Errorf("vapid: ecdsa.GenerateKey: %w", err)
	}
	uncomp := MarshalUncompressed(&priv.PublicKey)
	return &KeyPair{
		Private:               priv,
		PublicKeyUncompressed: uncomp,
		PublicKeyB64URL:       base64.RawURLEncoding.EncodeToString(uncomp),
	}, nil
}

// EncodePrivatePEM returns the PKCS#8 PEM encoding of priv suitable
// for the operator's secrets store. The wrapping type matches what
// ParseKeyPair / Manager.Load accept.
func EncodePrivatePEM(priv *ecdsa.PrivateKey) (string, error) {
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return "", fmt.Errorf("vapid: marshal PKCS8: %w", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: PrivateKeyPEMType, Bytes: der})), nil
}

// MarshalUncompressed returns the 65-byte uncompressed SEC1 form
// (0x04 || X || Y) of pub. RFC 8292 §3.2 mandates this form for the
// applicationServerKey delivered to the browser; the encryption layer
// (RFC 8291) also uses it as ECDH input in 3.8b.
//
// Internally we cross over from crypto/ecdsa to crypto/ecdh and call
// PublicKey.Bytes — the byte shape (0x04 || X || Y) is identical to
// the now-deprecated elliptic.Marshal.
func MarshalUncompressed(pub *ecdsa.PublicKey) []byte {
	if pub == nil || pub.Curve != elliptic.P256() || pub.X == nil || pub.Y == nil {
		return nil
	}
	// Reconstruct the 65-byte SEC1 uncompressed encoding so the
	// crypto/ecdh public-key parser accepts it. ecdh's PublicKey.Bytes
	// then re-emits the same shape.
	byteLen := (elliptic.P256().Params().BitSize + 7) / 8
	buf := make([]byte, 1+2*byteLen)
	buf[0] = 0x04
	pub.X.FillBytes(buf[1 : 1+byteLen])
	pub.Y.FillBytes(buf[1+byteLen:])
	ek, err := ecdh.P256().NewPublicKey(buf)
	if err != nil {
		return nil
	}
	return ek.Bytes()
}
