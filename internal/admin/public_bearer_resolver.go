package admin

// public_bearer_resolver.go composes bearer-token and suite-session-
// cookie authentication into a single store.PrincipalID resolver for
// public-listener surfaces that were cookie-only (re #332). The image
// proxy (/proxy/image) is the first consumer; any future public-
// listener surface with the same "cookie for a browser, Bearer for a
// native client" need can reuse newPublicBearerOrCookieResolver rather
// than re-deriving cookie-vs-Bearer precedence.

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// newPublicBearerOrCookieResolver returns a store.PrincipalID resolver
// that:
//
//  1. When the request carries an "Authorization: Bearer hk_..." header,
//     validates it exactly as protojmap's own Bearer path does --
//     protojmap.AuthenticateBearerToken applies the same "hk_" prefix
//     check, stored-hash match, ExpiresAt bound (short-lived OAuth2
//     access tokens), and principal.IsAuthenticatable rule JMAP requests
//     go through -- and returns that principal. An Authorization header
//     that fails validation is rejected outright; it does NOT fall
//     through to the cookie, matching protojmap.Server.authenticate's
//     precedence (Bearer / Basic always win over the cookie when
//     present).
//  2. Otherwise delegates to cookieResolver (the suite-session-cookie
//     resolver), so browser clients are unaffected.
//
// st and clk back the API-key lookup and expiry check; logger receives
// the same "auth.lookup_failed" / "auth.principal_lookup_failed"
// warnings protojmap's own Bearer path logs (nil suppresses them, used
// by tests that do not want log noise).
func newPublicBearerOrCookieResolver(
	st store.Store,
	clk clock.Clock,
	logger *slog.Logger,
	cookieResolver func(*http.Request) (store.PrincipalID, bool),
) func(*http.Request) (store.PrincipalID, bool) {
	lookup := func(ctx context.Context, hash string) (store.APIKey, error) {
		return st.Meta().GetAPIKeyByHash(ctx, hash)
	}
	return func(r *http.Request) (store.PrincipalID, bool) {
		h := r.Header.Get("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			return cookieResolver(r)
		}
		token := strings.TrimSpace(h[len("Bearer "):])
		p, _, ok := protojmap.AuthenticateBearerToken(r.Context(), st, lookup, clk, logger, token)
		if !ok {
			return 0, false
		}
		return p.ID, true
	}
}
