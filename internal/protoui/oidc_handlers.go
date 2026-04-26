package protoui

import (
	"net/http"

	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/store"
)

// handleOIDCBegin starts an OIDC sign-in flow against the named
// provider and redirects the operator's browser to the IdP.
func (s *Server) handleOIDCBegin(w http.ResponseWriter, r *http.Request) {
	if s.rp == nil {
		s.renderError(w, r, http.StatusBadRequest, "OIDC not configured on this server.")
		return
	}
	provider := r.PathValue("provider")
	authURL, _, err := s.rp.BeginSignIn(r.Context(), directoryoidc.ProviderID(provider))
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, "OIDC begin failed: "+err.Error())
		return
	}
	http.Redirect(w, r, authURL, http.StatusSeeOther)
}

// handleOIDCCallback completes either a sign-in or link flow. On
// successful sign-in it mints a UI session and redirects to the
// dashboard; on a link it returns to the principal's detail page.
func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	if s.rp == nil {
		s.renderError(w, r, http.StatusBadRequest, "OIDC not configured on this server.")
		return
	}
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	if state == "" || code == "" {
		s.renderError(w, r, http.StatusBadRequest, "Missing state or code.")
		return
	}
	switch s.rp.PeekPendingFlow(state) {
	case directoryoidc.FlowKindSignIn:
		pid, err := s.rp.CompleteSignIn(r.Context(), state, code)
		if err != nil {
			s.renderError(w, r, http.StatusBadRequest, "OIDC sign-in failed: "+err.Error())
			return
		}
		// OIDC sign-in inherits the per-listener scope policy
		// (REQ-AUTH-SCOPE-01). For the public listener that means
		// AllEndUserScopes; for the admin listener the OIDC path is
		// not the recommended issuance flow (admin scope wants TOTP
		// step-up per REQ-AUTH-SCOPE-03), so we still issue admin-
		// scope here and let the listener's loopback bind be the
		// outermost guard. TODO(3.6-followup): when external IdPs
		// land for the admin path, require an MFA assurance claim
		// on the OIDC token before issuing admin scope.
		var pp store.Principal
		if loaded, err := s.store.Meta().GetPrincipalByID(r.Context(), pid); err == nil {
			pp = loaded
		}
		sess := session{
			PrincipalID: pid,
			ExpiresAt:   s.clk.Now().Add(s.cfg.TTL),
			CSRFToken:   newCSRFToken(),
			Scopes:      s.scopeForLogin(pp),
		}
		s.setSessionCookie(w, sess)
		http.Redirect(w, r, s.pathPrefix+"/dashboard", http.StatusSeeOther)
	case directoryoidc.FlowKindLink:
		pid, err := s.rp.CompleteLink(r.Context(), state, code)
		if err != nil {
			s.renderError(w, r, http.StatusBadRequest, "OIDC link failed: "+err.Error())
			return
		}
		// Redirect back to the linked principal's detail page; if no
		// session, send to login.
		if _, ok := s.readSession(r); !ok {
			http.Redirect(w, r, s.pathPrefix+"/login", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, s.pathPrefix+"/principals/"+itoa(uint64(pid))+"?flash=oidc_unlinked", http.StatusSeeOther)
	default:
		s.renderError(w, r, http.StatusBadRequest, "OIDC state not recognised or already consumed.")
	}
}

// itoa is a tiny no-allocation uint64-to-string helper. (strconv.Itoa
// allocates; for one call site this saves the import line.)
func itoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
