package directoryoidc

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
)

// PrincipalID re-exports store.PrincipalID for callers that only need
// this package.
type PrincipalID = store.PrincipalID

// ProviderID is the operator-visible handle for a configured OIDC
// provider. It is stable across restarts (equal to the provider's Name).
type ProviderID string

// Provider is the RP-side view of an OIDC provider registration.
// ClientSecret is deliberately not included here; secrets live in the
// store's ClientSecretRef and are resolved at Exchange time.
type Provider struct {
	ID          ProviderID
	Name        string
	IssuerURL   string
	ClientID    string
	RedirectURL string
	Scopes      []string
}

// ProviderConfig is the registration payload accepted by AddProvider.
type ProviderConfig struct {
	// Name is the operator-chosen short identifier.
	Name string
	// IssuerURL is the OIDC issuer URL; discovery is performed at
	// <IssuerURL>/.well-known/openid-configuration.
	IssuerURL string
	// ClientID / ClientSecret are the OAuth2 credentials.
	ClientID     string
	ClientSecret string
	// RedirectURL is the redirect_uri registered with the provider.
	RedirectURL string
	// Scopes is the OAuth2 scope list; "openid" is always included.
	Scopes []string
}

// Sentinel errors.
var (
	ErrNotFound                = errors.New("directoryoidc: not found")
	ErrConflict                = errors.New("directoryoidc: conflict")
	ErrInvalidState            = errors.New("directoryoidc: invalid state")
	ErrProviderDiscoveryFailed = errors.New("directoryoidc: provider discovery failed")
)

// defaultTimeout caps every HTTP call we issue (discovery, token
// exchange, JWKS fetch during verification).
const defaultTimeout = 10 * time.Second

// pendingTTL bounds how long an auth-URL state token is valid. 5 min
// matches the spec and comfortably exceeds a human-driven redirect.
const pendingTTL = 5 * time.Minute

// RP is the relying-party handle. It is safe for concurrent use.
type RP struct {
	meta   store.Metadata
	logger *slog.Logger
	http   *http.Client
	clk    clock.Clock

	mu       sync.Mutex
	pending  map[string]pendingAuth
	secrets  map[ProviderID]string // client_secret cache, keyed by provider ID
	discover map[ProviderID]*oidc.Provider
	configs  map[ProviderID]ProviderConfig
}

// pendingAuth tracks in-flight OAuth flows. Entries expire after
// pendingTTL.
type pendingAuth struct {
	providerID ProviderID
	// PrincipalID is 0 for sign-in flows; non-zero for link flows.
	principalID PrincipalID
	nonce       string
	createdAt   time.Time
}

// New returns an RP ready to serve requests.
func New(meta store.Metadata, logger *slog.Logger, httpClient *http.Client, clk clock.Clock) *RP {
	if logger == nil {
		logger = slog.Default()
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultTimeout}
	}
	if clk == nil {
		clk = clock.NewReal()
	}
	return &RP{
		meta:     meta,
		logger:   logger,
		http:     httpClient,
		clk:      clk,
		pending:  make(map[string]pendingAuth),
		secrets:  make(map[ProviderID]string),
		discover: make(map[ProviderID]*oidc.Provider),
		configs:  make(map[ProviderID]ProviderConfig),
	}
}

