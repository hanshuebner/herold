package authsession

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/auth"
	"github.com/hanshuebner/herold/internal/store"
)

// SessionConfig configures cookie-backed sessions.
//
// The signing key is process-local: rotating it (on restart or by
// rebooting the process) invalidates every outstanding session. We do
// not persist sessions across restarts; the audit-trail is in the store
// and operators tolerate a re-login after a deploy. Storing session
// state in a server-signed cookie keeps this package free of any
// cross-process state coordination.
type SessionConfig struct {
	// SigningKey is the HMAC-SHA256 key used to sign session cookies.
	// Must be at least 32 bytes; shorter values fall through to a
	// freshly-generated random key on Server construction.
	SigningKey []byte
	// CookieName is the HTTP cookie name. Defaults to "herold_ui_session".
	CookieName string
	// CSRFCookieName is the HTTP cookie name carrying the CSRF token
	// for the double-submit pattern. Defaults to "herold_ui_csrf".
	CSRFCookieName string
	// TTL bounds session lifetime. Defaults to 24 h.
	TTL time.Duration
	// SecureCookies, when true, sets the Secure flag on all UI cookies.
	// Production deployments MUST set this true; the dev knob is the
	// only way to disable it.
	SecureCookies bool
}

// Session is the decoded form of a session cookie. The wire form is
// `<principalID>.<expiresAtUnix>.<csrfToken>.<base64url(scopeJSON)>.<base64url(hmacSig)>`.
// Five dot-separated fields; the signature covers the first four so a
// forged cookie can't tamper with scope. Constant-time comparison on
// the sig keeps the guess loop side-channel-free.
//
// Per REQ-AUTH-SCOPE-01 the scope set is part of the cookie payload so
// the handler-side auth.RequireScope check can run without a DB
// round-trip; rotating the signing key invalidates all outstanding
// cookies (operators tolerate re-login on restart -- see SessionConfig).
//
// Session is exported so protoadmin's JSON login endpoint can mint
// sessions without duplicating the encoding logic (REQ-AUTH-SESSION-REST).
type Session struct {
	PrincipalID store.PrincipalID
	ExpiresAt   time.Time
	// CSRFToken is the server-issued CSRF token associated with this
	// session. Returned to the user via the CSRFCookieName cookie and
	// also embedded in HTML forms; the double-submit middleware
	// requires the two match. For the SPA (REQ-AUTH-CSRF) it is also
	// returned via the non-HttpOnly herold_admin_csrf cookie so JS
	// can read it and attach it as X-CSRF-Token on mutating requests.
	CSRFToken string
	// Scopes is the closed-enum scope set granted to the holder
	// (REQ-AUTH-SCOPE-01). The set is fixed at issuance time;
	// changing scope means logging out and back in.
	Scopes auth.ScopeSet
}

// EncodeSession produces the wire form for a session. Exported so
// cross-package consumers (protoadmin's JSON login) can issue cookies
// without re-implementing the encoding (REQ-AUTH-SESSION-REST).
func EncodeSession(s Session, key []byte) string {
	return encodeSession(s, key)
}

// DecodeSession parses and verifies a session cookie value. It returns
// ErrSessionInvalid for any malformed or unsigned cookie and
// ErrSessionExpired when the signature checks out but the deadline has
// passed. Exported for cross-package cookie verification
// (REQ-AUTH-SESSION-REST).
func DecodeSession(raw string, key []byte, now time.Time) (Session, error) {
	return decodeSession(raw, key, now)
}

// encodeSession produces the wire form for a session.
func encodeSession(s Session, key []byte) string {
	scopeBytes, _ := json.Marshal(s.Scopes)
	scopeEnc := base64.RawURLEncoding.EncodeToString(scopeBytes)
	payload := fmt.Sprintf("%d.%d.%s.%s", uint64(s.PrincipalID), s.ExpiresAt.Unix(), s.CSRFToken, scopeEnc)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payload + "." + sig
}

// decodeSession parses and verifies a session cookie. Returns
// ErrSessionInvalid for any malformed or unsigned cookie and
// ErrSessionExpired when the signature checks out but the deadline has
// passed. The two are distinct so the caller can differentiate "log
// in" from "your session expired" in the redirect target.
func decodeSession(raw string, key []byte, now time.Time) (Session, error) {
	// Split into <pid>.<exp>.<csrf>.<scope>.<sig>
	parts := strings.Split(raw, ".")
	if len(parts) != 5 {
		return Session{}, ErrSessionInvalid
	}
	pidStr, expStr, csrfTok, scopeEnc, sig := parts[0], parts[1], parts[2], parts[3], parts[4]
	payload := pidStr + "." + expStr + "." + csrfTok + "." + scopeEnc
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(sig)) {
		return Session{}, ErrSessionInvalid
	}
	pid, err := strconv.ParseUint(pidStr, 10, 64)
	if err != nil || pid == 0 {
		return Session{}, ErrSessionInvalid
	}
	expUnix, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		return Session{}, ErrSessionInvalid
	}
	exp := time.Unix(expUnix, 0).UTC()
	if !now.Before(exp) {
		return Session{}, ErrSessionExpired
	}
	scopeBytes, err := base64.RawURLEncoding.DecodeString(scopeEnc)
	if err != nil {
		return Session{}, ErrSessionInvalid
	}
	var scopes auth.ScopeSet
	if len(scopeBytes) > 0 {
		if err := json.Unmarshal(scopeBytes, &scopes); err != nil {
			return Session{}, ErrSessionInvalid
		}
	}
	return Session{
		PrincipalID: store.PrincipalID(pid),
		ExpiresAt:   exp,
		CSRFToken:   csrfTok,
		Scopes:      scopes,
	}, nil
}

// ErrSessionInvalid is returned by DecodeSession for any malformed or
// unsigned cookie.
var ErrSessionInvalid = errors.New("authsession: session cookie invalid or unsigned")

// ErrSessionExpired is returned by DecodeSession when the signature
// checks out but the deadline has passed.
var ErrSessionExpired = errors.New("authsession: session cookie expired")
