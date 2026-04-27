package protoui

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
// state in a server-signed cookie keeps the protoui package free of any
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

// session is the internal alias kept for backwards compatibility within
// the protoui package. All internal code continues using 'session'; the
// exported Session type is for cross-package consumers.
type session = Session

// EncodeSession produces the wire form for a session. Exported so
// cross-package consumers (protoadmin's JSON login) can issue cookies
// without re-implementing the encoding (REQ-AUTH-SESSION-REST).
func EncodeSession(s Session, key []byte) string {
	return encodeSession(s, key)
}

// DecodeSession parses and verifies a session cookie value. It returns
// errSessionInvalid for any malformed or unsigned cookie and
// errSessionExpired when the signature checks out but the deadline has
// passed. Exported for cross-package cookie verification
// (REQ-AUTH-SESSION-REST).
func DecodeSession(raw string, key []byte, now time.Time) (Session, error) {
	return decodeSession(raw, key, now)
}

// NewCSRFToken returns a 24-byte random URL-safe token for use in the
// double-submit CSRF pattern. Exported so protoadmin's JSON login
// endpoint can mint a matching CSRF token without duplicating the
// random-generation logic.
func NewCSRFToken() string {
	return newCSRFToken()
}

// WriteSessionCookie writes both the session cookie and the matching
// CSRF cookie to w using the supplied SessionConfig. The CSRF cookie
// is intentionally NOT HttpOnly so the SPA's JS can read it and attach
// it as X-CSRF-Token on mutating requests (REQ-AUTH-CSRF).
//
// This is the standalone (config-driven) form of the session-cookie
// writer shared by protoui.Server.setSessionCookie and protoadmin's
// JSON login endpoint (REQ-AUTH-SESSION-REST). The session cookie Path
// is "/" so the same cookie accompanies /api/v1/*, /admin/*, and /ui/*
// on the same listener.
func WriteSessionCookie(w http.ResponseWriter, cfg SessionConfig, sess Session) {
	encoded := encodeSession(sess, cfg.SigningKey)
	maxAge := int(cfg.TTL.Seconds())
	if maxAge <= 0 {
		maxAge = 1
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cfg.CookieName,
		Value:    encoded,
		Path:     "/",
		MaxAge:   maxAge,
		Secure:   cfg.SecureCookies,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     cfg.CSRFCookieName,
		Value:    sess.CSRFToken,
		Path:     "/",
		MaxAge:   maxAge,
		Secure:   cfg.SecureCookies,
		HttpOnly: false,
		SameSite: http.SameSiteStrictMode,
	})
}

// ClearSessionCookies writes Set-Cookie headers that expire both the
// session cookie and the CSRF cookie immediately. The cookie path MUST
// match the path used at issuance (WriteSessionCookie uses "/") so the
// browser drops the right cookies.
func ClearSessionCookies(w http.ResponseWriter, cfg SessionConfig) {
	http.SetCookie(w, &http.Cookie{
		Name:     cfg.CookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		Secure:   cfg.SecureCookies,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     cfg.CSRFCookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		Secure:   cfg.SecureCookies,
		HttpOnly: false,
		SameSite: http.SameSiteStrictMode,
	})
}

// encodeSession produces the wire form for a session.
func encodeSession(s session, key []byte) string {
	scopeBytes, _ := json.Marshal(s.Scopes)
	scopeEnc := base64.RawURLEncoding.EncodeToString(scopeBytes)
	payload := fmt.Sprintf("%d.%d.%s.%s", uint64(s.PrincipalID), s.ExpiresAt.Unix(), s.CSRFToken, scopeEnc)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payload + "." + sig
}

// decodeSession parses and verifies a session cookie. Returns
// errSessionInvalid for any malformed or unsigned cookie and
// errSessionExpired when the signature checks out but the deadline has
// passed. The two are distinct so the caller can differentiate "log
// in" from "your session expired" in the redirect target.
func decodeSession(raw string, key []byte, now time.Time) (session, error) {
	// Split into <pid>.<exp>.<csrf>.<scope>.<sig>
	parts := strings.Split(raw, ".")
	if len(parts) != 5 {
		return session{}, errSessionInvalid
	}
	pidStr, expStr, csrfTok, scopeEnc, sig := parts[0], parts[1], parts[2], parts[3], parts[4]
	payload := pidStr + "." + expStr + "." + csrfTok + "." + scopeEnc
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(sig)) {
		return session{}, errSessionInvalid
	}
	pid, err := strconv.ParseUint(pidStr, 10, 64)
	if err != nil || pid == 0 {
		return session{}, errSessionInvalid
	}
	expUnix, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		return session{}, errSessionInvalid
	}
	exp := time.Unix(expUnix, 0).UTC()
	if !now.Before(exp) {
		return session{}, errSessionExpired
	}
	scopeBytes, err := base64.RawURLEncoding.DecodeString(scopeEnc)
	if err != nil {
		return session{}, errSessionInvalid
	}
	var scopes auth.ScopeSet
	if len(scopeBytes) > 0 {
		if err := json.Unmarshal(scopeBytes, &scopes); err != nil {
			return session{}, errSessionInvalid
		}
	}
	return session{
		PrincipalID: store.PrincipalID(pid),
		ExpiresAt:   exp,
		CSRFToken:   csrfTok,
		Scopes:      scopes,
	}, nil
}

var (
	errSessionInvalid = errors.New("protoui: session cookie invalid or unsigned")
	errSessionExpired = errors.New("protoui: session cookie expired")
)

