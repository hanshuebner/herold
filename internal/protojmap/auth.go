package protojmap

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/hanshuebner/herold/internal/auth"
	"github.com/hanshuebner/herold/internal/authsession"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
)

// APIKeyPrefix is the bearer-token prefix. Same scheme as protoadmin
// so an operator-issued admin API key authenticates JMAP too.
const APIKeyPrefix = "hk_"

// hashAPIKey returns the lowercase hex SHA-256 of the plaintext key.
// Mirrors protoadmin.HashAPIKey; redeclared here to keep protojmap
// independent of protoadmin's exported surface.
func hashAPIKey(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// ctxKey is a private type to namespace context keys so we never
// collide with other packages stuffing values into the same context.
type ctxKey int

const (
	ctxKeyPrincipal ctxKey = iota + 1
	ctxKeyRemoteAddr
	ctxKeyRequestID
	ctxKeyAPIKey
	ctxKeyLogger
	// ctxKeySessionID holds the CSRF token from the suite-session cookie,
	// which doubles as the sessions table primary key. Populated only when
	// the request is cookie-authenticated via the SessionResolver. Bearer-
	// and Basic-authenticated requests leave it absent.
	ctxKeySessionID
)

// PrincipalFromContext returns the authenticated principal attached to
// ctx, or zero-value Principal + false. Method handlers consume this
// to scope their reads/writes.
func PrincipalFromContext(ctx context.Context) (store.Principal, bool) {
	if v, ok := ctx.Value(ctxKeyPrincipal).(store.Principal); ok {
		return v, true
	}
	return store.Principal{}, false
}

// APIKeyFromContext returns the API key attached to ctx by Bearer
// authentication, or zero-value APIKey + false when the session was
// authenticated via Basic or when no auth context is present.
func APIKeyFromContext(ctx context.Context) (store.APIKey, bool) {
	if v, ok := ctx.Value(ctxKeyAPIKey).(store.APIKey); ok {
		return v, true
	}
	return store.APIKey{}, false
}

// SessionIDFromContext returns the session_id (CSRF token from the
// suite-session cookie) stashed by requireAuth, or "" when the request
// was authenticated via Bearer / Basic or when no session cookie was
// present. Callers use this to look up the sessions table row.
func SessionIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeySessionID).(string); ok {
		return v
	}
	return ""
}

