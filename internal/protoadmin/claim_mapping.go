package protoadmin

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/hanshuebner/herold/internal/authz"
	"github.com/hanshuebner/herold/internal/store"
)

// External-IdP claim-to-grant mapping admin surface (epic #188,
// REQ-AC-60..70). The store primitives (authz_trusted flag, claim
// allowlist, mapping rules) and the reconciliation/sweep engine that
// consumes them already exist (internal/authz.ReconcileIdP,
// internal/store.Metadata's claim-mapping methods); this file is the
// operator-facing REST surface: setting a provider's authz_trusted flag,
// managing its authorization-claim allowlist, and mapping-rule CRUD.
//
// Every handler is mounted behind authAdmin (requireAuth + requireElevation)
// in routes.go and additionally calls requireAdmin here for the
// defence-in-depth reasons documented on requireAdmin's own doc comment.
// authz_trusted specifically requires requireSuperAdmin (REQ-AC-66: "a
// deliberate, superadmin-only, per-provider decision"). Rule create/delete
// additionally re-validate the caller's own delegable authority over the
// rule's target resource, mirroring exactly the author-authority check
// internal/authz.ReconcileIdP performs at evaluation time (REQ-AC-64/68) --
// a rule an operator could not have authored today cannot be authored or
// removed by them via this API either.

// claimMappingRuleDTO is the wire representation of a claim-mapping rule
// row, augmented with two operator-visible authority facts REQ-AC-68
// otherwise makes invisible: Orphaned (the author principal was deleted)
// and AuthorAuthorityValid (the author, even if not deleted, currently
// holds delegable authority over the rule's target -- re-validated live,
// exactly as internal/authz.ReconcileIdP does at evaluation time). A rule
// with either Orphaned=true or AuthorAuthorityValid=false is permanently
// inert (REQ-AC-68) and a superadmin needs to see and remove it.
type claimMappingRuleDTO struct {
	ID                   uint64    `json:"id"`
	Provider             string    `json:"provider"`
	Claim                string    `json:"claim"`
	MatchValue           string    `json:"match_value"`
	ResourceKind         string    `json:"resource_kind"`
	ResourceID           string    `json:"resource_id"`
	Level                string    `json:"level"`
	CreatedBy            uint64    `json:"created_by,omitempty"`
	Orphaned             bool      `json:"orphaned"`
	AuthorAuthorityValid bool      `json:"author_authority_valid"`
	CreatedAt            time.Time `json:"created_at"`
}

// claimMappingRuleDTOWithAuthority builds the DTO and re-validates the
// rule's author authority live (best-effort; any resolution failure --
// author lookup or grant resolution -- is reported as invalid, fail-closed,
// matching internal/authz.ReconcileIdP's own "treat as inert" handling of
// the same failure modes).
func (s *Server) claimMappingRuleDTOWithAuthority(r *http.Request, rule store.ClaimMappingRule) claimMappingRuleDTO {
	dto := claimMappingRuleDTO{
		ID:           uint64(rule.ID),
		Provider:     rule.ProviderName,
		Claim:        rule.Claim,
		MatchValue:   rule.MatchValue,
		ResourceKind: string(rule.ResourceKind),
		ResourceID:   rule.ResourceID,
		Level:        string(rule.Level),
		CreatedBy:    uint64(rule.CreatedBy),
		CreatedAt:    rule.CreatedAt,
	}
	if rule.CreatedBy == 0 {
		dto.Orphaned = true
		return dto
	}
	dto.AuthorAuthorityValid = s.hasDelegableAuthority(r, rule.CreatedBy, rule.ResourceKind, rule.ResourceID)
	return dto
}

// hasDelegableAuthority reports whether principalID currently holds
// delegable authority (owner/superadmin -- authz.DelegableLevel) over
// (kind, resourceID). Fail-closed: any lookup or resolution error yields
// false. Shared by rule creation (does the ACTING caller have authority to
// author this rule, REQ-AC-64), rule deletion, and rule listing (does the
// ORIGINAL author still have it, REQ-AC-68).
func (s *Server) hasDelegableAuthority(r *http.Request, principalID store.PrincipalID, kind store.GrantResourceKind, resourceID string) bool {
	p, err := s.store.Meta().GetPrincipalByID(r.Context(), principalID)
	if err != nil {
		return false
	}
	level, err := authz.Resolve(r.Context(), s.store.Meta(), p, authz.Resource{Kind: kind, ID: resourceID})
	if err != nil {
		return false
	}
	return authz.AtLeast(kind, level, authz.DelegableLevel(kind))
}

// -- authz_trusted -------------------------------------------------------

type setAuthzTrustedRequest struct {
	AuthzTrusted bool `json:"authz_trusted"`
}

