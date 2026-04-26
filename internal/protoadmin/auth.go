package protoadmin

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"

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

// requireAuth is middleware that enforces Bearer-token authentication.
// On success it attaches the authenticated principal to the request
// context; on failure it writes a 401 problem and returns.
//
// Phase 1 admin auth is API-key only. Session tokens are Phase 2 work;
// the parser below rejects tokens missing the "hk_" prefix with a
// structured error, which also protects against forgetting to add the
// Phase 2 code path later (a missing-prefix bearer is definitely not
// a valid Phase 1 key). See REQ-ADM-03.
//
// TODO(phase2): accept session-cookie tokens here once protoadmin
// ships the UI. Ticket ref: docs/design/implementation/02-phasing.md §Phase 2.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		principal, ok := s.authenticate(ctx, r)
		if !ok {
			writeProblem(w, r, http.StatusUnauthorized,
				"unauthorized", "authentication required", "")
			return
		}
		if !s.checkRateLimit(w, r, authCacheKey(r, principal)) {
			return
		}
		ctx = context.WithValue(ctx, ctxKeyPrincipal, principal)
		ctx = context.WithValue(ctx, ctxKeyRemoteAddr, r.RemoteAddr)
		next(w, r.WithContext(ctx))
	}
}

// authenticate inspects the Authorization header and returns the
// associated principal if the Bearer token is a valid API key.
func (s *Server) authenticate(ctx context.Context, r *http.Request) (store.Principal, bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		observe.AuthAttemptsTotal.WithLabelValues("apikey", "fail").Inc()
		return store.Principal{}, false
	}
	const bearer = "Bearer "
	if !strings.HasPrefix(h, bearer) {
		observe.AuthAttemptsTotal.WithLabelValues("apikey", "fail").Inc()
		return store.Principal{}, false
	}
	token := strings.TrimSpace(h[len(bearer):])
	if !strings.HasPrefix(token, APIKeyPrefix) {
		observe.AuthAttemptsTotal.WithLabelValues("apikey", "fail").Inc()
		return store.Principal{}, false
	}
	hashed := HashAPIKey(token)
	key, err := s.apikeyLookup(ctx, hashed)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.loggerFrom(ctx).Warn("protoadmin.auth.lookup_failed", "err", err)
		}
		observe.AuthAttemptsTotal.WithLabelValues("apikey", "fail").Inc()
		return store.Principal{}, false
	}
	// Constant-time comparison against the stored hash to avoid a
	// hypothetical timing channel in a backend that returns keys by
	// prefix match rather than exact match. The default backend uses
	// SQL "WHERE hash = ?" so the check is redundant; we keep it for
	// defence-in-depth against future lookups that loosen that.
	if subtle.ConstantTimeCompare([]byte(key.Hash), []byte(hashed)) != 1 {
		observe.AuthAttemptsTotal.WithLabelValues("apikey", "fail").Inc()
		return store.Principal{}, false
	}
	p, err := s.store.Meta().GetPrincipalByID(ctx, key.PrincipalID)
	if err != nil {
		s.loggerFrom(ctx).Warn("protoadmin.auth.principal_lookup_failed",
			"err", err, "principal_id", key.PrincipalID)
		observe.AuthAttemptsTotal.WithLabelValues("apikey", "fail").Inc()
		return store.Principal{}, false
	}
	if p.Flags.Has(store.PrincipalFlagDisabled) {
		observe.AuthAttemptsTotal.WithLabelValues("apikey", "fail").Inc()
		return store.Principal{}, false
	}
	_ = s.store.Meta().TouchAPIKey(ctx, key.ID, s.clk.Now())
	observe.AuthAttemptsTotal.WithLabelValues("apikey", "ok").Inc()
	return p, true
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
