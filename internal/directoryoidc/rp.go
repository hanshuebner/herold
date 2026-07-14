package directoryoidc

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/mail"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/hanshuebner/herold/internal/authz"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/observe"
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
	ID                  ProviderID
	Name                string
	IssuerURL           string
	ClientID            string
	RedirectURL         string
	Scopes              []string
	AutoProvision       bool
	AutoProvisionDomain string
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
	// AutoProvision opts this provider into first-login auto-provisioning
	// (REQ-AUTH-56). Off by default; an operator must set this explicitly.
	AutoProvision bool
	// AutoProvisionDomain is the local domain a principal auto-provisioned
	// by this provider is created under. Required for AutoProvision to
	// provision anything (see store.OIDCProvider.AutoProvisionDomain).
	AutoProvisionDomain string
}

// PrincipalProvisioner creates a fully usable local principal (default
// mailboxes, address book) for OIDC first-login auto-provisioning
// (REQ-AUTH-56). Implemented by *internal/directory.Directory
// (CreateExternalPrincipal); injected via SetProvisioner rather than a
// New parameter so the many existing New callers are unaffected. A nil
// provisioner (the default) makes auto-provisioning fail closed even if
// a provider's AutoProvision flag is set -- wiring the capability in is
// an explicit, separate step from enabling it per provider.
type PrincipalProvisioner interface {
	CreateExternalPrincipal(ctx context.Context, email, displayName string) (PrincipalID, error)
}

// Sentinel errors.
var (
	ErrNotFound                = errors.New("directoryoidc: not found")
	ErrConflict                = errors.New("directoryoidc: conflict")
	ErrInvalidState            = errors.New("directoryoidc: invalid state")
	ErrProviderDiscoveryFailed = errors.New("directoryoidc: provider discovery failed")

	// ErrAutoProvisionRefused is returned by CompleteSignIn when a first
	// login for an unlinked sub is eligible for auto-provisioning
	// (REQ-AUTH-56, the provider has AutoProvision set) but cannot be
	// completed: the IdP's email claim is missing or not
	// `email_verified`, the provider has no AutoProvisionDomain
	// configured, or the derived address collides with an existing
	// principal or alias. Deliberately distinct from ErrNotFound (which
	// covers "no link and auto-provisioning is off for this provider" --
	// REQ-AUTH-56's plain rejection) so a genuine collision never reads
	// as a generic 404 to a caller deciding how to react -- but the
	// message itself stays generic (no email/collision detail) so an
	// unauthenticated caller cannot use it to probe which addresses
	// exist.
	ErrAutoProvisionRefused = errors.New("directoryoidc: auto-provisioning refused")
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

	mu          sync.Mutex
	pending     map[string]pendingAuth
	secrets     map[ProviderID]string // client_secret cache, keyed by provider ID
	discover    map[ProviderID]*oidc.Provider
	configs     map[ProviderID]ProviderConfig
	provisioner PrincipalProvisioner
}

// pendingAuth tracks in-flight OAuth flows. Entries expire after
// pendingTTL.
type pendingAuth struct {
	providerID ProviderID
	// PrincipalID is 0 for sign-in flows; non-zero for link flows.
	principalID PrincipalID
	nonce       string
	createdAt   time.Time
	// redirectOverride, when non-empty, is the redirect_uri BeginSignInAt
	// used for this flow instead of the provider's configured
	// RedirectURL (issue #238). exchangeAndVerify must send the same
	// redirect_uri at the token endpoint that was used at the authorize
	// endpoint -- providers that enforce exact-match redirect_uri
	// consistency (most do) reject the exchange otherwise.
	redirectOverride string
}

// FlowKind reports whether a pending state was opened by BeginLink or
// BeginSignIn. Callers (e.g. the admin OIDC callback) read this from
// PeekPending so they can dispatch to the correct completion without
// consuming the state.
type FlowKind int

const (
	// FlowKindUnknown is the zero value; returned for unknown states.
	FlowKindUnknown FlowKind = iota
	// FlowKindLink is a link-existing-principal flow.
	FlowKindLink
	// FlowKindSignIn is a sign-in flow (no prior principal).
	FlowKindSignIn
)

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

