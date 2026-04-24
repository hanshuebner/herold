package protoadmin

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

type createAliasRequest struct {
	LocalPart         string     `json:"local"`
	Domain            string     `json:"domain"`
	TargetPrincipalID uint64     `json:"target_principal_id"`
	ExpiresAt         *time.Time `json:"expires_at,omitempty"`
}

func (s *Server) handleListAliases(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	domain := r.URL.Query().Get("domain")
	rows, err := s.store.Meta().ListAliases(r.Context(), domain)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	items := make([]aliasDTO, 0, len(rows))
	for _, a := range rows {
		items = append(items, toAliasDTO(a))
	}
	writeJSON(w, http.StatusOK, pageDTO[aliasDTO]{Items: items, Next: nil})
}

func (s *Server) handleCreateAlias(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	var req createAliasRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.LocalPart == "" || req.Domain == "" || req.TargetPrincipalID == 0 {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"local, domain, and target_principal_id are required", "")
		return
	}
	a, err := s.store.Meta().InsertAlias(r.Context(), store.Alias{
		LocalPart:       req.LocalPart,
		Domain:          req.Domain,
		TargetPrincipal: store.PrincipalID(req.TargetPrincipalID),
		ExpiresAt:       req.ExpiresAt,
	})
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	s.appendAudit(r.Context(), "alias.create",
		fmt.Sprintf("alias:%d", a.ID),
		store.OutcomeSuccess, "", map[string]string{
			"address":   a.LocalPart + "@" + a.Domain,
			"target_id": strconv.FormatUint(uint64(a.TargetPrincipal), 10),
		})
	w.Header().Set("Location", fmt.Sprintf("/api/v1/aliases/%d", a.ID))
	writeJSON(w, http.StatusCreated, toAliasDTO(a))
}

func (s *Server) handleDeleteAlias(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	raw := r.PathValue("id")
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || n == 0 {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id",
			"alias id must be a positive integer", raw)
		return
	}
	if err := s.store.Meta().DeleteAlias(r.Context(), store.AliasID(n)); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	s.appendAudit(r.Context(), "alias.delete",
		fmt.Sprintf("alias:%d", n),
		store.OutcomeSuccess, "", nil)
	w.WriteHeader(http.StatusNoContent)
}
