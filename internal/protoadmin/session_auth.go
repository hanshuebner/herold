package protoadmin

// session_auth.go implements the JSON login / logout / whoami / step-up
// endpoints for the public REST surface (REQ-AUTH-SESSION-REST, REQ-AUTH-74).
//
// POST /api/v1/auth/login    -- accepts {email, password} only; issues
//
//	herold_public_session + herold_public_csrf cookies carrying end-user
//	scope only (REQ-AUTH-SCOPE-01, REQ-AUTH-JSON-LOGIN).  TOTP is NOT
//	evaluated at login; admin scope is never baked into the cookie.
//
// POST /api/v1/auth/step-up  -- accepts {totp_code}; verifies TOTP and
//
//	creates a server-side elevation record (REQ-AUTH-74).  Returns
//	{elevation_expires_at}.  Admin endpoints require a valid elevation.
//
// POST /api/v1/auth/logout   -- clears the cookies, returns 204.
// GET  /api/v1/auth/whoami   -- returns 200 + {principal_id, email, scopes,
//
//	session_idle_deadline, elevation_expires_at} (REQ-AUTH-75).
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
// Login accepts only email and password; TOTP is NOT evaluated here
// (REQ-AUTH-JSON-LOGIN, REQ-AUTH-42, REQ-AUTH-SCOPE-03). Admin scope is
// never baked into the issued cookie. After login, an admin principal must
// POST /api/v1/auth/step-up with their TOTP code to obtain an elevation
// record that gates admin-scoped endpoints (REQ-AUTH-74).
type loginRequest struct {
	// Email is the principal's canonical email address.
	Email string `json:"email"`
	// Password is the principal's plain-text password for verification.
	Password string `json:"password"`
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
	// SessionIdleDeadline is the rolling idle deadline for this session
	// (REQ-AUTH-75, re #77). Computed as now + Session.IdleTTL at the time
	// of the response; the server slides it forward on every authenticated
	// request. The Suite SPA schedules a proactive forced-login transition at
	// this deadline and re-arms it after each whoami/me response.
	// Omitted when IdleTTL is zero (idle gate disabled).
	SessionIdleDeadline string `json:"session_idle_deadline,omitempty"`
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
	// SessionIdleDeadline is the rolling idle deadline (REQ-AUTH-75, re #77).
	// Computed as now + Session.IdleTTL; omitted for Bearer-key callers.
	SessionIdleDeadline string `json:"session_idle_deadline,omitempty"`
	// ElevationExpiresAt is the RFC 3339 UTC expiry of the current admin
	// elevation record (REQ-AUTH-74, issue #79). Null/omitted when no
	// active elevation exists. The admin SPA uses this to show whether
	// TOTP step-up is currently active and when it expires.
	ElevationExpiresAt *string `json:"elevation_expires_at"`
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
// Session lifetime: all sessions (admin and end-user) are governed by the
// idle-only lifetime configured in Options.Session.IdleTTL (default 7 days).
// There is no separate shorter TTL for admin sessions. The session cookie's
// MaxAge is set to a 365-day fallback when TTL=0 (idle-only mode); the
// server-side idle gate is the sole expiry mechanism (REQ-AUTH-72, issue #78).
//
// TOTP is NOT evaluated at login (REQ-AUTH-JSON-LOGIN, REQ-AUTH-42): the
// issued session always carries only end-user scope (REQ-AUTH-SCOPE-01).
// Admin principals must separately POST /api/v1/auth/step-up to create an
// elevation record for admin-gated endpoints (REQ-AUTH-74, issue #79).
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

	// Issue an end-user-scoped session. The admin scope is never baked
	// into the cookie (REQ-AUTH-SCOPE-01, REQ-AUTH-SCOPE-03). Admin
	// operations require a subsequent TOTP step-up that creates a
	// server-side elevation record (REQ-AUTH-74, issue #79).
	sessScopes := auth.NewScopeSet(auth.AllEndUserScopes...)

	// Session lifetime: all sessions use the idle-only model. When
	// Session.TTL=0 (no absolute cap), ExpiresAt is set to now+365d so
	// DecodeSession never rejects the cookie on time; server-side
	// LastSeenAt + Session.IdleTTL is the sole expiry mechanism
	// (REQ-AUTH-72, REQ-AUTH-73, issue #78).
	ttl := s.opts.Session.TTL
	if ttl <= 0 {
		ttl = 365 * 24 * time.Hour
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
	// without a principal lookup on the clientlog hot path (REQ-OPS-208),
	// and to record device context (user-agent, login IP) for session
	// listing and idle-gate enforcement (REQ-AUTH-72, issue #78, issue #80).
	// The effective telemetry flag is resolved here and cached on the row.
	// defaultTelemetryEnabled is true until task #8 wires the sysconfig block.
	const defaultTelemetryEnabled = true
	sessionRow := store.SessionRow{
		SessionID:                 sess.CSRFToken,
		PrincipalID:               pid,
		CreatedAt:                 s.clk.Now(),
		ExpiresAt:                 sess.ExpiresAt,
		UserAgent:                 r.Header.Get("User-Agent"),
		LastSeenIP:                remoteHost(r.RemoteAddr),
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
		PrincipalID:         uint64(p.ID),
		Email:               p.CanonicalEmail,
		Scopes:              sessScopes.Slice(),
		SessionExpiresAt:    sess.ExpiresAt.UTC().Format(time.RFC3339),
		SessionIdleDeadline: s.sessionIdleDeadlineStr(),
	})
}