// sessionCookiePath returns the Path attribute for the session cookie.
// Both public and admin listener cookies use Path="/" so the same
// browser session covers /api/v1/*, /admin/*, and /ui/* mounts on the
// same listener. The cookie name difference (herold_admin_session vs
// herold_public_session) prevents cross-listener reuse at the parser
// level (REQ-OPS-ADMIN-LISTENER-03). The CSRF cookie also uses Path="/"
// so the SPA at /admin/* can read it and present it as X-CSRF-Token on
// mutating requests (REQ-AUTH-CSRF; see WriteSessionCookie).
func (s *Server) sessionCookiePath() string {
	return "/"
}

// setSessionCookie writes both the session cookie and the matching CSRF
// cookie to w by delegating to WriteSessionCookie. See WriteSessionCookie
// for the cookie attribute contract (Path="/", HttpOnly on session,
// non-HttpOnly on CSRF so the SPA can read it per REQ-AUTH-CSRF).
//
// MaxAge is derived from the configured TTL, not time.Until(sess.ExpiresAt),
// to avoid mixing the FakeClock-derived deadline with the real-clock
// cookie jar in tests (which would silently drop cookies with negative
// MaxAge). Server-side validation still uses the signed ExpiresAt.
func (s *Server) setSessionCookie(w http.ResponseWriter, sess session) {
	WriteSessionCookie(w, s.cfg, sess)
}

// clearSessionCookie writes Set-Cookie headers that expire both the
// session and CSRF cookies immediately by delegating to
// ClearSessionCookies. Path must match the issuance path ("/") so the
// browser removes the correct cookies.
func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	ClearSessionCookies(w, s.cfg)
}

// ResolveSession returns the principal id authenticated by the suite
// session cookie on r, or zero + false if the request is anonymous,
// the cookie is missing/invalid, or the principal has been disabled.
//
// Used by sibling handlers (e.g. internal/protoimg's image proxy) that
// reuse the suite session as their auth surface without re-implementing
// cookie validation. The check intentionally rejects disabled
// principals so a flagged operator's lingering cookie cannot keep
// authenticating side channels.
func (s *Server) ResolveSession(r *http.Request) (store.PrincipalID, bool) {
	pid, _, ok := s.ResolveSessionWithScope(r)
	return pid, ok
}

// ResolveSessionWithScope is the scope-aware sibling of ResolveSession
// (REQ-AUTH-SCOPE-01). Returns (principal, scope, true) if a valid
// session cookie is on r and the principal is not disabled; (0, nil,
// false) otherwise. The scope set is the value the issuing /login
// flow stamped on the cookie; it is immutable for the cookie's
// lifetime (rotate by logging out + back in).
func (s *Server) ResolveSessionWithScope(r *http.Request) (store.PrincipalID, auth.ScopeSet, bool) {
	sess, ok := s.readSession(r)
	if !ok {
		return 0, nil, false
	}
	p, err := s.store.Meta().GetPrincipalByID(r.Context(), sess.PrincipalID)
	if err != nil {
		return 0, nil, false
	}
	if p.Flags.Has(store.PrincipalFlagDisabled) {
		return 0, nil, false
	}
	return sess.PrincipalID, sess.Scopes, true
}

// readSession parses the session cookie from r. The returned bool is
// true iff a valid, unexpired session was found.
func (s *Server) readSession(r *http.Request) (session, bool) {
	c, err := r.Cookie(s.cfg.CookieName)
	if err != nil || c.Value == "" {
		return session{}, false
	}
	sess, err := decodeSession(c.Value, s.cfg.SigningKey, s.clk.Now())
	if err != nil {
		return session{}, false
	}
	return sess, true
}

// requireSession is the middleware that gates every authenticated UI
// route. On miss it redirects to the login page with the original URL
// in `?redirect=`. On hit it slides the session expiry forward and
// attaches the principal to the context.
func (s *Server) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := s.readSession(r)
		if !ok {
			// Preserve the path (with query) so we land back here after
			// a successful login.
			redirect := r.URL.RequestURI()
			http.Redirect(w, r, s.pathPrefix+"/login?redirect="+escapeQuery(redirect), http.StatusSeeOther)
			return
		}
		// Sliding renewal: stamp a fresh ExpiresAt every authenticated
		// request so an active operator never gets logged out mid-edit.
		sess.ExpiresAt = s.clk.Now().Add(s.cfg.TTL)
		s.setSessionCookie(w, sess)

		p, err := s.store.Meta().GetPrincipalByID(r.Context(), sess.PrincipalID)
		if err != nil {
			s.clearSessionCookie(w)
			http.Redirect(w, r, s.pathPrefix+"/login", http.StatusSeeOther)
			return
		}
		if p.Flags.Has(store.PrincipalFlagDisabled) {
			s.clearSessionCookie(w)
			http.Redirect(w, r, s.pathPrefix+"/login", http.StatusSeeOther)
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeyPrincipal, p)
		ctx = context.WithValue(ctx, ctxKeySession, sess)
		// Attach the closed-enum auth.AuthContext so handlers can
		// run RequireScope(...) without re-parsing the cookie.
		ctx = auth.WithContext(ctx, &auth.AuthContext{
			PrincipalID: uint64(sess.PrincipalID),
			Scopes:      sess.Scopes,
			Listener:    s.listenerKind,
		})
		next(w, r.WithContext(ctx))
	}
}

// escapeQuery is a tiny helper that escapes the redirect target for use
// inside a URL query parameter. We only need to escape the characters
// the http.Redirect helper does not handle for us; query.Escape covers
// the lot.
func escapeQuery(s string) string {
	// Inline the bare minimum: net/url.QueryEscape is the right tool but
	// importing url for one call here keeps the dependency surface
	// trivial.
	const hex = "0123456789ABCDEF"
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9',
			c == '-', c == '_', c == '.', c == '~', c == '/':
			out = append(out, c)
		default:
			out = append(out, '%', hex[c>>4], hex[c&0xF])
		}
	}
	return string(out)
}
