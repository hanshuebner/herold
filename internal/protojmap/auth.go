package protojmap

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

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
// On success the principal is attached to the request context. On
// failure a 401 problem is written and the request short-circuits.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		principal, ok := s.authenticate(ctx, r)
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="jmap", Basic realm="jmap"`)
			WriteJMAPError(w, http.StatusUnauthorized,
				"unauthorized", "authentication required")
			return
		}
		ctx = context.WithValue(ctx, ctxKeyPrincipal, principal)
		ctx = context.WithValue(ctx, ctxKeyRemoteAddr, r.RemoteAddr)
		next(w, r.WithContext(ctx))
	}
}

// authenticate parses the Authorization header and resolves the
// requesting principal. Returns the zero Principal + false on any
// failure (no information leak through differentiated reasons).
func (s *Server) authenticate(ctx context.Context, r *http.Request) (store.Principal, bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return store.Principal{}, false
	}
	switch {
	case strings.HasPrefix(h, "Bearer "):
		return s.authenticateBearer(ctx, strings.TrimSpace(h[len("Bearer "):]))
	case strings.HasPrefix(h, "Basic "):
		return s.authenticateBasic(ctx, strings.TrimSpace(h[len("Basic "):]))
	default:
		return store.Principal{}, false
	}
}

func (s *Server) authenticateBearer(ctx context.Context, token string) (store.Principal, bool) {
	if !strings.HasPrefix(token, APIKeyPrefix) {
		return store.Principal{}, false
	}
	hashed := hashAPIKey(token)
	key, err := s.apikeyLookup(ctx, hashed)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.Warn("protojmap.auth.lookup_failed", "err", err)
		}
		return store.Principal{}, false
	}
	if subtle.ConstantTimeCompare([]byte(key.Hash), []byte(hashed)) != 1 {
		return store.Principal{}, false
	}
	p, err := s.store.Meta().GetPrincipalByID(ctx, key.PrincipalID)
	if err != nil {
		s.log.Warn("protojmap.auth.principal_lookup_failed",
			"err", err, "principal_id", key.PrincipalID)
		return store.Principal{}, false
	}
	if p.Flags.Has(store.PrincipalFlagDisabled) {
		return store.Principal{}, false
	}
	_ = s.store.Meta().TouchAPIKey(ctx, key.ID, s.clk.Now())
	return p, true
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
		s.log.Warn("protojmap.auth.basic_principal_lookup_failed",
			"err", err, "principal_id", pid)
		return store.Principal{}, false
	}
	return p, true
}
