package keymgmt

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
)

// RSAKeyBits is the RSA modulus size for newly-generated DKIM keys. RFC 8301
// requires at least 1024 bit and recommends at least 2048; we go straight to
// 2048 to stay above future deprecation deadlines and match REQ-SEC-90.
const RSAKeyBits = 2048

// PEMTypePrivateKey is the PEM block type used for marshalled PKCS#8
// private keys. Both RSA and Ed25519 share this label so the loader does
// not need an algorithm-specific branch.
const PEMTypePrivateKey = "PRIVATE KEY"

// ErrUnsupportedAlgorithm is returned when GenerateKey is called with an
// algorithm constant outside the {RSA-SHA256, Ed25519-SHA256} set.
var ErrUnsupportedAlgorithm = errors.New("keymgmt: unsupported DKIM algorithm")

// Manager owns the DKIM key lifecycle for every signing domain: it
// generates new keys, persists them via the metadata store, and answers
// lookups for the active key when signing or sealing. Manager is safe for
// concurrent use; the metadata store provides the row-level locking.
type Manager struct {
	meta   store.Metadata
	logger *slog.Logger
	clock  clock.Clock
	rand   io.Reader
}

// NewManager returns a Manager that persists keys via meta. logger and clk
// must not be nil; rand may be nil to use crypto/rand.Reader. Tests inject
// a deterministic reader to make GenerateKey reproducible.
func NewManager(meta store.Metadata, logger *slog.Logger, clk clock.Clock, randReader io.Reader) *Manager {
	if meta == nil {
		panic("keymgmt: nil meta")
	}
	if logger == nil {
		panic("keymgmt: nil logger")
	}
	if clk == nil {
		panic("keymgmt: nil clock")
	}
	if randReader == nil {
		randReader = rand.Reader
	}
	return &Manager{meta: meta, logger: logger, clock: clk, rand: randReader}
}

// GenerateKey produces a fresh signing key for domain, persists it as the
// new active key, and retires any previously-active key (status->retiring).
// Returns the chosen selector. The selector format is "herold<unix-millis>";
// it is opaque to callers but ordered so list outputs sort sensibly.
func (m *Manager) GenerateKey(ctx context.Context, domain string, alg store.DKIMAlgorithm) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	domain = canonDomain(domain)
	if domain == "" {
		return "", errors.New("keymgmt: empty domain")
	}
	priv, pub, err := generateKeyPair(alg, m.rand)
	if err != nil {
		return "", err
	}
	privPEM, err := encodePrivatePEM(priv)
	if err != nil {
		return "", fmt.Errorf("keymgmt: encode private: %w", err)
	}
	pubB64, err := encodePublicB64(pub)
	if err != nil {
		return "", fmt.Errorf("keymgmt: encode public: %w", err)
	}

	now := m.clock.Now().UTC()
	selector := newSelector(now)
	newKey := store.DKIMKey{
		Domain:        domain,
		Selector:      selector,
		Algorithm:     alg,
		PrivateKeyPEM: privPEM,
		PublicKeyB64:  pubB64,
		Status:        store.DKIMKeyStatusActive,
		CreatedAt:     now,
		RotatedAt:     now,
	}

	prev, err := m.meta.GetActiveDKIMKey(ctx, domain)
	switch {
	case err == nil:
		if err := m.meta.RotateDKIMKey(ctx, domain, prev.Selector, newKey); err != nil {
			return "", fmt.Errorf("keymgmt: rotate: %w", err)
		}
	case errors.Is(err, store.ErrNotFound):
		if err := m.meta.UpsertDKIMKey(ctx, newKey); err != nil {
			return "", fmt.Errorf("keymgmt: upsert: %w", err)
		}
	default:
		return "", fmt.Errorf("keymgmt: lookup active: %w", err)
	}

	m.logger.InfoContext(ctx, "keymgmt: generated DKIM key",
		slog.String("domain", domain),
		slog.String("selector", selector),
		slog.String("algorithm", alg.String()))
	return selector, nil
}

// ActiveKey returns the active key for domain. Wraps store.ErrNotFound
// transparently so callers can errors.Is for the missing-key case.
func (m *Manager) ActiveKey(ctx context.Context, domain string) (store.DKIMKey, error) {
	if err := ctx.Err(); err != nil {
		return store.DKIMKey{}, err
	}
	return m.meta.GetActiveDKIMKey(ctx, canonDomain(domain))
}