// AddProvider registers an OIDC provider. Discovery validates the
// issuer and caches the Provider handle for future exchanges.
//
// TODO(store): the current store.Metadata.InsertOIDCProvider persists
// only the ClientSecretRef (opaque string). We pass ClientID here but
// keep the cleartext ClientSecret in-process only. A proper secret
// resolver (REQ-NFR-100: no inline secrets in system.toml) lands
// alongside the admin-config wave; for Wave 1 we rely on the operator
// pointing ClientSecretRef at an out-of-band source.
func (r *RP) AddProvider(ctx context.Context, p ProviderConfig) (ProviderID, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if p.Name == "" || p.IssuerURL == "" || p.ClientID == "" {
		return "", fmt.Errorf("directoryoidc: missing required fields")
	}
	dctx, cancel := context.WithTimeout(oidc.ClientContext(ctx, r.http), defaultTimeout)
	defer cancel()
	prov, err := oidc.NewProvider(dctx, p.IssuerURL)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrProviderDiscoveryFailed, err)
	}
	if err := r.meta.InsertOIDCProvider(ctx, store.OIDCProvider{
		Name:            p.Name,
		IssuerURL:       p.IssuerURL,
		ClientID:        p.ClientID,
		ClientSecretRef: "inline:" + p.Name, // see TODO above.
		Scopes:          scopesWithOpenID(p.Scopes),
	}); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return "", fmt.Errorf("%w: provider %s", ErrConflict, p.Name)
		}
		return "", fmt.Errorf("directoryoidc: insert provider: %w", err)
	}
	id := ProviderID(p.Name)
	r.mu.Lock()
	r.discover[id] = prov
	r.secrets[id] = p.ClientSecret
	r.configs[id] = p
	r.mu.Unlock()
	r.logger.LogAttrs(ctx, slog.LevelInfo, "directoryoidc.provider.add",
		slog.String("name", p.Name),
		slog.String("issuer", p.IssuerURL))
	return id, nil
}

// ListProviders returns every registered provider.
//
// TODO(store): store.Metadata lacks a list method for OIDC providers.
// We return the in-memory configs for Wave 1; this is lost on restart
// unless AddProvider is called again (operator bootstrap always does).
func (r *RP) ListProviders(ctx context.Context) ([]Provider, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Provider, 0, len(r.configs))
	for id, cfg := range r.configs {
		out = append(out, Provider{
			ID:          id,
			Name:        cfg.Name,
			IssuerURL:   cfg.IssuerURL,
			ClientID:    cfg.ClientID,
			RedirectURL: cfg.RedirectURL,
			Scopes:      scopesWithOpenID(cfg.Scopes),
		})
	}
	return out, nil
}

// DeleteProvider removes a provider from the in-memory cache.
//
// TODO(store): store.Metadata has no DeleteOIDCProvider; the DB row
// remains until the Wave 2 method lands. The in-memory handle is
// cleared so subsequent Begin* calls return ErrNotFound.
func (r *RP) DeleteProvider(ctx context.Context, id ProviderID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.discover[id]; !ok {
		return fmt.Errorf("%w: provider %s", ErrNotFound, id)
	}
	delete(r.discover, id)
	delete(r.secrets, id)
	delete(r.configs, id)
	return nil
}

// BeginLink starts a link flow for an already-authenticated principal.
// The returned authURL is the OAuth2 redirect; the caller hands the
// state back on CompleteLink.
func (r *RP) BeginLink(ctx context.Context, pid PrincipalID, providerID ProviderID) (authURL, state string, err error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	prov, cfg, secret, ok := r.lookupProvider(providerID)
	if !ok {
		return "", "", fmt.Errorf("%w: provider %s", ErrNotFound, providerID)
	}
	st, nonce, err := r.newStateAndNonce()
	if err != nil {
		return "", "", err
	}
	r.mu.Lock()
	r.gcExpiredLocked()
	r.pending[st] = pendingAuth{
		providerID:  providerID,
		principalID: pid,
		nonce:       nonce,
		createdAt:   r.clk.Now(),
	}
	r.mu.Unlock()
	oauthCfg := oauth2Config(prov, cfg, secret)
	return oauthCfg.AuthCodeURL(st, oidc.Nonce(nonce)), st, nil
}

