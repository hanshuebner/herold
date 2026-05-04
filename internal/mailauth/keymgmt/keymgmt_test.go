package keymgmt_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/chacha20"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/mailauth/keymgmt"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// chachaReader is an unbounded deterministic byte source: a ChaCha20
// keystream over a fixed key+nonce. RSA key generation consumes a
// hard-to-bound amount of entropy, so a finite bytes.NewReader does not
// work; the keystream is effectively unbounded while still being a
// pure function of the seed (so test runs are reproducible).
type chachaReader struct {
	cipher *chacha20.Cipher
}

func newChachaReader(seed uint64) io.Reader {
	var key [32]byte
	binary.BigEndian.PutUint64(key[:], seed)
	var nonce [12]byte
	c, err := chacha20.NewUnauthenticatedCipher(key[:], nonce[:])
	if err != nil {
		panic(err)
	}
	return &chachaReader{cipher: c}
}

func (r *chachaReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	r.cipher.XORKeyStream(p, p)
	return len(p), nil
}

// deterministicReader returns a fresh seeded keystream reader. Tests
// call this per fixture so RSA prime search succeeds without leaking
// state between tests.
func deterministicReader() io.Reader { return newChachaReader(0xdeadbeefcafebabe) }

func newFixture(t *testing.T) (context.Context, *keymgmt.Manager, store.Store, *clock.FakeClock) {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := keymgmt.NewManager(fs.Meta(), logger, clk, deterministicReader())
	return t.Context(), mgr, fs, clk
}

func TestGenerateKey_RSA_PersistsActive(t *testing.T) {
	ctx, mgr, fs, _ := newFixture(t)
	sel, err := mgr.GenerateKey(ctx, "example.test", store.DKIMAlgorithmRSASHA256)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if sel == "" {
		t.Fatalf("empty selector")
	}
	got, err := fs.Meta().GetActiveDKIMKey(ctx, "example.test")
	if err != nil {
		t.Fatalf("GetActiveDKIMKey: %v", err)
	}
	if got.Status != store.DKIMKeyStatusActive {
		t.Fatalf("status: got %s, want active", got.Status)
	}
	if got.Algorithm != store.DKIMAlgorithmRSASHA256 {
		t.Fatalf("algorithm: got %s, want rsa-sha256", got.Algorithm)
	}
	if got.Selector != sel {
		t.Fatalf("selector mismatch: got %q want %q", got.Selector, sel)
	}
	// Sanity: Manager.ActiveKey returns the same row.
	via, err := mgr.ActiveKey(ctx, "example.test")
	if err != nil {
		t.Fatalf("ActiveKey: %v", err)
	}
	if via.ID != got.ID {
		t.Fatalf("ActiveKey returned different row: %d vs %d", via.ID, got.ID)
	}
}

func TestGenerateKey_Ed25519_PersistsActive(t *testing.T) {
	ctx, mgr, fs, _ := newFixture(t)
	sel, err := mgr.GenerateKey(ctx, "example.test", store.DKIMAlgorithmEd25519SHA256)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	got, err := fs.Meta().GetActiveDKIMKey(ctx, "example.test")
	if err != nil {
		t.Fatalf("GetActiveDKIMKey: %v", err)
	}
	if got.Status != store.DKIMKeyStatusActive {
		t.Fatalf("status: %s", got.Status)
	}
	if got.Algorithm != store.DKIMAlgorithmEd25519SHA256 {
		t.Fatalf("algorithm: %s", got.Algorithm)
	}
	if got.Selector != sel {
		t.Fatalf("selector: %q vs %q", got.Selector, sel)
	}
}

func TestRotate_RetiresPriorActive(t *testing.T) {
	ctx, mgr, fs, clk := newFixture(t)
	first, err := mgr.GenerateKey(ctx, "example.test", store.DKIMAlgorithmEd25519SHA256)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	// Advance the clock a tick so the second selector differs.
	clk.Advance(2 * time.Millisecond)
	if err := mgr.Rotate(ctx, "example.test", store.DKIMAlgorithmEd25519SHA256); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	keys, err := fs.Meta().ListDKIMKeys(ctx, "example.test")
	if err != nil {
		t.Fatalf("ListDKIMKeys: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("want 2 keys, got %d", len(keys))
	}
	var prior, active store.DKIMKey
	for _, k := range keys {
		if k.Selector == first {
			prior = k
		} else {
			active = k
		}
	}
	if prior.Status != store.DKIMKeyStatusRetiring {
		t.Fatalf("prior status: got %s want retiring", prior.Status)
	}
	if active.Status != store.DKIMKeyStatusActive {
		t.Fatalf("active status: got %s want active", active.Status)
	}
	if active.Selector == prior.Selector {
		t.Fatalf("rotate produced identical selector %q", active.Selector)
	}
}

func TestPublishedRecord_RSA_Format(t *testing.T) {
	ctx, mgr, fs, _ := newFixture(t)
	if _, err := mgr.GenerateKey(ctx, "example.test", store.DKIMAlgorithmRSASHA256); err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	key, err := fs.Meta().GetActiveDKIMKey(ctx, "example.test")
	if err != nil {
		t.Fatalf("GetActiveDKIMKey: %v", err)
	}
	rec, err := mgr.PublishedRecord(ctx, key)
	if err != nil {
		t.Fatalf("PublishedRecord: %v", err)
	}
	if !strings.HasPrefix(rec, "v=DKIM1; k=rsa; p=") {
		t.Fatalf("RSA record prefix wrong: %q", rec)
	}
	// p= decodes to a SubjectPublicKeyInfo carrying an RSA modulus that
	// matches the persisted private key.
	pubB64 := strings.TrimPrefix(rec, "v=DKIM1; k=rsa; p=")
	der, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil {
		t.Fatalf("decode p=: %v", err)
	}
	pub, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		t.Fatalf("parse SPKI: %v", err)
	}
	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		t.Fatalf("public key is not RSA: %T", pub)
	}
	// Compare against modulus extracted from the stored private key.
	signer, err := keymgmt.LoadPrivateKey(key)
	if err != nil {
		t.Fatalf("LoadPrivateKey: %v", err)
	}
	priv, ok := signer.(*rsa.PrivateKey)
	if !ok {
		t.Fatalf("private key is not RSA: %T", signer)
	}
	if priv.N.Cmp(rsaPub.N) != 0 {
		t.Fatalf("RSA modulus mismatch between private and TXT public")
	}
}

