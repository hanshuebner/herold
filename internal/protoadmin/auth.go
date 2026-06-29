package protoadmin

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/hanshuebner/herold/internal/auth"
	"github.com/hanshuebner/herold/internal/authsession"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// APIKeyPrefix is the three-character string that precedes every
// protoadmin API key. Serving as a lexical marker it lets operators
// spot leaked keys in log files and distinguishes API keys from future
// session tokens that will use a different prefix.
const APIKeyPrefix = "hk_"

// HashAPIKey returns the lowercase hex SHA-256 of the plaintext key.
// Stored APIKey.Hash values use this encoding so API key lookup can run
// a hex equality check without decoding.
func HashAPIKey(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// requireAuth is middleware that enforces authentication for all
// /api/v1/... routes. It accepts two credential forms:
//
//  1. Authorization: Bearer hk_... — protoadmin API key. The Bearer
//     form is exempt from CSRF checks because it carries no ambient
//     browser credential (REQ-AUTH-CSRF).
//  2. Session cookie (herold_public_session by default) — issued by
//     POST /api/v1/auth/login. Enabled only when Options.Session.SigningKey
//     is set (REQ-AUTH-SESSION-REST). Mutating requests (POST/PUT/PATCH/
//     DELETE) authenticated this way MUST also present an X-CSRF-Token
//     header whose value matches the herold_public_csrf cookie
//     (constant-time compare, REQ-AUTH-CSRF). Safe methods (GET/HEAD/
//     OPTIONS) are exempt from CSRF.
//
// On success the auth.AuthContext is attached to the request context.
// On failure a 401 problem is written and the chain aborts.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		principal, scope, viaCookie, typeSlug, ok := s.authenticateWithMode(ctx, r)
		if !ok {
			// Use the typed slug from cookie-auth failure when available
			// (e.g. "session_expired" per REQ-AUTH-76); fall back to the
			// generic "unauthorized" slug for all other failures.
			if typeSlug == "" {
				typeSlug = "unauthorized"
			}
			writeProblem(w, r, http.StatusUnauthorized,
				typeSlug, "authentication required", "")
			return
		}
		// CSRF gate: cookie-authenticated mutating requests must carry
		// X-CSRF-Token matching the CSRF cookie (REQ-AUTH-CSRF).
		// Bearer-authenticated requests are exempt (no ambient credential).
		if viaCookie && isMutatingMethod(r.Method) {
			if !s.validateCSRF(w, r) {
				return
			}
		}
		if !s.checkRateLimit(w, r, authCacheKey(r, principal)) {
			return
		}
		ctx = context.WithValue(ctx, ctxKeyPrincipal, principal)
		ctx = context.WithValue(ctx, ctxKeyRemoteAddr, r.RemoteAddr)
		// Attach the closed-enum scope set so downstream handlers'
		// auth.RequireScope checks see what the credential granted
		// (REQ-AUTH-SCOPE-02). The listener label is read from the
		// ctxKeyListener context value stamped by withListenerTag (or
		// by the outer WithListenerTag wrapper when the handler is
		// mounted on the public listener). This allows protoadmin to
		// serve correctly on both the retired admin listener and the
		// current public listener (re #58).
		listenerTag := "public"
		if tag, ok := ctx.Value(ctxKeyListener).(string); ok && tag != "" {
			listenerTag = tag
		}
		ctx = auth.WithContext(ctx, &auth.AuthContext{
			PrincipalID: uint64(principal.ID),
			Scopes:      scope,
			Listener:    listenerTag,
		})
		next(w, r.WithContext(ctx))
	}
}

// isMutatingMethod reports whether the HTTP method has side effects and
// therefore requires CSRF protection when the request is cookie-authenticated.
// GET/HEAD/OPTIONS are safe per RFC 7231 §4.2.1.
func isMutatingMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}

