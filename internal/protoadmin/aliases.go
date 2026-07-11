package protoadmin

import (
	"fmt"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// createAliasRequest accepts exactly one of TargetPrincipalID (the
// pre-existing internal-target shape) or TargetAddress, an external
// email address the alias forwards inbound mail to (re #181).
type createAliasRequest struct {
	LocalPart         string     `json:"local"`
	Domain            string     `json:"domain"`
	TargetPrincipalID uint64     `json:"target_principal_id,omitempty"`
	TargetAddress     string     `json:"target_address,omitempty"`
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
	targetAddress := strings.TrimSpace(req.TargetAddress)
	hasPrincipal := req.TargetPrincipalID != 0
	hasAddress := targetAddress != ""
	if req.LocalPart == "" || req.Domain == "" || hasPrincipal == hasAddress {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"local and domain are required, and exactly one of target_principal_id / target_address must be set", "")
		return
	}
	if hasAddress {
		addr, perr := mail.ParseAddress(targetAddress)
		if perr != nil {
			writeProblem(w, r, http.StatusBadRequest, "validation_failed",
				"target_address is not a valid email address", "")
			return
		}
		targetAddress = strings.ToLower(addr.Address)
		// Reject a target address on a domain this deployment is
		// authoritative for (re #181): that is always an operator
		// mistake (a typo'd or not-yet-created principal), never a
		// genuine external forward — the outbound queue cannot deliver
		// mail for a domain we serve locally to "outside".
		if _, atDomain, ok := strings.Cut(targetAddress, "@"); ok {
			if d, derr := s.store.Meta().GetDomain(r.Context(), atDomain); derr == nil && d.IsLocal {
				writeProblem(w, r, http.StatusBadRequest, "validation_failed",
					"target_address is on a locally hosted domain; use target_principal_id for an internal address", "")
				return
			}
		}
	}
	a, err := s.store.Meta().InsertAlias(r.Context(), store.Alias{
		LocalPart:       req.LocalPart,
		Domain:          req.Domain,
		TargetPrincipal: store.PrincipalID(req.TargetPrincipalID),
		TargetAddress:   targetAddress,
		ExpiresAt:       req.ExpiresAt,
	})
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	auditMeta := map[string]string{
		"address": a.LocalPart + "@" + a.Domain,
	}
	if a.TargetAddress != "" {
		auditMeta["target_address"] = a.TargetAddress
	} else {
		auditMeta["target_id"] = strconv.FormatUint(uint64(a.TargetPrincipal), 10)
	}
	s.appendAudit(r.Context(), "alias.create",
		fmt.Sprintf("alias:%d", a.ID),
		store.OutcomeSuccess, "", auditMeta)
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
