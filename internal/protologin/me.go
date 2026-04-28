package protologin

// me.go implements handleMe -- GET /api/v1/auth/me. Returns the
// principal_id, email, and scopes embedded in the current session cookie
// so the Suite SPA can resolve its own principal_id after a page reload
// without re-issuing credentials. Phase 4 of the merge plan (REQ-ADM-203)
// uses the returned principal_id to address the self-service REST surface
// (PUT /api/v1/principals/{pid}/password, /totp/*, /api-keys, etc.).

import (
	"encoding/json"
	"net/http"

	"github.com/hanshuebner/herold/internal/auth"
	"github.com/hanshuebner/herold/internal/store"
)

// meResponse is the JSON body returned on a successful /auth/me lookup.
// Mirrors loginResponse so the Suite can use a single type for both.
type meResponse struct {
	PrincipalID uint64       `json:"principal_id"`
	Email       string       `json:"email"`
	Scopes      []auth.Scope `json:"scopes"`
}

// handleMe handles GET /api/v1/auth/me.
//
// On a valid session cookie: 200 with {principal_id, email, scopes}.
// On no/expired/invalid cookie: 401 application/problem+json.
// On a disabled principal: 401 (same envelope as login).
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	sess, err := resolveSessionCookie(r, s.opts.Session, s.opts.Clock)
	if err != nil {
		writeProblem(w, r, http.StatusUnauthorized,
			"unauthorized", "no valid session", "")
		return
	}
	p, err := s.opts.Store.Meta().GetPrincipalByID(r.Context(), sess.PrincipalID)
	if err != nil {
		writeProblem(w, r, http.StatusUnauthorized,
			"unauthorized", "principal not found", "")
		return
	}
	if p.Flags.Has(store.PrincipalFlagDisabled) {
		writeProblem(w, r, http.StatusUnauthorized,
			"unauthorized", "account is disabled", "")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(meResponse{
		PrincipalID: uint64(p.ID),
		Email:       p.CanonicalEmail,
		Scopes:      sess.Scopes.Slice(),
	})
}
