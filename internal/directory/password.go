package directory

import (
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters. These match the conservative defaults suggested
// in STANDARDS.md §9 and docs/design/server/requirements/02-identity-and-auth.md. We
// pick time=2, memory=64*1024 KiB (64 MiB), threads=4, keyLen=32 bytes,
// saltLen=16 bytes. Callers needing different parameters should change
// them here; verification reads the params out of the encoded hash.
const (
	argonTime    uint32 = 2
	argonMemory  uint32 = 64 * 1024
	argonThreads uint8  = 4
	argonKeyLen  uint32 = 32
	argonSaltLen int    = 16
)

// hashPassword produces an Argon2id-encoded hash in the standard
// "$argon2id$v=19$m=...,t=...,p=...$salt$hash" format. It reads saltLen
// bytes from rnd.
func hashPassword(rnd io.Reader, password string) (string, error) {
	if password == "" {
		return "", errors.New("password empty")
	}
	salt := make([]byte, argonSaltLen)
	if _, err := io.ReadFull(rnd, salt); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	enc := fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	)
	return enc, nil
}

// verifyPassword compares password against encoded. Returns true only on
// exact match. Always runs in constant time over the two hashes.
func verifyPassword(encoded, password string) bool {
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
	var memory, time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
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
	got := argon2.IDKey([]byte(password), salt, time, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}
