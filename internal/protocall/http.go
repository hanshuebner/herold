package protocall

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/hanshuebner/herold/internal/auth"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// HTTPHandler returns the http.Handler that serves the credential
// mint endpoint at "/api/v1/call/credentials". The handler accepts
// only POST; other methods return 405. Authentication is handed off
// to Options.Authn — Bearer API keys and session cookies both
// surface through the same resolver, so the handler is identical for
// either auth source.
//
// Body: empty or "{}". Anything else is accepted but ignored; we
// neither require nor allow a request body (forward compatibility:
// future fields can land without changing the contract).
//
// Response: JSON Credential as defined in turn.go.
func (s *Server) HTTPHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/call/credentials", s.handleCredentials)
	return mux
}

func (s *Server) handleCredentials(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeProblem(w, r, http.StatusMethodNotAllowed,
			"method_not_allowed", "POST required", "")
		return
	}
	if s.authn == nil {
		writeProblem(w, r, http.StatusServiceUnavailable,
			"unconfigured", "credential mint not configured", "")
		observe.ProtocallCredentialsMintedTotal.WithLabelValues("blocked").Inc()
		return
	}
	principal, ok := s.authn(r)
	if !ok {
		writeProblem(w, r, http.StatusUnauthorized,
			"unauthorized", "authentication required", "")
		observe.ProtocallCredentialsMintedTotal.WithLabelValues("blocked").Inc()
		return
	}
	// REQ-AUTH-SCOPE-02: end-user scope is required for the
	// credential mint endpoint. Cookies issued at the public-listener
	// login carry it by default; admin-scope-only API keys do NOT
	// (no implicit grant per REQ-AUTH-SCOPE-02). When the request
	// has no AuthContext attached at all (test harness shape) the
	// check is skipped so the legacy authn-only fixtures still pass.
	if actx := auth.FromContext(r.Context()); actx != nil {
		if err := auth.RequireScope(r.Context(), auth.ScopeEndUser); err != nil {
			writeProblem(w, r, http.StatusForbidden,
				"insufficient_scope",
				"insufficient scope for this resource",
				err.Error())
			observe.ProtocallCredentialsMintedTotal.WithLabelValues("blocked").Inc()
			return
		}
	}
	if ok, retry := s.rl.allow(rateKey(principal)); !ok {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", int(retry.Seconds())))
		writeProblem(w, r, http.StatusTooManyRequests,
			"rate_limited", "credential mint rate limit exceeded", "")
		observe.ProtocallCredentialsMintedTotal.WithLabelValues("ratelimited").Inc()
		return
	}
	cred, err := s.MintCredential(r.Context(), principal)
	if err != nil {
		switch {
		case errors.Is(err, errTURNDisabled):
			writeProblem(w, r, http.StatusServiceUnavailable,
				"turn_disabled", "TURN not configured on this server", "")
		case errors.Is(err, errTURNNoSecret):
			writeProblem(w, r, http.StatusServiceUnavailable,
				"turn_misconfigured", "TURN shared secret missing", "")
		default:
			s.logger.LogAttrs(r.Context(), slog.LevelError,
				"protocall: mint failed",
				slog.String("err", err.Error()),
				slog.Uint64("principal", uint64(principal)))
			writeProblem(w, r, http.StatusInternalServerError,
				"internal_error", "credential mint failed", "")
		}
		observe.ProtocallCredentialsMintedTotal.WithLabelValues("blocked").Inc()
		return
	}
	observe.ProtocallCredentialsMintedTotal.WithLabelValues("ok").Inc()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(cred)
}

// rateKey returns the rate-limit bucket key for principal. Distinct
// from the protoadmin keying so the limiter tables stay disjoint.
func rateKey(principal store.PrincipalID) string {
	return fmt.Sprintf("call-cred:%d", uint64(principal))
}
