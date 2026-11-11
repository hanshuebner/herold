package protoadmin

// oidc_browser.go is the browser leg of per-user external OIDC
// federation (REQ-AUTH-50..58) for the Suite SPA: listing the
// configured providers on the sign-in page, starting a sign-in, and the
// redirect target that completes either a sign-in (issuing the same
// session cookie password+TOTP login issues) or a link started from the
// Suite's security settings.
//
// Both flows use one redirect_uri, <public origin>/api/v1/oidc/callback,
// derived from s.ownOrigin (the operator-configured public base URL), so
// the operator registers exactly one URL per deployment at the provider.
// The callback tells the flows apart by the pending state's kind.
//
// Every flow start sets a short-lived cookie carrying the OIDC state, and
// the callback only proceeds when the cookie matches the state in the
// URL. That binds the provider's response to the browser that started
// the flow: a callback URL minted in someone else's browser (an attacker
// who completed the provider login with their own account and forwards
// the redirect) is refused, so it can neither sign the victim in as the
// attacker nor attach the victim's provider identity to the attacker's
// principal.

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// oidcCallbackPath is the browser redirect target registered at the
// provider for both the Suite sign-in and the link flow.
const oidcCallbackPath = "/api/v1/oidc/callback"

// oidcFlowCookieTTL bounds the flow-binding cookie's life to the RP's
// pending-state TTL; a state older than that is gone anyway.
const oidcFlowCookieTTL = 5 * time.Minute

// Suite landing targets after the callback. The sign-in page reads
// ?oidc_error= to explain a refused sign-in; the security settings read
// the oidc_link hash parameter to report the link outcome.
const (
	oidcSignInLanding    = "/"
	oidcLinkLandingTempl = "/#/settings/security?oidc_link=%s"
)

// oidcFlowCookieName returns the flow-binding cookie's name. With secure
// cookies the __Host- prefix pins it to this exact origin, so a sibling
// subdomain cannot plant a value that matches an attacker's state.
func (s *Server) oidcFlowCookieName() string {
	if s.opts.Session.SecureCookies {
		return "__Host-herold_oidc_state"
	}
	return "herold_oidc_state"
}

// oidcCallbackURL is the absolute redirect_uri for the browser flows.
func (s *Server) oidcCallbackURL(r *http.Request) string {
	return s.ownOrigin(r) + oidcCallbackPath
}

func (s *Server) setOIDCFlowCookie(w http.ResponseWriter, state string) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.oidcFlowCookieName(),
		Value:    state,
		Path:     "/",
		MaxAge:   int(oidcFlowCookieTTL / time.Second),
		HttpOnly: true,
		Secure:   s.opts.Session.SecureCookies,
		// Lax: the cookie must ride along on the provider's top-level
		// GET redirect back to the callback, which is a cross-site
		// navigation.
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearOIDCFlowCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.oidcFlowCookieName(),
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.opts.Session.SecureCookies,
		SameSite: http.SameSiteLaxMode,
	})
}

