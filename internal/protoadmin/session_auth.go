package protoadmin

// session_auth.go implements the JSON login / logout / whoami endpoints
// for the public REST surface (REQ-AUTH-SESSION-REST).
//
// POST /api/v1/auth/login  -- accepts {email, password, totp_code?},
//
//	issues herold_public_session + herold_public_csrf cookies, returns
//	{principal_id, email, scopes:[...]}.
//
// POST /api/v1/auth/logout -- clears the cookies, returns 204.
// GET  /api/v1/auth/whoami -- returns 200 + {principal_id, email, scopes}
//
//	when the session is valid, 401 otherwise. Used by the admin SPA to
//	probe session state on page load.
//
// These endpoints are NOT protected by requireAuth (they ARE the auth
// boundary). They are rate-limited via the per-source-IP bucket so
// brute-force is throttled before any principal is resolved.

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
	"github.com/hanshuebner/herold/internal/observe"
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
	// SessionExpiresAt is the RFC 3339 UTC deadline of the issued session
	// cookie. The Suite SPA uses it to schedule a client-side expiry timer
	// (re #58: now that the public listener also issues admin-scoped cookies,
	// the suite SPA needs the expiry just like it did with protologin).
	SessionExpiresAt string `json:"session_expires_at,omitempty"`
}

// clientlogSessionMeta is the per-session clientlog descriptor embedded
// in whoamiResponse. The admin SPA reads it to configure the clientlog
// wrapper per REQ-CLOG-05, REQ-CLOG-12.
type clientlogSessionMeta struct {
	// TelemetryEnabled is the resolved per-session telemetry flag.
	// False when the session row is absent (API-key auth with no
	// persisted session).
	TelemetryEnabled bool `json:"telemetry_enabled"`
	// LivetailUntil is the RFC 3339 timestamp (ms precision) of the
	// live-tail expiry (REQ-OPS-211). Omitted when null or in the past.
	LivetailUntil *string `json:"livetail_until,omitempty"`
}

// whoamiResponse is the JSON body returned by GET /api/v1/auth/whoami
// and also augments GET /api/v1/server/status so the admin SPA can
// identify the calling principal from a single round-trip.
type whoamiResponse struct {
	// PrincipalID is the authenticated principal's numeric ID.
	PrincipalID uint64 `json:"principal_id"`
	// Email is the principal's canonical email address.
	Email string `json:"email"`
	// Scopes is the scope set carried by the session or API key
	// (REQ-AUTH-SCOPE-01). The SPA uses this to gate UI surfaces.
	Scopes []auth.Scope `json:"scopes"`
	// Clientlog carries the per-session clientlog descriptor. Present
	// on every authenticated response so the admin SPA can observe
	// livetail_until and telemetry_enabled without a separate round-trip
	// (REQ-CLOG-05, REQ-CLOG-12).
	Clientlog clientlogSessionMeta `json:"clientlog"`
}

