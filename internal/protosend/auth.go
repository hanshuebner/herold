package protosend

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/hanshuebner/herold/internal/store"
)

// APIKeyPrefix is the three-character marker that precedes every
// Herold API key. Identical to protoadmin's prefix — both surfaces
// share the bearer-token vocabulary so a single key works against
// either API surface (subject to scope checks). See
// internal/protoadmin/auth.go for the scheme rationale.
const APIKeyPrefix = "hk_"

// HashAPIKey returns the lowercase hex SHA-256 of the plaintext key.
// Mirrors protoadmin/auth.go HashAPIKey; copied rather than imported
// to keep the protosend package self-contained on its auth path. A
// future cleanup should converge both onto a small internal/apikey
// helper once a third caller arrives.
func HashAPIKey(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// requireAuth enforces Bearer-token authentication and per-key rate
// limiting. On success it attaches the principal + API key to the
// request context.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		principal, key, ok := s.authenticate(ctx, r)
		if !ok {
			writeProblem(w, r, http.StatusUnauthorized,
				"unauthorized", "authentication required", "")
			return
		}
		// Per-key rate limit (REQ-SEND-32).
		if !s.checkRateLimit(w, r, fmt.Sprintf("apikey:%d", key.ID)) {
			return
		}
		ctx = context.WithValue(ctx, ctxKeyPrincipal, principal)
		ctx = context.WithValue(ctx, ctxKeyAPIKey, key)
		next(w, r.WithContext(ctx))
	}
}

// authenticate inspects the Authorization header.
func (s *Server) authenticate(ctx context.Context, r *http.Request) (store.Principal, store.APIKey, bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return store.Principal{}, store.APIKey{}, false
	}
	const bearer = "Bearer "
	if !strings.HasPrefix(h, bearer) {
		return store.Principal{}, store.APIKey{}, false
	}
	token := strings.TrimSpace(h[len(bearer):])
	if !strings.HasPrefix(token, APIKeyPrefix) {
		return store.Principal{}, store.APIKey{}, false
	}
	hashed := HashAPIKey(token)
	key, err := s.apikeyLookup(ctx, hashed)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.loggerFrom(ctx).Warn("protosend.auth.lookup_failed", "err", err)
		}
		return store.Principal{}, store.APIKey{}, false
	}
	if subtle.ConstantTimeCompare([]byte(key.Hash), []byte(hashed)) != 1 {
		return store.Principal{}, store.APIKey{}, false
	}
	p, err := s.store.Meta().GetPrincipalByID(ctx, key.PrincipalID)
	if err != nil {
		s.loggerFrom(ctx).Warn("protosend.auth.principal_lookup_failed",
			"err", err, "principal_id", key.PrincipalID)
		return store.Principal{}, store.APIKey{}, false
	}
	if p.Flags.Has(store.PrincipalFlagDisabled) {
		return store.Principal{}, store.APIKey{}, false
	}
	_ = s.store.Meta().TouchAPIKey(ctx, key.ID, s.clk.Now())
	return p, key, true
}

// checkRateLimit enforces the per-API-key sliding window. Returns
// false (and writes a 429) when exceeded.
func (s *Server) checkRateLimit(w http.ResponseWriter, r *http.Request, key string) bool {
	ok, retry := s.rl.allow(key)
	if ok {
		return true
	}
	w.Header().Set("Retry-After", fmt.Sprintf("%d", int(retry.Seconds())))
	writeProblem(w, r, http.StatusTooManyRequests,
		"rate-limited", "rate limit exceeded", fmt.Sprintf("retry after %s", retry))
	return false
}
