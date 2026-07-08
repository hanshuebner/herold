package protoadmin

// oauth_init.go implements the server-mediated OAuth 2.0 start + callback
// for external SMTP submission credentials (REQ-MAIL-SUBMIT-02,
// REQ-AUTH-EXT-SUBMIT-03).
//
// Flow:
//  1. Suite calls POST /api/v1/identities/{id}/submission/oauth/start?provider=<name>
//  2. Handler validates the provider exists in OAuthProviders, generates a
//     PKCE code_verifier + code_challenge, stores a state token (16 random
//     bytes, 5-minute TTL) in oauthStateStore (including the identity_id), and
//     redirects the browser to the provider's auth_url with state + code_challenge.
//  3. Provider redirects back to the FIXED path
//     GET /api/v1/oauth/external-submission/callback?code=...&state=...
//     (no identity id in the URL — the callback recovers the identity id from
//     the state token, which allows a single redirect URI to be registered with
//     Google/Microsoft for all identities).
//  4. Handler validates the state token (not expired, matches provider),
//     exchanges the code at the provider's token_url using PKCE, seals the
//     resulting access + refresh tokens, runs the probe, persists on success,
//     and returns 204 (or redirects to the suite's settings URL when
//     ReturnURL is embedded in the state token).
//
// Multi-instance note (v1 limitation): the state store is in-memory.  A
// multi-instance deployment (multiple herold processes behind a load-balancer)
// will fail the callback if it lands on a different instance than the start
// request. Operators in that configuration must use sticky sessions or a
// shared state store. A future release should move oauthStateStore to the DB.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/hanshuebner/herold/internal/extsubmit"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/secrets"
	"github.com/hanshuebner/herold/internal/store"
)

// oauthStateEntry is one pending OAuth flow, keyed by the state token value.
type oauthStateEntry struct {
	// IdentityID is the JMAP Identity id the flow is for.
	IdentityID string
	// Provider is the normalised (lowercase) provider name.
	Provider string
	// CodeVerifier is the PKCE code_verifier. Stored here so the callback can
	// construct the code_verifier for the token exchange.
	CodeVerifier string
	// ExpiresAt is when this entry is considered invalid.
	ExpiresAt time.Time
	// ReturnURL, when non-empty, is the suite URL to redirect to on success.
	ReturnURL string
}

// oauthStateStore is the server-wide in-memory state store.
// Protected by stateMu. Entries are lazily GC'd on lookup.
//
// v1 limitation: in-memory only. See package comment above.
var (
	stateMu    sync.Mutex
	stateStore = map[string]oauthStateEntry{}
)

// storeOAuthState inserts a new state entry and returns the opaque state
// token (base64url-encoded 16 random bytes).
func storeOAuthState(entry oauthStateEntry) (string, error) {
	var b [16]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		return "", fmt.Errorf("oauth_state: generate: %w", err)
	}
	tok := base64.RawURLEncoding.EncodeToString(b[:])
	stateMu.Lock()
	stateStore[tok] = entry
	stateMu.Unlock()
	return tok, nil
}

// lookupOAuthState retrieves and removes a state entry. Returns an error
// when the token is unknown or expired.
func lookupOAuthState(tok string, now time.Time) (oauthStateEntry, error) {
	stateMu.Lock()
	defer stateMu.Unlock()
	// Lazy GC: sweep expired entries on every lookup.
	for k, v := range stateStore {
		if now.After(v.ExpiresAt) {
			delete(stateStore, k)
		}
	}
	e, ok := stateStore[tok]
	if !ok {
		return oauthStateEntry{}, errors.New("state token not found or expired")
	}
	delete(stateStore, tok)
	return e, nil
}