// SetProvisioner wires the capability CompleteSignIn needs to actually
// create a principal on an auto-provisioning login (REQ-AUTH-56).
// Production wiring calls this once at startup with the running
// *directory.Directory; tests that do not exercise auto-provisioning
// never need it (the zero-value RP correctly refuses auto-provisioning
// regardless of any provider's AutoProvision flag). Not safe to call
// concurrently with CompleteSignIn calls that are actively
// auto-provisioning; callers set it once during startup before serving
// traffic.
func (r *RP) SetProvisioner(p PrincipalProvisioner) {
	r.mu.Lock()
	r.provisioner = p
	r.mu.Unlock()
}

// SetAutoProvision flips a provider's auto-provisioning opt-in and its
// target local domain in one call (REQ-AUTH-56). Returns ErrNotFound for
// an unregistered provider. Callers are responsible for whatever
// operator-authorization check gates this (protoadmin requires an admin
// caller); the store performs none of its own, matching
// SetOIDCProviderAuthzTrusted's posture.
func (r *RP) SetAutoProvision(ctx context.Context, providerID ProviderID, enabled bool, domain string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.meta.SetOIDCProviderAutoProvision(ctx, string(providerID), enabled, domain); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("%w: provider %s", ErrNotFound, providerID)
		}
		return fmt.Errorf("directoryoidc: set auto-provision: %w", err)
	}
	r.mu.Lock()
	if cfg, ok := r.configs[providerID]; ok {
		cfg.AutoProvision = enabled
		cfg.AutoProvisionDomain = domain
		r.configs[providerID] = cfg
	}
	r.mu.Unlock()
	r.logger.LogAttrs(ctx, slog.LevelInfo, "directoryoidc.provider.set_auto_provision",
		slog.String("activity", observe.ActivityAudit),
		slog.String("provider", string(providerID)),
		slog.Bool("enabled", enabled),
		slog.String("domain", domain))
	return nil
}

// AddProvider registers an OIDC provider. Discovery validates the
// issuer and caches the Provider handle for future exchanges. The
// cleartext ClientSecret stays in process memory only; operators who
// want secret resolution (REQ-NFR-100) point ClientSecretRef at an
// out-of-band source.
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
		Name:                p.Name,
		IssuerURL:           p.IssuerURL,
		ClientID:            p.ClientID,
		ClientSecretRef:     "inline:" + p.Name,
		Scopes:              scopesWithOpenID(p.Scopes),
		AutoProvision:       p.AutoProvision,
		AutoProvisionDomain: p.AutoProvisionDomain,
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
		slog.String("activity", observe.ActivityAudit),
		slog.String("name", p.Name),
		slog.String("issuer", p.IssuerURL))
	return id, nil
}

// ListProviders returns every registered provider, reading from the
// persistent store. The in-memory RedirectURL / Scopes cache is
// consulted for fields not persisted on the store row (RedirectURL
// lives with the RP, not the provider record).
func (r *RP) ListProviders(ctx context.Context) ([]Provider, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := r.meta.ListOIDCProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("directoryoidc: list providers: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Provider, 0, len(rows))
	for _, row := range rows {
		id := ProviderID(row.Name)
		cfg := r.configs[id]
		out = append(out, Provider{
			ID:                  id,
			Name:                row.Name,
			IssuerURL:           row.IssuerURL,
			ClientID:            row.ClientID,
			RedirectURL:         cfg.RedirectURL,
			Scopes:              scopesWithOpenID(row.Scopes),
			AutoProvision:       row.AutoProvision,
			AutoProvisionDomain: row.AutoProvisionDomain,
		})
	}
	return out, nil
}

// DeleteProvider removes a provider from the store (cascading its
// oidc_links rows) and clears the RP's in-memory handles so subsequent
// Begin* calls return ErrNotFound.
func (r *RP) DeleteProvider(ctx context.Context, id ProviderID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.meta.DeleteOIDCProvider(ctx, store.OIDCProviderID(id)); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("%w: provider %s", ErrNotFound, id)
		}
		return fmt.Errorf("directoryoidc: delete provider: %w", err)
	}
	r.mu.Lock()
	delete(r.discover, id)
	delete(r.secrets, id)
	delete(r.configs, id)
	r.mu.Unlock()
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
	authURL = oauthCfg.AuthCodeURL(st, oidc.Nonce(nonce))
	r.logger.LogAttrs(ctx, slog.LevelInfo, "directoryoidc.redirect",
		slog.String("activity", observe.ActivityAudit),
		slog.String("flow", "link"),
		slog.String("issuer", cfg.IssuerURL),
		slog.Uint64("principal_id", uint64(pid)),
	)
	return authURL, st, nil
}