// authMeResponse is the JSON body returned by GET /api/v1/auth/me.
// Mirrors the protologin login response shape so the Suite SPA can
// use a single type for both the login response and the page-reload
// session probe (REQ-ADM-203).
type authMeResponse struct {
	PrincipalID         uint64       `json:"principal_id"`
	Email               string       `json:"email"`
	Scopes              []auth.Scope `json:"scopes"`
	SessionExpiresAt    string       `json:"session_expires_at,omitempty"`
	SessionIdleDeadline string       `json:"session_idle_deadline,omitempty"`
	// ElevationExpiresAt is the RFC 3339 UTC expiry of the current admin
	// elevation record (REQ-AUTH-74, issue #79). Null/omitted when no
	// active elevation exists.
	ElevationExpiresAt *string `json:"elevation_expires_at"`
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
		PrincipalID:         uint64(p.ID),
		Email:               p.CanonicalEmail,
		Scopes:              scopes,
		SessionExpiresAt:    sessionExpiresAt,
		SessionIdleDeadline: s.sessionIdleDeadlineStr(),
		ElevationExpiresAt:  s.activeElevationExpiry(r),
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
		PrincipalID:         uint64(p.ID),
		Email:               p.CanonicalEmail,
		Scopes:              scopes,
		SessionIdleDeadline: s.sessionIdleDeadlineStr(),
		ElevationExpiresAt:  s.activeElevationExpiry(r),
		Clientlog:           s.buildClientlogMeta(r),
	})
}

// stepUpRequest is the JSON body for POST /api/v1/auth/step-up.
type stepUpRequest struct {
	TOTPCode string `json:"totp_code"`
}

// stepUpResponse is the JSON body returned on a successful step-up.
type stepUpResponse struct {
	// ElevationExpiresAt is the RFC 3339 UTC expiry of the new elevation
	// record. The admin SPA displays this and schedules a re-step-up prompt
	// before it expires (REQ-AUTH-74, issue #79).
	ElevationExpiresAt string `json:"elevation_expires_at"`
}