// generatePKCE returns a (verifier, challenge) pair using S256 as the
// code_challenge_method (RFC 7636 §4.2).
func generatePKCE() (verifier, challenge string, err error) {
	var b [32]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		return "", "", fmt.Errorf("pkce: generate verifier: %w", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(b[:])
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

// handleOAuthStart implements
// POST /api/v1/identities/{id}/submission/oauth/start?provider=<name>
//
// Required query parameter: provider (must be a key in OAuthProviders).
// Optional query parameter: return_url (the suite settings URL to redirect to
// after the callback completes).
//
// Gated by requireSelfOnly: the caller must own the identity.
func (s *Server) handleOAuthStart(w http.ResponseWriter, r *http.Request) {
	identityID := r.PathValue("id")
	if identityID == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id", "identity id is required", "")
		return
	}
	caller, _ := principalFrom(r.Context())

	ownerID, err := resolveIdentityOwner(r.Context(), s.store.Meta(), identityID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeProblem(w, r, http.StatusNotFound, "not_found", "identity not found", identityID)
		} else {
			s.writeStoreError(w, r, err)
		}
		return
	}
	if !requireSelfOnly(w, r, caller, ownerID) {
		return
	}

	providerName := strings.ToLower(r.URL.Query().Get("provider"))
	if providerName == "" {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"provider query parameter is required", "")
		return
	}

	prov, ok := s.opts.OAuthProviders[providerName]
	if !ok {
		writeProblem(w, r, http.StatusServiceUnavailable, "oauth_provider_not_configured",
			fmt.Sprintf("OAuth provider %q is not configured; add a [server.oauth_providers.%s] block to system.toml", providerName, providerName), "")
		return
	}
	if prov.ClientSecret == "" {
		// The client secret reference resolved to an empty string at boot.
		// Surface as 503 so the user gets a clear error rather than an
		// OAuth error from the provider.
		writeProblem(w, r, http.StatusServiceUnavailable, "oauth_provider_not_configured",
			fmt.Sprintf("OAuth provider %q client secret is not available; check [server.oauth_providers.%s].client_secret_ref", providerName, providerName), "")
		return
	}

	verifier, challenge, err := generatePKCE()
	if err != nil {
		s.loggerFrom(r.Context()).Error("protoadmin.oauth_start.pkce_failed",
			"activity", observe.ActivityInternal, "err", err)
		writeProblem(w, r, http.StatusInternalServerError, "internal_error", "failed to generate PKCE", "")
		return
	}

	returnURL := r.URL.Query().Get("return_url")
	stateTok, err := storeOAuthState(oauthStateEntry{
		IdentityID:   identityID,
		Provider:     providerName,
		CodeVerifier: verifier,
		ExpiresAt:    s.clk.Now().Add(5 * time.Minute),
		ReturnURL:    returnURL,
	})
	if err != nil {
		s.loggerFrom(r.Context()).Error("protoadmin.oauth_start.state_failed",
			"activity", observe.ActivityInternal, "err", err)
		writeProblem(w, r, http.StatusInternalServerError, "internal_error", "failed to generate state token", "")
		return
	}

	// Build the redirect URL.
	authURL, _ := url.Parse(prov.AuthURL)
	q := authURL.Query()
	q.Set("client_id", prov.ClientID)
	q.Set("response_type", "code")
	q.Set("state", stateTok)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("scope", strings.Join(prov.Scopes, " "))
	// Provider-specific extra parameters (re #131). For Google/Gmail this is
	// access_type=offline + prompt=consent; operators configure them in
	// [server.oauth_providers.<name>.extra_auth_params]. Applied before
	// redirect_uri so provider-specific params cannot shadow the standard ones.
	for k, v := range prov.ExtraAuthParams {
		q.Set(k, v)
	}
	// The redirect_uri is the fixed callback endpoint. The identity id is
	// carried in the state token, not in the URL, so operators only register
	// one redirect URI with their OAuth provider.
	callbackURL := buildCallbackURL(r)
	q.Set("redirect_uri", callbackURL)
	authURL.RawQuery = q.Encode()

	// When the caller signals it can accept JSON (i.e. a fetch call carrying
	// X-CSRF-Token), return the auth URL in the response body so the client
	// can navigate there via window.location.href without a form submit.
	// A native form submit cannot carry custom request headers, so it cannot
	// satisfy the CSRF middleware; this JSON path is the correct fix for
	// cookie-authenticated callers (REQ-AUTH-CSRF).
	//
	// When Accept does not include application/json the handler falls back to
	// the original 302 redirect so that Bearer-authenticated API callers and
	// existing test harnesses continue to work unchanged.
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(struct {
			AuthURL string `json:"auth_url"`
		}{AuthURL: authURL.String()})
		return
	}

	http.Redirect(w, r, authURL.String(), http.StatusFound)
}