// requireAuth is middleware that enforces authentication. It supports
// two schemes:
//
//  1. Bearer hk_... — the protoadmin API-key form. Hashed and looked
//     up via the API-key store.
//  2. Basic base64(user:pass) — username + password (the directory
//     subsystem's Authenticate). RFC 8620 §3.1 leaves auth scheme
//     selection to deployments; we accept both so JMAP clients that
//     only speak Basic (Thunderbird's autoconfig flow, k-9 mail) work
//     against the same surface as power users with API keys.
//
// On success the principal is attached to the request context, along
// with an auth.AuthContext carrying the credential's scope set
// (REQ-AUTH-SCOPE-01) so requireScope and per-method gates (dispatch.go)
// can enforce REQ-AUTH-SCOPE-02. On failure a 401 problem is written
// and the request short-circuits.
//
// The WWW-Authenticate challenge advertises only Bearer, not Basic,
// even though the server accepts Basic credentials when they are sent
// proactively. Advertising Basic triggers a native browser login dialog
// in Firefox (RFC 7235 conformant behaviour); omitting it from the
// challenge suppresses the dialog while leaving Basic-only clients
// (Thunderbird, k-9 mail) unaffected because they send credentials
// without waiting for a challenge.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		principal, key, sessID, scope, ok := s.authenticate(ctx, r)
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="jmap"`)
			WriteJMAPError(w, http.StatusUnauthorized,
				"unauthorized", "authentication required")
			return
		}
		ctx = context.WithValue(ctx, ctxKeyPrincipal, principal)
		ctx = context.WithValue(ctx, ctxKeyRemoteAddr, r.RemoteAddr)
		if key != nil {
			ctx = context.WithValue(ctx, ctxKeyAPIKey, *key)
		}
		if sessID != "" {
			ctx = context.WithValue(ctx, ctxKeySessionID, sessID)
		}
		ctx = auth.WithContext(ctx, &auth.AuthContext{
			PrincipalID: uint64(principal.ID),
			Scopes:      scope,
			Listener:    "public",
		})
		next(w, r.WithContext(ctx))
	}
}

// requireScope wraps next with a REQ-AUTH-SCOPE-02 gate: the credential
// attached to ctx by requireAuth must carry scope, or auth.ScopeAdmin
// (an operator-issued admin key or admin session always passes every
// JMAP gate). Must be applied behind requireAuth so an auth.AuthContext
// is present; a missing one is treated as insufficient scope rather
// than panicking. On denial it writes a 403 RFC 7807 problem detail
// (NOT 401 — the caller IS authenticated, just not authorised for this
// scope, matching protoadmin's insufficient_scope shape).
func (s *Server) requireScope(scope auth.Scope, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if auth.RequireScope(ctx, scope) != nil && auth.RequireScope(ctx, auth.ScopeAdmin) != nil {
			WriteJMAPError(w, http.StatusForbidden,
				"insufficient_scope", "credential lacks required scope "+string(scope))
			return
		}
		next(w, r)
	}
}

// authenticate parses the Authorization header and resolves the
// requesting principal. On success it returns the principal, the API
// key (non-nil only for Bearer-authenticated sessions), the session_id
// (non-empty only for cookie-authenticated sessions), the credential's
// scope set (REQ-AUTH-SCOPE-01), and true. Returns false on any failure
// (no information leak through differentiated reasons).
//
// When no Authorization header is present and a SessionResolver is
// configured, cookie-based authentication is attempted (suite-session
// cookie from the public-listener login flow). Bearer / Basic always
// take precedence over the cookie when both are present.
func (s *Server) authenticate(ctx context.Context, r *http.Request) (store.Principal, *store.APIKey, string, auth.ScopeSet, bool) {
	h := r.Header.Get("Authorization")
	if h != "" {
		switch {
		case strings.HasPrefix(h, "Bearer "):
			p, key, ok := s.authenticateBearer(ctx, strings.TrimSpace(h[len("Bearer "):]))
			if !ok {
				return store.Principal{}, nil, "", nil, false
			}
			return p, key, "", ParseAPIKeyScope(key.ScopeJSON), true
		case strings.HasPrefix(h, "Basic "):
			// A directly password-authenticated request (Thunderbird's
			// autoconfig flow, k-9 mail) is equivalent to a fresh Suite
			// login: it carries the full end-user scope set, matching
			// what a session cookie issued at login would carry
			// (REQ-AUTH-SCOPE-01). Only Bearer API keys can be scoped
			// down to less than that.
			p, ok := s.authenticateBasic(ctx, strings.TrimSpace(h[len("Basic "):]))
			if !ok {
				return store.Principal{}, nil, "", nil, false
			}
			return p, nil, "", auth.NewScopeSet(auth.AllEndUserScopes...), true
		default:
			return store.Principal{}, nil, "", nil, false
		}
	}
	// No Authorization header: try the Suite session cookie if the
	// server is configured with a resolver (public listener only).
	if s.sessionResolver != nil {
		pid, scope, ok := s.sessionResolver(r)
		if ok {
			p, err := s.store.Meta().GetPrincipalByID(ctx, pid)
			if err != nil {
				s.log.Warn("auth.cookie_principal_lookup_failed",
					"err", err, "principal_id", pid)
				return store.Principal{}, nil, "", nil, false
			}
			// REQ-SUBACCT-02: a sub-principal is never authenticatable, on
			// any credential kind. IsAuthenticatable also covers
			// PrincipalFlagDisabled.
			if !p.IsAuthenticatable() {
				return store.Principal{}, nil, "", nil, false
			}
			// Extract the CSRF token (session_id) from the cookie when a
			// SessionCookieConfig is available, so the clientlog-meta
			// middleware can look up the sessions table row.
			sessID := s.extractSessionID(r)
			return p, nil, sessID, scope, true
		}
	}
	return store.Principal{}, nil, "", nil, false
}

// extractSessionID decodes the suite-session cookie and returns its
// CSRFToken, which doubles as the sessions table primary key. Returns
// "" when no config is present or the cookie cannot be decoded.
func (s *Server) extractSessionID(r *http.Request) string {
	if s.sessionCookieConfig == nil || len(s.sessionCookieConfig.SigningKey) < 32 {
		return ""
	}
	c, err := r.Cookie(s.sessionCookieConfig.CookieName)
	if err != nil || c.Value == "" {
		return ""
	}
	sess, err := authsession.DecodeSession(c.Value, s.sessionCookieConfig.SigningKey, s.clk.Now())
	if err != nil {
		return ""
	}
	return sess.CSRFToken
}

func (s *Server) authenticateBearer(ctx context.Context, token string) (store.Principal, *store.APIKey, bool) {
	p, key, ok := AuthenticateBearerToken(ctx, s.store, s.apikeyLookup, s.clk, s.log, token)
	if !ok {
		return store.Principal{}, nil, false
	}
	return p, &key, true
}

// AuthenticateBearerToken validates an "Authorization: Bearer hk_..."
// token against the API-key store and returns the authenticated
// principal and key row. It applies the same rules requireAuth applies
// to JMAP requests: the "hk_" prefix, a matching stored hash, an
// unexpired ExpiresAt (device tokens and operator-issued keys leave it
// zero; short-lived OAuth2 access tokens populate it), and an
// authenticatable principal (REQ-SUBACCT-02).
//
// Exported so other public-listener surfaces that accept the same
// device-token / OAuth2 bearer credential (e.g. the image proxy,
// internal/protoimg) resolve it through this one implementation rather
// than re-deriving the hash-and-lookup logic (re #332). log may be nil
// to suppress warning-level diagnostics.
func AuthenticateBearerToken(ctx context.Context, st store.Store, lookup APIKeyLookup, clk clock.Clock, log *slog.Logger, token string) (store.Principal, store.APIKey, bool) {
	if !strings.HasPrefix(token, APIKeyPrefix) {
		return store.Principal{}, store.APIKey{}, false
	}
	hashed := hashAPIKey(token)
	key, err := lookup(ctx, hashed)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) && log != nil {
			log.Warn("auth.lookup_failed", "err", err)
		}
		return store.Principal{}, store.APIKey{}, false
	}
	if subtle.ConstantTimeCompare([]byte(key.Hash), []byte(hashed)) != 1 {
		return store.Principal{}, store.APIKey{}, false
	}
	// Short-lived OAuth2 access tokens (issue #199, REQ-AND-AUTH-02)
	// populate ExpiresAt; every other Bearer key (operator-issued,
	// device token) leaves it zero and never expires this way. A zero
	// ExpiresAt therefore always passes; a non-zero one must be in the
	// future.
	if !key.ExpiresAt.IsZero() && !clk.Now().Before(key.ExpiresAt) {
		return store.Principal{}, store.APIKey{}, false
	}
	p, err := st.Meta().GetPrincipalByID(ctx, key.PrincipalID)
	if err != nil {
		if log != nil {
			log.Warn("auth.principal_lookup_failed",
				"err", err, "principal_id", key.PrincipalID)
		}
		return store.Principal{}, store.APIKey{}, false
	}
	// REQ-SUBACCT-02: a sub-principal is never authenticatable, on any
	// credential kind. IsAuthenticatable also covers PrincipalFlagDisabled.
	if !p.IsAuthenticatable() {
		return store.Principal{}, store.APIKey{}, false
	}
	_ = st.Meta().TouchAPIKey(ctx, key.ID, clk.Now())
	return p, key, true
}

// ParseAPIKeyScope decodes the JSON-encoded scope list stored on an
// APIKey row (REQ-AUTH-SCOPE-01). Unlike protoadmin's parseAPIKeyScope
// (whose legacy fallback grants the admin surface's pre-scope-column
// rows full admin scope), JMAP's fallback is least-privilege: an
// empty, malformed, or empty-array scope value reads back as
// [mail.send], which passes protosend's HTTP send API but is refused
// by every JMAP read/EventSource/download/upload gate that requires
// mail.receive. In production every row is backfilled to a concrete
// scope at INSERT time (storesqlite/storepg InsertAPIKey), so this
// path only fires for a genuinely pre-scope-column legacy row or a
// test fixture that predates the scope column.
//
// Exported so internal/admin's public-listener Bearer-or-cookie
// resolver (the image proxy's auth path, re #332) applies the same
// gate without re-deriving the fallback rules.
func ParseAPIKeyScope(raw string) auth.ScopeSet {
	if raw == "" {
		return auth.NewScopeSet(auth.ScopeMailSend)
	}
	var s auth.ScopeSet
	if err := json.Unmarshal([]byte(raw), &s); err != nil || len(s) == 0 {
		return auth.NewScopeSet(auth.ScopeMailSend)
	}
	return s
}

func (s *Server) authenticateBasic(ctx context.Context, encoded string) (store.Principal, bool) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return store.Principal{}, false
	}
	parts := strings.SplitN(string(raw), ":", 2)
	if len(parts) != 2 {
		return store.Principal{}, false
	}
	if s.dir == nil {
		return store.Principal{}, false
	}
	pid, err := s.dir.Authenticate(ctx, parts[0], parts[1])
	if err != nil {
		return store.Principal{}, false
	}
	p, err := s.store.Meta().GetPrincipalByID(ctx, pid)
	if err != nil {
		s.log.Warn("auth.basic_principal_lookup_failed",
			"err", err, "principal_id", pid)
		return store.Principal{}, false
	}
	return p, true
}
