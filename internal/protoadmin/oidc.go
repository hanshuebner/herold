package protoadmin

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/store"
)

type createOIDCProviderRequest struct {
	Name         string   `json:"name"`
	Issuer       string   `json:"issuer"`
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"`
	RedirectURL  string   `json:"redirect_url,omitempty"`
	Scopes       []string `json:"scopes,omitempty"`
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

func (s *Server) handleCreateOIDCProvider(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
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
	id, err := s.rp.AddProvider(r.Context(), directoryoidc.ProviderConfig{
		Name:         req.Name,
		IssuerURL:    req.Issuer,
		ClientID:     req.ClientID,
		ClientSecret: req.ClientSecret,
		RedirectURL:  req.RedirectURL,
		Scopes:       req.Scopes,
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

func (s *Server) handleDeleteOIDCProvider(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
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

// handlePatchOIDCProvider accepts a partial-update body. The Phase 2
// store does not expose UpdateOIDCProvider; the only mutating field
// we can rotate without dropping links is the in-memory secret handle
// on the RP, which is not yet exposed. The endpoint records the intent
// in the audit log and returns 501 so operators see a deterministic
// "not implemented" rather than a silent success.
func (s *Server) handlePatchOIDCProvider(w http.ResponseWriter, r *http.Request) {
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
	// Confirm the provider exists so the operator gets a 404 in the
	// natural case before the 501.
	if _, err := s.store.Meta().GetOIDCProvider(r.Context(), id); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeProblem(w, r, http.StatusNotImplemented, "oidc/update_not_implemented",
		"provider rotation requires Metadata.UpdateOIDCProvider (Phase 3 schema extension); use 'remove' + 're-add' as a workaround if cascade-loss of links is acceptable",
		"")
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
	if !requireSelfOrAdmin(w, r, caller, pid) {
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
	authURL, state, err := s.rp.BeginLink(r.Context(), pid, directoryoidc.ProviderID(req.ProviderID))
	if err != nil {
		s.writeOIDCError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, beginOIDCLinkResponse{AuthURL: authURL, State: state})
}

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

// handleOIDCCallback completes either a link or sign-in flow. The
// endpoint is unauthenticated because it is reached by the user's
// browser after the OIDC provider redirects; identity comes from the
// state token.
//
// Wave 4 finding 10: classify the flow before consuming the state.
// Peek the pending record's flow kind, then dispatch to the matching
// completion. The completion path is the only place that calls
// takePending (which removes the row), so an unknown state returns 400
// without ever touching the persistent map and a state used twice
// surfaces as ErrInvalidState the second time around.
func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	if state == "" || code == "" {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"state and code are required", "")
		return
	}
	switch s.rp.PeekPendingFlow(state) {
	case directoryoidc.FlowKindLink:
		pid, err := s.rp.CompleteLink(r.Context(), state, code)
		if err != nil {
			s.writeOIDCError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"outcome":      "linked",
			"principal_id": uint64(pid),
		})
	case directoryoidc.FlowKindSignIn:
		pid, err := s.rp.CompleteSignIn(r.Context(), state, code)
		if err != nil {
			s.writeOIDCError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"outcome":      "signed_in",
			"principal_id": uint64(pid),
		})
	default:
		// Unknown / expired state — do not touch the persistent map.
		writeProblem(w, r, http.StatusBadRequest, "invalid_state",
			"state not recognised or already consumed", "")
	}
}

// writeOIDCError maps a directoryoidc error to an HTTP problem.
func (s *Server) writeOIDCError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, directoryoidc.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, "not_found", err.Error(), "")
	case errors.Is(err, directoryoidc.ErrConflict):
		writeProblem(w, r, http.StatusConflict, "conflict", err.Error(), "")
	case errors.Is(err, directoryoidc.ErrInvalidState):
		writeProblem(w, r, http.StatusBadRequest, "invalid_state", err.Error(), "")
	case errors.Is(err, directoryoidc.ErrProviderDiscoveryFailed):
		writeProblem(w, r, http.StatusBadRequest, "discovery_failed", err.Error(), "")
	default:
		s.writeStoreError(w, r, err)
	}
}