// BeginSignIn starts a sign-in flow (no prior principal). The caller
// hands state back on CompleteSignIn. The provider's configured
// RedirectURL is used; callers that need a different callback path for
// this one flow (issue #238) use BeginSignInAt instead.
func (r *RP) BeginSignIn(ctx context.Context, providerID ProviderID) (authURL, state string, err error) {
	return r.beginSignIn(ctx, providerID, "")
}

// BeginSignInAt is BeginSignIn with the provider's configured
// RedirectURL overridden for this one flow. Issue #238: the
// /oauth2/authorize native-client login page's federated leg returns to
// its own callback (GET /oauth2/authorize/federated/callback), not the
// provider's default self-service-link callback -- both are valid
// completions of the same registered provider, so the IdP-side client
// registration must allow both redirect URIs (an operational note for
// the provider registration, not something this package enforces).
func (r *RP) BeginSignInAt(ctx context.Context, providerID ProviderID, redirectURL string) (authURL, state string, err error) {
	if redirectURL == "" {
		return "", "", fmt.Errorf("%w: redirectURL required", ErrInvalidState)
	}
	return r.beginSignIn(ctx, providerID, redirectURL)
}

func (r *RP) beginSignIn(ctx context.Context, providerID ProviderID, redirectOverride string) (authURL, state string, err error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	prov, cfg, secret, ok := r.lookupProvider(providerID)
	if !ok {
		return "", "", fmt.Errorf("%w: provider %s", ErrNotFound, providerID)
	}
	if redirectOverride != "" {
		cfg.RedirectURL = redirectOverride
	}
	st, nonce, err := r.newStateAndNonce()
	if err != nil {
		return "", "", err
	}
	r.mu.Lock()
	r.gcExpiredLocked()
	r.pending[st] = pendingAuth{
		providerID:       providerID,
		nonce:            nonce,
		createdAt:        r.clk.Now(),
		redirectOverride: redirectOverride,
	}
	r.mu.Unlock()
	oauthCfg := oauth2Config(prov, cfg, secret)
	authURL = oauthCfg.AuthCodeURL(st, oidc.Nonce(nonce))
	r.logger.LogAttrs(ctx, slog.LevelInfo, "directoryoidc.redirect",
		slog.String("activity", observe.ActivityAudit),
		slog.String("flow", "sign-in"),
		slog.String("issuer", cfg.IssuerURL),
	)
	return authURL, st, nil
}

// CompleteLink exchanges code for a token set, verifies the ID token,
// and stores the {principal_id, provider_id, sub} link.
func (r *RP) CompleteLink(ctx context.Context, state, code string) (PrincipalID, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	pending, err := r.takePending(state)
	if err != nil {
		r.logger.LogAttrs(ctx, slog.LevelWarn, "directoryoidc.callback.invalid_state",
			slog.String("activity", observe.ActivityAudit),
			slog.String("flow", "link"),
			slog.String("err", err.Error()),
		)
		return 0, err
	}
	if pending.principalID == 0 {
		return 0, fmt.Errorf("%w: state is for sign-in, not link", ErrInvalidState)
	}
	r.logger.LogAttrs(ctx, slog.LevelInfo, "directoryoidc.callback",
		slog.String("activity", observe.ActivityAudit),
		slog.String("flow", "link"),
		slog.String("provider", string(pending.providerID)),
		slog.Uint64("principal_id", uint64(pending.principalID)),
	)
	sub, _, err := r.exchangeAndVerify(ctx, pending, code)
	if err != nil {
		r.logger.LogAttrs(ctx, slog.LevelWarn, "directoryoidc.callback.failed",
			slog.String("activity", observe.ActivityAudit),
			slog.String("flow", "link"),
			slog.String("provider", string(pending.providerID)),
			slog.Uint64("principal_id", uint64(pending.principalID)),
			slog.String("err", err.Error()),
		)
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
		slog.String("activity", observe.ActivityAudit),
		slog.String("provider", string(pending.providerID)),
		slog.Uint64("principal_id", uint64(pending.principalID)),
	)
	return pending.principalID, nil
}

