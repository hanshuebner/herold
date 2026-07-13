package protoadmin

// oauth2_clients.go implements the admin REST CRUD surface for the
// DB-backed OAuth2 client registry (issue #199's "DB-backed OAuth2
// client registry" work item): an operator registers a native client,
// its redirect URIs, and its scopes without a herold rebuild. All
// business logic lives in internal/directory (oauth2client.go); this
// file is the thin REST adapter plus admin authorization and audit
// logging, mirroring oidc.go's split for OIDC provider CRUD.
//
// Endpoints (admin-only, mounted on the admin listener):
//
//	POST   /api/v1/oauth2/clients       Register a new client. The
//	                                     response includes client_secret
//	                                     exactly once when the client is
//	                                     registered confidential.
//	GET    /api/v1/oauth2/clients       List every registered client.
//	GET    /api/v1/oauth2/clients/{id}  Get one client.
//	PATCH  /api/v1/oauth2/clients/{id}  Update name / redirect_uris /
//	                                     scopes. public and the secret
//	                                     are immutable after creation.
//	DELETE /api/v1/oauth2/clients/{id}  Remove a client. Already-issued
//	                                     tokens are unaffected.
import (
	"errors"
	"fmt"
	"net/http"

	"github.com/hanshuebner/herold/internal/auth"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/store"
)

// oauthClientDTO is the wire representation of a registered OAuth2
// client. The client secret is never included except as a distinct
// top-level field on the create response, exactly once.
type oauthClientDTO struct {
	ClientID     string   `json:"client_id"`
	Name         string   `json:"name"`
	RedirectURIs []string `json:"redirect_uris"`
	Scopes       []string `json:"scopes"`
	Public       bool     `json:"public"`
}

func toOAuthClientDTO(c directory.OAuthClient) oauthClientDTO {
	scopes := make([]string, len(c.Scopes))
	for i, sc := range c.Scopes {
		scopes[i] = string(sc)
	}
	return oauthClientDTO{
		ClientID:     c.ID,
		Name:         c.Name,
		RedirectURIs: append([]string(nil), c.RedirectURIs...),
		Scopes:       scopes,
		Public:       c.Public,
	}
}

type createOAuthClientRequest struct {
	ClientID     string   `json:"client_id"`
	Name         string   `json:"name"`
	RedirectURIs []string `json:"redirect_uris"`
	Scopes       []string `json:"scopes,omitempty"`
	Confidential bool     `json:"confidential,omitempty"`
}

type createOAuthClientResponse struct {
	oauthClientDTO
	// ClientSecret is populated exactly once, on creation, only when
	// the client was registered confidential. It is never returned by
	// any other endpoint and never logged.
	ClientSecret string `json:"client_secret,omitempty"`
}

func parseScopeStrings(strs []string) ([]auth.Scope, error) {
	out := make([]auth.Scope, 0, len(strs))
	for _, s := range strs {
		sc, err := auth.ParseScope(s)
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, nil
}

func (s *Server) handleCreateOAuthClient(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	var req createOAuthClientRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.ClientID == "" || len(req.RedirectURIs) == 0 {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"client_id and at least one redirect_uri are required", "")
		return
	}
	scopes, err := parseScopeStrings(req.Scopes)
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed", err.Error(), "")
		return
	}

	client, secret, err := s.dir.RegisterOAuthClient(r.Context(), directory.OAuthClientRegistration{
		ClientID:     req.ClientID,
		Name:         req.Name,
		RedirectURIs: req.RedirectURIs,
		Scopes:       scopes,
		Confidential: req.Confidential,
	})
	if err != nil {
		s.writeOAuthClientError(w, r, err)
		return
	}
	s.appendAudit(r.Context(), "oauth2.client.create",
		fmt.Sprintf("oauth_client:%s", client.ID),
		store.OutcomeSuccess, "", map[string]string{"confidential": fmt.Sprintf("%t", req.Confidential)})
	w.Header().Set("Location", fmt.Sprintf("/api/v1/oauth2/clients/%s", client.ID))
	writeJSON(w, http.StatusCreated, createOAuthClientResponse{
		oauthClientDTO: toOAuthClientDTO(client),
		ClientSecret:   secret,
	})
}

func (s *Server) handleListOAuthClients(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	clients, err := s.dir.ListOAuthClients(r.Context())
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	items := make([]oauthClientDTO, 0, len(clients))
	for _, c := range clients {
		items = append(items, toOAuthClientDTO(c))
	}
	writeJSON(w, http.StatusOK, pageDTO[oauthClientDTO]{Items: items, Next: nil})
}

func (s *Server) handleGetOAuthClient(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id", "id is required", "")
		return
	}
	client, err := s.dir.LookupOAuthClient(r.Context(), id)
	if err != nil {
		s.writeOAuthClientError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toOAuthClientDTO(client))
}

type updateOAuthClientRequest struct {
	Name         string   `json:"name"`
	RedirectURIs []string `json:"redirect_uris"`
	Scopes       []string `json:"scopes,omitempty"`
}

func (s *Server) handleUpdateOAuthClient(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id", "id is required", "")
		return
	}
	var req updateOAuthClientRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if len(req.RedirectURIs) == 0 {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"at least one redirect_uri is required", "")
		return
	}
	scopes, err := parseScopeStrings(req.Scopes)
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed", err.Error(), "")
		return
	}
	client, err := s.dir.UpdateOAuthClient(r.Context(), id, req.Name, req.RedirectURIs, scopes)
	if err != nil {
		s.writeOAuthClientError(w, r, err)
		return
	}
	s.appendAudit(r.Context(), "oauth2.client.update",
		fmt.Sprintf("oauth_client:%s", id), store.OutcomeSuccess, "", nil)
	writeJSON(w, http.StatusOK, toOAuthClientDTO(client))
}

func (s *Server) handleDeleteOAuthClient(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id", "id is required", "")
		return
	}
	if err := s.dir.DeleteOAuthClient(r.Context(), id); err != nil {
		s.writeOAuthClientError(w, r, err)
		return
	}
	s.appendAudit(r.Context(), "oauth2.client.delete",
		fmt.Sprintf("oauth_client:%s", id), store.OutcomeSuccess, "", nil)
	w.WriteHeader(http.StatusNoContent)
}

// writeOAuthClientError maps a directory OAuth2-client-registry error to
// an HTTP problem.
func (s *Server) writeOAuthClientError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, directory.ErrUnknownOAuthClient):
		writeProblem(w, r, http.StatusNotFound, "not_found", "oauth2 client not registered", "")
	case errors.Is(err, directory.ErrOAuthClientExists):
		writeProblem(w, r, http.StatusConflict, "conflict", "client_id is already registered", "")
	case errors.Is(err, directory.ErrOAuthClientInvalid):
		writeProblem(w, r, http.StatusBadRequest, "validation_failed", err.Error(), "")
	default:
		s.writeStoreError(w, r, err)
	}
}