// Rotate generates a new active key for domain and transitions the prior
// active key to retiring. Equivalent to GenerateKey when an active key
// already exists; returns ErrNotFound when no prior key is published, so
// operators see a clean failure instead of a silent first-time generation.
func (m *Manager) Rotate(ctx context.Context, domain string, alg store.DKIMAlgorithm) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	domain = canonDomain(domain)
	if _, err := m.meta.GetActiveDKIMKey(ctx, domain); err != nil {
		return err
	}
	_, err := m.GenerateKey(ctx, domain, alg)
	return err
}

// PublishedRecord returns the v=DKIM1 TXT content for key in the form
// expected at <selector>._domainkey.<domain>. The autodns publisher passes
// this verbatim to the DNS plugin.
func (m *Manager) PublishedRecord(ctx context.Context, key store.DKIMKey) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if key.PublicKeyB64 == "" {
		return "", errors.New("keymgmt: key has no public material")
	}
	var k string
	switch key.Algorithm {
	case store.DKIMAlgorithmRSASHA256:
		k = "rsa"
	case store.DKIMAlgorithmEd25519SHA256:
		k = "ed25519"
	default:
		return "", ErrUnsupportedAlgorithm
	}
	return "v=DKIM1; k=" + k + "; p=" + key.PublicKeyB64, nil
}

// LoadPrivateKey decodes a PEM-encoded PKCS#8 private key from key.PrivateKeyPEM.
// It returns the typed crypto.Signer suitable for handing to the dkim signer.
func LoadPrivateKey(key store.DKIMKey) (crypto.Signer, error) {
	block, _ := pem.Decode([]byte(key.PrivateKeyPEM))
	if block == nil {
		return nil, errors.New("keymgmt: no PEM block found")
	}
	if block.Type != PEMTypePrivateKey {
		return nil, fmt.Errorf("keymgmt: unexpected PEM type %q", block.Type)
	}
	priv, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("keymgmt: parse PKCS8: %w", err)
	}
	signer, ok := priv.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("keymgmt: parsed key is not a signer (%T)", priv)
	}
	switch key.Algorithm {
	case store.DKIMAlgorithmRSASHA256:
		if _, ok := signer.Public().(*rsa.PublicKey); !ok {
			return nil, errors.New("keymgmt: rsa-sha256 key is not RSA")
		}
	case store.DKIMAlgorithmEd25519SHA256:
		if _, ok := signer.Public().(ed25519.PublicKey); !ok {
			return nil, errors.New("keymgmt: ed25519-sha256 key is not Ed25519")
		}
	default:
		return nil, ErrUnsupportedAlgorithm
	}
	return signer, nil
}

// generateKeyPair returns a fresh signing keypair for alg using r as the
// entropy source. The caller-visible signer is the private half; the
// public half is returned separately so the encoders can produce both PEM
// (private) and base64 (public) without re-deriving.
func generateKeyPair(alg store.DKIMAlgorithm, r io.Reader) (crypto.Signer, crypto.PublicKey, error) {
	switch alg {
	case store.DKIMAlgorithmRSASHA256:
		priv, err := rsa.GenerateKey(r, RSAKeyBits)
		if err != nil {
			return nil, nil, fmt.Errorf("keymgmt: rsa.GenerateKey: %w", err)
		}
		return priv, priv.Public(), nil
	case store.DKIMAlgorithmEd25519SHA256:
		pub, priv, err := ed25519.GenerateKey(r)
		if err != nil {
			return nil, nil, fmt.Errorf("keymgmt: ed25519.GenerateKey: %w", err)
		}
		return priv, pub, nil
	default:
		return nil, nil, ErrUnsupportedAlgorithm
	}
}

// encodePrivatePEM marshals priv as PKCS#8 inside a PEM block.
func encodePrivatePEM(priv crypto.Signer) (string, error) {
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: PEMTypePrivateKey, Bytes: der})), nil
}

// encodePublicB64 returns the base64 SubjectPublicKeyInfo that lands in
// the DNS TXT p= tag. The DKIM RFCs (6376 §3.6.1, 8463 §3) require the
// SPKI form for both rsa and ed25519.
func encodePublicB64(pub crypto.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(der), nil
}

// newSelector returns "herold<unix-millis>". The prefix advertises the
// implementation; the millis suffix is monotonically ordered so admin
// listings sort by creation time without an extra timestamp column.
func newSelector(t time.Time) string {
	return "herold" + strconv.FormatInt(t.UnixMilli(), 10)
}

func canonDomain(d string) string {
	d = strings.ToLower(strings.TrimSpace(d))
	d = strings.TrimSuffix(d, ".")
	return d
}