// CompleteSignIn exchanges code, verifies the ID token, and resolves the
// (provider, sub) link to a local principal. When no link exists yet and
// the provider has opted into first-login auto-provisioning
// (REQ-AUTH-56, store.OIDCProvider.AutoProvision), it provisions a new
// principal and links it instead of failing; a subsequent sign-in for
// the same sub then resolves that principal like any other linked
// identity (LookupOIDCLink finds it and this method never re-enters
// auto-provisioning for it). Returns ErrNotFound when the subject has
// never been linked and auto-provisioning is off (or unreachable --
// SetProvisioner was never called) for this provider -- the same error
// REQ-AUTH-56 already specified for the "off" case, deliberately
// indistinguishable from "there is no such user" either way. Returns
// ErrAutoProvisionRefused when auto-provisioning is on but this specific
// login cannot complete it (unverified email claim, no
// AutoProvisionDomain configured, or the derived address collides with
// an existing principal or alias -- REQ #230's account-takeover guard).
func (r *RP) CompleteSignIn(ctx context.Context, state, code string) (PrincipalID, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	pending, err := r.takePending(state)
	if err != nil {
		r.logger.LogAttrs(ctx, slog.LevelWarn, "directoryoidc.callback.invalid_state",
			slog.String("activity", observe.ActivityAudit),
			slog.String("flow", "sign-in"),
			slog.String("err", err.Error()),
		)
		return 0, err
	}
	if pending.principalID != 0 {
		return 0, fmt.Errorf("%w: state is for link, not sign-in", ErrInvalidState)
	}
	r.logger.LogAttrs(ctx, slog.LevelInfo, "directoryoidc.callback",
		slog.String("activity", observe.ActivityAudit),
		slog.String("flow", "sign-in"),
		slog.String("provider", string(pending.providerID)),
	)
	sub, claims, err := r.exchangeAndVerify(ctx, pending, code)
	if err != nil {
		r.logger.LogAttrs(ctx, slog.LevelWarn, "directoryoidc.callback.failed",
			slog.String("activity", observe.ActivityAudit),
			slog.String("flow", "sign-in"),
			slog.String("provider", string(pending.providerID)),
			slog.String("err", err.Error()),
		)
		return 0, err
	}
	link, err := r.meta.LookupOIDCLink(ctx, string(pending.providerID), sub)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return r.autoProvisionAndSignIn(ctx, pending, sub, claims)
		}
		return 0, fmt.Errorf("directoryoidc: lookup link: %w", err)
	}
	r.logger.LogAttrs(ctx, slog.LevelInfo, "directoryoidc.sign_in",
		slog.String("activity", observe.ActivityAudit),
		slog.String("provider", string(pending.providerID)),
		slog.Uint64("principal_id", uint64(link.PrincipalID)),
	)
	r.reconcileAfterSignIn(ctx, pending.providerID, link.PrincipalID, claims, false)
	return link.PrincipalID, nil
}

// reconcileAfterSignIn runs the claim-to-grant reconciliation pass
// (epic #188, REQ-AC-62) shared by the existing-link and
// auto-provisioning sign-in paths. isNewPrincipal is REQ-AC-69's bar:
// true on an auto-provisioning login withholds any administrative-level
// mapped grant on this pass (applied on the principal's next, non-
// provisioning, login instead). A reconciliation failure never fails the
// sign-in itself (REQ-AC-70: a rules-store outage neither escalates nor
// locks out) -- it is logged and the principal's existing idp: grants
// are left exactly as ReconcileIdP found them.
func (r *RP) reconcileAfterSignIn(ctx context.Context, providerID ProviderID, pid PrincipalID, claims map[string]any, isNewPrincipal bool) {
	p, perr := r.meta.GetPrincipalByID(ctx, pid)
	if perr != nil {
		r.logger.LogAttrs(ctx, slog.LevelWarn, "directoryoidc.reconcile_idp.principal_lookup_failed",
			slog.String("activity", observe.ActivityAudit),
			slog.String("provider", string(providerID)),
			slog.Uint64("principal_id", uint64(pid)),
			slog.String("err", perr.Error()),
		)
		return
	}
	if _, _, rerr := authz.ReconcileIdP(ctx, r.meta, r.clk, p, string(providerID), claims, isNewPrincipal); rerr != nil {
		r.logger.LogAttrs(ctx, slog.LevelWarn, "directoryoidc.reconcile_idp.failed",
			slog.String("activity", observe.ActivityAudit),
			slog.String("provider", string(providerID)),
			slog.Uint64("principal_id", uint64(pid)),
			slog.String("err", rerr.Error()),
		)
	}
}

