package protoadmin

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// handleListOperators lists every domain-scoped operator (admin but not
// super-admin) together with each operator's managed-domain set.
// Super-admin only (REQ-ADM-307).
func (s *Server) handleListOperators(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireSuperAdmin(w, r, caller) {
		return
	}
	principals, err := s.store.Meta().ListDomainOperators(r.Context())
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	items := make([]operatorDTO, 0, len(principals))
	for _, p := range principals {
		domains, err := s.store.Meta().ListManagedDomains(r.Context(), p.ID)
		if err != nil {
			s.writeStoreError(w, r, err)
			return
		}
		if domains == nil {
			domains = []string{}
		}
		items = append(items, operatorDTO{
			principalDTO:   toPrincipalDTO(p),
			ManagedDomains: domains,
		})
	}
	writeJSON(w, http.StatusOK, pageDTO[operatorDTO]{Items: items, Next: nil})
}

// assignManagedDomainRequest is the body for POST .../managed-domains.
type assignManagedDomainRequest struct {
	Domain string `json:"domain"`
}

// handleListManagedDomains returns the managed-domain set for a principal.
// Super-admin only (REQ-ADM-307).
func (s *Server) handleListManagedDomains(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireSuperAdmin(w, r, caller) {
		return
	}
	pid, ok := parsePID(w, r)
	if !ok {
		return
	}
	domains, err := s.store.Meta().ListManagedDomains(r.Context(), pid)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	if domains == nil {
		domains = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"domains": domains})
}

// handleAssignManagedDomain assigns a domain to a principal's managed-domain
// set. Super-admin only (REQ-ADM-307).
func (s *Server) handleAssignManagedDomain(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireSuperAdmin(w, r, caller) {
		return
	}
	pid, ok := parsePID(w, r)
	if !ok {
		return
	}
	var req assignManagedDomainRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	domain := strings.ToLower(strings.TrimSpace(req.Domain))
	if domain == "" {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"domain is required", "")
		return
	}
	// Verify the target principal exists before assigning.
	target, err := s.store.Meta().GetPrincipalByID(r.Context(), pid)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	if err := s.store.Meta().AssignManagedDomain(r.Context(), pid, domain); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	s.appendAudit(r.Context(), "operator.assign_domain",
		fmt.Sprintf("principal:%d", pid),
		store.OutcomeSuccess, "",
		map[string]string{"domain": domain, "operator_email": target.CanonicalEmail})
	s.loggerFrom(r.Context()).Info("protoadmin.operator.assign_domain",
		"activity", observe.ActivityAudit,
		"actor_id", caller.ID,
		"target_id", pid,
		"domain", domain)
	w.WriteHeader(http.StatusNoContent)
}

// handleRevokeManagedDomain removes a domain from a principal's managed-domain
// set. Super-admin only (REQ-ADM-307).
func (s *Server) handleRevokeManagedDomain(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireSuperAdmin(w, r, caller) {
		return
	}
	pid, ok := parsePID(w, r)
	if !ok {
		return
	}
	domain := strings.ToLower(strings.TrimSpace(r.PathValue("domain")))
	if domain == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id",
			"domain is required", "")
		return
	}
	// Verify the target principal exists for better error messaging.
	target, err := s.store.Meta().GetPrincipalByID(r.Context(), pid)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	if err := s.store.Meta().RevokeManagedDomain(r.Context(), pid, domain); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	s.appendAudit(r.Context(), "operator.revoke_domain",
		fmt.Sprintf("principal:%d", pid),
		store.OutcomeSuccess, "",
		map[string]string{"domain": domain, "operator_email": target.CanonicalEmail})
	s.loggerFrom(r.Context()).Info("protoadmin.operator.revoke_domain",
		"activity", observe.ActivityAudit,
		"actor_id", caller.ID,
		"target_id", pid,
		"domain", domain)
	w.WriteHeader(http.StatusNoContent)
}