// signInProviderDTO is one entry of the public provider list. Only what
// the sign-in page needs to render a button is exposed.
type signInProviderDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// handleListSignInProviders implements GET /api/v1/auth/oidc/providers:
// the unauthenticated list of providers the Suite sign-in page offers.
// Providers whose secret cannot be resolved are omitted. A store failure
// yields an empty list so password sign-in is never blocked by it.
func (s *Server) handleListSignInProviders(w http.ResponseWriter, r *http.Request) {
	items := []signInProviderDTO{}
	providers, err := s.rp.ListProviders(r.Context())
	if err != nil {
		s.loggerFrom(r.Context()).Warn("protoadmin.oidc.list_providers_failed",
			"activity", observe.ActivityInternal, "err", err)
	}
	for _, p := range providers {
		if !p.Available {
			continue
		}
		items = append(items, signInProviderDTO{ID: string(p.ID), Name: p.Name})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

type beginOIDCSignInRequest struct {
	ProviderID string `json:"provider_id"`
}

// handleBeginOIDCSignIn implements POST /api/v1/auth/oidc/begin: starts
// an unauthenticated sign-in against the named provider, sets the
// flow-binding cookie, and returns the provider's authorization URL for
// the SPA to navigate to.
func (s *Server) handleBeginOIDCSignIn(w http.ResponseWriter, r *http.Request) {
	if !s.checkRateLimit(w, r, "oidc-signin-ip:"+remoteHost(r.RemoteAddr)) {
		return
	}
	var req beginOIDCSignInRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.ProviderID == "" {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed", "provider_id is required", "")
		return
	}
	authURL, state, err := s.rp.BeginSignInAt(r.Context(), directoryoidc.ProviderID(req.ProviderID), s.oidcCallbackURL(r))
	if err != nil {
		s.writeOIDCError(w, r, err)
		return
	}
	s.setOIDCFlowCookie(w, state)
	writeJSON(w, http.StatusOK, map[string]string{"auth_url": authURL})
}

// handleOIDCBrowserCallback implements GET /api/v1/oidc/callback, the
// provider's redirect target for both browser flows. It always answers
// with a redirect into the Suite; the outcome travels as a query or hash
// parameter so the SPA can explain it. The state is consumed only after
// the flow-binding cookie matched, so a forwarded callback URL cannot
// burn the legitimate browser's pending flow.
func (s *Server) handleOIDCBrowserCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state := q.Get("state")
	code := q.Get("code")
	kind := s.rp.PeekPendingFlow(state)

	signInFail := func(reason string) {
		http.Redirect(w, r, oidcSignInLanding+"?oidc_error="+url.QueryEscape(reason), http.StatusFound)
	}
	linkResult := func(outcome string) {
		http.Redirect(w, r, fmt.Sprintf(oidcLinkLandingTempl, url.QueryEscape(outcome)), http.StatusFound)
	}
	fail := func(reason string) {
		if kind == directoryoidc.FlowKindLink {
			linkResult(reason)
			return
		}
		signInFail(reason)
	}

	if state == "" || kind == directoryoidc.FlowKindUnknown {
		s.auditAuthFailure(r, "auth.oidc.callback", "", 0, "unknown or expired state")
		fail("expired")
		return
	}
	cookie, cerr := r.Cookie(s.oidcFlowCookieName())
	if cerr != nil || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(state)) != 1 {
		s.auditAuthFailure(r, "auth.oidc.callback", "", 0, "flow cookie missing or does not match state")
		fail("browser_mismatch")
		return
	}
	s.clearOIDCFlowCookie(w)
	if code == "" {
		// The provider redirected back without a code: the user denied
		// consent or the provider reported an error (?error=...). The
		// pending state is left to expire.
		s.auditAuthFailure(r, "auth.oidc.callback", "", 0, "provider returned no code: "+q.Get("error"))
		fail("cancelled")
		return
	}

	switch kind {
	case directoryoidc.FlowKindLink:
		pid, err := s.rp.CompleteLink(r.Context(), state, code)
		if err != nil {
			s.auditAuthFailure(r, "oidc.link.create", "", 0, err.Error())
			if errors.Is(err, directoryoidc.ErrConflict) {
				linkResult("already_linked")
				return
			}
			linkResult("failed")
			return
		}
		s.appendAudit(r.Context(), "oidc.link.create", fmt.Sprintf("principal:%d", pid),
			store.OutcomeSuccess, "", map[string]string{"remote": remoteHost(r.RemoteAddr)})
		linkResult("linked")
	case directoryoidc.FlowKindSignIn:
		pid, err := s.rp.CompleteSignIn(r.Context(), state, code)
		if err != nil {
			s.auditAuthFailure(r, "auth.login", "", 0, "oidc: "+humanOIDCError(err))
			switch {
			case errors.Is(err, directoryoidc.ErrNotFound), errors.Is(err, directoryoidc.ErrAutoProvisionRefused):
				signInFail("not_linked")
			case errors.Is(err, directoryoidc.ErrInvalidState):
				signInFail("expired")
			default:
				signInFail("failed")
			}
			return
		}
		p, err := s.store.Meta().GetPrincipalByID(r.Context(), pid)
		if err != nil {
			s.loggerFrom(r.Context()).Error("protoadmin.oidc.signin_principal_lookup_failed",
				"activity", observe.ActivityInternal, "principal_id", uint64(pid), "err", err)
			signInFail("failed")
			return
		}
		if !p.IsAuthenticatable() {
			s.auditAuthFailure(r, "auth.login", p.CanonicalEmail, pid, "oidc: principal not authenticatable")
			signInFail("not_linked")
			return
		}
		s.startSession(r.Context(), w, r, p, false)
		auditCtx := context.WithValue(r.Context(), ctxKeyPrincipal, p)
		s.loggerFrom(r.Context()).InfoContext(r.Context(), "protoadmin.auth.login_success",
			"activity", observe.ActivityAudit,
			"principal_id", uint64(p.ID),
			"email", p.CanonicalEmail,
			"method", "oidc")
		s.appendAudit(auditCtx, "auth.login", "principal:"+p.CanonicalEmail, store.OutcomeSuccess, "",
			map[string]string{"remote": remoteHost(r.RemoteAddr), "method": "oidc"})
		http.Redirect(w, r, oidcSignInLanding, http.StatusFound)
	}
}
