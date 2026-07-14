package protoadmin

// oauth2_federated.go implements the external-OIDC sign-in leg of the
// /oauth2/authorize native-client login page (issue #238, split from
// #199): a principal that authenticates through a linked external OIDC
// provider (internal/directoryoidc, REQ-AUTH-50..58) completes the same
// authorization-code + PKCE grant that the password+TOTP leg
// (oauth2_native.go) reaches, via
// directory.IssueAuthorizationCodeForFederatedPrincipal -- so a single
// code/token issuance path serves both credential kinds and a client
// exchanging the code cannot tell which leg a given login took.
//
// Flow, layered on top of oauth2_native.go's existing GET/POST
// /oauth2/authorize (which validate client_id/redirect_uri/PKCE and
// render the login form -- unchanged by this file except that the
// rendered form now also lists every configured provider):
//
//  1. Each "Sign in with <provider>" button on the rendered login form
//     posts the same signed pre-login request token ("req") and CSRF
//     cookie value the password form posts, plus which provider was
//     chosen, to POST /oauth2/authorize/federated.
//  2. handleOAuthAuthorizeFederatedBegin verifies req + CSRF exactly
//     like oauth2_native.go's password POST does, starts an OIDC
//     sign-in (directoryoidc.RP.BeginSignIn), and 302s the browser to
//     the provider. The oauth2/authorize request token does not fit
//     inside the OIDC state parameter BeginSignIn already owns (that
//     state is opaque and keys the RP's own pending-flow map), so this
//     file keeps its own short-lived correlation from that OIDC state
//     back to the encoded req (federatedLoginStore) -- the same
//     "carry local state across an external redirect" problem
//     oauth_init.go's oauthStateStore solves for the external-SMTP-
//     submission OAuth flow, solved the same way here.
//  3. GET /oauth2/authorize/federated/callback is the provider's
//     redirect target. It recovers the encoded req from
//     federatedLoginStore (single-use), re-verifies it (signature + TTL,
//     the same DecodeAuthorizeRequest check the password POST uses),
//     completes the OIDC sign-in (RP.CompleteSignIn), and mints the
//     authorization code via IssueAuthorizationCodeForFederatedPrincipal
//     -- the same redirect-with-code response oauth2_native.go's POST
//     handler gives the password+TOTP leg.
//
// A failure at any step re-renders the login form (with the still-valid
// req token, when one could be recovered) rather than erroring the whole
// authorization request, so the user can retry with a different
// provider or with password+TOTP without restarting the native client's
// whole flow.

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/store"
)

// federatedLoginStateTTL bounds how long a federated-login correlation
// entry survives. Matches directoryoidc's own pending-state TTL (5
// minutes) -- no point outliving the OIDC state it is keyed to.
const federatedLoginStateTTL = 5 * time.Minute

// federatedLoginEntry is the state this file's short-lived correlation
// store carries across the provider redirect: the signed pre-login
// request token from GET /oauth2/authorize (re-verified on the way
// back) and this entry's own expiry.
type federatedLoginEntry struct {
	EncodedReq string
	ExpiresAt  time.Time
}

// federatedLoginStore is the server-wide in-memory correlation from an
// OIDC state token (minted by directoryoidc.RP.BeginSignIn) to the
// oauth2/authorize request it belongs to. Same v1 in-memory limitation
// as oauth_init.go's oauthStateStore: a multi-instance deployment needs
// sticky sessions or a shared store for this flow to survive landing on
// a different instance than it started on.
var (
	federatedLoginMu    sync.Mutex
	federatedLoginStore = map[string]federatedLoginEntry{}
)

// storeFederatedLoginState records the correlation from oidcState to
// encodedReq, expiring at expiresAt.
func storeFederatedLoginState(oidcState, encodedReq string, expiresAt time.Time) {
	federatedLoginMu.Lock()
	federatedLoginStore[oidcState] = federatedLoginEntry{EncodedReq: encodedReq, ExpiresAt: expiresAt}
	federatedLoginMu.Unlock()
}