// buildCallbackURL constructs the absolute callback URI for the OAuth flow
// from the inbound request. Uses X-Forwarded-Host / Host fallback.
//
// The returned URL is the FIXED path /api/v1/oauth/external-submission/callback
// — no identity id appears in the URL. The identity id travels in the opaque
// state token so that operators can register a single redirect URI with OAuth
// providers (Google, Microsoft) that performs exact-match validation.
func buildCallbackURL(r *http.Request) string {
	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	return fmt.Sprintf("%s://%s/api/v1/oauth/external-submission/callback", scheme, host)
}

// tokenResponse is the subset of the OAuth 2.0 token response we care about.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresIn    int    `json:"expires_in,omitempty"`
	TokenType    string `json:"token_type"`
}

// exchangeCode performs the OAuth 2.0 token exchange at tokenURL and returns
// the parsed token response.
func exchangeCode(ctx context.Context, httpClient *http.Client, tokenURL, clientID, clientSecret, code, verifier, redirectURI string) (*tokenResponse, error) {
	body := url.Values{}
	body.Set("grant_type", "authorization_code")
	body.Set("code", code)
	body.Set("client_id", clientID)
	body.Set("client_secret", clientSecret)
	body.Set("code_verifier", verifier)
	body.Set("redirect_uri", redirectURI)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(body.Encode()))
	if err != nil {
		return nil, fmt.Errorf("token exchange: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token exchange: POST: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return nil, fmt.Errorf("token exchange: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange: provider returned %d: %s", resp.StatusCode, raw)
	}
	var tr tokenResponse
	if err := json.Unmarshal(raw, &tr); err != nil {
		return nil, fmt.Errorf("token exchange: decode: %w", err)
	}
	if tr.AccessToken == "" {
		return nil, fmt.Errorf("token exchange: provider returned no access_token")
	}
	return &tr, nil
}

