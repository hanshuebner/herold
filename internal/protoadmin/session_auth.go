package protoadmin

// session_auth.go implements the JSON login / logout endpoints for the
// admin REST surface (REQ-AUTH-SESSION-REST).
//
// POST /api/v1/auth/login  -- accepts {email, password, totp_code?},
//
//	issues herold_admin_session + herold_admin_csrf cookies, returns
//	{principal_id, email, scopes:[...]}.
//
// POST /api/v1/auth/logout -- clears the cookies, returns 204.
//
// These endpoints are NOT protected by requireAuth (they ARE the auth
// boundary). They are rate-limited via the per-source-IP bucket so
// brute-force is throttled before any principal is resolved.

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/hanshuebner/herold/internal/auth"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/protoui"
	"github.com/hanshuebner/herold/internal/store"
)

// loginRequest is the JSON body accepted by POST /api/v1/auth/login.
type loginRequest struct {
	// Email is the principal's canonical email address.
	Email string `json:"email"`
	// Password is the principal's plain-text password for verification.
	Password string `json:"password"`
	// TOTPCode is the current TOTP one-time password. Required when the
	// principal has TOTP enrolled (REQ-AUTH-SCOPE-03). Omit or send ""
	// on the first POST to discover whether step-up is required (the
	// response returns 401 with step_up_required=true).
	TOTPCode string `json:"totp_code,omitempty"`
}

// loginResponse is the JSON body returned on a successful login.
type loginResponse struct {
	// PrincipalID is the authenticated principal's numeric ID.
	PrincipalID uint64 `json:"principal_id"`
	// Email is the principal's canonical email address.
	Email string `json:"email"`
	// Scopes is the scope set encoded into the issued session cookie
	// (REQ-AUTH-SCOPE-01). The SPA uses this to gate UI surfaces.
	Scopes []auth.Scope `json:"scopes"`
}

// handleLogin handles POST /api/v1/auth/login.
//
// The endpoint is unauthenticated -- it IS the authentication boundary.
// Rate limiting uses the bootstrap limiter's per-source-IP bucket so
// brute-force is throttled; the per-principal bucket is applied after
// the principal is resolved to stay consistent with the API-key path.
//
// On success it issues herold_admin_session (HttpOnly) and
// herold_admin_csrf (non-HttpOnly, readable by the SPA's JS) cookies
// via protoui.WriteSessionCookie and returns 200 with {principal_id,
// email, scopes}. See REQ-AUTH-SESSION-REST and REQ-AUTH-CSRF.
//
// TOTP step-up (REQ-AUTH-SCOPE-03): if the principal has TOTP enrolled
// and totp_code is absent or wrong, the response is 401 with
// {step_up_required: true} in the problem detail extensions.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	// Rate-limit by source IP before touching the directory, matching
	// the bootstrap and JMAP login posture.
	ipKey := "login-ip:" + remoteHost(r.RemoteAddr)
	if !s.checkRateLimit(w, r, ipKey) {
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

	pid, err := s.dir.Authenticate(ctx, req.Email, req.Password)
	if err != nil {
		// No differentiation between wrong email and wrong password in
		// the response (anti-enumeration). Rate-limited via directory.
		writeProblem(w, r, http.StatusUnauthorized,
			"unauthorized", humanLoginError(err), "")
		return
	}

	p, err := s.store.Meta().GetPrincipalByID(ctx, pid)
	if err != nil {
		s.loggerFrom(ctx).Warn("protoadmin.login.principal_lookup_failed",
			"err", err, "principal_id", pid)
		writeProblem(w, r, http.StatusInternalServerError,
			"internal_error", "principal load failed", "")
		return
	}
	if p.Flags.Has(store.PrincipalFlagDisabled) {
		writeProblem(w, r, http.StatusUnauthorized,
			"unauthorized", "account is disabled", "")
		return
	}

	// TOTP step-up (REQ-AUTH-SCOPE-03): admin listener requires a TOTP
	// code for 2FA-enabled principals before issuing admin-scoped cookie.
	if p.Flags.Has(store.PrincipalFlagTOTPEnabled) {
		if req.TOTPCode == "" {
			writeLoginProblemStepUp(w, r)
			return
		}
		if err := s.dir.VerifyTOTP(ctx, pid, req.TOTPCode); err != nil {
			if errors.Is(err, directory.ErrRateLimited) {
				writeProblem(w, r, http.StatusUnauthorized,
					"unauthorized", "too many TOTP attempts; please wait", "")
				return
			}
			writeLoginProblemStepUp(w, r)
			return
		}
	}

	// Issue the session. Admin listener -> admin scope only
	// (REQ-AUTH-SCOPE-01..03).
	sessScopes := auth.NewScopeSet(auth.ScopeAdmin)
	ttl := s.opts.Session.TTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	sess := protoui.Session{
		PrincipalID: pid,
		ExpiresAt:   s.clk.Now().Add(ttl),
		CSRFToken:   protoui.NewCSRFToken(),
		Scopes:      sessScopes,
	}

	cfg := s.sessionConfig()
	protoui.WriteSessionCookie(w, cfg, sess)

	s.appendAudit(r.Context(),
		"auth.login",
		"principal:"+p.CanonicalEmail,
		store.OutcomeSuccess,
		"",
		map[string]string{
			"remote": remoteHost(r.RemoteAddr),
		},
	)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(loginResponse{
		PrincipalID: uint64(p.ID),
		Email:       p.CanonicalEmail,
		Scopes:      sessScopes.Slice(),
	})
}

// handleLogout handles POST /api/v1/auth/logout.
//
// Clears the session and CSRF cookies by issuing expired Set-Cookie
// headers and returns 204. The endpoint accepts both cookie and Bearer
// authentication; a caller who is already logged out (no cookies, no
// Bearer) just gets 401 from requireAuth, which is consistent with the
// "nothing to do" case being a no-op.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	cfg := s.sessionConfig()
	protoui.ClearSessionCookies(w, cfg)
	s.appendAudit(r.Context(),
		"auth.logout",
		"",
		store.OutcomeSuccess,
		"",
		nil,
	)
	w.WriteHeader(http.StatusNoContent)
}

// sessionConfig builds the protoui.SessionConfig from the server's
// Options.Session, applying defaults for empty fields so callers don't
// have to worry about them.
func (s *Server) sessionConfig() protoui.SessionConfig {
	cfg := s.opts.Session
	if cfg.CookieName == "" {
		cfg.CookieName = "herold_admin_session"
	}
	if cfg.CSRFCookieName == "" {
		cfg.CSRFCookieName = "herold_admin_csrf"
	}
	return cfg
}

// writeLoginProblemStepUp writes a 401 problem with step_up_required=true
// in the problem detail extensions (REQ-AUTH-SCOPE-03).
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

// humanLoginError maps directory errors to terse user-facing strings.
// It deliberately does not differentiate between wrong email and wrong
// password to prevent account enumeration.
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