// takeFederatedLoginState retrieves and removes (single-use) the
// oauth2/authorize request token correlated with oidcState. Lazily
// sweeps expired entries on every lookup, mirroring
// oauth_init.go's lookupOAuthState.
func takeFederatedLoginState(oidcState string, now time.Time) (federatedLoginEntry, bool) {
	federatedLoginMu.Lock()
	defer federatedLoginMu.Unlock()
	for k, v := range federatedLoginStore {
		if now.After(v.ExpiresAt) {
			delete(federatedLoginStore, k)
		}
	}
	e, ok := federatedLoginStore[oidcState]
	if !ok {
		return federatedLoginEntry{}, false
	}
	delete(federatedLoginStore, oidcState)
	return e, true
}

// federatedCallbackURL builds the absolute callback URI this flow
// registers with directoryoidc.RP.BeginSignInAt for the current request,
// mirroring oauth_init.go's buildCallbackURL for the external-SMTP-
// submission OAuth flow. Uses the same s.ownOrigin origin resolution --
// the operator-configured Options.BaseURL over request Host /
// X-Forwarded-Host headers -- so this leg cannot diverge from that one on
// which origin an attacker-controlled header can steer (re #240).
func (s *Server) federatedCallbackURL(r *http.Request) string {
	return s.ownOrigin(r) + "/oauth2/authorize/federated/callback"
}

// oauthLoginProviders returns the login-form-template option list for
// every configured external OIDC provider. A store failure is logged
// and treated as "no providers configured" rather than failing the
// whole login page -- password+TOTP sign-in must keep working even if
// the OIDC provider listing is unavailable.
func (s *Server) oauthLoginProviders(ctx context.Context) []oauthLoginProviderOption {
	providers, err := s.rp.ListProviders(ctx)
	if err != nil {
		s.loggerFrom(ctx).Warn("protoadmin.oauth2.list_providers_failed", "err", err)
		return nil
	}
	out := make([]oauthLoginProviderOption, 0, len(providers))
	for _, p := range providers {
		out = append(out, oauthLoginProviderOption{ID: string(p.ID), Name: p.Name})
	}
	return out
}

// handleOAuthAuthorizeFederatedBegin implements
// POST /oauth2/authorize/federated: the login form's "Sign in with
// <provider>" buttons post here. Validates the same CSRF double-submit
// oauth2_native.go's password POST requires, then starts an OIDC
// sign-in and redirects the browser to the provider.
func (s *Server) handleOAuthAuthorizeFederatedBegin(w http.ResponseWriter, r *http.Request) {
	ipKey := "oauth2-authorize-federated-ip:" + remoteHost(r.RemoteAddr)
	if !s.checkRateLimit(w, r, ipKey) {
		return
	}
	if err := r.ParseForm(); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "bad_request", "malformed form body", "")
		return
	}
	encodedReq := r.FormValue("req")
	formCSRF := r.FormValue("csrf")
	providerID := r.FormValue("provider")

	cookie, cookieErr := r.Cookie(oauthCSRFCookieName)
	if cookieErr != nil || formCSRF == "" ||
		subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(formCSRF)) != 1 {
		writeProblem(w, r, http.StatusForbidden, "csrf_mismatch", "CSRF check failed; please restart sign-in", "")
		return
	}
	req, err := s.dir.DecodeAuthorizeRequest(encodedReq, s.clk.Now().UTC())
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid_request", "sign-in request expired or invalid; please restart sign-in", "")
		return
	}
	if subtle.ConstantTimeCompare([]byte(req.CSRFToken), []byte(formCSRF)) != 1 {
		writeProblem(w, r, http.StatusForbidden, "csrf_mismatch", "CSRF check failed; please restart sign-in", "")
		return
	}
	if providerID == "" {
		s.renderOAuthLoginForm(r.Context(), w, encodedReq, formCSRF, "Choose a sign-in provider.", false)
		return
	}

	authURL, oidcState, err := s.rp.BeginSignInAt(r.Context(), directoryoidc.ProviderID(providerID), s.federatedCallbackURL(r))
	if err != nil {
		s.auditAuthFailure(r, "auth.oauth2.authorize.federated", "provider:"+providerID, 0, humanOIDCError(err))
		s.renderOAuthLoginForm(r.Context(), w, encodedReq, formCSRF, "That sign-in provider is not available.", false)
		return
	}

	expiresAt := s.clk.Now().UTC().Add(federatedLoginStateTTL)
	if req.ExpiresAt.Before(expiresAt) {
		expiresAt = req.ExpiresAt
	}
	storeFederatedLoginState(oidcState, encodedReq, expiresAt)

	s.appendAudit(r.Context(), "auth.oauth2.authorize.federated_begin", "provider:"+providerID,
		store.OutcomeSuccess, "", map[string]string{"remote": remoteHost(r.RemoteAddr), "client_id": req.ClientID})

	http.Redirect(w, r, authURL, http.StatusFound)
}

