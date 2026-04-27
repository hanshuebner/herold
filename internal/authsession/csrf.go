package authsession

import (
	"crypto/rand"
	"encoding/base64"
)

// NewCSRFToken returns a 24-byte random URL-safe token for use in the
// double-submit CSRF pattern. 24 bytes -> 192 bits -- comfortably above
// the recommended 128-bit minimum and short enough to land in a cookie
// without trimming.
//
// Exported so protoadmin's JSON login endpoint can mint a matching CSRF
// token without duplicating the random-generation logic.
func NewCSRFToken() string {
	return newCSRFToken()
}

func newCSRFToken() string {
	var b [24]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}