// handleSetOIDCProviderAuthzTrusted flips a provider's authz_trusted flag
// (REQ-AC-66). Superadmin-only: claim-to-grant mapping trust is a distinct,
// deliberate decision from "this provider may authenticate logins", and
// REQ-AC-66 requires it be gated separately and more tightly.
func (s *Server) handleSetOIDCProviderAuthzTrusted(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireSuperAdmin(w, r, caller) {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id", "id is required", "")
		return
	}
	var req setAuthzTrustedRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if err := s.store.Meta().SetOIDCProviderAuthzTrusted(r.Context(), id, req.AuthzTrusted); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	got, err := s.store.Meta().GetOIDCProvider(r.Context(), id)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	s.appendAudit(r.Context(), "oidc.provider.authz_trusted.set",
		fmt.Sprintf("oidc_provider:%s", id), store.OutcomeSuccess, "",
		map[string]string{"authz_trusted": strconv.FormatBool(req.AuthzTrusted)})
	writeJSON(w, http.StatusOK, toOIDCProviderDTO(got))
}

// -- Authorization-claim allowlist ----------------------------------------

type claimAllowlistEntryDTO struct {
	Claim string `json:"claim"`
}

func (s *Server) handleListClaimAllowlist(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id", "id is required", "")
		return
	}
	if _, err := s.store.Meta().GetOIDCProvider(r.Context(), id); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	claims, err := s.store.Meta().ListClaimAllowlist(r.Context(), id)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	items := make([]claimAllowlistEntryDTO, 0, len(claims))
	for _, c := range claims {
		items = append(items, claimAllowlistEntryDTO{Claim: c})
	}
	writeJSON(w, http.StatusOK, pageDTO[claimAllowlistEntryDTO]{Items: items, Next: nil})
}

type addClaimAllowlistRequest struct {
	Claim string `json:"claim"`
}

func (s *Server) handleAddClaimAllowlistEntry(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id", "id is required", "")
		return
	}
	var req addClaimAllowlistRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.Claim == "" {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed", "claim is required", "")
		return
	}
	if _, err := s.store.Meta().GetOIDCProvider(r.Context(), id); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	if err := s.store.Meta().InsertClaimAllowlistEntry(r.Context(), id, req.Claim); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	s.appendAudit(r.Context(), "oidc.provider.claim_allowlist.add",
		fmt.Sprintf("oidc_provider:%s", id), store.OutcomeSuccess, "",
		map[string]string{"claim": req.Claim})
	writeJSON(w, http.StatusCreated, claimAllowlistEntryDTO(req))
}

func (s *Server) handleDeleteClaimAllowlistEntry(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	id := r.PathValue("id")
	claim := r.PathValue("claim")
	if id == "" || claim == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id", "id and claim are required", "")
		return
	}
	if err := s.store.Meta().DeleteClaimAllowlistEntry(r.Context(), id, claim); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	s.appendAudit(r.Context(), "oidc.provider.claim_allowlist.remove",
		fmt.Sprintf("oidc_provider:%s", id), store.OutcomeSuccess, "",
		map[string]string{"claim": claim})
	w.WriteHeader(http.StatusNoContent)
}

// -- Claim-mapping rules ---------------------------------------------------

func (s *Server) handleListClaimMappingRules(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id", "id is required", "")
		return
	}
	if _, err := s.store.Meta().GetOIDCProvider(r.Context(), id); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	rules, err := s.store.Meta().ListClaimMappingRules(r.Context(), id)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	items := make([]claimMappingRuleDTO, 0, len(rules))
	for _, rule := range rules {
		items = append(items, s.claimMappingRuleDTOWithAuthority(r, rule))
	}
	writeJSON(w, http.StatusOK, pageDTO[claimMappingRuleDTO]{Items: items, Next: nil})
}

type createClaimMappingRuleRequest struct {
	Claim        string `json:"claim"`
	MatchValue   string `json:"match_value"`
	ResourceKind string `json:"resource_kind"`
	ResourceID   string `json:"resource_id"`
	Level        string `json:"level"`
}

