package directory

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/store"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
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

// EnrollTOTP generates a new TOTP secret for the principal and persists
// it in an unconfirmed state. The returned secret and otpauth URI are
// shown to the user once; ConfirmTOTP flips the enrollment live once
// the user has entered a valid code.
//
// The secret is stored on the Principal.TOTPSecret field as the raw
// base32-encoded secret. A wrapper byte (0x00 = pending, 0x01 =
// confirmed) prefixes the stored bytes so VerifyTOTP and ConfirmTOTP can
// distinguish the two states without an additional column.
//
// TODO(store): the store currently treats TOTPSecret as opaque bytes;
// once a dedicated "totp_enabled" flag or envelope exists, drop the
// 1-byte prefix convention in favour of it.
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
	if isTOTPConfirmed(p.TOTPSecret) {
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
	p.TOTPSecret = wrapPendingSecret(key.Secret())
	p.Flags &^= principalFlagTOTPEnabled
	if err := d.meta.UpdatePrincipal(ctx, p); err != nil {
		return "", "", fmt.Errorf("directory: persist totp secret: %w", err)
	}
	d.audit(ctx, pid, "principal.totp.enroll")
	return key.Secret(), key.URL(), nil
}

// ConfirmTOTP accepts the first valid code after enrollment and promotes
// the principal's state to "TOTP enabled". Subsequent calls return
// ErrTOTPAlreadyEnabled.
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
	if isTOTPConfirmed(p.TOTPSecret) {
		return ErrTOTPAlreadyEnabled
	}
	secret := unwrapSecret(p.TOTPSecret)
	if !validateCode(secret, code, d.clk.Now()) {
		return ErrUnauthorized
	}
	p.TOTPSecret = wrapConfirmedSecret(secret)
	p.Flags |= principalFlagTOTPEnabled
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
	if !isTOTPConfirmed(p.TOTPSecret) {
		return ErrTOTPNotEnrolled
	}
	key := rlKey{email: p.CanonicalEmail, source: authSource(ctx) + "|totp"}
	if !d.rl.allow(key) {
		return ErrRateLimited
	}
	secret := unwrapSecret(p.TOTPSecret)
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
	p.Flags &^= principalFlagTOTPEnabled
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

// wrapPendingSecret prefixes 0x00 to indicate an unconfirmed secret.
func wrapPendingSecret(b32 string) []byte {
	out := make([]byte, 1+len(b32))
	out[0] = 0x00
	copy(out[1:], b32)
	return out
}

// wrapConfirmedSecret prefixes 0x01 to indicate a confirmed secret.
func wrapConfirmedSecret(b32 string) []byte {
	out := make([]byte, 1+len(b32))
	out[0] = 0x01
	copy(out[1:], b32)
	return out
}

// unwrapSecret returns the base32 secret string from stored bytes.
func unwrapSecret(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if b[0] != 0x00 && b[0] != 0x01 {
		// Legacy/raw secret: treat as-is.
		return string(b)
	}
	return string(b[1:])
}

// isTOTPConfirmed reports whether stored TOTP bytes are in the confirmed
// state.
func isTOTPConfirmed(b []byte) bool {
	return len(b) >= 1 && b[0] == 0x01
}