// validateCSRF compares the X-CSRF-Token request header against the
// CSRF cookie value using constant-time comparison (REQ-AUTH-CSRF).
// On mismatch it writes a 403 RFC 7807 problem and returns false.
func (s *Server) validateCSRF(w http.ResponseWriter, r *http.Request) bool {
	csrfHeader := r.Header.Get("X-CSRF-Token")
	if csrfHeader == "" {
		writeProblem(w, r, http.StatusForbidden,
			"csrf_required",
			"X-CSRF-Token header required for cookie-authenticated mutating requests",
			"")
		return false
	}
	csrfCookieName := s.opts.Session.CSRFCookieName
	if csrfCookieName == "" {
		csrfCookieName = "herold_public_csrf"
	}
	c, err := r.Cookie(csrfCookieName)
	if err != nil || c.Value == "" {
		writeProblem(w, r, http.StatusForbidden,
			"csrf_required",
			"CSRF cookie missing; re-authenticate to obtain a new CSRF token",
			"")
		return false
	}
	if subtle.ConstantTimeCompare([]byte(c.Value), []byte(csrfHeader)) != 1 {
		writeProblem(w, r, http.StatusForbidden,
			"csrf_mismatch",
			"X-CSRF-Token does not match CSRF cookie",
			"")
		return false
	}
	return true
}

// authenticateWithMode inspects the request and returns the principal
// plus a bool indicating whether authentication succeeded via session
// cookie (true) or Bearer API key (false). The viaCookie flag drives
// the CSRF gate in requireAuth.
//
// Priority: Bearer hk_... > session cookie. This matches the protojmap
// pattern (Bearer / Basic win over cookie when both are present).
// Bearer-authenticated requests are NOT subject to CSRF (no ambient
// credential, REQ-AUTH-CSRF).
//
// Cookie-based auth is enabled only when Options.Session.SigningKey is
// set and at least 32 bytes long (REQ-AUTH-SESSION-REST). When the key
// is absent all cookie-bearing requests fall through to 401.
//
// typeSlug is the RFC 7807 type slug to use when ok=false. It is only
// meaningful on the failure path; success always yields "". An empty
// typeSlug means the caller should fall back to "unauthorized".
func (s *Server) authenticateWithMode(ctx context.Context, r *http.Request) (store.Principal, auth.ScopeSet, bool, string, bool) {
	h := r.Header.Get("Authorization")
	if h != "" {
		p, scope, ok := s.authenticateBearer(ctx, h)
		return p, scope, false, "", ok
	}
	// No Authorization header: try the admin session cookie if the
	// server was configured with a signing key (REQ-AUTH-SESSION-REST).
	if len(s.opts.Session.SigningKey) >= 32 {
		p, scope, slug, ok := s.authenticateCookie(ctx, r)
		return p, scope, ok, slug, ok
	}
	observe.AuthAttemptsTotal.WithLabelValues("apikey", "fail").Inc()
	return store.Principal{}, nil, false, "", false
}

// authenticateBearer validates an Authorization header value that starts
// with "Bearer ". Only hk_... tokens are accepted; anything else is an
// immediate fail so a wrong-prefix bearer is a definitive rejection.
func (s *Server) authenticateBearer(ctx context.Context, h string) (store.Principal, auth.ScopeSet, bool) {
	const bearer = "Bearer "
	if !strings.HasPrefix(h, bearer) {
		observe.AuthAttemptsTotal.WithLabelValues("apikey", "fail").Inc()
		return store.Principal{}, nil, false
	}
	token := strings.TrimSpace(h[len(bearer):])
	if !strings.HasPrefix(token, APIKeyPrefix) {
		observe.AuthAttemptsTotal.WithLabelValues("apikey", "fail").Inc()
		return store.Principal{}, nil, false
	}
	hashed := HashAPIKey(token)
	key, err := s.apikeyLookup(ctx, hashed)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.loggerFrom(ctx).Warn("protoadmin.auth.lookup_failed",
				"activity", observe.ActivityAudit, "err", err)
		}
		observe.AuthAttemptsTotal.WithLabelValues("apikey", "fail").Inc()
		return store.Principal{}, nil, false
	}
	// Constant-time comparison against the stored hash to avoid a
	// hypothetical timing channel in a backend that returns keys by
	// prefix match rather than exact match. The default backend uses
	// SQL "WHERE hash = ?" so the check is redundant; we keep it for
	// defence-in-depth against future lookups that loosen that.
	if subtle.ConstantTimeCompare([]byte(key.Hash), []byte(hashed)) != 1 {
		observe.AuthAttemptsTotal.WithLabelValues("apikey", "fail").Inc()
		return store.Principal{}, nil, false
	}
	p, err := s.store.Meta().GetPrincipalByID(ctx, key.PrincipalID)
	if err != nil {
		s.loggerFrom(ctx).Warn("protoadmin.auth.principal_lookup_failed",
			"activity", observe.ActivityAudit,
			"err", err, "principal_id", key.PrincipalID)
		observe.AuthAttemptsTotal.WithLabelValues("apikey", "fail").Inc()
		return store.Principal{}, nil, false
	}
	if p.Flags.Has(store.PrincipalFlagDisabled) {
		observe.AuthAttemptsTotal.WithLabelValues("apikey", "fail").Inc()
		return store.Principal{}, nil, false
	}
	_ = s.store.Meta().TouchAPIKey(ctx, key.ID, s.clk.Now())
	observe.AuthAttemptsTotal.WithLabelValues("apikey", "ok").Inc()
	return p, parseAPIKeyScope(key.ScopeJSON), true
}

