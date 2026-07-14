package protoadmin

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/hanshuebner/herold/internal/authz"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// handleListOperators lists every domain-scoped operator (admin but not
// super-admin) together with each operator's managed-domain set, sourced
// from grants (REQ-ADM-307, re #237). Super-admin only.
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
		domains, err := authz.OperatorDomains(r.Context(), s.store.Meta(), p)
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

// handleListManagedDomains returns the managed-domain set for a principal,
// sourced from its domain:operator (or higher) grants (REQ-ADM-307, re #237).
// Super-admin only.
func (s *Server) handleListManagedDomains(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireSuperAdmin(w, r, caller) {
		return
	}
	pid, ok := parsePID(w, r)
	if !ok {
		return
	}
	target, err := s.store.Meta().GetPrincipalByID(r.Context(), pid)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	domains, err := authz.OperatorDomains(r.Context(), s.store.Meta(), target)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	if domains == nil {
		domains = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"domains": domains})
}

// handleAssignManagedDomain grants a principal domain:operator authority on a
// domain (REQ-ADM-307, re #237). The grant is provenance "local", writeable
// only by a super-admin. Idempotent: assigning a domain the principal already
// holds domain:operator or higher on is a no-op.
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
	granter := caller.ID
	if _, err := s.store.Meta().InsertGrant(r.Context(), store.Grant{
		SubjectKind:  store.GrantSubjectPrincipal,
		SubjectID:    uint64(pid),
		ResourceKind: store.GrantResourceDomain,
		ResourceID:   domain,
		Level:        store.GrantLevelOperator,
		Provenance:   store.GrantProvenanceLocal,
		GrantedBy:    &granter,
	}); err != nil {
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

// handleRevokeManagedDomain deletes a principal's local domain:operator grant
// on a domain (REQ-ADM-307, re #237). Super-admin only.
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
	if err := s.store.Meta().DeleteGrant(r.Context(), store.Grant{
		SubjectKind:  store.GrantSubjectPrincipal,
		SubjectID:    uint64(pid),
		ResourceKind: store.GrantResourceDomain,
		ResourceID:   domain,
		Provenance:   store.GrantProvenanceLocal,
	}); err != nil {
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
