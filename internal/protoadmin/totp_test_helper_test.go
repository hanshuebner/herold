package protoadmin_test

import (
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// otpGenerateCode returns a RFC 6238 TOTP code for secret at time t
// using the same parameter set the directory package uses (SHA-1,
// 6 digits, 30 s). Kept in a _test.go file so pquerna/otp stays out
// of the production import tree of protoadmin.
func otpGenerateCode(secret string, t time.Time) (string, error) {
	return totp.GenerateCodeCustom(secret, t, totp.ValidateOpts{
		Period:    30,
		Skew:      1,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	})
}