// authenticateCookie validates the admin session cookie on r. It uses
// the signing key from Options.Session to verify the HMAC-signed cookie
// value and then looks up the principal in the store. The scope set is
// decoded from the cookie envelope (REQ-AUTH-SCOPE-01). Disabled
// principals are rejected.
//
// The returned typeSlug is non-empty only on the failure path and
// indicates the RFC 7807 type to use in the 401 response (REQ-AUTH-76).
// Currently "session_expired" is emitted when the idle gate trips. All
// other failures yield "" (caller uses the generic "unauthorized" slug).
func (s *Server) authenticateCookie(ctx context.Context, r *http.Request) (store.Principal, auth.ScopeSet, string, bool) {
	cookieName := s.opts.Session.CookieName
	if cookieName == "" {
		cookieName = "herold_public_session"
	}
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		observe.AuthAttemptsTotal.WithLabelValues("session", "fail").Inc()
		return store.Principal{}, nil, "", false
	}
	sess, err := authsession.DecodeSession(c.Value, s.opts.Session.SigningKey, s.clk.Now())
	if err != nil {
		observe.AuthAttemptsTotal.WithLabelValues("session", "fail").Inc()
		return store.Principal{}, nil, "", false
	}
	p, err := s.store.Meta().GetPrincipalByID(ctx, sess.PrincipalID)
	if err != nil {
		s.loggerFrom(ctx).Warn("protoadmin.auth.cookie_principal_lookup_failed",
			"activity", observe.ActivityAudit,
			"err", err, "principal_id", sess.PrincipalID)
		observe.AuthAttemptsTotal.WithLabelValues("session", "fail").Inc()
		return store.Principal{}, nil, "", false
	}
	if p.Flags.Has(store.PrincipalFlagDisabled) {
		observe.AuthAttemptsTotal.WithLabelValues("session", "fail").Inc()
		return store.Principal{}, nil, "", false
	}
	scopes := sess.Scopes
	if len(scopes) == 0 {
		// A scope-less cookie shouldn't reach here -- the JSON login
		// flow at /api/v1/auth/login stamps the scope set explicitly,
		// and the HTML /login flow on the admin listener is retired
		// (Phase 3b of the merge plan; the admin listener now 308-
		// redirects /ui/* to /admin/*). An empty scope is therefore
		// either a forged cookie (HMAC must have been broken) or a
		// genuine pre-3b artefact that should also be rejected so a
		// crafted empty-scope cookie cannot escalate. Reject.
		observe.AuthAttemptsTotal.WithLabelValues("session", "fail").Inc()
		return store.Principal{}, nil, "", false
	}
	// Idle-timeout gate (REQ-AUTH-72, REQ-AUTH-73, issue #78). All sessions
	// — admin and end-user alike — use Session.IdleTTL. Zero disables the
	// gate; production wiring sets IdleTTL=7d via sysconfig.SessionTTL.
	if s.opts.Session.IdleTTL > 0 {
		row, err := s.store.Meta().GetSession(ctx, sess.CSRFToken)
		if err != nil {
			// Row missing => session was deleted (logout) or evicted.
			observe.AuthAttemptsTotal.WithLabelValues("session", "fail").Inc()
			return store.Principal{}, nil, "", false
		}
		// Tombstone check (REQ-AUTH-76, REQ-AUTH-77, issue #80): a revoked
		// session has revoked_at_us set. Reject immediately with a typed slug
		// so the client can display "signed out from another device".
		if row.Tombstoned {
			observe.AuthAttemptsTotal.WithLabelValues("session", "fail").Inc()
			return store.Principal{}, nil, "session_revoked", false
		}
		now := s.clk.Now()
		if !row.LastSeenAt.IsZero() && now.Sub(row.LastSeenAt) > s.opts.Session.IdleTTL {
			// Idle gate trips: drop the row so the same cookie cannot
			// resurrect the session on a later request. Return the
			// typed "session_expired" slug so requireAuth emits an RFC
			// 7807 body that the client recognises (REQ-AUTH-76).
			_ = s.store.Meta().DeleteSession(ctx, sess.CSRFToken)
			observe.AuthAttemptsTotal.WithLabelValues("session", "fail").Inc()
			return store.Principal{}, nil, "session_expired", false
		}
		// Best-effort touch; failures are logged but don't reject the
		// in-flight request.
		if err := s.store.Meta().UpdateSessionLastSeen(ctx, sess.CSRFToken, now.UnixMicro(), remoteHost(r.RemoteAddr)); err != nil {
			s.loggerFrom(ctx).Warn("protoadmin.auth.session_touch_failed",
				"activity", observe.ActivityInternal,
				"err", err)
		}
	}
	observe.AuthAttemptsTotal.WithLabelValues("session", "ok").Inc()
	return p, scopes, "", true
}

