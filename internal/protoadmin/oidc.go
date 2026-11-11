package protoadmin

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/store"
)

type createOIDCProviderRequest struct {
	Name         string `json:"name"`
	Issuer       string `json:"issuer"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	// ClientSecretRef is a secret reference ("$VAR" or "file:/path"),
	// persisted and resolved again whenever the provider is loaded after a
	// restart. Preferred over ClientSecret, which lives in process memory
	// only.
	ClientSecretRef string `json:"client_secret_ref,omitempty"`
	// RedirectURL is the redirect_uri for flows started through the Go
	// API without an explicit one. The Suite's browser flows always use
	// <public origin>/api/v1/oidc/callback.
	RedirectURL string   `json:"redirect_url,omitempty"`
	Scopes      []string `json:"scopes,omitempty"`
	// AutoProvision / AutoProvisionDomain opt this provider into
	// REQ-AUTH-56 first-login auto-provisioning (issue #230). Setting
	// AutoProvision true requires a server:superadmin caller: it lets
	// anyone with an account at the configured IdP mint a local
	// principal, so enabling it is a whole-deployment security decision,
	// not an ordinary provider edit (mirrors the authz_trusted claim-
	// mapping gate's superadmin-only posture).
	AutoProvision       bool   `json:"auto_provision,omitempty"`
	AutoProvisionDomain string `json:"auto_provision_domain,omitempty"`
}

func (s *Server) handleListOIDCProviders(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	rows, err := s.store.Meta().ListOIDCProviders(r.Context())
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	items := make([]oidcProviderDTO, 0, len(rows))
	for _, p := range rows {
		items = append(items, toOIDCProviderDTO(p))
	}
	writeJSON(w, http.StatusOK, pageDTO[oidcProviderDTO]{Items: items, Next: nil})
}

// handleCreateOIDCProvider registers a provider. A provider is
// deployment-wide: every principal can link it and, once enabled, sign in
// with it, and client_secret_ref makes the server read a file or an
// environment variable and send its content to the issuer's token
// endpoint. Registering one therefore requires a server:superadmin
// caller, the same bar as auto-provisioning.
func (s *Server) handleCreateOIDCProvider(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireSuperAdmin(w, r, caller) {
		return
	}
	var req createOIDCProviderRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.Name == "" || req.Issuer == "" || req.ClientID == "" {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"name, issuer, and client_id are required", "")
		return
	}
	if req.AutoProvision {
		if !requireSuperAdmin(w, r, caller) {
			return
		}
		if req.AutoProvisionDomain == "" {
			writeProblem(w, r, http.StatusBadRequest, "validation_failed",
				"auto_provision_domain is required when auto_provision is true", "")
			return
		}
	}
	id, err := s.rp.AddProvider(r.Context(), directoryoidc.ProviderConfig{
		Name:                req.Name,
		IssuerURL:           req.Issuer,
		ClientID:            req.ClientID,
		ClientSecret:        req.ClientSecret,
		ClientSecretRef:     req.ClientSecretRef,
		RedirectURL:         req.RedirectURL,
		Scopes:              req.Scopes,
		AutoProvision:       req.AutoProvision,
		AutoProvisionDomain: req.AutoProvisionDomain,
	})
	if err != nil {
		s.writeOIDCError(w, r, err)
		return
	}
	got, err := s.store.Meta().GetOIDCProvider(r.Context(), string(id))
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	s.appendAudit(r.Context(), "oidc.provider.create",
		fmt.Sprintf("oidc_provider:%s", id),
		store.OutcomeSuccess, "", nil)
	w.Header().Set("Location", fmt.Sprintf("/api/v1/oidc/providers/%s", id))
	writeJSON(w, http.StatusCreated, toOIDCProviderDTO(got))
}

// handleDeleteOIDCProvider removes a deployment-wide provider and, with
// it, every principal's link to it, so it requires server:superadmin.
func (s *Server) handleDeleteOIDCProvider(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireSuperAdmin(w, r, caller) {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id", "id is required", "")
		return
	}
	if err := s.rp.DeleteProvider(r.Context(), directoryoidc.ProviderID(id)); err != nil {
		s.writeOIDCError(w, r, err)
		return
	}
	s.appendAudit(r.Context(), "oidc.provider.delete",
		fmt.Sprintf("oidc_provider:%s", id),
		store.OutcomeSuccess, "", nil)
	w.WriteHeader(http.StatusNoContent)
}

// handleGetOIDCProvider returns the provider row for the given id-or-name.
// Secret material is never serialised — the wire form omits both the
// secret value and the secret reference.
func (s *Server) handleGetOIDCProvider(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id",
			"id is required", "")
		return
	}
	got, err := s.store.Meta().GetOIDCProvider(r.Context(), id)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toOIDCProviderDTO(got))
}

// patchOIDCProviderRequest is the only mutable slice of an OIDC
// provider's config exposed today: the REQ-AUTH-56 auto-provisioning
// opt-in and its target domain. Pointer fields distinguish "absent"
// (leave unchanged) from an explicit false/"" value.
type patchOIDCProviderRequest struct {
	AutoProvision       *bool   `json:"auto_provision,omitempty"`
	AutoProvisionDomain *string `json:"auto_provision_domain,omitempty"`
	// ClientSecretEnv is accepted (so the `herold oidc provider update
	// --client-secret-env` CLI path decodes instead of 400ing on an
	// unknown field) but not actionable -- secret rotation still
	// requires Metadata.UpdateOIDCProvider (Phase 3 schema extension).
	// A body naming only this field falls through to the 501 branch
	// below exactly as it did before auto_provision/auto_provision_domain
	// became patchable.
	ClientSecretEnv *string `json:"client_secret_env,omitempty"`
}

