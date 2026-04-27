package protoui

// session.go -- adapter shim left behind after Phase 3a extracted the
// core session/cookie/CSRF logic into internal/authsession.
//
// All encoding, decoding, cookie-writing, and CSRF-token primitives now
// live in internal/authsession. This file re-exports them via type
// aliases and function/var aliases so the rest of internal/protoui keeps
// compiling without changing every call site. These aliases are a
// one-release adapter; Phase 3b deletes internal/protoui entirely and
// the aliases go with it.

import (
	"context"
	"net/http"

	"github.com/hanshuebner/herold/internal/auth"
	"github.com/hanshuebner/herold/internal/authsession"
	"github.com/hanshuebner/herold/internal/store"
)

// Type aliases -- consumers continue to use protoui.SessionConfig and
// protoui.Session without any source change.

// SessionConfig is an alias for authsession.SessionConfig.
type SessionConfig = authsession.SessionConfig

// Session is the decoded form of a session cookie. Alias for
// authsession.Session. See authsession.Session for the wire format and
// scope semantics (REQ-AUTH-SCOPE-01, REQ-AUTH-SESSION-REST).
type Session = authsession.Session

// session is the internal alias kept for backwards compatibility within
// the protoui package. All internal code continues using 'session'; the
// exported Session type is for cross-package consumers.
type session = Session

// Function and var aliases so in-package call sites compile unchanged.

// WriteSessionCookie delegates to authsession.WriteSessionCookie.
// The session cookie Path is "/" per REQ-AUTH-COOKIE-PATH.
var WriteSessionCookie = authsession.WriteSessionCookie

// ClearSessionCookies delegates to authsession.ClearSessionCookies.
// Path must match the issuance path ("/") per REQ-AUTH-COOKIE-PATH.
var ClearSessionCookies = authsession.ClearSessionCookies

// DecodeSession delegates to authsession.DecodeSession.
// Exported for cross-package cookie verification (REQ-AUTH-SESSION-REST).
var DecodeSession = authsession.DecodeSession

// NewCSRFToken delegates to authsession.NewCSRFToken.
// Exported so protoadmin's JSON login endpoint can mint a CSRF token.
var NewCSRFToken = authsession.NewCSRFToken

// EncodeSession delegates to authsession.EncodeSession.
// Exported so cross-package consumers can issue cookies without
// re-implementing the encoding (REQ-AUTH-SESSION-REST).
func EncodeSession(s Session, key []byte) string {
	return authsession.EncodeSession(s, key)
}

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
	sess, err := authsession.DecodeSession(c.Value, s.cfg.SigningKey, s.clk.Now())
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