// handleCreateClaimMappingRule authors a new claim-mapping rule
// (REQ-AC-60). Safety rails enforced here, before the row is ever written:
//
//   - resource_kind "server" is always rejected -- server:superadmin is
//     never IdP-derivable (REQ-AC-64), full stop, so there is no admin
//     path to create such a rule even transiently.
//   - resource_kind/level must be one of the known per-kind values
//     (authz.ValidLevel); a typo is rejected rather than silently stored
//     as a permanently-non-matching rule.
//   - the caller must currently hold delegable authority (owner /
//     superadmin) over the named (resource_kind, resource_id) -- "a rule
//     MAY only target resources the authoring operator controls"
//     (REQ-AC-64). This is the exact check internal/authz.ReconcileIdP
//     re-runs against the rule's author at every evaluation (REQ-AC-68),
//     so a rule that passes creation stays live only as long as its
//     author's own authority does.
func (s *Server) handleCreateClaimMappingRule(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id", "id is required", "")
		return
	}
	var req createClaimMappingRuleRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.Claim == "" || req.MatchValue == "" {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"claim and match_value are required", "")
		return
	}
	// REQ-AC-64's hardest bar is checked first and unconditionally,
	// independent of resource_id: no admin path may ever create a
	// server-kind rule, so this rejection cannot be sidestepped by an
	// unusual resource_id value.
	kind := store.GrantResourceKind(req.ResourceKind)
	if kind == store.GrantResourceServer {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"resource_kind \"server\" is never IdP-derivable (REQ-AC-64)", "")
		return
	}
	if req.ResourceID == "" {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"resource_id is required", "")
		return
	}
	level := store.GrantLevel(req.Level)
	if !authz.ValidLevel(kind, level) {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			fmt.Sprintf("level %q is not a valid level for resource_kind %q", req.Level, req.ResourceKind), "")
		return
	}
	if _, err := s.store.Meta().GetOIDCProvider(r.Context(), id); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	if !s.hasDelegableAuthority(r, caller.ID, kind, req.ResourceID) {
		writeProblem(w, r, http.StatusForbidden, "forbidden",
			"you may only author a claim-mapping rule targeting a resource you control (owner/superadmin)", "")
		return
	}
	rule, err := s.store.Meta().InsertClaimMappingRule(r.Context(), store.ClaimMappingRule{
		ProviderName: id,
		Claim:        req.Claim,
		MatchValue:   req.MatchValue,
		ResourceKind: kind,
		ResourceID:   req.ResourceID,
		Level:        level,
		CreatedBy:    caller.ID,
	})
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	domain := ""
	if kind == store.GrantResourceDomain {
		domain = req.ResourceID
	}
	s.appendAuditDomain(r.Context(), "oidc.claim_mapping_rule.create",
		fmt.Sprintf("oidc_claim_mapping_rule:%d", rule.ID), store.OutcomeSuccess, "",
		map[string]string{
			"provider":      id,
			"claim":         req.Claim,
			"resource_kind": req.ResourceKind,
			"resource_id":   req.ResourceID,
			"level":         req.Level,
		}, domain)
	writeJSON(w, http.StatusCreated, s.claimMappingRuleDTOWithAuthority(r, rule))
}

// handleDeleteClaimMappingRule removes a claim-mapping rule. Permitted for
// a server:superadmin (who must be able to clean up a permanently-inert
// orphaned rule, REQ-AC-68's stated operator need), the rule's original
// author, or any principal who currently holds delegable authority over
// the rule's target resource -- the same authority bar as creation
// (REQ-AC-64), so "you can revoke what you could grant" holds symmetrically.
func (s *Server) handleDeleteClaimMappingRule(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	id := r.PathValue("id")
	ruleIDStr := r.PathValue("rule_id")
	if id == "" || ruleIDStr == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id", "id and rule_id are required", "")
		return
	}
	ruleIDNum, err := strconv.ParseUint(ruleIDStr, 10, 64)
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id", "rule_id must be numeric", "")
		return
	}
	ruleID := store.ClaimMappingRuleID(ruleIDNum)

	rules, err := s.store.Meta().ListClaimMappingRules(r.Context(), id)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	var target *store.ClaimMappingRule
	for i := range rules {
		if rules[i].ID == ruleID {
			target = &rules[i]
			break
		}
	}
	if target == nil {
		writeProblem(w, r, http.StatusNotFound, "not_found", "claim-mapping rule not found", "")
		return
	}

	isSuperAdmin := caller.Flags.Has(store.PrincipalFlagAdmin) && caller.Flags.Has(store.PrincipalFlagSuperAdmin)
	isAuthor := target.CreatedBy != 0 && target.CreatedBy == caller.ID
	if !isSuperAdmin && !isAuthor && !s.hasDelegableAuthority(r, caller.ID, target.ResourceKind, target.ResourceID) {
		writeProblem(w, r, http.StatusForbidden, "forbidden",
			"insufficient privileges to remove this claim-mapping rule", "")
		return
	}

	if err := s.store.Meta().DeleteClaimMappingRule(r.Context(), ruleID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeProblem(w, r, http.StatusNotFound, "not_found", "claim-mapping rule not found", "")
			return
		}
		s.writeStoreError(w, r, err)
		return
	}
	domain := ""
	if target.ResourceKind == store.GrantResourceDomain {
		domain = target.ResourceID
	}
	s.appendAuditDomain(r.Context(), "oidc.claim_mapping_rule.delete",
		fmt.Sprintf("oidc_claim_mapping_rule:%d", ruleID), store.OutcomeSuccess, "",
		map[string]string{
			"provider":      id,
			"claim":         target.Claim,
			"resource_kind": string(target.ResourceKind),
			"resource_id":   target.ResourceID,
			"level":         string(target.Level),
		}, domain)
	w.WriteHeader(http.StatusNoContent)
}