// handlePatchOIDCProvider accepts a partial-update body. The Phase 2
// store does not expose a general UpdateOIDCProvider -- issuer/client_id/
// secret rotation without dropping links still isn't possible here, and
// a body naming any field other than auto_provision /
// auto_provision_domain is rejected as an unknown field (400) by the
// strict decoder rather than silently accepted. The only field this
// endpoint can mutate is the REQ-AUTH-56 auto-provisioning opt-in
// (issue #230), via RP.SetAutoProvision; enabling it requires a
// server:superadmin caller for the same reason handleCreateOIDCProvider
// does; every patch of a provider does.
func (s *Server) handlePatchOIDCProvider(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireSuperAdmin(w, r, caller) {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id",
			"id is required", "")
		return
	}
	existing, err := s.store.Meta().GetOIDCProvider(r.Context(), id)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	var req patchOIDCProviderRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.AutoProvision == nil && req.AutoProvisionDomain == nil {
		writeProblem(w, r, http.StatusNotImplemented, "oidc/update_not_implemented",
			"only auto_provision / auto_provision_domain can be patched today; provider rotation requires Metadata.UpdateOIDCProvider (Phase 3 schema extension) -- use 'remove' + 're-add' as a workaround if cascade-loss of links is acceptable",
			"")
		return
	}
	enabled := existing.AutoProvision
	if req.AutoProvision != nil {
		enabled = *req.AutoProvision
	}
	domain := existing.AutoProvisionDomain
	if req.AutoProvisionDomain != nil {
		domain = *req.AutoProvisionDomain
	}
	if enabled {
		if !requireSuperAdmin(w, r, caller) {
			return
		}
		if domain == "" {
			writeProblem(w, r, http.StatusBadRequest, "validation_failed",
				"auto_provision_domain is required when auto_provision is true", "")
			return
		}
	}
	if err := s.rp.SetAutoProvision(r.Context(), directoryoidc.ProviderID(id), enabled, domain); err != nil {
		s.writeOIDCError(w, r, err)
		return
	}
	got, err := s.store.Meta().GetOIDCProvider(r.Context(), id)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	s.appendAudit(r.Context(), "oidc.provider.set_auto_provision",
		fmt.Sprintf("oidc_provider:%s", id),
		store.OutcomeSuccess, "", map[string]string{
			"auto_provision":        fmt.Sprintf("%v", enabled),
			"auto_provision_domain": domain,
		})
	writeJSON(w, http.StatusOK, toOIDCProviderDTO(got))
}

