package directory_test

import (
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// totpGenerate produces the canonical code for secret at t using the
// same parameters the Directory enforces. Test-only.
func totpGenerate(secret string, t time.Time) (string, error) {
	return totp.GenerateCodeCustom(secret, t, totp.ValidateOpts{
		Period:    30,
		Skew:      1,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	})
}