// parseAPIKeyScope decodes the JSON-encoded scope list stored on an
// APIKey row.
//
// Fallback rules (security-critical — admin surface now faces the internet):
//   - raw == "":   legacy unset column → ScopeAdmin (pre-scope-column rows;
//     the migration has already backfilled all rows so this only fires
//     for test fixtures that predate the column).
//   - malformed JSON: degrade to ScopeMailSend (least-privilege) rather
//     than granting admin scope — a storage bug must not escalate
//     privileges (B-3).
//   - raw == "[]" (empty array): degrade to ScopeMailSend — an explicitly
//     empty scope set stored in the DB must not escalate to admin scope
//     (B-3 privilege-escalation guard).
func parseAPIKeyScope(raw string) auth.ScopeSet {
	if raw == "" {
		// Legacy: column absent before the scope migration; treat as admin.
		return auth.NewScopeSet(auth.ScopeAdmin)
	}
	var s auth.ScopeSet
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		// Malformed JSON: least-privilege fallback.
		return auth.NewScopeSet(auth.ScopeMailSend)
	}
	if len(s) == 0 {
		// Empty array "[]": least-privilege fallback. resolveAPIKeyScope
		// never produces an empty set (it defaults to [mail.send]), so an
		// empty stored value indicates a migration anomaly, not intent.
		return auth.NewScopeSet(auth.ScopeMailSend)
	}
	return s
}

// authCacheKey returns the rate-limit bucket key for an authenticated
// request. We use the principal ID (not the API key ID) so a principal
// cannot side-step the limit by rotating keys mid-attack.
func authCacheKey(r *http.Request, p store.Principal) string {
	return fmt.Sprintf("principal:%d", p.ID)
}

// checkRateLimit enforces the per-principal sliding window. On breach
// it writes a 429 with Retry-After and returns false so the handler
// aborts; on success it returns true.
func (s *Server) checkRateLimit(w http.ResponseWriter, r *http.Request, key string) bool {
	ok, retry := s.rl.allow(key)
	if ok {
		return true
	}
	observe.AdminRateLimitedTotal.WithLabelValues("api-key").Inc()
	w.Header().Set("Retry-After", fmt.Sprintf("%d", int(retry.Seconds())))
	writeProblem(w, r, http.StatusTooManyRequests,
		"rate_limited", "rate limit exceeded", fmt.Sprintf("retry after %s", retry))
	return false
}

// requireSelfOrAdmin returns 403 when the caller is neither the target
// principal (self-scope) nor an admin. Read-only endpoints that are
// public (e.g. listing the caller's own keys) must not use this gate.
// Permission denials are logged activity=audit at warn (REQ-OPS-86).
func requireSelfOrAdmin(w http.ResponseWriter, r *http.Request, caller store.Principal, target store.PrincipalID) bool {
	if caller.ID == target || caller.Flags.Has(store.PrincipalFlagAdmin) {
		return true
	}
	slog.WarnContext(r.Context(), "protoadmin.permission_denied",
		"activity", observe.ActivityAudit,
		"actor_id", caller.ID,
		"target_id", target,
		"method", r.Method,
		"path", r.URL.Path)
	writeProblem(w, r, http.StatusForbidden, "forbidden",
		"insufficient privileges", "")
	return false
}

