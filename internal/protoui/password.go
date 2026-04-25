package protoui

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters must stay in sync with internal/directory/password.go
// AND internal/protoadmin/password.go: a hash produced here must verify
// in directory.verifyPassword. When a third caller arrives, all three
// should converge on a single exported helper in internal/directory
// (see the protosend/problem.go duplication-justification pattern).
const (
	argonTime    uint32 = 2
	argonMemory  uint32 = 64 * 1024
	argonThreads uint8  = 4
	argonKeyLen  uint32 = 32
	argonSaltLen int    = 16
)

// hashPasswordArgon2id encodes password as an Argon2id hash in the
// standard "$argon2id$v=19$m=...,t=...,p=...$salt$hash" wire form.
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
