package store

// fileshare_password.go — pure helpers for file-share passwords and
// capability tokens. These are exported so protocol handlers (protoshare,
// JMAP) can verify passwords without importing a backend package.
//
// The Argon2id parameters MUST stay in sync with internal/directory/password.go
// so hashes produced here are cross-verifiable with that package.

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters for file-share passwords. Must stay in sync with
// internal/directory/password.go so hashes are cross-verifiable.
const (
	ShareArgonTime    uint32 = 2
	ShareArgonMemory  uint32 = 64 * 1024
	ShareArgonThreads uint8  = 4
	ShareArgonKeyLen  uint32 = 32
	ShareArgonSaltLen int    = 16
)

// HashSharePassword encodes password as an Argon2id PHC string using rnd
// as the entropy source. When rnd is nil, crypto/rand.Reader is used.
// The PHC format is: $argon2id$v=19$m=...,t=...,p=...$<salt>$<hash>
// REQ-SHARE-11c.
func HashSharePassword(rnd io.Reader, password string) (string, error) {
	if rnd == nil {
		rnd = rand.Reader
	}
	salt := make([]byte, ShareArgonSaltLen)
	if _, err := io.ReadFull(rnd, salt); err != nil {
		return "", fmt.Errorf("store: share password entropy: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, ShareArgonTime, ShareArgonMemory, ShareArgonThreads, ShareArgonKeyLen)
	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, ShareArgonMemory, ShareArgonTime, ShareArgonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifySharePassword returns true when password matches the Argon2id PHC
// string encoded. Always constant-time; returns false on malformed input so
// the caller sees no oracle distinguishing bad-hash from wrong-password
// (REQ-SHARE-11c). Returns false immediately if either argument is empty.
func VerifySharePassword(encoded, password string) bool {
	if encoded == "" || password == "" {
		return false
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false
	}
	if version != argon2.Version {
		return false
	}
	var memory, t uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &t, &threads); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, t, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// NewCapabilityToken generates the share capability token: 20 random bytes
// encoded as URL-safe base64 (no padding), giving >= 160 bits of entropy.
// URL-safe base64 keeps the token directly embeddable in URLs (REQ-SHARE-11b).
// When rnd is nil, crypto/rand.Reader is used.
func NewCapabilityToken(rnd io.Reader) (string, error) {
	if rnd == nil {
		rnd = rand.Reader
	}
	var b [20]byte // 160 bits >= 128-bit minimum (REQ-SHARE-11b)
	if _, err := io.ReadFull(rnd, b[:]); err != nil {
		return "", fmt.Errorf("store: capability token entropy: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