// requireSelfOnly returns 403 when the caller is not the target principal.
// Unlike requireSelfOrAdmin this helper returns 403 even for admin-scoped
// callers: submission credentials on a foreign identity cannot be read or
// written by admins in v1 (REQ-AUTH-EXT-SUBMIT-04 last bullet; no
// impersonation in v1). Apply to all submission endpoints.
// Permission denials are logged activity=audit at warn (REQ-OPS-86).
func requireSelfOnly(w http.ResponseWriter, r *http.Request, caller store.Principal, target store.PrincipalID) bool {
	if caller.ID == target {
		return true
	}
	slog.WarnContext(r.Context(), "protoadmin.permission_denied",
		"activity", observe.ActivityAudit,
		"actor_id", caller.ID,
		"target_id", target,
		"method", r.Method,
		"path", r.URL.Path,
		"reason", "self-only surface; admin impersonation disallowed")
	writeProblem(w, r, http.StatusForbidden, "forbidden",
		"submission credentials may only be accessed by the owning principal", "")
	return false
}

// requireAdmin returns 403 when the caller does not have the
// PrincipalFlagAdmin DB flag. Permission denials are logged
// activity=audit at warn (REQ-OPS-86).
//
// Defence-in-depth: some handlers (handleServerStatus,
// handleServerConfigCheck) call both authAdmin (requireElevation) in
// routes.go AND requireAdmin here. The two gates are independent:
// requireElevation checks the scope embedded in the API-key credential or
// the elevation record in session_elevations; requireAdmin checks the live DB
// flag at request time. Together they ensure that revoking the admin flag from
// a principal also revokes access even when an existing long-lived API key
// carrying ScopeAdmin is still in circulation.
func requireAdmin(w http.ResponseWriter, r *http.Request, caller store.Principal) bool {
	if caller.Flags.Has(store.PrincipalFlagAdmin) {
		return true
	}
	slog.WarnContext(r.Context(), "protoadmin.permission_denied",
		"activity", observe.ActivityAudit,
		"actor_id", caller.ID,
		"method", r.Method,
		"path", r.URL.Path)
	writeProblem(w, r, http.StatusForbidden, "forbidden",
		"admin privileges required", "")
	return false
}

// requireSelfServiceElevation gates a sensitive self-service operation behind
// TOTP step-up when the caller has TOTP enrolled (REQ-AUTH-78, issue #79).
//
// Bearer API-key callers (Authorization header present) are exempt entirely:
// API keys are long-lived credentials managed outside the browser TOTP flow,
// and TOTP step-up is a session-interactive mechanism.
//
// When TOTP is not enrolled the gate returns true unconditionally. There is
// no enroll_required path on the self-service gate; that requirement is
// admin-only.
//
// When TOTP is enrolled and no active elevation record exists for the current
// session, the method writes 403 with {"step_up_required":true,
// "elevation_scope":"self-service"} and returns false. The handler must
// return immediately in that case.
//
// A single elevation record (created by POST /api/v1/auth/step-up) satisfies
// both the admin gate (requireElevation) and this self-service gate.
func (s *Server) requireSelfServiceElevation(w http.ResponseWriter, r *http.Request, caller store.Principal) bool {
	// Bearer callers have no persistent session and are exempt.
	if r.Header.Get("Authorization") != "" {
		return true
	}
	// If TOTP is not enrolled, no elevation is needed for self-service ops.
	if !caller.Flags.Has(store.PrincipalFlagTOTPEnabled) {
		return true
	}
	// TOTP is enrolled: require a live elevation record for the session.
	sessID := s.sessionIDFromRequest(r)
	if sessID == "" {
		// No session ID after requireAuth is a defensive edge case (e.g.
		// no session signing key in test). Treat as unauthenticated.
		writeProblem(w, r, http.StatusUnauthorized,
			"unauthorized", "authentication required", "")
		return false
	}
	_, err := s.store.Meta().GetActiveElevation(r.Context(), sessID, s.clk.Now().UnixMicro())
	if err != nil {
		s.loggerFrom(r.Context()).WarnContext(r.Context(), "protoadmin.self_service_elevation_required",
			"activity", observe.ActivityAudit,
			"actor_id", caller.ID,
			"method", r.Method,
			"path", r.URL.Path)
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"type":             "about:blank",
			"title":            "Step-up elevation required",
			"status":           http.StatusForbidden,
			"detail":           "This operation requires a current TOTP step-up. POST /api/v1/auth/step-up with your TOTP code.",
			"step_up_required": true,
			"elevation_scope":  "self-service",
			"step_up_url":      "/api/v1/auth/step-up",
		})
		return false
	}
	return true
}

