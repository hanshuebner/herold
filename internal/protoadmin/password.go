package protoadmin

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters must stay in sync with internal/directory/password.go
// so a hash produced here verifies there (and vice versa). When a third
// caller arrives these should be promoted to a single exported helper
// in internal/directory.
const (
	argonTime    uint32 = 2
	argonMemory  uint32 = 64 * 1024
	argonThreads uint8  = 4
	argonKeyLen  uint32 = 32
	argonSaltLen int    = 16
)

// hashPasswordArgon2id encodes password as an Argon2id hash in the
// "$argon2id$v=19$m=...,t=...,p=...$salt$hash" format. The salt is
// drawn from crypto/rand.
func hashPasswordArgon2id(password string) (string, error) {
	if password == "" {
		return "", errors.New("password empty")
	}
	salt := make([]byte, argonSaltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}