// handleLogin handles POST /api/v1/auth/login.
//
// The endpoint is unauthenticated -- it IS the authentication boundary.
// Rate limiting uses the bootstrap limiter's per-source-IP bucket so
// brute-force is throttled; the per-principal bucket is applied after
// the principal is resolved to stay consistent with the API-key path.
//
// On success it issues herold_public_session (HttpOnly) and
// herold_public_csrf (non-HttpOnly, readable by the SPA's JS) cookies
// via authsession.WriteSessionCookie and returns 200 with {principal_id,
// email, scopes}. See REQ-AUTH-SESSION-REST and REQ-AUTH-CSRF.
//
// Session lifetime: admin-scoped sessions (ScopeAdmin) use Options.AdminTTL
// (default 8 h, max 12 h) to enforce a shorter absolute lifetime than
// end-user sessions (Options.Session.TTL, default 7 days). The gap
// reduces the risk window on a captured admin cookie (REQ-AUTH-72, re #58).
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
		// Audit the failure (REQ-ADM-300, REQ-ADM-303): failed auth
		// attempts MUST land in the durable audit log so SIEM /
		// fail2ban pipelines can see brute-force activity.
		s.loggerFrom(r.Context()).WarnContext(r.Context(), "protoadmin.auth.login_failed",
			"activity", observe.ActivityAudit,
			"email", req.Email,
			"reason", humanLoginError(err))
		s.auditLoginFailure(r, req.Email, 0, humanLoginError(err))
		writeProblem(w, r, http.StatusUnauthorized,
			"unauthorized", humanLoginError(err), "")
		return
	}

	p, err := s.store.Meta().GetPrincipalByID(ctx, pid)
	if err != nil {
		s.loggerFrom(ctx).Warn("protoadmin.login.principal_lookup_failed",
			"activity", observe.ActivityAudit,
			"err", err, "principal_id", pid)
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

	// Mandatory TOTP for admin role (REQ-AUTH-44, issue #12): an admin
	// principal without TOTP enrolled CANNOT obtain an admin-scoped
	// session via the password-login endpoint. Password login refuses
	// here so the public-facing admin surface is safe to expose without
	// an IP allowlist.
	//
	// First-time enrollment: use the one-shot bootstrap API key
	// (initial_api_key from POST /api/v1/bootstrap) as a Bearer token
	// to reach POST /api/v1/principals/{pid}/totp/enroll and then
	// /totp/confirm. The bootstrap endpoint only works when there are
	// no principals yet (new installation). For a sole-admin lockout
	// (admin lost TOTP device, no second admin), use
	// `herold recover --reset-totp <email>` on the server host
	// (re #24, re #21) — the bootstrap path is not available after
	// the first principal is created.
	if p.Flags.Has(store.PrincipalFlagAdmin) && !p.Flags.Has(store.PrincipalFlagTOTPEnabled) {
		s.loggerFrom(ctx).WarnContext(ctx, "protoadmin.auth.admin_totp_missing",
			"activity", observe.ActivityAudit,
			"principal_id", pid)
		s.auditLoginFailure(r, p.CanonicalEmail, pid, "admin role requires TOTP enrollment")
		writeLoginProblemTOTPEnrollmentRequired(w, r)
		return
	}

	// TOTP step-up (REQ-AUTH-SCOPE-03): admin listener requires a TOTP
	// code for 2FA-enabled principals before issuing admin-scoped cookie.
	if p.Flags.Has(store.PrincipalFlagTOTPEnabled) {
		if req.TOTPCode == "" {
			s.loggerFrom(r.Context()).WarnContext(r.Context(), "protoadmin.auth.totp_missing",
				"activity", observe.ActivityAudit,
				"principal_id", pid)
			s.auditLoginFailure(r, p.CanonicalEmail, pid, "totp code missing")
			writeLoginProblemStepUp(w, r)
			return
		}
		if err := s.dir.VerifyTOTP(ctx, pid, req.TOTPCode); err != nil {
			if errors.Is(err, directory.ErrRateLimited) {
				s.loggerFrom(r.Context()).WarnContext(r.Context(), "protoadmin.auth.totp_rate_limited",
					"activity", observe.ActivityAudit,
					"principal_id", pid)
				s.auditLoginFailure(r, p.CanonicalEmail, pid, "totp rate-limited")
				writeProblem(w, r, http.StatusUnauthorized,
					"unauthorized", "too many TOTP attempts; please wait", "")
				return
			}
			s.loggerFrom(r.Context()).WarnContext(r.Context(), "protoadmin.auth.totp_invalid",
				"activity", observe.ActivityAudit,
				"principal_id", pid)
			s.auditLoginFailure(r, p.CanonicalEmail, pid, "totp code invalid")
			writeLoginProblemStepUp(w, r)
			return
		}
	}

	// Issue the session. The scope set depends on the principal's admin flag:
	// - Admin principals who completed TOTP step-up receive ScopeAdmin plus
	//   all end-user scopes, so the admin SPA and the suite SPA both work from
	//   the single public session (REQ-AUTH-SCOPE-01..03, re #58).
	// - Non-admin principals receive the full end-user scope set only.
	//
	// TOTP hard-require: the block above already refused admin principals
	// without TOTP enrolled. Reaching here with PrincipalFlagAdmin set implies
	// TOTP was verified (or re-verified) in the block above.
	var sessScopes auth.ScopeSet
	if p.Flags.Has(store.PrincipalFlagAdmin) {
		// Admin principal with TOTP verified: grant admin scope in addition to
		// all end-user scopes. ScopeAdmin is the innermost guard checked by
		// protoadmin's requireScope middleware on admin-only routes.
		sessScopes = auth.NewScopeSet(append([]auth.Scope{auth.ScopeAdmin}, auth.AllEndUserScopes...)...)
	} else {
		// Non-admin principal: end-user scopes only. ScopeAdmin is explicitly
		// excluded — no privilege escalation via this login path.
		sessScopes = auth.NewScopeSet(auth.AllEndUserScopes...)
	}

	// Session lifetime: admin-scoped sessions use the shorter AdminTTL
	// (default 8 h, ceiling 12 h) to limit the risk window from a captured
	// cookie. End-user sessions use the longer Session.TTL (default 7 days)
	// because they carry no elevated privilege (REQ-AUTH-72, re #58).
	var ttl time.Duration
	if sessScopes.Has(auth.ScopeAdmin) {
		ttl = s.opts.AdminTTL
		if ttl <= 0 {
			ttl = 8 * time.Hour
		}
	} else {
		ttl = s.opts.Session.TTL
		if ttl <= 0 {
			ttl = 7 * 24 * time.Hour
		}
	}
	sess := authsession.Session{
		PrincipalID: pid,
		ExpiresAt:   s.clk.Now().Add(ttl),
		CSRFToken:   authsession.NewCSRFToken(),
		Scopes:      sessScopes,
	}

	cfg := s.sessionConfig()
	authsession.WriteSessionCookie(w, cfg, sess)

	// Persist a session row so TelemetryGate.IsEnabled can answer
	// without a principal lookup on the clientlog hot path (REQ-OPS-208).
	// The effective telemetry flag is resolved here and cached on the row.
	// defaultTelemetryEnabled is true until task #8 wires the sysconfig block.
	const defaultTelemetryEnabled = true
	sessionRow := store.SessionRow{
		SessionID:                 sess.CSRFToken,
		PrincipalID:               pid,
		CreatedAt:                 s.clk.Now(),
		ExpiresAt:                 sess.ExpiresAt,
		ClientlogTelemetryEnabled: directory.EffectiveTelemetry(p, defaultTelemetryEnabled),
	}
	if err := s.store.Meta().UpsertSession(ctx, sessionRow); err != nil {
		// Non-fatal: log at warn and continue; the cookie is already set.
		// The TelemetryGate will return ErrNotFound (treated as disabled)
		// until the row is created on the next successful login.
		s.loggerFrom(ctx).Warn("protoadmin.login.session_upsert_failed",
			"activity", observe.ActivityInternal,
			"principal_id", uint64(pid),
			"err", err)
	}

	// Attach the just-authenticated principal to the audit context so
	// the success record carries actor=principal/<id> rather than the
	// pre-auth actor=system fallback (REQ-ADM-300).
	auditCtx := context.WithValue(r.Context(), ctxKeyPrincipal, p)
	s.loggerFrom(r.Context()).InfoContext(r.Context(), "protoadmin.auth.login_success",
		"activity", observe.ActivityAudit,
		"principal_id", uint64(p.ID),
		"email", p.CanonicalEmail)
	s.appendAudit(auditCtx,
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
		PrincipalID:      uint64(p.ID),
		Email:            p.CanonicalEmail,
		Scopes:           sessScopes.Slice(),
		SessionExpiresAt: sess.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

// authMeResponse is the JSON body returned by GET /api/v1/auth/me.
// Mirrors the protologin login response shape so the Suite SPA can
// use a single type for both the login response and the page-reload
// session probe (REQ-ADM-203).
type authMeResponse struct {
	PrincipalID      uint64       `json:"principal_id"`
	Email            string       `json:"email"`
	Scopes           []auth.Scope `json:"scopes"`
	SessionExpiresAt string       `json:"session_expires_at,omitempty"`
}

// handleAuthMe handles GET /api/v1/auth/me.
//
// Returns 200 + {principal_id, email, scopes, session_expires_at} when the
// request carries a valid session cookie or Bearer API key. Returns 401 when
// no valid credential is present. The Suite SPA calls this on page load to
// determine whether an existing session is still valid and to schedule the
// client-side session-expiry timer (REQ-ADM-203).
//
// session_expires_at is populated from the session cookie when cookie auth is
// in use. Bearer-authenticated callers receive an empty string because API
// keys have no encoded expiry.
func (s *Server) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFrom(r.Context())
	if !ok {
		writeProblem(w, r, http.StatusUnauthorized,
			"unauthorized", "authentication required", "")
		return
	}
	ac := auth.FromContext(r.Context())
	var scopes []auth.Scope
	if ac != nil {
		scopes = ac.Scopes.Slice()
	}
	// Resolve session_expires_at from the cookie, if present. This re-parses
	// the cookie rather than threading ExpiresAt through context so the
	// requireAuth middleware signature stays unchanged.
	var sessionExpiresAt string
	cfg := s.sessionConfig()
	if c, err := r.Cookie(cfg.CookieName); err == nil {
		if sess, err := authsession.DecodeSession(c.Value, cfg.SigningKey, s.clk.Now()); err == nil {
			sessionExpiresAt = sess.ExpiresAt.UTC().Format(time.RFC3339)
		}
	}
	writeJSON(w, http.StatusOK, authMeResponse{
		PrincipalID:      uint64(p.ID),
		Email:            p.CanonicalEmail,
		Scopes:           scopes,
		SessionExpiresAt: sessionExpiresAt,
	})
}

// handleWhoAmI handles GET /api/v1/auth/whoami.
//
// Returns 200 + {principal_id, email, scopes} when the request carries
// valid credentials (session cookie or Bearer API key). Returns 401
// when no valid credential is present. The endpoint is protected by
// requireAuth and therefore inherits the same dual-auth path (cookie
// or Bearer) as every other authenticated endpoint.
//
// The SPA calls this on page load to determine whether an existing
// session cookie is still valid (REQ-AUTH-SESSION-REST). It is a
// read-only GET so CSRF is not required even for cookie-authenticated
// callers (REQ-AUTH-CSRF: safe methods are exempt).
func (s *Server) handleWhoAmI(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFrom(r.Context())
	if !ok {
		// requireAuth already enforces this; belt-and-suspenders.
		writeProblem(w, r, http.StatusUnauthorized,
			"unauthorized", "authentication required", "")
		return
	}
	ac := auth.FromContext(r.Context())
	var scopes []auth.Scope
	if ac != nil {
		scopes = ac.Scopes.Slice()
	}
	writeJSON(w, http.StatusOK, whoamiResponse{
		PrincipalID: uint64(p.ID),
		Email:       p.CanonicalEmail,
		Scopes:      scopes,
		Clientlog:   s.buildClientlogMeta(r),
	})
}

// buildClientlogMeta populates the clientlog block in whoamiResponse by
// reading the sessions table row for the current request. The session_id
// is the CSRF token extracted from the admin session cookie (the same key
// used by sessionIDFromRequest). When no session row exists (API key auth
// or cookie decode failure) the meta block has TelemetryEnabled=false and
// no LivetailUntil.
func (s *Server) buildClientlogMeta(r *http.Request) clientlogSessionMeta {
	sessID := s.sessionIDFromRequest(r)
	if sessID == "" {
		return clientlogSessionMeta{}
	}
	row, err := s.store.Meta().GetSession(r.Context(), sessID)
	if err != nil {
		// ErrNotFound is fine (API key auth, or session row not yet
		// created). Any other error is transient; degrade gracefully.
		return clientlogSessionMeta{}
	}
	meta := clientlogSessionMeta{
		TelemetryEnabled: row.ClientlogTelemetryEnabled,
	}
	if row.ClientlogLivetailUntil != nil && row.ClientlogLivetailUntil.After(s.clk.Now()) {
		ts := formatAdminRFC3339Millis(*row.ClientlogLivetailUntil)
		meta.LivetailUntil = &ts
	}
	return meta
}

// formatAdminRFC3339Millis formats t as RFC 3339 with millisecond
// precision for the admin REST surface, matching the JMAP surface format
// (REQ-OPS-211).
func formatAdminRFC3339Millis(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// handleLogout handles POST /api/v1/auth/logout.
//
// Clears the session and CSRF cookies by issuing expired Set-Cookie
// headers and returns 204. The endpoint accepts both cookie and Bearer
// authentication; a caller who is already logged out (no cookies, no
// Bearer) just gets 401 from requireAuth, which is consistent with the
// "nothing to do" case being a no-op.
//
// Sessions are stateless HMAC-signed cookies (REQ-AUTH-JSON-LOGOUT);
// logout invalidates the client-side cookies only. There is no
// server-side revocation list -- residual sessions on a stolen device
// expire when the cookie's TTL elapses.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	cfg := s.sessionConfig()
	authsession.ClearSessionCookies(w, cfg)
	subject := ""
	if p, ok := principalFrom(r.Context()); ok {
		subject = "principal:" + p.CanonicalEmail
		s.loggerFrom(r.Context()).InfoContext(r.Context(), "protoadmin.auth.logout",
			"activity", observe.ActivityAudit,
			"principal_id", uint64(p.ID))
	}
	s.appendAudit(r.Context(),
		"auth.logout",
		subject,
		store.OutcomeSuccess,
		"",
		nil,
	)
	w.WriteHeader(http.StatusNoContent)
}

// auditLoginFailure writes a failed-login audit record. The actor is
// always actor=system (we do not trust the supplied email to identify
// a real principal); the subject carries the attempted email so an
// operator searching the audit log for "email:alice@example.com" sees
// every attempt against that account, including pre-existence ones.
// principalID is non-zero only when the post-Authenticate steps fail
// (TOTP, disabled-account); the record's metadata carries it.
func (s *Server) auditLoginFailure(r *http.Request, attemptedEmail string, principalID directory.PrincipalID, message string) {
	meta := map[string]string{
		"remote":          remoteHost(r.RemoteAddr),
		"attempted_email": attemptedEmail,
	}
	if principalID > 0 {
		meta["principal_id"] = strconv.FormatUint(uint64(principalID), 10)
	}
	s.appendAudit(r.Context(),
		"auth.login",
		"email:"+attemptedEmail,
		store.OutcomeFailure,
		message,
		meta,
	)
}

// sessionConfig builds the authsession.SessionConfig from the server's
// Options.Session, applying defaults for empty fields so callers don't
// have to worry about them.
func (s *Server) sessionConfig() authsession.SessionConfig {
	cfg := s.opts.Session
	if cfg.CookieName == "" {
		cfg.CookieName = "herold_public_session"
	}
	if cfg.CSRFCookieName == "" {
		cfg.CSRFCookieName = "herold_public_csrf"
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

// writeLoginProblemTOTPEnrollmentRequired writes a 401 problem with both
// step_up_required and totp_enrollment_required set (REQ-AUTH-44, issue
// #12). Returned to admin principals that authenticate with a correct
// password but have not yet enrolled TOTP — the admin scope requires it.
// The enroll_url extension points the client at the enrollment endpoint,
// which is reachable via the bootstrap API key for the first-time
// superadmin path (slice 6).
func writeLoginProblemTOTPEnrollmentRequired(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":                     "about:blank",
		"title":                    "TOTP enrollment required",
		"status":                   http.StatusUnauthorized,
		"detail":                   "The admin role requires TOTP enrollment before password sign-in is permitted.",
		"step_up_required":         true,
		"totp_enrollment_required": true,
		"enroll_url":               "/api/v1/totp/enroll",
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