func (s *Server) handleListOIDCLinks(w http.ResponseWriter, r *http.Request) {
	pid, ok := parsePID(w, r)
	if !ok {
		return
	}
	caller, _ := principalFrom(r.Context())
	if !requireSelfOrAdmin(w, r, caller, pid) {
		return
	}
	rows, err := s.store.Meta().ListOIDCLinksByPrincipal(r.Context(), pid)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	items := make([]oidcLinkDTO, 0, len(rows))
	for _, l := range rows {
		items = append(items, toOIDCLinkDTO(l))
	}
	writeJSON(w, http.StatusOK, pageDTO[oidcLinkDTO]{Items: items, Next: nil})
}

type beginOIDCLinkRequest struct {
	ProviderID string `json:"provider_id"`
}

type beginOIDCLinkResponse struct {
	AuthURL string `json:"auth_url"`
	State   string `json:"state"`
}

func (s *Server) handleBeginOIDCLink(w http.ResponseWriter, r *http.Request) {
	pid, ok := parsePID(w, r)
	if !ok {
		return
	}
	caller, _ := principalFrom(r.Context())
	// Only the principal itself may start a link: the flow attaches the
	// identity of whoever signs in at the provider to pid, so an admin
	// starting it for another principal could then sign in as that
	// principal through their own provider account.
	if !requireSelfOnly(w, r, caller, pid) {
		return
	}
	if !s.requireNotSubPrincipal(w, r, pid) {
		return
	}
	var req beginOIDCLinkRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.ProviderID == "" {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"provider_id is required", "")
		return
	}
	authURL, state, err := s.rp.BeginLinkAt(r.Context(), pid, directoryoidc.ProviderID(req.ProviderID), s.oidcCallbackURL(r))
	if err != nil {
		s.writeOIDCError(w, r, err)
		return
	}
	s.setOIDCFlowCookie(w, state)
	writeJSON(w, http.StatusOK, beginOIDCLinkResponse{AuthURL: authURL, State: state})
}

// handleUnlinkOIDC removes a link. The principal may remove its own; an
// admin may too (support case: a lost or compromised provider account).
// Removing a link only narrows how the principal can sign in, so it needs
// no further gate, and every removal is audited.
func (s *Server) handleUnlinkOIDC(w http.ResponseWriter, r *http.Request) {
	pid, ok := parsePID(w, r)
	if !ok {
		return
	}
	caller, _ := principalFrom(r.Context())
	if !requireSelfOrAdmin(w, r, caller, pid) {
		return
	}
	providerID := r.PathValue("provider_id")
	if providerID == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id",
			"provider_id is required", "")
		return
	}
	if err := s.rp.Unlink(r.Context(), pid, directoryoidc.ProviderID(providerID)); err != nil {
		s.writeOIDCError(w, r, err)
		return
	}
	s.appendAudit(r.Context(), "oidc.link.delete",
		fmt.Sprintf("principal:%d", pid),
		store.OutcomeSuccess, "", map[string]string{"provider_id": providerID})
	w.WriteHeader(http.StatusNoContent)
}

// writeOIDCError maps a directoryoidc error to an HTTP problem.
func (s *Server) writeOIDCError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, directoryoidc.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, "not_found", err.Error(), "")
	case errors.Is(err, directoryoidc.ErrAutoProvisionRefused):
		writeProblem(w, r, http.StatusForbidden, "auto_provision_refused", err.Error(), "")
	case errors.Is(err, directoryoidc.ErrConflict):
		writeProblem(w, r, http.StatusConflict, "conflict", err.Error(), "")
	case errors.Is(err, directoryoidc.ErrInvalidState):
		writeProblem(w, r, http.StatusBadRequest, "invalid_state", err.Error(), "")
	case errors.Is(err, directoryoidc.ErrProviderDiscoveryFailed):
		writeProblem(w, r, http.StatusBadRequest, "discovery_failed", err.Error(), "")
	case errors.Is(err, directoryoidc.ErrInvalidSecretRef):
		// err.Error() carries only the sanitised reason; the resolver's
		// detail (paths, OS errors) stays in the server log.
		writeProblem(w, r, http.StatusBadRequest, "invalid_client_secret_ref", err.Error(), "")
	case errors.Is(err, directoryoidc.ErrProviderUnavailable):
		writeProblem(w, r, http.StatusServiceUnavailable, "provider_unavailable", err.Error(), "")
	default:
		s.writeStoreError(w, r, err)
	}
}
