package protologin

// logout.go implements handleLogout.

import (
	"net/http"

	"github.com/hanshuebner/herold/internal/authsession"
	"github.com/hanshuebner/herold/internal/store"
)

// handleLogout handles POST /api/v1/auth/logout.
//
// Clears the session and CSRF cookies by issuing expired Set-Cookie
// headers and returns 204. Sessions are stateless HMAC-signed cookies
// (REQ-AUTH-JSON-LOGOUT); logout invalidates the client-side cookies only.
// There is no server-side revocation list.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	authsession.ClearSessionCookies(w, s.opts.Session)

	// Resolve the current principal for the audit record. We read the
	// session cookie directly rather than going through a full auth
	// middleware since the logout handler does not require authentication
	// (the worst case is an unauthenticated caller sending a logout request,
	// which is a harmless no-op -- the cookies it presents are cleared).
	var subject string
	if sess, err := resolveSessionCookie(r, s.opts.Session, s.opts.Clock); err == nil {
		if p, err := s.opts.Store.Meta().GetPrincipalByID(r.Context(), sess.PrincipalID); err == nil {
			subject = "principal:" + p.CanonicalEmail
		}
	}

	if s.opts.AuditAppender != nil {
		s.opts.AuditAppender(r.Context(),
			"auth.logout",
			subject,
			store.OutcomeSuccess,
			"",
			map[string]string{"listener": s.opts.Listener},
		)
	}

	w.WriteHeader(http.StatusNoContent)
}