// handleOAuthCallback implements
// GET /api/v1/oauth/external-submission/callback?code=...&state=...
//
// Validates state, exchanges the code, seals the tokens, runs the probe,
// and persists on success. Returns 204 or redirects to ReturnURL.
//
// The identity id is recovered from the opaque state token (stored there by
// handleOAuthStart). No identity id appears in this URL — a single fixed
// redirect URI is registered with OAuth providers so exact-match validation
// works across all identities.
//
// Intentionally unauthenticated (re #95): the browser arrives here via a
// cross-site top-level redirect from the OAuth provider. Session cookies
// with SameSite=Strict are not sent on such navigations, so a requireAuth
// gate would reject every real callback before the code exchange runs.
//
// Authorization comes from the opaque state token alone (CSPRNG, 128 bits,
// single-use, 5-min TTL, bound to a specific IdentityID) — the same trust
// model used by POST /api/v1/oidc/callback. The start endpoint
// (handleOAuthStart) retains its auth+requireSelfOnly gate, so only the
// owning principal can ever generate a state token for a given identity.
func (s *Server) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	stateTok := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")

	if stateTok == "" || code == "" {
		writeProblem(w, r, http.StatusBadRequest, "oauth_state_invalid",
			"state and code query parameters are required", "")
		return
	}

	entry, err := lookupOAuthState(stateTok, s.clk.Now())
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, "oauth_state_invalid",
			"OAuth state token is invalid, expired, or already consumed", "")
		return
	}

	// Recover the identity from the state token. Ownership is established by
	// the fact that only handleOAuthStart (which is auth1+requireSelfOnly
	// gated) can create a valid state token for this IdentityID.
	identityID := entry.IdentityID
	identity, err := resolveIdentity(r.Context(), s.store.Meta(), identityID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeProblem(w, r, http.StatusNotFound, "not_found", "identity not found", identityID)
		} else {
			s.writeStoreError(w, r, err)
		}
		return
	}
	ownerID := identity.PrincipalID

	prov, ok := s.opts.OAuthProviders[entry.Provider]
	if !ok {
		writeProblem(w, r, http.StatusServiceUnavailable, "oauth_provider_not_configured",
			fmt.Sprintf("OAuth provider %q is no longer configured", entry.Provider), "")
		return
	}
	if prov.ClientSecret == "" {
		writeProblem(w, r, http.StatusServiceUnavailable, "oauth_provider_not_configured",
			fmt.Sprintf("OAuth provider %q client secret is not available", entry.Provider), "")
		return
	}

	dataKey := s.opts.ExternalSubmissionDataKey
	if len(dataKey) == 0 {
		writeProblem(w, r, http.StatusServiceUnavailable, "not_configured",
			"external submission is not configured on this server", "")
		return
	}

	redirectURI := buildCallbackURL(r)
	httpClient := &http.Client{Timeout: 30 * time.Second}
	tr, err := exchangeCode(r.Context(), httpClient, prov.TokenURL,
		prov.ClientID, prov.ClientSecret, code, entry.CodeVerifier, redirectURI)
	if err != nil {
		s.loggerFrom(r.Context()).Warn("protoadmin.oauth_callback.exchange_failed",
			"activity", observe.ActivityAudit,
			"provider", entry.Provider,
			"identity_id", identityID,
			"err", err)
		writeProblem(w, r, http.StatusBadGateway, "oauth_exchange_failed",
			"token exchange with provider failed", err.Error())
		return
	}

	// Seal tokens.
	atCT, err := secrets.Seal(dataKey, []byte(tr.AccessToken))
	if err != nil {
		s.loggerFrom(r.Context()).Error("protoadmin.oauth_callback.seal_failed",
			"activity", observe.ActivityInternal, "err", err)
		writeProblem(w, r, http.StatusInternalServerError, "internal_error", "failed to seal access token", "")
		return
	}

	// Endpoint defaults come from the provider's well-known SMTP host. When an
	// external submission is already configured for this identity, this is a
	// re-authentication (re #70): keep the existing endpoint and refresh only
	// the token material, so a custom or non-well-known host survives the
	// re-auth rather than being reset to the provider default.
	submitHost := providerSMTPHost(entry.Provider)
	submitPort := providerSMTPPort(entry.Provider)
	submitSecurity := "implicit_tls"
	if existing, gerr := s.store.Meta().GetIdentitySubmission(r.Context(), identityID); gerr == nil && existing.SubmitHost != "" {
		submitHost = existing.SubmitHost
		submitPort = existing.SubmitPort
		submitSecurity = existing.SubmitSecurity
	}

	sub := store.IdentitySubmission{
		IdentityID:         identityID,
		SubmitHost:         submitHost,
		SubmitPort:         submitPort,
		SubmitSecurity:     submitSecurity,
		SubmitAuthMethod:   "oauth2",
		OAuthAccessCT:      atCT,
		OAuthTokenEndpoint: prov.TokenURL,
		// Use identity.Email as the XOAUTH2 user identifier: this is the
		// address the OAuth grant is for. The session principal is not
		// available here (unauthenticated callback, re #95).
		OAuthClientID: identity.Email,
		State:         store.IdentitySubmissionStateOK,
	}

	if tr.RefreshToken != "" {
		rtCT, err := secrets.Seal(dataKey, []byte(tr.RefreshToken))
		if err != nil {
			s.loggerFrom(r.Context()).Error("protoadmin.oauth_callback.seal_failed",
				"activity", observe.ActivityInternal, "err", err)
			writeProblem(w, r, http.StatusInternalServerError, "internal_error", "failed to seal refresh token", "")
			return
		}
		sub.OAuthRefreshCT = rtCT
	}

	if tr.ExpiresIn > 0 {
		sub.OAuthExpiresAt = s.clk.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
		// Refresh extsubmit.RefreshLeadTime before expiry. Using the shared
		// constant keeps this call site consistent with the sweeper refresh
		// path in extsubmit.Refresher.Refresh (REQ-AUTH-EXT-SUBMIT-05).
		sub.RefreshDue = sub.OAuthExpiresAt.Add(-extsubmit.RefreshLeadTime)
	}

	// Run probe before persisting.
	probeOutcome := s.opts.ExternalProbe(r.Context(), sub, identity.Email)
	if probeOutcome.State != extsubmit.OutcomeOK {
		s.appendAudit(r.Context(), "submission.external.failure",
			fmt.Sprintf("identity:%s", identityID),
			store.OutcomeFailure,
			fmt.Sprintf("oauth callback probe failed: %s", probeOutcome.Diagnostic),
			map[string]string{
				"category":    string(probeOutcome.State),
				"provider":    entry.Provider,
				"auth_method": "oauth2",
			})
		writeProbeFailed(w, r, probeOutcome)
		return
	}

	if err := s.store.Meta().UpsertIdentitySubmission(r.Context(), sub); err != nil {
		s.writeStoreError(w, r, err)
		return
	}

	// Mark the identity verified: the OAuth grant proves ownership of the
	// email address, so verified_at_us must be set atomically with the
	// submission record. Without this call, verified_at_us stays NULL and
	// the identity reads as unverified on every subsequent JMAP fetch
	// (re #99, acceptance criterion from #92).
	if err := s.store.Meta().MarkIdentityVerified(r.Context(), identityID); err != nil {
		s.loggerFrom(r.Context()).Error("protoadmin.oauth_callback.mark_verified_failed",
			"activity", observe.ActivityInternal, "err", err,
			"identity_id", identityID)
		s.writeStoreError(w, r, err)
		return
	}

	// Trigger retry for any submissions parked during the broken auth
	// window. The call is best-effort: individual submission failures are
	// logged inside RetryForIdentity and never prevent the 204 response
	// (re #70, REQ-AUTH-EXT-SUBMIT-05).
	if s.opts.ExternalRetryer != nil {
		s.opts.ExternalRetryer.RetryForIdentity(r.Context(), identityID, sub)
	}

	s.appendAudit(r.Context(), "identity.submission.set",
		fmt.Sprintf("identity:%s", identityID),
		store.OutcomeSuccess, "",
		map[string]string{
			"principal_id": fmt.Sprintf("%d", ownerID),
			"identity_id":  identityID,
			"auth_method":  "oauth2",
			"provider":     entry.Provider,
		})

	if entry.ReturnURL != "" {
		http.Redirect(w, r, entry.ReturnURL, http.StatusFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// providerSMTPHost returns the well-known SMTP submission host for
// recognised provider names. Falls back to an empty string for unknown
// providers (caller must supply SubmitHost separately in that case).
func providerSMTPHost(provider string) string {
	switch provider {
	case "gmail":
		return "smtp.gmail.com"
	case "m365":
		return "smtp.office365.com"
	}
	return ""
}

// providerSMTPPort returns the well-known SMTP submission port.
func providerSMTPPort(provider string) int {
	switch provider {
	case "gmail", "m365":
		return 465
	}
	return 587
}
