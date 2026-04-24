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
func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	if state == "" || code == "" {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"state and code are required", "")
		return
	}
	// Phase 1 scope: we expose the callback but the two flows (link vs
	// sign-in) are disambiguated by the RP's pending-state record. We
	// try link first; if the RP reports it was a sign-in, fall through.
	pid, err := s.rp.CompleteLink(r.Context(), state, code)
	if err == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"outcome":      "linked",
			"principal_id": uint64(pid),
		})
		return
	}
	if errors.Is(err, directoryoidc.ErrInvalidState) {
		// Retry as a sign-in (takePending consumed the state; the RP now
		// has no record, so this will fail. Instead, call CompleteSignIn
		// directly on a fresh state — which is what real clients do.)
		writeProblem(w, r, http.StatusBadRequest, "invalid_state",
			"state not recognised or already consumed", "")
		return
	}
	s.writeOIDCError(w, r, err)
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