// handleStepUp handles POST /api/v1/auth/step-up (REQ-AUTH-74, issue #79).
//
// The caller must be authenticated (requireAuth) and CSRF-checked. It
// supplies a TOTP code; on success a server-side elevation record is created
// (or refreshed) for the session, expiring at now + Options.ElevationTTL.
// Admin-gated endpoints then check GetActiveElevation to authorise the call.
//
// Error responses:
//   - 400  principal has no TOTP enrolled:  {enroll_required: true}
//   - 401  TOTP code invalid or rate-limited: RFC 7807 "unauthorized"
//   - 403  principal does not have the admin flag (elevation is only
//     meaningful for admin principals)
func (s *Server) handleStepUp(w http.ResponseWriter, r *http.Request) {
	sessID := s.sessionIDFromRequest(r)
	if sessID == "" {
		// Bearer-key callers have no persistent session and therefore no
		// elevation record. Step-up is only meaningful for cookie sessions.
		writeProblem(w, r, http.StatusBadRequest,
			"bad_request", "step-up requires a cookie session", "")
		return
	}

	p, ok := principalFrom(r.Context())
	if !ok {
		writeProblem(w, r, http.StatusUnauthorized,
			"unauthorized", "authentication required", "")
		return
	}
	// Only admin principals can elevate — non-admin step-up is a no-op
	// that would never be consumed.
	if !p.Flags.Has(store.PrincipalFlagAdmin) {
		writeProblem(w, r, http.StatusForbidden,
			"forbidden", "admin privileges required for step-up", "")
		return
	}

	// TOTP must be enrolled before step-up can be granted (REQ-AUTH-44).
	// Return enroll_required so the SPA can redirect to TOTP setup.
	if !p.Flags.Has(store.PrincipalFlagTOTPEnabled) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"type":            "about:blank",
			"title":           "TOTP enrollment required",
			"status":          http.StatusBadRequest,
			"detail":          "TOTP must be enrolled before step-up elevation can be granted.",
			"enroll_required": true,
			"enroll_url":      "/api/v1/principals/" + strconv.FormatUint(uint64(p.ID), 10) + "/totp/enroll",
		})
		return
	}

	var req stepUpRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.TOTPCode == "" {
		writeProblem(w, r, http.StatusBadRequest,
			"bad_request", "request body must be JSON {totp_code}", "")
		return
	}

	// Rate-limit step-up attempts per session to prevent brute-force
	// against the 6-digit TOTP window (directory also rate-limits per
	// principal via its own bucket).
	ipKey := "stepup-ip:" + remoteHost(r.RemoteAddr)
	sessKey := "stepup-sess:" + sessID
	if !s.checkRateLimit(w, r, ipKey) {
		return
	}
	if !s.checkRateLimit(w, r, sessKey) {
		return
	}

	ctx := r.Context()
	if err := s.dir.VerifyTOTP(ctx, p.ID, req.TOTPCode); err != nil {
		if errors.Is(err, directory.ErrRateLimited) {
			s.loggerFrom(ctx).WarnContext(ctx, "protoadmin.auth.stepup_rate_limited",
				"activity", observe.ActivityAudit,
				"principal_id", uint64(p.ID))
			s.appendAudit(ctx, "auth.step_up", "principal:"+p.CanonicalEmail,
				store.OutcomeFailure, "totp rate-limited",
				map[string]string{"remote": remoteHost(r.RemoteAddr)})
			writeProblem(w, r, http.StatusTooManyRequests,
				"rate_limited", "too many TOTP attempts; please wait", "")
			return
		}
		s.loggerFrom(ctx).WarnContext(ctx, "protoadmin.auth.stepup_invalid",
			"activity", observe.ActivityAudit,
			"principal_id", uint64(p.ID))
		s.appendAudit(ctx, "auth.step_up", "principal:"+p.CanonicalEmail,
			store.OutcomeFailure, "totp code invalid",
			map[string]string{"remote": remoteHost(r.RemoteAddr)})
		writeProblem(w, r, http.StatusUnauthorized,
			"unauthorized", "TOTP code is invalid or expired", "")
		return
	}

	ttl := s.opts.ElevationTTL
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	now := s.clk.Now()
	expiresAt := now.Add(ttl)
	elev := store.ElevationRow{
		SessionID:   sessID,
		PrincipalID: p.ID,
		ElevatedAt:  now,
		ExpiresAt:   expiresAt,
	}
	if err := s.store.Meta().UpsertElevation(ctx, elev); err != nil {
		s.loggerFrom(ctx).Error("protoadmin.auth.stepup_upsert_failed",
			"activity", observe.ActivityInternal,
			"err", err, "principal_id", uint64(p.ID))
		writeProblem(w, r, http.StatusInternalServerError,
			"internal_error", "failed to create elevation record", "")
		return
	}

	s.loggerFrom(ctx).InfoContext(ctx, "protoadmin.auth.stepup_success",
		"activity", observe.ActivityAudit,
		"principal_id", uint64(p.ID),
		"expires_at", expiresAt.UTC().Format(time.RFC3339))
	s.appendAudit(ctx, "auth.step_up", "principal:"+p.CanonicalEmail,
		store.OutcomeSuccess, "",
		map[string]string{
			"remote":     remoteHost(r.RemoteAddr),
			"expires_at": expiresAt.UTC().Format(time.RFC3339),
		})

	writeJSON(w, http.StatusOK, stepUpResponse{
		ElevationExpiresAt: expiresAt.UTC().Format(time.RFC3339),
	})
}

// activeElevationExpiry returns the elevation_expires_at string for the
// current session, or nil when there is no active elevation. Used to
// populate whoami and me responses (REQ-AUTH-74, issue #79).
func (s *Server) activeElevationExpiry(r *http.Request) *string {
	sessID := s.sessionIDFromRequest(r)
	if sessID == "" {
		return nil
	}
	elev, err := s.store.Meta().GetActiveElevation(r.Context(), sessID, s.clk.Now().UnixMicro())
	if err != nil {
		return nil
	}
	ts := elev.ExpiresAt.UTC().Format(time.RFC3339)
	return &ts
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

// sessionIdleDeadlineStr returns the current rolling idle deadline as an
// RFC 3339 UTC string (REQ-AUTH-75, re #77). The deadline is always
// computed as now + Session.IdleTTL because authenticateCookie has
// already touched the session row with the current time before any
// handler runs. Returns "" when IdleTTL is zero (idle gate disabled).
func (s *Server) sessionIdleDeadlineStr() string {
	if s.opts.Session.IdleTTL <= 0 {
		return ""
	}
	return s.clk.Now().Add(s.opts.Session.IdleTTL).UTC().Format(time.RFC3339)
}

// handleLogout handles POST /api/v1/auth/logout.
//
// Clears the session and CSRF cookies by issuing expired Set-Cookie
// headers, deletes the server-side session row so the idle gate cannot
// be tripped by a replayed cookie, and returns 204.
//
// The endpoint accepts both cookie and Bearer authentication; a caller
// who is already logged out (no cookies, no Bearer) gets 401 from
// requireAuth, which is consistent with "nothing to do" being a no-op.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	cfg := s.sessionConfig()
	// Delete the server-side session row so the idle gate rejects any
	// future request carrying the same cookie (REQ-AUTH-72, issue #78).
	// Best-effort: a missing row (already evicted or API-key-only caller)
	// is not an error.
	if sessID := s.sessionIDFromRequest(r); sessID != "" {
		_ = s.store.Meta().DeleteSession(r.Context(), sessID)
	}
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
