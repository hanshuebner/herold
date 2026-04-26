package protoadmin

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/hanshuebner/herold/internal/store"
)

// attpolRequest is the body shape consumed by the per-recipient and
// per-domain attpol PUT handlers. Both fields are optional in the
// sense that an empty Policy is treated as AttPolicyUnset and deletes
// the row.
type attpolRequest struct {
	// Policy is the wire-form token: "accept" | "reject_at_data" |
	// "" (delete the row).
	Policy string `json:"policy"`
	// RejectText is the operator-overridable reply text used on a
	// 552 5.3.4 refusal. Empty falls back to the documented default
	// "attachments not accepted on this address".
	RejectText string `json:"reject_text,omitempty"`
}

// attpolResponse is the body shape returned on read / write.
type attpolResponse struct {
	Policy     string `json:"policy"`
	RejectText string `json:"reject_text,omitempty"`
}

// handleGetMailboxAttPol handles GET
// /api/v1/mailboxes/{addr}/attachment-policy. Returns the row
// effective at the address (per-recipient explicit, otherwise the
// recipient's domain row, otherwise the implicit default
// "accept").
func (s *Server) handleGetMailboxAttPol(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	addr := strings.ToLower(strings.TrimSpace(r.PathValue("addr")))
	if addr == "" || !strings.Contains(addr, "@") {
		writeProblem(w, r, http.StatusBadRequest, "invalid_address",
			"address must be lowercased local@domain", addr)
		return
	}
	row, err := s.store.Meta().GetInboundAttachmentPolicy(r.Context(), addr)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, attpolResponse{
		Policy:     row.Policy.String(),
		RejectText: row.RejectText,
	})
}

// handlePutMailboxAttPol handles PUT
// /api/v1/mailboxes/{addr}/attachment-policy. Upserts the
// per-recipient row. An empty Policy in the body deletes the row so
// the recipient falls back to the domain default.
func (s *Server) handlePutMailboxAttPol(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	addr := strings.ToLower(strings.TrimSpace(r.PathValue("addr")))
	if addr == "" || !strings.Contains(addr, "@") {
		writeProblem(w, r, http.StatusBadRequest, "invalid_address",
			"address must be lowercased local@domain", addr)
		return
	}
	var req attpolRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	policy := store.ParseInboundAttachmentPolicy(strings.TrimSpace(req.Policy))
	if req.Policy != "" && policy == store.AttPolicyUnset {
		writeProblem(w, r, http.StatusBadRequest, "invalid_policy",
			"policy must be one of accept, reject_at_data, or empty to delete", req.Policy)
		return
	}
	row := store.InboundAttachmentPolicyRow{
		Policy:     policy,
		RejectText: strings.TrimSpace(req.RejectText),
	}
	if err := s.store.Meta().SetInboundAttachmentPolicyRecipient(r.Context(), addr, row); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	s.appendAudit(r.Context(), "attpol.recipient.upsert",
		"recipient:"+addr,
		store.OutcomeSuccess,
		fmt.Sprintf("policy=%s", row.Policy.String()),
		map[string]string{"policy": row.Policy.String()})
	writeJSON(w, http.StatusOK, attpolResponse{
		Policy:     row.Policy.String(),
		RejectText: row.RejectText,
	})
}

// handleGetDomainAttPol handles GET
// /api/v1/domains/{name}/attachment-policy. Returns the per-domain row
// (or the implicit default).
func (s *Server) handleGetDomainAttPol(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	domain := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	if domain == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_domain",
			"domain must be a lowercase ASCII name", domain)
		return
	}
	// Resolve via a synthetic "@<domain>" lookup so the same code path
	// is exercised. The store's GetInboundAttachmentPolicy walks
	// per-recipient first then per-domain; passing a placeholder local
	// part forces the per-recipient lookup to miss and the result is
	// the per-domain row (or the implicit default).
	row, err := s.store.Meta().GetInboundAttachmentPolicy(r.Context(), "_attpol_get_@"+domain)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, attpolResponse{
		Policy:     row.Policy.String(),
		RejectText: row.RejectText,
	})
}

// handlePutDomainAttPol handles PUT
// /api/v1/domains/{name}/attachment-policy. Upserts the per-domain row.
func (s *Server) handlePutDomainAttPol(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	domain := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	if domain == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_domain",
			"domain must be a lowercase ASCII name", domain)
		return
	}
	var req attpolRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	policy := store.ParseInboundAttachmentPolicy(strings.TrimSpace(req.Policy))
	if req.Policy != "" && policy == store.AttPolicyUnset {
		writeProblem(w, r, http.StatusBadRequest, "invalid_policy",
			"policy must be one of accept, reject_at_data, or empty to delete", req.Policy)
		return
	}
	row := store.InboundAttachmentPolicyRow{
		Policy:     policy,
		RejectText: strings.TrimSpace(req.RejectText),
	}
	if err := s.store.Meta().SetInboundAttachmentPolicyDomain(r.Context(), domain, row); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	s.appendAudit(r.Context(), "attpol.domain.upsert",
		"domain:"+domain,
		store.OutcomeSuccess,
		fmt.Sprintf("policy=%s", row.Policy.String()),
		map[string]string{"policy": row.Policy.String()})
	writeJSON(w, http.StatusOK, attpolResponse{
		Policy:     row.Policy.String(),
		RejectText: row.RejectText,
	})
}