// currentProvisioner returns the RP's configured PrincipalProvisioner
// (nil if SetProvisioner was never called).
func (r *RP) currentProvisioner() PrincipalProvisioner {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.provisioner
}

// autoProvisionAndSignIn implements the REQ-AUTH-56 first-login path:
// sub has no existing link. It provisions and links a new principal when
// the provider has opted in and every safety check passes, or refuses
// with ErrNotFound (opted out / not wired up -- indistinguishable from
// "no such user") or ErrAutoProvisionRefused (opted in but this login
// cannot complete it).
//
// Policy, in order:
//
//  1. The provider must have AutoProvision set (REQ-AUTH-56 opt-in,
//     off by default) and the RP must have a PrincipalProvisioner wired
//     in (SetProvisioner) -- an operator can enable the flag without the
//     capability being live, which must fail closed, not panic or
//     silently no-op into "signed in with no principal".
//  2. The ID token's `email` claim must be present and asserted
//     `email_verified` (accepting either JSON bool true or the string
//     "true", since some IdPs encode it as a string). An unverified or
//     absent email claim never provisions or matches anything -- REQ
//     #230: trusting an unverified claim would let any subject at the
//     IdP claim any email address.
//  3. The provider must have AutoProvisionDomain configured. The new
//     principal's domain always comes from this operator-set field,
//     never from the claim, so an untrusted IdP cannot choose which
//     local domain it lands a principal in; the local-part comes from
//     the verified email claim (REQ-AUTH-56's "generated local email").
//  4. The derived address must not already resolve to a principal or
//     alias. Linking an OIDC identity onto an existing local account
//     without proof of ownership is an account-takeover vector, so a
//     collision refuses rather than silently attaching -- an operator
//     performs an explicit link instead (REQ-AUTH-51).
func (r *RP) autoProvisionAndSignIn(ctx context.Context, pending pendingAuth, sub string, claims map[string]any) (PrincipalID, error) {
	provider, err := r.meta.GetOIDCProvider(ctx, string(pending.providerID))
	if err != nil {
		return 0, fmt.Errorf("directoryoidc: load provider for autoprovision: %w", err)
	}
	if !provider.AutoProvision {
		r.logger.LogAttrs(ctx, slog.LevelInfo, "directoryoidc.autoprovision.disabled",
			slog.String("activity", observe.ActivityAudit),
			slog.String("provider", string(pending.providerID)),
		)
		return 0, fmt.Errorf("%w: no link for sub", ErrNotFound)
	}
	provisioner := r.currentProvisioner()
	if provisioner == nil {
		r.logger.LogAttrs(ctx, slog.LevelError, "directoryoidc.autoprovision.not_wired",
			slog.String("activity", observe.ActivityAudit),
			slog.String("provider", string(pending.providerID)),
		)
		return 0, fmt.Errorf("%w: no link for sub", ErrNotFound)
	}

	email, emailOK := claims["email"].(string)
	email = strings.TrimSpace(email)
	if !emailOK || email == "" || !claimEmailVerified(claims) {
		r.auditAutoProvisionRefused(ctx, pending.providerID, "unverified_email")
		return 0, ErrAutoProvisionRefused
	}
	addr, perr := mail.ParseAddress(email)
	if perr != nil {
		r.auditAutoProvisionRefused(ctx, pending.providerID, "invalid_email_claim")
		return 0, ErrAutoProvisionRefused
	}
	lowered := strings.ToLower(addr.Address)
	at := strings.LastIndexByte(lowered, '@')
	if at <= 0 || at == len(lowered)-1 {
		r.auditAutoProvisionRefused(ctx, pending.providerID, "invalid_email_claim")
		return 0, ErrAutoProvisionRefused
	}
	localPart := lowered[:at]

	domain := strings.ToLower(strings.TrimSpace(provider.AutoProvisionDomain))
	if domain == "" {
		r.auditAutoProvisionRefused(ctx, pending.providerID, "no_domain_configured")
		return 0, ErrAutoProvisionRefused
	}
	canonical := localPart + "@" + domain

	// Collision check: the derived address must not already belong to a
	// principal or resolve via an alias. A race between this check and
	// the eventual insert is closed by CreateExternalPrincipal's own
	// uniqueness constraint (store.ErrConflict on the canonical email),
	// which surfaces as a generic provisioning failure below.
	if _, cerr := r.meta.GetPrincipalByEmail(ctx, canonical); cerr == nil {
		r.auditAutoProvisionRefused(ctx, pending.providerID, "address_collision")
		return 0, ErrAutoProvisionRefused
	} else if !errors.Is(cerr, store.ErrNotFound) {
		return 0, fmt.Errorf("directoryoidc: lookup principal for autoprovision: %w", cerr)
	}
	if _, aerr := r.meta.ResolveAlias(ctx, localPart, domain); aerr == nil {
		r.auditAutoProvisionRefused(ctx, pending.providerID, "address_collision")
		return 0, ErrAutoProvisionRefused
	} else if !errors.Is(aerr, store.ErrNotFound) {
		return 0, fmt.Errorf("directoryoidc: resolve alias for autoprovision: %w", aerr)
	}
	if _, aerr := r.meta.ResolveAliasExternalTarget(ctx, localPart, domain); aerr == nil {
		r.auditAutoProvisionRefused(ctx, pending.providerID, "address_collision")
		return 0, ErrAutoProvisionRefused
	} else if !errors.Is(aerr, store.ErrNotFound) {
		return 0, fmt.Errorf("directoryoidc: resolve external-target alias for autoprovision: %w", aerr)
	}

	displayName, _ := claims["name"].(string)
	pid, cerr := provisioner.CreateExternalPrincipal(ctx, canonical, displayName)
	if cerr != nil {
		r.logger.LogAttrs(ctx, slog.LevelError, "directoryoidc.autoprovision.create_failed",
			slog.String("activity", observe.ActivityAudit),
			slog.String("provider", string(pending.providerID)),
			slog.String("err", cerr.Error()),
		)
		if errors.Is(cerr, store.ErrConflict) {
			return 0, ErrAutoProvisionRefused
		}
		return 0, fmt.Errorf("directoryoidc: auto-provision principal: %w", cerr)
	}

	if lerr := r.meta.LinkOIDC(ctx, store.OIDCLink{
		PrincipalID:     pid,
		ProviderName:    string(pending.providerID),
		Subject:         sub,
		EmailAtProvider: email,
	}); lerr != nil {
		// The principal now exists but is unlinked. A retry of this same
		// sign-in re-enters this method (LookupOIDCLink still misses),
		// but the collision check above now finds the just-created
		// principal and refuses -- fails closed rather than minting a
		// second principal for the same claimed address.
		r.logger.LogAttrs(ctx, slog.LevelError, "directoryoidc.autoprovision.link_failed",
			slog.String("activity", observe.ActivityAudit),
			slog.String("provider", string(pending.providerID)),
			slog.Uint64("principal_id", uint64(pid)),
			slog.String("err", lerr.Error()),
		)
		return 0, fmt.Errorf("directoryoidc: link auto-provisioned principal: %w", lerr)
	}

	r.logger.LogAttrs(ctx, slog.LevelInfo, "directoryoidc.autoprovision",
		slog.String("activity", observe.ActivityAudit),
		slog.String("provider", string(pending.providerID)),
		slog.Uint64("principal_id", uint64(pid)),
	)
	// REQ-AC-69: isNewPrincipal=true withholds any administrative-level
	// mapped grant on this login; a later, non-provisioning login for the
	// same sub applies it normally.
	r.reconcileAfterSignIn(ctx, pending.providerID, pid, claims, true)
	return pid, nil
}