// BeginSignIn starts a sign-in flow (no prior principal). The caller
// hands state back on CompleteSignIn.
func (r *RP) BeginSignIn(ctx context.Context, providerID ProviderID) (authURL, state string, err error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	prov, cfg, secret, ok := r.lookupProvider(providerID)
	if !ok {
		return "", "", fmt.Errorf("%w: provider %s", ErrNotFound, providerID)
	}
	st, nonce, err := r.newStateAndNonce()
	if err != nil {
		return "", "", err
	}
	r.mu.Lock()
	r.gcExpiredLocked()
	r.pending[st] = pendingAuth{
		providerID: providerID,
		nonce:      nonce,
		createdAt:  r.clk.Now(),
	}
	r.mu.Unlock()
	oauthCfg := oauth2Config(prov, cfg, secret)
	return oauthCfg.AuthCodeURL(st, oidc.Nonce(nonce)), st, nil
}

// CompleteLink exchanges code for a token set, verifies the ID token,
// and stores the {principal_id, provider_id, sub} link.
func (r *RP) CompleteLink(ctx context.Context, state, code string) (PrincipalID, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	pending, err := r.takePending(state)
	if err != nil {
		return 0, err
	}
	if pending.principalID == 0 {
		return 0, fmt.Errorf("%w: state is for sign-in, not link", ErrInvalidState)
	}
	sub, _, err := r.exchangeAndVerify(ctx, pending, code)
	if err != nil {
		return 0, err
	}
	if err := r.meta.LinkOIDC(ctx, store.OIDCLink{
		PrincipalID:  pending.principalID,
		ProviderName: string(pending.providerID),
		Subject:      sub,
	}); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return 0, fmt.Errorf("%w: already linked", ErrConflict)
		}
		return 0, fmt.Errorf("directoryoidc: link: %w", err)
	}
	r.logger.LogAttrs(ctx, slog.LevelInfo, "directoryoidc.link",
		slog.String("provider", string(pending.providerID)),
		slog.Uint64("principal_id", uint64(pending.principalID)))
	return pending.principalID, nil
}

// CompleteSignIn exchanges code, verifies the ID token, looks up the
// (provider, sub) link, and returns the local principal ID. Returns
// ErrNotFound if the subject has never been linked.
func (r *RP) CompleteSignIn(ctx context.Context, state, code string) (PrincipalID, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	pending, err := r.takePending(state)
	if err != nil {
		return 0, err
	}
	if pending.principalID != 0 {
		return 0, fmt.Errorf("%w: state is for link, not sign-in", ErrInvalidState)
	}
	sub, _, err := r.exchangeAndVerify(ctx, pending, code)
	if err != nil {
		return 0, err
	}
	link, err := r.meta.LookupOIDCLink(ctx, string(pending.providerID), sub)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return 0, fmt.Errorf("%w: no link for sub", ErrNotFound)
		}
		return 0, fmt.Errorf("directoryoidc: lookup link: %w", err)
	}
	return link.PrincipalID, nil
}

// Unlink removes a principal's OIDC association.
//
// TODO(store): there is no UnlinkOIDC / DeleteOIDCLink method on
// store.Metadata. We cannot currently delete a link; the method
// returns an explanatory error so callers can surface it.
func (r *RP) Unlink(ctx context.Context, pid PrincipalID, providerID ProviderID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return errors.New("directoryoidc: Unlink not supported: store.Metadata lacks DeleteOIDCLink")
}

// VerifyAccessToken is the adaptor used by sasl.TokenVerifier: accept
// an opaque access token and map it to a local PrincipalID via the
// configured providers. The current flow validates the token as a JWT
// id_token against the provider's JWKS; operators who want opaque
// access-token introspection (RFC 7662) should extend this method.
//
// TODO: add opaque-token introspection (RFC 7662) when the second
// caller arrives.
func (r *RP) VerifyAccessToken(ctx context.Context, token string) (PrincipalID, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	r.mu.Lock()
	providers := make([]*oidc.Provider, 0, len(r.discover))
	names := make([]ProviderID, 0, len(r.discover))
	clientIDs := make([]string, 0, len(r.discover))
	for id, prov := range r.discover {
		providers = append(providers, prov)
		names = append(names, id)
		cfg := r.configs[id]
		clientIDs = append(clientIDs, cfg.ClientID)
	}
	r.mu.Unlock()
	for i, prov := range providers {
		verifier := prov.Verifier(&oidc.Config{ClientID: clientIDs[i]})
		idTok, err := verifier.Verify(oidc.ClientContext(ctx, r.http), token)
		if err != nil {
			continue
		}
		link, err := r.meta.LookupOIDCLink(ctx, string(names[i]), idTok.Subject)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			return 0, err
		}
		return link.PrincipalID, nil
	}
	return 0, fmt.Errorf("%w: no verifier accepted token", ErrNotFound)
}