func TestPublishedRecord_Ed25519_Format(t *testing.T) {
	ctx, mgr, fs, _ := newFixture(t)
	if _, err := mgr.GenerateKey(ctx, "example.test", store.DKIMAlgorithmEd25519SHA256); err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	key, err := fs.Meta().GetActiveDKIMKey(ctx, "example.test")
	if err != nil {
		t.Fatalf("GetActiveDKIMKey: %v", err)
	}
	rec, err := mgr.PublishedRecord(ctx, key)
	if err != nil {
		t.Fatalf("PublishedRecord: %v", err)
	}
	if !strings.HasPrefix(rec, "v=DKIM1; k=ed25519; p=") {
		t.Fatalf("ed25519 record prefix wrong: %q", rec)
	}
	pubB64 := strings.TrimPrefix(rec, "v=DKIM1; k=ed25519; p=")
	der, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil {
		t.Fatalf("decode p=: %v", err)
	}
	pub, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		t.Fatalf("parse SPKI: %v", err)
	}
	if _, ok := pub.(ed25519.PublicKey); !ok {
		t.Fatalf("public key is not ed25519: %T", pub)
	}
	// Round-trip: the parsed PEM private key's public matches.
	block, _ := pem.Decode([]byte(key.PrivateKeyPEM))
	if block == nil {
		t.Fatalf("no PEM block in stored key")
	}
	priv, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse PKCS8: %v", err)
	}
	edPriv, ok := priv.(ed25519.PrivateKey)
	if !ok {
		t.Fatalf("priv key not ed25519: %T", priv)
	}
	pubFromPriv := edPriv.Public().(ed25519.PublicKey)
	if !bytes.Equal(pubFromPriv, pub.(ed25519.PublicKey)) {
		t.Fatalf("ed25519 public mismatch between private and TXT")
	}
}

// TestPublishedRecord_RSA_LengthVsTXTChunking documents the chunking
// boundary: keymgmt.PublishedRecord returns a single, unsplit string;
// the autodns publisher (or the DNS plugin) is responsible for the
// 255-byte segmentation. The test asserts the record exceeds 255 bytes
// for an RSA 2048 key, which is the load-bearing prerequisite for that
// downstream chunking behaviour.
func TestPublishedRecord_RSA_LengthVsTXTChunking(t *testing.T) {
	ctx, mgr, fs, _ := newFixture(t)
	if _, err := mgr.GenerateKey(ctx, "example.test", store.DKIMAlgorithmRSASHA256); err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	key, err := fs.Meta().GetActiveDKIMKey(ctx, "example.test")
	if err != nil {
		t.Fatalf("GetActiveDKIMKey: %v", err)
	}
	rec, err := mgr.PublishedRecord(ctx, key)
	if err != nil {
		t.Fatalf("PublishedRecord: %v", err)
	}
	if len(rec) <= 255 {
		t.Fatalf("RSA 2048 published record unexpectedly short: %d bytes", len(rec))
	}
	if strings.Contains(rec, "\n") {
		t.Fatalf("PublishedRecord must return a single unsplit string; got newline")
	}
}

func TestActiveKey_ErrNotFound_WhenNoKeys(t *testing.T) {
	ctx, mgr, _, _ := newFixture(t)
	_, err := mgr.ActiveKey(ctx, "missing.test")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ActiveKey on empty store: got %v, want ErrNotFound", err)
	}
}

func TestRotate_BeforeAnyKey(t *testing.T) {
	ctx, mgr, _, _ := newFixture(t)
	// Per current keymgmt.Rotate contract, calling Rotate with no prior
	// active key returns store.ErrNotFound (deliberately fails clean
	// rather than silently bootstrapping a first-time generation).
	err := mgr.Rotate(ctx, "fresh.test", store.DKIMAlgorithmEd25519SHA256)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Rotate before any key: got %v, want ErrNotFound", err)
	}
}
