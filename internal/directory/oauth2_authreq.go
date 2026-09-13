package directory

// oauth2_authreq.go carries the OAuth2 authorize request's parameters
// (issue #199, REQ-AND-AUTH-01/02) across the browser's GET (render the
// login form) -> POST (submit credentials) round trip without a server-
// side database row: the wire form is signed (HMAC-SHA256, Directory's
// process-local oauthReqKey) so it is tamper-evident, and it is short-
// lived (AuthorizeRequestTTL). This mirrors internal/authsession's
// signed-cookie session encoding, applied to a one-shot pre-login blob
// instead of a standing session.
//
// CSRF: the encoded token carries a CSRFToken value generated at GET
// time. The HTTP layer (protoadmin) also sets that same value in an
// HttpOnly cookie; the POST handler requires the cookie, the hidden-form
// field, and the value embedded in the signed token to all agree
// (double-submit, reinforced by the signature covering the CSRF value so
// it cannot be swapped independently of the rest of the request).
//
// TOTP step-up: once the password step succeeds for a TOTP-enrolled
// principal, the HTTP layer binds StepUpPrincipalID/StepUpExpiresAt into
// a freshly re-signed token and re-renders the login form with only the
// six-digit field (issue #372) -- the human never retypes email/password.
// The binding is short-lived (PasswordStepUpTTL, tighter than the overall
// AuthorizeRequestTTL) so a captured mid-flow token doesn't stay a usable
// "skip the password" credential for long; an expired binding makes the
// HTTP layer fall back to the full form again.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// AuthorizeRequestTTL bounds how long a rendered login form remains
// submittable. Ten minutes comfortably covers a human typing credentials
// (including a password-manager/passkey round trip) while keeping the
// window a captured/leaked form-encoded token stays live short.
const AuthorizeRequestTTL = 10 * time.Minute

// PasswordStepUpTTL bounds how long a password-verified binding
// (AuthorizeRequest.StepUpPrincipalID/StepUpExpiresAt) remains usable to
// skip straight to the TOTP-code-only form (issue #372). Five minutes
// covers a human reading a code off an authenticator app while keeping
// the "password already proven" window materially shorter than the
// overall AuthorizeRequestTTL.
const PasswordStepUpTTL = 5 * time.Minute

// AuthorizeRequest is the decoded form of the parameters captured at
// GET /oauth2/authorize and required again at the POST that submits the
// login form and at the code-issuing step.
type AuthorizeRequest struct {
	ClientID            string
	RedirectURI         string
	Scope               string
	State               string
	CodeChallenge       string
	CodeChallengeMethod string
	CSRFToken           string
	ExpiresAt           time.Time

	// StepUpPrincipalID is nonzero once a prior POST to this same
	// browser round trip verified the principal's password and found
	// TOTP enrolled (issue #372). The HTTP layer treats a nonzero value
	// (still within StepUpExpiresAt) as "password already proven": the
	// next POST need only carry totp_code.
	StepUpPrincipalID store.PrincipalID
	// StepUpExpiresAt bounds how long StepUpPrincipalID remains usable.
	// Zero when StepUpPrincipalID is zero.
	StepUpExpiresAt time.Time
}

// PasswordVerified reports whether req carries a live (unexpired)
// password-verified binding, i.e. the HTTP layer should render/accept the
// TOTP-code-only form rather than the full email+password form.
func (req AuthorizeRequest) PasswordVerified(now time.Time) bool {
	return req.StepUpPrincipalID != 0 && now.Before(req.StepUpExpiresAt)
}

// WithPasswordVerified returns a copy of req bound to pid as of now, for
// PasswordStepUpTTL.
func (req AuthorizeRequest) WithPasswordVerified(pid store.PrincipalID, now time.Time) AuthorizeRequest {
	req.StepUpPrincipalID = pid
	req.StepUpExpiresAt = now.Add(PasswordStepUpTTL)
	return req
}

// WithoutPasswordVerified returns a copy of req with any step-up binding
// cleared, for falling back to the full email+password form (an expired
// binding, or an authentication error that should not leave a stale
// binding usable).
func (req AuthorizeRequest) WithoutPasswordVerified() AuthorizeRequest {
	req.StepUpPrincipalID = 0
	req.StepUpExpiresAt = time.Time{}
	return req
}

