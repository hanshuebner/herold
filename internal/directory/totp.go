package directory

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"

	"github.com/hanshuebner/herold/internal/store"
)

// TOTPIssuer is the label placed in the provisioning URI's issuer field.
// Authenticator apps show this to the user.
const TOTPIssuer = "Herold"

// totpOpts is the canonical TOTP parameter set: SHA-1, 6 digits, 30 s
// period, ±1 window. These are the RFC 6238 defaults every major
// authenticator app supports.
var totpOpts = totp.ValidateOpts{
	Period:    30,
	Skew:      1,
	Digits:    otp.DigitsSix,
	Algorithm: otp.AlgorithmSHA1,
}

// EnrollTOTP generates a new TOTP secret for the principal and
// persists it in an unconfirmed state. The returned secret and
// otpauth URI are shown to the user once; ConfirmTOTP flips the
// enrollment live once the user has entered a valid code.
//
// Storage contract: Principal.TOTPSecret holds the raw base32-encoded
// secret (no wrapper byte; that Wave 1 workaround is gone). Enrolment
// state lives in PrincipalFlagTOTPEnabled: cleared during EnrollTOTP,
// set by ConfirmTOTP, cleared again by DisableTOTP.
func (d *Directory) EnrollTOTP(ctx context.Context, pid PrincipalID) (secret, provisioningURI string, err error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	p, err := d.meta.GetPrincipalByID(ctx, pid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", "", fmt.Errorf("%w: principal %d", ErrNotFound, pid)
		}
		return "", "", fmt.Errorf("directory: load principal: %w", err)
	}
	if p.Flags.Has(store.PrincipalFlagTOTPEnabled) {
		return "", "", ErrTOTPAlreadyEnabled
	}
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      TOTPIssuer,
		AccountName: p.CanonicalEmail,
		Rand:        d.rand,
	})
	if err != nil {
		return "", "", fmt.Errorf("directory: generate totp: %w", err)
	}
	p.TOTPSecret = []byte(key.Secret())
	p.Flags &^= store.PrincipalFlagTOTPEnabled
	if err := d.meta.UpdatePrincipal(ctx, p); err != nil {
		return "", "", fmt.Errorf("directory: persist totp secret: %w", err)
	}
	d.audit(ctx, pid, "principal.totp.enroll")
	return key.Secret(), key.URL(), nil
}

// ConfirmTOTP accepts the first valid code after enrollment and
// promotes the principal's state to "TOTP enabled". Subsequent calls
// return ErrTOTPAlreadyEnabled.
func (d *Directory) ConfirmTOTP(ctx context.Context, pid PrincipalID, code string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := d.meta.GetPrincipalByID(ctx, pid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("%w: principal %d", ErrNotFound, pid)
		}
		return fmt.Errorf("directory: load principal: %w", err)
	}
	if len(p.TOTPSecret) == 0 {
		return ErrTOTPNotEnrolled
	}
	if p.Flags.Has(store.PrincipalFlagTOTPEnabled) {
		return ErrTOTPAlreadyEnabled
	}
	secret := string(p.TOTPSecret)
	if !validateCode(secret, code, d.clk.Now()) {
		return ErrUnauthorized
	}
	p.Flags |= store.PrincipalFlagTOTPEnabled
	if err := d.meta.UpdatePrincipal(ctx, p); err != nil {
		return fmt.Errorf("directory: confirm totp: %w", err)
	}
	d.audit(ctx, pid, "principal.totp.confirm")
	return nil
}

// VerifyTOTP validates code against the principal's confirmed secret.
// Tolerates a ±1 30-second skew window. Rate-limits failures using the
// same bucket as Authenticate.
func (d *Directory) VerifyTOTP(ctx context.Context, pid PrincipalID, code string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := d.meta.GetPrincipalByID(ctx, pid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("%w: principal %d", ErrNotFound, pid)
		}
		return fmt.Errorf("directory: load principal: %w", err)
	}
	if !p.Flags.Has(store.PrincipalFlagTOTPEnabled) || len(p.TOTPSecret) == 0 {
		return ErrTOTPNotEnrolled
	}
	key := rlKey{email: p.CanonicalEmail, source: authSource(ctx) + "|totp"}
	if !d.rl.allow(key) {
		return ErrRateLimited
	}
	secret := string(p.TOTPSecret)
	if !validateCode(secret, code, d.clk.Now()) {
		d.rl.record(key)
		return ErrUnauthorized
	}
	d.rl.clear(key)
	return nil
}

// DisableTOTP removes the principal's TOTP enrollment. The password is
// required and compared in constant time; ErrUnauthorized is returned on
// mismatch.
func (d *Directory) DisableTOTP(ctx context.Context, pid PrincipalID, password string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := d.meta.GetPrincipalByID(ctx, pid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("%w: principal %d", ErrNotFound, pid)
		}
		return fmt.Errorf("directory: load principal: %w", err)
	}
	if !verifyPassword(p.PasswordHash, password) {
		return ErrUnauthorized
	}
	p.TOTPSecret = nil
	p.Flags &^= store.PrincipalFlagTOTPEnabled
	if err := d.meta.UpdatePrincipal(ctx, p); err != nil {
		return fmt.Errorf("directory: disable totp: %w", err)
	}
	d.audit(ctx, pid, "principal.totp.disable")
	return nil
}

// validateCode reports whether code matches a valid TOTP at t within
// the default ±1 step window. The code string is whitespace-stripped.
// Comparison of the computed code and the caller's code is done with
// subtle.ConstantTimeCompare so a timing channel does not leak partial
// digits.
func validateCode(secret, code string, t time.Time) bool {
	code = strings.ReplaceAll(code, " ", "")
	if len(code) == 0 {
		return false
	}
	// Probe the centre step and ±skew neighbours explicitly so the
	// comparison is constant-time per probe (totp.ValidateCustom is
	// not).
	for delta := -int64(totpOpts.Skew); delta <= int64(totpOpts.Skew); delta++ {
		cand, err := totp.GenerateCodeCustom(secret, t.Add(time.Duration(delta)*time.Duration(totpOpts.Period)*time.Second), totpOpts)
		if err != nil {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(cand), []byte(code)) == 1 {
			return true
		}
	}
	return false
}
