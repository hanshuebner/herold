package protoui

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

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

// session is the decoded form of a session cookie. The wire form is
// `<principalID>.<expiresAtUnix>.<base64url(hmacSig)>`. Three dot-
// separated fields keep the parser obvious; the comparator is constant-
// time on the signature so a forged cookie cannot side-channel the
// guess loop.
type session struct {
	PrincipalID store.PrincipalID
	ExpiresAt   time.Time
	// CSRFToken is the server-issued CSRF token associated with this
	// session. Returned to the user via the CSRFCookieName cookie and
	// also embedded in HTML forms; the double-submit middleware
	// requires the two match.
	CSRFToken string
}

// encodeSession produces the wire form for a session.
func encodeSession(s session, key []byte) string {
	payload := fmt.Sprintf("%d.%d.%s", uint64(s.PrincipalID), s.ExpiresAt.Unix(), s.CSRFToken)
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
	// Split into <pid>.<exp>.<csrf>.<sig>
	parts := strings.Split(raw, ".")
	if len(parts) != 4 {
		return session{}, errSessionInvalid
	}
	pidStr, expStr, csrfTok, sig := parts[0], parts[1], parts[2], parts[3]
	payload := pidStr + "." + expStr + "." + csrfTok
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
	return session{
		PrincipalID: store.PrincipalID(pid),
		ExpiresAt:   exp,
		CSRFToken:   csrfTok,
	}, nil
}

var (
	errSessionInvalid = errors.New("protoui: session cookie invalid or unsigned")
	errSessionExpired = errors.New("protoui: session cookie expired")
)

// setSessionCookie writes both the session cookie and the matching
// CSRF cookie to w. The CSRF cookie is intentionally NOT HttpOnly so
// the future client-side fetch helpers (the small Alpine bits we ship)
// can read it; the session cookie IS HttpOnly so JS cannot exfiltrate
// it.
//
// MaxAge is computed against the injected clock — using time.Until
// here would mix the real wall clock with the FakeClock-driven
// session deadlines and produce negative MaxAge values in tests,
// causing the test cookie jar to drop the cookie immediately.
func (s *Server) setSessionCookie(w http.ResponseWriter, sess session) {
	encoded := encodeSession(sess, s.cfg.SigningKey)
	// Compute MaxAge from the session's TTL knob, not from the
	// injected clock's view of ExpiresAt. The cookie jar enforces
	// expiry against the real wall clock; mixing the FakeClock-
	// derived deadline with the real-clock jar caused tests to
	// silently drop the cookie when the FakeClock anchor differed
	// from the real wall time. Server-side session validation still
	// uses the signed ExpiresAt for its source of truth.
	maxAge := int(s.cfg.TTL.Seconds())
	if maxAge <= 0 {
		maxAge = 1
	}
	http.SetCookie(w, &http.Cookie{
		Name:     s.cfg.CookieName,
		Value:    encoded,
		Path:     s.pathPrefix + "/",
		MaxAge:   maxAge,
		Secure:   s.cfg.SecureCookies,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     s.cfg.CSRFCookieName,
		Value:    sess.CSRFToken,
		Path:     s.pathPrefix + "/",
		MaxAge:   maxAge,
		Secure:   s.cfg.SecureCookies,
		HttpOnly: false,
		SameSite: http.SameSiteStrictMode,
	})
}

// clearSessionCookie writes Set-Cookie headers that expire immediately,
// so a logged-out browser drops both the session and CSRF cookies.
func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	for _, name := range []string{s.cfg.CookieName, s.cfg.CSRFCookieName} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     s.pathPrefix + "/",
			Expires:  time.Unix(0, 0),
			MaxAge:   -1,
			Secure:   s.cfg.SecureCookies,
			HttpOnly: name == s.cfg.CookieName,
			SameSite: http.SameSiteStrictMode,
		})
	}
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
	sess, ok := s.readSession(r)
	if !ok {
		return 0, false
	}
	p, err := s.store.Meta().GetPrincipalByID(r.Context(), sess.PrincipalID)
	if err != nil {
		return 0, false
	}
	if p.Flags.Has(store.PrincipalFlagDisabled) {
		return 0, false
	}
	return sess.PrincipalID, true
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