// ErrAuthorizeRequestInvalid is returned by DecodeAuthorizeRequest for a
// malformed or unsigned token.
var ErrAuthorizeRequestInvalid = errors.New("directory: oauth2 authorize request invalid or unsigned")

// ErrAuthorizeRequestExpired is returned by DecodeAuthorizeRequest when
// the signature checks out but AuthorizeRequestTTL has elapsed.
var ErrAuthorizeRequestExpired = errors.New("directory: oauth2 authorize request expired")

// EncodeAuthorizeRequest signs req and returns the opaque wire value
// embedded as a hidden field in the rendered login form. Each field is
// base64url-encoded before joining with "." (rather than, say,
// url.QueryEscape) specifically because base64url's alphabet never
// contains ".", guaranteeing the delimiter cannot appear inside a field
// -- several field values here (a redirect_uri, a PKCE challenge) contain
// literal dots that would otherwise corrupt the split on decode.
func (d *Directory) EncodeAuthorizeRequest(req AuthorizeRequest) string {
	fields := []string{
		req.ClientID, req.RedirectURI, req.Scope, req.State,
		req.CodeChallenge, req.CodeChallengeMethod, req.CSRFToken,
		strconv.FormatInt(req.ExpiresAt.Unix(), 10),
		strconv.FormatUint(uint64(req.StepUpPrincipalID), 10),
		strconv.FormatInt(req.StepUpExpiresAt.Unix(), 10),
	}
	for i, f := range fields {
		fields[i] = base64.RawURLEncoding.EncodeToString([]byte(f))
	}
	payload := strings.Join(fields, ".")
	mac := hmac.New(sha256.New, d.oauthReqKey)
	mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payload + "." + sig
}

// DecodeAuthorizeRequest verifies and parses a wire value produced by
// EncodeAuthorizeRequest. now is compared against the embedded
// ExpiresAt.
func (d *Directory) DecodeAuthorizeRequest(raw string, now time.Time) (AuthorizeRequest, error) {
	const numFields = 10
	parts := strings.Split(raw, ".")
	if len(parts) != numFields+1 {
		return AuthorizeRequest{}, ErrAuthorizeRequestInvalid
	}
	payload := strings.Join(parts[:numFields], ".")
	sig := parts[numFields]
	mac := hmac.New(sha256.New, d.oauthReqKey)
	mac.Write([]byte(payload))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(sig)) {
		return AuthorizeRequest{}, ErrAuthorizeRequestInvalid
	}
	dec := make([]string, numFields)
	for i, f := range parts[:numFields] {
		v, err := base64.RawURLEncoding.DecodeString(f)
		if err != nil {
			return AuthorizeRequest{}, ErrAuthorizeRequestInvalid
		}
		dec[i] = string(v)
	}
	expUnix, err := strconv.ParseInt(dec[7], 10, 64)
	if err != nil {
		return AuthorizeRequest{}, ErrAuthorizeRequestInvalid
	}
	stepUpPID, err := strconv.ParseUint(dec[8], 10, 64)
	if err != nil {
		return AuthorizeRequest{}, ErrAuthorizeRequestInvalid
	}
	stepUpExpUnix, err := strconv.ParseInt(dec[9], 10, 64)
	if err != nil {
		return AuthorizeRequest{}, ErrAuthorizeRequestInvalid
	}
	req := AuthorizeRequest{
		ClientID:            dec[0],
		RedirectURI:         dec[1],
		Scope:               dec[2],
		State:               dec[3],
		CodeChallenge:       dec[4],
		CodeChallengeMethod: dec[5],
		CSRFToken:           dec[6],
		ExpiresAt:           time.Unix(expUnix, 0).UTC(),
		StepUpPrincipalID:   store.PrincipalID(stepUpPID),
		StepUpExpiresAt:     time.Unix(stepUpExpUnix, 0).UTC(),
	}
	if !now.Before(req.ExpiresAt) {
		return AuthorizeRequest{}, ErrAuthorizeRequestExpired
	}
	return req, nil
}

// NewAuthorizeRequestCSRFToken returns a fresh random CSRF token for
// EncodeAuthorizeRequest, using rnd as the entropy source.
func NewAuthorizeRequestCSRFToken(rnd io.Reader) (string, error) {
	var b [18]byte
	if _, err := io.ReadFull(rnd, b[:]); err != nil {
		return "", fmt.Errorf("directory: generate csrf token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
