package protoui_test

import (
	"net/http/cookiejar"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// totpGenerate returns the canonical 6-digit TOTP code for the
// supplied base32 secret at the supplied instant. Mirrors directory's
// internal validateCode() probe loop minus the ±skew window: tests
// ask for the code at the same instant the directory verifier would
// observe, so a single call suffices.
func totpGenerate(secret string, at time.Time) (string, error) {
	return totp.GenerateCodeCustom(secret, at, totp.ValidateOpts{
		Period:    30,
		Skew:      1,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	})
}

// newCookieJar constructs a stdlib in-memory jar. Wrapper exists so
// tests do not need to import net/http/cookiejar directly.
func newCookieJar() (*cookiejar.Jar, error) {
	return cookiejar.New(nil)
}