// requireElevation is the admin-route guard for the step-up elevation
// model (REQ-AUTH-SCOPE-02, REQ-AUTH-74, issue #79).
//
// Two caller modes:
//
//  1. Bearer API-key callers (Authorization header present): must carry
//     ScopeAdmin in the key's scope_json. A key with any other scope set
//     receives 403 insufficient_scope. Elevation records are not consulted
//     because API-key callers never go through the browser TOTP flow.
//
//  2. Cookie-authenticated callers: must have PrincipalFlagAdmin in the DB
//     AND a valid unexpired elevation record in session_elevations. Missing
//     or expired elevation → 403 step_up_required so the SPA can redirect
//     the user to POST /api/v1/auth/step-up.
//
// When the principal is not an admin the response is always 403 "forbidden".
func (s *Server) requireElevation(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lg := s.loggerFrom(r.Context())
		caller, ok := principalFrom(r.Context())
		if !ok {
			writeProblem(w, r, http.StatusUnauthorized,
				"unauthorized", "authentication required", "")
			return
		}
		if !caller.Flags.Has(store.PrincipalFlagAdmin) {
			lg.WarnContext(r.Context(), "protoadmin.elevation_denied",
				"activity", observe.ActivityAudit,
				"actor_id", caller.ID,
				"method", r.Method,
				"path", r.URL.Path,
				"reason", "not an admin principal")
			writeProblem(w, r, http.StatusForbidden,
				"forbidden", "admin privileges required", "")
			return
		}

		// Distinguish Bearer callers (Authorization header) from cookie callers.
		if r.Header.Get("Authorization") != "" {
			// API-key caller: gate on ScopeAdmin in the credential.
			// A principal that is admin in the DB but used a limited-scope
			// key (e.g. mail.send) is still rejected — the scope is the
			// effective grant, not the DB flag.
			ac := auth.FromContext(r.Context())
			if ac != nil && ac.Scopes.Has(auth.ScopeAdmin) {
				// Bearer admin with ScopeAdmin: bypass elevation check.
				next(w, r)
				return
			}
			// Bearer caller without ScopeAdmin: 403 insufficient_scope
			// (REQ-AUTH-SCOPE-02, REQ-OPS-86).
			lg.WarnContext(r.Context(), "protoadmin.scope_denied",
				"activity", observe.ActivityAudit,
				"actor_id", caller.ID,
				"method", r.Method,
				"path", r.URL.Path,
				"reason", "Bearer caller lacks ScopeAdmin")
			writeProblem(w, r, http.StatusForbidden,
				"insufficient_scope", "admin scope required", "")
			return
		}

		// Cookie-authenticated callers must have a live elevation record.
		sessID := s.sessionIDFromRequest(r)
		if sessID == "" {
			// requireAuth already validated the cookie; a missing sessID here
			// means the signing key is unset (test fixture without a session
			// key). Defensive: treat as unauthenticated.
			writeProblem(w, r, http.StatusUnauthorized,
				"unauthorized", "authentication required", "")
			return
		}
		_, err := s.store.Meta().GetActiveElevation(r.Context(), sessID, s.clk.Now().UnixMicro())
		if err != nil {
			// No active elevation: respond with the typed step_up_required body.
			lg.WarnContext(r.Context(), "protoadmin.elevation_required",
				"activity", observe.ActivityAudit,
				"actor_id", caller.ID,
				"method", r.Method,
				"path", r.URL.Path)
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"type":             "about:blank",
				"title":            "Step-up elevation required",
				"status":           http.StatusForbidden,
				"detail":           "Admin operations require a current TOTP step-up. POST /api/v1/auth/step-up with your TOTP code.",
				"step_up_required": true,
				"elevation_scope":  "admin",
				"step_up_url":      "/api/v1/auth/step-up",
			})
			return
		}

		next(w, r)
	}
}