// claimEmailVerified reports whether claims carries a truthy
// email_verified value, accepting both the JSON-boolean shape (the OIDC
// core spec) and a string "true" (some IdPs encode it that way).
func claimEmailVerified(claims map[string]any) bool {
	switch v := claims["email_verified"].(type) {
	case bool:
		return v
	case string:
		return v == "true"
	default:
		return false
	}
}

// auditAutoProvisionRefused logs a refused auto-provisioning attempt.
// The reason is logged (operator-visible, for diagnosing a
// misconfiguration) but never returned to the caller -- ErrAutoProvisionRefused's
// message stays generic so an unauthenticated caller cannot use it to
// probe which addresses exist.
func (r *RP) auditAutoProvisionRefused(ctx context.Context, providerID ProviderID, reason string) {
	r.logger.LogAttrs(ctx, slog.LevelWarn, "directoryoidc.autoprovision.refused",
		slog.String("activity", observe.ActivityAudit),
		slog.String("provider", string(providerID)),
		slog.String("reason", reason),
	)
}

// Unlink removes a principal's OIDC association. Returns ErrNotFound
// if no link existed for (pid, providerID).
func (r *RP) Unlink(ctx context.Context, pid PrincipalID, providerID ProviderID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.meta.UnlinkOIDC(ctx, pid, store.OIDCProviderID(providerID)); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("%w: %s/%d", ErrNotFound, providerID, pid)
		}
		return fmt.Errorf("directoryoidc: unlink: %w", err)
	}
	r.logger.LogAttrs(ctx, slog.LevelInfo, "directoryoidc.unlink",
		slog.String("activity", observe.ActivityAudit),
		slog.String("provider", string(providerID)),
		slog.Uint64("principal_id", uint64(pid)),
	)
	// Unlinking removes every idp:<providerID> grant for pid immediately
	// (REQ-AC-63): an empty desired set makes ReconcileIdPGrants delete the
	// whole idp:<providerID> row set for this principal. Best-effort, like
	// the CompleteSignIn reconciliation call: a failure here is logged, not
	// surfaced as an Unlink failure, since the OIDC association itself is
	// already gone and REQ-AC-70's fail-safe applies equally to this path.
	if _, removed, rerr := r.meta.ReconcileIdPGrants(ctx, pid, string(providerID), nil, r.clk.Now()); rerr != nil {
		r.logger.LogAttrs(ctx, slog.LevelWarn, "directoryoidc.unlink.grant_sweep_failed",
			slog.String("activity", observe.ActivityAudit),
			slog.String("provider", string(providerID)),
			slog.Uint64("principal_id", uint64(pid)),
			slog.String("err", rerr.Error()),
		)
	} else {
		for _, g := range removed {
			r.logger.LogAttrs(ctx, slog.LevelInfo, "idp.grant.remove",
				slog.String("activity", observe.ActivityAudit),
				slog.String("provider", string(providerID)),
				slog.Uint64("principal_id", uint64(pid)),
				slog.String("resource_kind", string(g.ResourceKind)),
				slog.String("resource_id", g.ResourceID),
				slog.String("level", string(g.Level)),
			)
		}
	}
	return nil
}