// handleOAuthAuthorizeFederatedCallback implements
// GET /oauth2/authorize/federated/callback: the provider's redirect
// target. Recovers the oauth2/authorize request this login belongs to,
// completes the OIDC sign-in, and mints the same authorization code the
// password+TOTP leg mints -- redirecting to req.RedirectURI with
// ?code=&state= exactly like oauth2_native.go's POST handler.
func (s *Server) handleOAuthAuthorizeFederatedCallback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	if state == "" || code == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_request", "state and code are required", "")
		return
	}

	entry, ok := takeFederatedLoginState(state, s.clk.Now().UTC())
	if !ok {
		writeProblem(w, r, http.StatusBadRequest, "invalid_state",
			"sign-in request not recognised or already consumed; please restart sign-in", "")
		return
	}
	req, err := s.dir.DecodeAuthorizeRequest(entry.EncodedReq, s.clk.Now().UTC())
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid_request",
			"sign-in request expired or invalid; please restart sign-in", "")
		return
	}

	pid, err := s.rp.CompleteSignIn(r.Context(), state, code)
	if err != nil {
		s.auditAuthFailure(r, "auth.oauth2.authorize.federated", "state:"+state, 0, humanOIDCError(err))
		s.renderOAuthLoginForm(r.Context(), w, entry.EncodedReq, req.CSRFToken, humanOIDCError(err), false)
		return
	}

	authCode, err := s.dir.IssueAuthorizationCodeForFederatedPrincipal(r.Context(), pid, req)
	if err != nil {
		s.loggerFrom(r.Context()).Error("protoadmin.oauth2.federated_code_issue_failed", "err", err)
		s.renderOAuthLoginForm(r.Context(), w, entry.EncodedReq, req.CSRFToken, "sign-in failed; please try again", false)
		return
	}

	s.appendAudit(r.Context(), "auth.oauth2.authorize", fmt.Sprintf("principal:%d", pid), store.OutcomeSuccess, "",
		map[string]string{"remote": remoteHost(r.RemoteAddr), "client_id": req.ClientID, "method": "federated"})

	// Clear the CSRF cookie: the authorize request has been consumed,
	// same as the password+TOTP leg does on success.
	http.SetCookie(w, &http.Cookie{
		Name: oauthCSRFCookieName, Value: "", Path: "/oauth2/authorize",
		HttpOnly: true, Secure: s.opts.Session.SecureCookies, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})

	u, _ := url.Parse(req.RedirectURI)
	qq := u.Query()
	qq.Set("code", authCode)
	if req.State != "" {
		qq.Set("state", req.State)
	}
	u.RawQuery = qq.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// humanOIDCError maps a directoryoidc error to a terse user-facing
// string. Deliberately generic for ErrNotFound / ErrAutoProvisionRefused
// (mirroring directoryoidc's own anti-enumeration posture: neither
// "unlinked identity" nor "auto-provisioning refused" says which is
// true) and for ErrInvalidState (a used or expired OIDC state does not
// need to distinguish itself from any other sign-in failure).
func humanOIDCError(err error) string {
	switch {
	case errors.Is(err, directoryoidc.ErrNotFound), errors.Is(err, directoryoidc.ErrAutoProvisionRefused):
		return "This identity provider account is not linked to a herold account."
	case errors.Is(err, directoryoidc.ErrInvalidState):
		return "Sign-in request expired or was already used; please try again."
	default:
		return "Sign-in with this provider failed; please try again."
	}
}
