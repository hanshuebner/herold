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
)

// AuthorizeRequestTTL bounds how long a rendered login form remains
// submittable. Ten minutes comfortably covers a human typing credentials
// (including a password-manager/passkey round trip) while keeping the
// window a captured/leaked form-encoded token stays live short.
const AuthorizeRequestTTL = 10 * time.Minute

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
	parts := strings.Split(raw, ".")
	if len(parts) != 9 {
		return AuthorizeRequest{}, ErrAuthorizeRequestInvalid
	}
	payload := strings.Join(parts[:8], ".")
	sig := parts[8]
	mac := hmac.New(sha256.New, d.oauthReqKey)
	mac.Write([]byte(payload))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(sig)) {
		return AuthorizeRequest{}, ErrAuthorizeRequestInvalid
	}
	dec := make([]string, 8)
	for i, f := range parts[:8] {
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
	req := AuthorizeRequest{
		ClientID:            dec[0],
		RedirectURI:         dec[1],
		Scope:               dec[2],
		State:               dec[3],
		CodeChallenge:       dec[4],
		CodeChallengeMethod: dec[5],
		CSRFToken:           dec[6],
		ExpiresAt:           time.Unix(expUnix, 0).UTC(),
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