// VerifyAccessToken is the adaptor used by sasl.TokenVerifier: accept
// an access token presented at SASL OAUTHBEARER / XOAUTH2 and map it to
// a local PrincipalID. The flow validates the token as a JWT id_token
// against the named provider's JWKS; operators who want opaque
// access-token introspection (RFC 7662) should extend this method.
//
// providerID is mandatory and identifies which configured OIDC provider
// the token came from (Wave 4 finding 9). Without it a token signed by
// provider A whose `sub` happened to match a different principal's link
// to provider B would unlock that principal — the previous
// "try every verifier" loop made this an audience-confusion bug. The
// caller in the SMTP/IMAP submission path takes the value from the
// OAUTHBEARER gs2 host= advertisement (or an operator-configured
// default for XOAUTH2 which has no equivalent field).
//
// The audience check happens both ways for defence in depth: the caller
// names the provider, and the JWT verifier is built with that
// provider's ClientID so go-oidc rejects tokens whose `aud` claim does
// not match the provider's audience.
//
// TODO: add opaque-token introspection (RFC 7662) when the second
// caller arrives.
func (r *RP) VerifyAccessToken(ctx context.Context, providerID, token string) (PrincipalID, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if providerID == "" {
		return 0, fmt.Errorf("%w: provider hint required", ErrInvalidState)
	}
	id := ProviderID(providerID)
	prov, cfg, _, ok := r.lookupProvider(id)
	if !ok {
		return 0, fmt.Errorf("%w: provider %s", ErrNotFound, providerID)
	}
	verifier := prov.Verifier(&oidc.Config{ClientID: cfg.ClientID})
	idTok, err := verifier.Verify(oidc.ClientContext(ctx, r.http), token)
	if err != nil {
		return 0, fmt.Errorf("%w: token rejected", ErrNotFound)
	}
	// Belt-and-braces audience check: oidc.Verifier already validates
	// `aud` against ClientID, but the loop is documented as a defence
	// against future code paths that loosen the verifier's audience
	// rule (e.g. an operator-supplied additional ClientID). If `aud`
	// names ClientID at any position, accept; otherwise reject.
	if !audMatchesClientID(idTok.Audience, cfg.ClientID) {
		return 0, fmt.Errorf("%w: aud mismatch", ErrNotFound)
	}
	link, err := r.meta.LookupOIDCLink(ctx, string(id), idTok.Subject)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return 0, fmt.Errorf("%w: no link for sub", ErrNotFound)
		}
		return 0, err
	}
	return link.PrincipalID, nil
}

