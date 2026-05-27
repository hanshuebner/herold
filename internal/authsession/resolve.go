package authsession

// resolve.go provides stateless session-resolution helpers that decode
// and validate a session cookie on an incoming request without knowing
// about server lifecycle. Consumers (internal/protoimg, internal/protochat,
// internal/protocall, internal/protojmap JMAP cookie-auth) call these
// functions via closures wired in internal/admin/server.go. The closures
// capture the public-listener SessionConfig, store, and clock so each
// subsystem gets cookie validation without depending on a server struct.

import (
	"net/http"

	"github.com/hanshuebner/herold/internal/auth"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
)

// ResolveSession returns the principal ID authenticated by the session cookie
// on r, using cfg to find and verify the cookie, st to confirm the principal
// still exists and is not disabled, and clk for expiry comparison.
//
// Returns (id, true) on success; (0, false) if the cookie is absent,
// invalid, expired, or the principal is disabled/missing.
//
// This is the scope-blind convenience wrapper. Use ResolveSessionWithScope
// when the caller needs to check scopes.
func ResolveSession(r *http.Request, cfg SessionConfig, st store.Store, clk clock.Clock) (store.PrincipalID, bool) {
	pid, _, ok := ResolveSessionWithScope(r, cfg, st, clk)
	return pid, ok
}

// ResolveSessionWithScope is the scope-aware resolver (REQ-AUTH-SCOPE-01).
//
// Returns (principalID, scopeSet, true) when:
//   - a cookie named cfg.CookieName is present on r,
//   - DecodeSession validates the HMAC and the cookie is not expired,
//   - the principal exists in st and PrincipalFlagDisabled is clear,
//   - if cfg.IdleTTL > 0, the session row's LastSeenAt is within the
//     idle window (REQ-AUTH-72, issue #12 slice 3).
//
// Returns (0, nil, false) in every other case. The scope set is the value
// stamped at login time; it is immutable for the cookie's lifetime
// (REQ-AUTH-SCOPE-01: rotating scopes requires logging out and back in).
//
// Idle-timeout enforcement (when cfg.IdleTTL > 0):
//   - GetSession(sessionID) looks up the persisted row by CSRFToken
//     (which doubles as the session row PK at write time).
//   - When the row is missing the session is treated as not-found.
//   - When (now - row.LastSeenAt) > cfg.IdleTTL the session is
//     rejected and the row is deleted so the cookie cannot be revived
//     by simply resending it.
//   - Otherwise the row is touched via UpdateSessionLastSeen(now),
//     sliding the idle deadline forward. The touch is best-effort:
//     a touch failure does NOT reject the request (logged-out vs
//     just-took-a-bit-longer would be indistinguishable to the user).
//
// When cfg.IdleTTL == 0 (the public-listener default) the resolver
// behaves exactly as before: no row lookup, no touch — the HMAC and
// absolute-expiry check on the cookie are sufficient.
//
// The function is pure in the sense that it takes store and clock as
// parameters rather than capturing server state, so it can be used from any
// package without importing server lifecycle objects.
func ResolveSessionWithScope(r *http.Request, cfg SessionConfig, st store.Store, clk clock.Clock) (store.PrincipalID, auth.ScopeSet, bool) {
	c, err := r.Cookie(cfg.CookieName)
	if err != nil || c.Value == "" {
		return 0, nil, false
	}
	sess, err := DecodeSession(c.Value, cfg.SigningKey, clk.Now())
	if err != nil {
		return 0, nil, false
	}
	p, err := st.Meta().GetPrincipalByID(r.Context(), sess.PrincipalID)
	if err != nil {
		return 0, nil, false
	}
	if p.Flags.Has(store.PrincipalFlagDisabled) {
		return 0, nil, false
	}

	if cfg.IdleTTL > 0 {
		ctx := r.Context()
		row, err := st.Meta().GetSession(ctx, sess.CSRFToken)
		if err != nil {
			// Row missing means the session was deleted (logout) or
			// expired+evicted. Either way the cookie is no longer valid.
			return 0, nil, false
		}
		now := clk.Now()
		if !row.LastSeenAt.IsZero() && now.Sub(row.LastSeenAt) > cfg.IdleTTL {
			// Idle gate trips: remove the row so a future request with
			// the same cookie cannot resurrect the session.
			_ = st.Meta().DeleteSession(ctx, sess.CSRFToken)
			return 0, nil, false
		}
		// Touch is best-effort; a transient store failure does not
		// reject the in-flight request.
		_ = st.Meta().UpdateSessionLastSeen(ctx, sess.CSRFToken, now.UnixMicro())
	}

	return sess.PrincipalID, sess.Scopes, true
}
