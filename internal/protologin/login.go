package protologin

// login.go implements handleLogin -- lifted from
// internal/protoadmin/session_auth.go with the struct-method receiver
// replaced by a plain Server receiver and the admin-specific scope issuance
// replaced by the configurable Options.Scopes callback.
//
// Duplication vs protoadmin/session_auth.go: the core logic (~65 lines) is
// essentially the same. Phase 3c-iii may collapse this by migrating the
// admin handler here too. The duplication is documented in the commit message
// as requested.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/hanshuebner/herold/internal/auth"
	"github.com/hanshuebner/herold/internal/authsession"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/store"
)

// loginRequest is the JSON body accepted by POST /api/v1/auth/login.
type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	// TOTPCode is optional on the first POST; required when the principal has
	// TOTP enrolled (REQ-AUTH-SCOPE-03).
	TOTPCode string `json:"totp_code,omitempty"`
}

// loginResponse is the JSON body returned on a successful login.
type loginResponse struct {
	PrincipalID uint64       `json:"principal_id"`
	Email       string       `json:"email"`
	Scopes      []auth.Scope `json:"scopes"`
}

// handleLogin handles POST /api/v1/auth/login.
//
// On success it issues a session cookie and a CSRF cookie via
// authsession.WriteSessionCookie and returns 200 with {principal_id, email,
// scopes}. The issued scopes are determined by Options.Scopes.
//
// TOTP step-up (REQ-AUTH-SCOPE-03): if the principal has TOTP enrolled and
// totp_code is absent or wrong, responds 401 with step_up_required=true.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ipKey := "login-ip:" + remoteHost(r.RemoteAddr)
	if s.opts.RateLimiter != nil && !s.opts.RateLimiter(w, r, ipKey) {
		return
	}

	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest,
			"bad_request", "request body must be JSON {email, password}", "")
		return
	}
	if req.Email == "" || req.Password == "" {
		writeProblem(w, r, http.StatusBadRequest,
			"bad_request", "email and password are required", "")
		return
	}

	ctx := directory.WithAuthSource(r.Context(), remoteHost(r.RemoteAddr))

	pid, err := s.opts.Directory.Authenticate(ctx, req.Email, req.Password)
	if err != nil {
		s.auditLoginFailure(r, req.Email, 0, humanLoginError(err))
		writeProblem(w, r, http.StatusUnauthorized,
			"unauthorized", humanLoginError(err), "")
		return
	}

	p, err := s.opts.Store.Meta().GetPrincipalByID(ctx, pid)
	if err != nil {
		s.log.Warn("protologin.login.principal_lookup_failed",
			"err", err, "principal_id", pid, "listener", s.opts.Listener)
		s.auditLoginFailure(r, req.Email, pid, "principal load failed")
		writeProblem(w, r, http.StatusInternalServerError,
			"internal_error", "principal load failed", "")
		return
	}
	if p.Flags.Has(store.PrincipalFlagDisabled) {
		s.auditLoginFailure(r, p.CanonicalEmail, pid, "account is disabled")
		writeProblem(w, r, http.StatusUnauthorized,
			"unauthorized", "account is disabled", "")
		return
	}

	// TOTP step-up (REQ-AUTH-SCOPE-03): require a TOTP code for 2FA-enabled
	// principals before issuing a cookie.
	if p.Flags.Has(store.PrincipalFlagTOTPEnabled) {
		if req.TOTPCode == "" {
			s.auditLoginFailure(r, p.CanonicalEmail, pid, "totp code missing")
			writeLoginProblemStepUp(w, r)
			return
		}
		if err := s.opts.Directory.VerifyTOTP(ctx, pid, req.TOTPCode); err != nil {
			if errors.Is(err, directory.ErrRateLimited) {
				s.auditLoginFailure(r, p.CanonicalEmail, pid, "totp rate-limited")
				writeProblem(w, r, http.StatusUnauthorized,
					"unauthorized", "too many TOTP attempts; please wait", "")
				return
			}
			s.auditLoginFailure(r, p.CanonicalEmail, pid, "totp code invalid")
			writeLoginProblemStepUp(w, r)
			return
		}
	}

	// Issue the session with the listener-specific scope set.
	var sessScopes auth.ScopeSet
	if s.opts.Scopes != nil {
		sessScopes = s.opts.Scopes(p)
	} else {
		sessScopes = auth.NewScopeSet(auth.ScopeEndUser)
	}
	ttl := s.opts.Session.TTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	sess := authsession.Session{
		PrincipalID: pid,
		ExpiresAt:   s.opts.Clock.Now().Add(ttl),
		CSRFToken:   authsession.NewCSRFToken(),
		Scopes:      sessScopes,
	}
	authsession.WriteSessionCookie(w, s.opts.Session, sess)

	// Audit the success. Attach the principal to the context so the record
	// carries actor=principal/<id> (REQ-ADM-300).
	if s.opts.AuditAppender != nil {
		auditCtx := context.WithValue(r.Context(), ctxKeyLoginPrincipal{}, p)
		s.opts.AuditAppender(auditCtx,
			"auth.login",
			"principal:"+p.CanonicalEmail,
			store.OutcomeSuccess,
			"",
			map[string]string{
				"remote":   remoteHost(r.RemoteAddr),
				"listener": s.opts.Listener,
			},
		)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(loginResponse{
		PrincipalID: uint64(p.ID),
		Email:       p.CanonicalEmail,
		Scopes:      sessScopes.Slice(),
	})
}

// auditLoginFailure writes a failed-login audit record.
func (s *Server) auditLoginFailure(r *http.Request, attemptedEmail string, principalID directory.PrincipalID, message string) {
	if s.opts.AuditAppender == nil {
		return
	}
	meta := map[string]string{
		"remote":          remoteHost(r.RemoteAddr),
		"attempted_email": attemptedEmail,
		"listener":        s.opts.Listener,
	}
	if principalID > 0 {
		meta["principal_id"] = strconv.FormatUint(uint64(principalID), 10)
	}
	s.opts.AuditAppender(r.Context(),
		"auth.login",
		"email:"+attemptedEmail,
		store.OutcomeFailure,
		message,
		meta,
	)
}

// ctxKeyLoginPrincipal is the context key for the authenticated principal
// within this package's audit call path.
type ctxKeyLoginPrincipal struct{}

// humanLoginError maps directory errors to terse user-facing strings.
// Mirrors protoadmin/session_auth.go to maintain identical UX. Does not
// differentiate wrong email from wrong password (anti-enumeration).
func humanLoginError(err error) string {
	switch {
	case errors.Is(err, directory.ErrUnauthorized):
		return "email or password is incorrect"
	case errors.Is(err, directory.ErrRateLimited):
		return "too many login attempts; please wait and try again"
	default:
		return "authentication failed"
	}
}

// writeLoginProblemStepUp writes a 401 problem with step_up_required=true
// (REQ-AUTH-SCOPE-03). Mirrors protoadmin/session_auth.go.
func writeLoginProblemStepUp(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":             "about:blank",
		"title":            "TOTP code required",
		"status":           http.StatusUnauthorized,
		"detail":           "This account requires a TOTP code; supply totp_code and re-submit.",
		"step_up_required": true,
	})
}