// lookupProvider returns the go-oidc Provider, the registered config,
// and the (in-memory) client secret for id. ok is false when the
// provider is not registered.
func (r *RP) lookupProvider(id ProviderID) (*oidc.Provider, ProviderConfig, string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	prov, ok := r.discover[id]
	if !ok {
		return nil, ProviderConfig{}, "", false
	}
	cfg := r.configs[id]
	secret := r.secrets[id]
	return prov, cfg, secret, true
}

// takePending removes and returns the pending-auth entry for state.
func (r *RP) takePending(state string) (pendingAuth, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gcExpiredLocked()
	p, ok := r.pending[state]
	if !ok {
		return pendingAuth{}, fmt.Errorf("%w: unknown state", ErrInvalidState)
	}
	delete(r.pending, state)
	return p, nil
}

func (r *RP) gcExpiredLocked() {
	now := r.clk.Now()
	for s, p := range r.pending {
		if now.Sub(p.createdAt) > pendingTTL {
			delete(r.pending, s)
		}
	}
}

// exchangeAndVerify trades code for tokens against the configured
// provider and verifies the ID token's signature, issuer, audience,
// expiry, and nonce. Returns the subject and the raw ID token.
func (r *RP) exchangeAndVerify(ctx context.Context, p pendingAuth, code string) (sub, rawIDToken string, err error) {
	prov, cfg, secret, ok := r.lookupProvider(p.providerID)
	if !ok {
		return "", "", fmt.Errorf("%w: provider %s", ErrNotFound, p.providerID)
	}
	exCtx, cancel := context.WithTimeout(oidc.ClientContext(ctx, r.http), defaultTimeout)
	defer cancel()
	oauthCfg := oauth2Config(prov, cfg, secret)
	tok, err := oauthCfg.Exchange(exCtx, code)
	if err != nil {
		return "", "", fmt.Errorf("directoryoidc: exchange: %w", err)
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok || raw == "" {
		return "", "", fmt.Errorf("directoryoidc: missing id_token")
	}
	verifier := prov.Verifier(&oidc.Config{ClientID: cfg.ClientID})
	idTok, err := verifier.Verify(exCtx, raw)
	if err != nil {
		return "", "", fmt.Errorf("directoryoidc: verify id_token: %w", err)
	}
	if idTok.Nonce != p.nonce {
		return "", "", fmt.Errorf("%w: nonce mismatch", ErrInvalidState)
	}
	return idTok.Subject, raw, nil
}

// oauth2Config is a helper that assembles the oauth2.Config for a
// provider. Separated for readability.
func oauth2Config(prov *oidc.Provider, cfg ProviderConfig, secret string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: secret,
		Endpoint:     prov.Endpoint(),
		RedirectURL:  cfg.RedirectURL,
		Scopes:       scopesWithOpenID(cfg.Scopes),
	}
}

// newStateAndNonce returns fresh random state and nonce tokens.
func (r *RP) newStateAndNonce() (state, nonce string, err error) {
	state, err = randToken()
	if err != nil {
		return "", "", err
	}
	nonce, err = randToken()
	if err != nil {
		return "", "", err
	}
	return state, nonce, nil
}

func randToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// scopesWithOpenID ensures "openid" is present exactly once.
func scopesWithOpenID(in []string) []string {
	out := make([]string, 0, len(in)+1)
	seen := false
	for _, s := range in {
		if s == "openid" {
			seen = true
		}
		out = append(out, s)
	}
	if !seen {
		out = append([]string{"openid"}, out...)
	}
	return out
}
