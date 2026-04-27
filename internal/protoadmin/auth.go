package protoadmin

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
//  2. Session cookie (herold_admin_session by default) — issued by the
//     protoui /login flow or POST /api/v1/auth/login. Enabled only
//     when Options.Session.SigningKey is set (REQ-AUTH-SESSION-REST).
//     Mutating requests (POST/PUT/PATCH/DELETE) authenticated this way
//     MUST also present an X-CSRF-Token header whose value matches the
//     herold_admin_csrf cookie (constant-time compare, REQ-AUTH-CSRF).
//     Safe methods (GET/HEAD/OPTIONS) are exempt from CSRF.
//
// On success the auth.AuthContext is attached to the request context.
// On failure a 401 problem is written and the chain aborts.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		principal, scope, viaCookie, ok := s.authenticateWithMode(ctx, r)
		if !ok {
			writeProblem(w, r, http.StatusUnauthorized,
				"unauthorized", "authentication required", "")
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
		// (REQ-AUTH-SCOPE-02). Listener label is "admin" because
		// protoadmin REST is mounted on the admin listener
		// (REQ-OPS-ADMIN-LISTENER-01); the public-listener handler
		// chain attaches "public".
		ctx = auth.WithContext(ctx, &auth.AuthContext{
			PrincipalID: uint64(principal.ID),
			Scopes:      scope,
			Listener:    "admin",
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
		csrfCookieName = "herold_admin_csrf"
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
func (s *Server) authenticateWithMode(ctx context.Context, r *http.Request) (store.Principal, auth.ScopeSet, bool, bool) {
	h := r.Header.Get("Authorization")
	if h != "" {
		p, scope, ok := s.authenticateBearer(ctx, h)
		return p, scope, false, ok
	}
	// No Authorization header: try the admin session cookie if the
	// server was configured with a signing key (REQ-AUTH-SESSION-REST).
	if len(s.opts.Session.SigningKey) >= 32 {
		p, scope, ok := s.authenticateCookie(ctx, r)
		return p, scope, ok, ok
	}
	observe.AuthAttemptsTotal.WithLabelValues("apikey", "fail").Inc()
	return store.Principal{}, nil, false, false
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
			s.loggerFrom(ctx).Warn("protoadmin.auth.lookup_failed", "err", err)
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
func (s *Server) authenticateCookie(ctx context.Context, r *http.Request) (store.Principal, auth.ScopeSet, bool) {
	cookieName := s.opts.Session.CookieName
	if cookieName == "" {
		cookieName = "herold_admin_session"
	}
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		observe.AuthAttemptsTotal.WithLabelValues("session", "fail").Inc()
		return store.Principal{}, nil, false
	}
	sess, err := authsession.DecodeSession(c.Value, s.opts.Session.SigningKey, s.clk.Now())
	if err != nil {
		observe.AuthAttemptsTotal.WithLabelValues("session", "fail").Inc()
		return store.Principal{}, nil, false
	}
	p, err := s.store.Meta().GetPrincipalByID(ctx, sess.PrincipalID)
	if err != nil {
		s.loggerFrom(ctx).Warn("protoadmin.auth.cookie_principal_lookup_failed",
			"err", err, "principal_id", sess.PrincipalID)
		observe.AuthAttemptsTotal.WithLabelValues("session", "fail").Inc()
		return store.Principal{}, nil, false
	}
	if p.Flags.Has(store.PrincipalFlagDisabled) {
		observe.AuthAttemptsTotal.WithLabelValues("session", "fail").Inc()
		return store.Principal{}, nil, false
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
		return store.Principal{}, nil, false
	}
	observe.AuthAttemptsTotal.WithLabelValues("session", "ok").Inc()
	return p, scopes, true
}

// authenticate is kept for any internal callers that don't need the
// viaCookie discriminator. It calls authenticateWithMode and discards
// the flag. New code should call authenticateWithMode directly.
func (s *Server) authenticate(ctx context.Context, r *http.Request) (store.Principal, auth.ScopeSet, bool) {
	p, scope, _, ok := s.authenticateWithMode(ctx, r)
	return p, scope, ok
}

// parseAPIKeyScope decodes the JSON-encoded scope list stored on an
// APIKey row. Empty / malformed values fall back to the legacy admin
// scope so a bug in the storage layer can't silently drop access; the
// migration body has already backfilled every row, so the fallback is
// only triggered by fresh test fixtures.
func parseAPIKeyScope(raw string) auth.ScopeSet {
	if raw == "" {
		return auth.NewScopeSet(auth.ScopeAdmin)
	}
	var s auth.ScopeSet
	if err := json.Unmarshal([]byte(raw), &s); err != nil || len(s) == 0 {
		return auth.NewScopeSet(auth.ScopeAdmin)
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
func requireSelfOrAdmin(w http.ResponseWriter, r *http.Request, caller store.Principal, target store.PrincipalID) bool {
	if caller.ID == target || caller.Flags.Has(store.PrincipalFlagAdmin) {
		return true
	}
	writeProblem(w, r, http.StatusForbidden, "forbidden",
		"insufficient privileges", "")
	return false
}

// requireAdmin returns 403 when the caller is not an admin.
func requireAdmin(w http.ResponseWriter, r *http.Request, caller store.Principal) bool {
	if caller.Flags.Has(store.PrincipalFlagAdmin) {
		return true
	}
	writeProblem(w, r, http.StatusForbidden, "forbidden",
		"admin privileges required", "")
	return false
}

// requireScope is the auth-scope (REQ-AUTH-SCOPE-02) middleware
// counterpart to requireAuth: it asserts the auth.AuthContext attached
// to r holds every scope in scs and writes a 403 RFC 7807 problem
// detail otherwise. Callers chain it after requireAuth in routes.go.
func (s *Server) requireScope(scs ...auth.Scope) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if err := auth.RequireScope(r.Context(), scs...); err != nil {
				if errors.Is(err, auth.ErrUnauthenticated) {
					writeProblem(w, r, http.StatusUnauthorized,
						"unauthorized", "authentication required", "")
					return
				}
				writeProblem(w, r, http.StatusForbidden,
					"insufficient_scope",
					"insufficient scope for this resource",
					err.Error())
				return
			}
			next(w, r)
		}
	}
}
