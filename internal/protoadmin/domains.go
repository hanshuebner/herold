package protoadmin

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/hanshuebner/herold/internal/store"
)

type createDomainRequest struct {
	Name  string `json:"name"`
	Local *bool  `json:"local,omitempty"`
}

func (s *Server) handleListDomains(w http.ResponseWriter, r *http.Request) {
	// Listing domains is admin-only; domain metadata is operator config.
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	rows, err := s.store.Meta().ListLocalDomains(r.Context())
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	items := make([]domainDTO, 0, len(rows))
	for _, d := range rows {
		items = append(items, toDomainDTO(d))
	}
	writeJSON(w, http.StatusOK, pageDTO[domainDTO]{Items: items, Next: nil})
}

func (s *Server) handleCreateDomain(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	var req createDomainRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	name := strings.ToLower(strings.TrimSpace(req.Name))
	if name == "" {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"name is required", "")
		return
	}
	local := true
	if req.Local != nil {
		local = *req.Local
	}
	d := store.Domain{Name: name, IsLocal: local}
	if err := s.store.Meta().InsertDomain(r.Context(), d); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	// Re-read so we return the store-resolved CreatedAt.
	got, err := s.store.Meta().GetDomain(r.Context(), name)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	s.appendAudit(r.Context(), "domain.create",
		fmt.Sprintf("domain:%s", name),
		store.OutcomeSuccess, "", nil)
	w.Header().Set("Location", fmt.Sprintf("/api/v1/domains/%s", name))
	writeJSON(w, http.StatusCreated, toDomainDTO(got))
}

func (s *Server) handleDeleteDomain(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	if name == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id",
			"name is required", "")
		return
	}
	if err := s.store.Meta().DeleteDomain(r.Context(), name); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	s.appendAudit(r.Context(), "domain.delete",
		fmt.Sprintf("domain:%s", name),
		store.OutcomeSuccess, "", nil)
	w.WriteHeader(http.StatusNoContent)
}