// audMatchesClientID returns true when clientID appears in aud. The
// `aud` claim is conventionally an array but JSON allows a single
// string; oidc.IDToken normalises it to []string.
func audMatchesClientID(aud []string, clientID string) bool {
	for _, a := range aud {
		if a == clientID {
			return true
		}
	}
	return false
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

// PeekPendingFlow reports the flow kind of the pending state without
// consuming it. Returns FlowKindUnknown when the state is unknown or
// has expired. The OIDC callback uses this to dispatch the correct
// CompleteLink / CompleteSignIn before consuming the state — fixing the
// finding-10 bug where the callback consumed the state pre-classification
// and made the sign-in branch unreachable.
func (r *RP) PeekPendingFlow(state string) FlowKind {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gcExpiredLocked()
	p, ok := r.pending[state]
	if !ok {
		return FlowKindUnknown
	}
	if p.principalID != 0 {
		return FlowKindLink
	}
	return FlowKindSignIn
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
// expiry, and nonce. Returns the subject and the ID token's claim set
// (decoded as a generic map so callers -- namely authz.ReconcileIdP's
// group/role claim-mapping evaluation, epic #188 -- can look up any claim
// name without this package needing to know it in advance).
func (r *RP) exchangeAndVerify(ctx context.Context, p pendingAuth, code string) (sub string, claims map[string]any, err error) {
	prov, cfg, secret, ok := r.lookupProvider(p.providerID)
	if !ok {
		return "", nil, fmt.Errorf("%w: provider %s", ErrNotFound, p.providerID)
	}
	if p.redirectOverride != "" {
		cfg.RedirectURL = p.redirectOverride
	}
	exCtx, cancel := context.WithTimeout(oidc.ClientContext(ctx, r.http), defaultTimeout)
	defer cancel()
	oauthCfg := oauth2Config(prov, cfg, secret)
	tok, err := oauthCfg.Exchange(exCtx, code)
	if err != nil {
		return "", nil, fmt.Errorf("directoryoidc: exchange: %w", err)
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok || raw == "" {
		return "", nil, fmt.Errorf("directoryoidc: missing id_token")
	}
	verifier := prov.Verifier(&oidc.Config{ClientID: cfg.ClientID})
	idTok, err := verifier.Verify(exCtx, raw)
	if err != nil {
		return "", nil, fmt.Errorf("directoryoidc: verify id_token: %w", err)
	}
	if idTok.Nonce != p.nonce {
		return "", nil, fmt.Errorf("%w: nonce mismatch", ErrInvalidState)
	}
	var claimSet map[string]any
	if err := idTok.Claims(&claimSet); err != nil {
		return "", nil, fmt.Errorf("directoryoidc: decode claims: %w", err)
	}
	return idTok.Subject, claimSet, nil
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
