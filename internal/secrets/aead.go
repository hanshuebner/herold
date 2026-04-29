package secrets

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
)

// versionPrefix is prepended to every ciphertext so Open can detect algorithm
// version mismatches during future rotation (e.g. a future "v2:" prefix).
const versionPrefix = "v1:"

// versionPrefixLen is len(versionPrefix). Named to make arithmetic readable.
const versionPrefixLen = len(versionPrefix)

// nonceSize is the ChaCha20-Poly1305 nonce size (12 bytes per RFC 7539).
const nonceSize = chacha20poly1305.NonceSize // 12

// ErrBadCiphertext is returned by Open when the sealed blob is structurally
// invalid (truncated, wrong version, or unauthenticated / tampered).
var ErrBadCiphertext = errors.New("secrets: invalid or tampered ciphertext")

// Seal encrypts plaintext with key using ChaCha20-Poly1305 and returns the
// sealed blob. A fresh random nonce is generated for every call.
//
// The returned slice is prefixed with versionPrefix so Open can detect the
// algorithm version. key must be exactly 32 bytes; callers should use
// LoadDataKey to produce a validated key.
//
// Seal never returns an error unless the OS random source fails, which is
// treated as fatal in production.
func Seal(key, plaintext []byte) ([]byte, error) {
	if len(key) != chacha20poly1305.KeySize {
		return nil, fmt.Errorf("secrets: Seal: key must be %d bytes, got %d", chacha20poly1305.KeySize, len(key))
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, fmt.Errorf("secrets: Seal: create AEAD: %w", err)
	}
	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("secrets: Seal: generate nonce: %w", err)
	}
	ct := aead.Seal(nil, nonce, plaintext, nil)
	out := make([]byte, versionPrefixLen+nonceSize+len(ct))
	copy(out, versionPrefix)
	copy(out[versionPrefixLen:], nonce)
	copy(out[versionPrefixLen+nonceSize:], ct)
	return out, nil
}

// Open decrypts a sealed blob produced by Seal. key must be the same 32-byte
// key passed to Seal.
//
// Returns ErrBadCiphertext when the blob is structurally invalid, uses an
// unknown version prefix, or fails the AEAD authentication tag check. Any
// of these conditions indicates corruption or tampering.
func Open(key, sealed []byte) ([]byte, error) {
	if len(key) != chacha20poly1305.KeySize {
		return nil, fmt.Errorf("secrets: Open: key must be %d bytes, got %d", chacha20poly1305.KeySize, len(key))
	}
	if !bytes.HasPrefix(sealed, []byte(versionPrefix)) {
		return nil, ErrBadCiphertext
	}
	rest := sealed[versionPrefixLen:]
	if len(rest) < nonceSize {
		return nil, ErrBadCiphertext
	}
	nonce := rest[:nonceSize]
	ct := rest[nonceSize:]
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, fmt.Errorf("secrets: Open: create AEAD: %w", err)
	}
	plain, err := aead.Open(nil, nonce, ct, nil)
	if err != nil {
		// AEAD returns a generic error on auth failure; wrap as ErrBadCiphertext
		// so callers do not need to import chacha20poly1305 to distinguish it.
		return nil, ErrBadCiphertext
	}
	return plain, nil
}
