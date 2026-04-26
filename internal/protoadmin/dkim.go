package protoadmin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/hanshuebner/herold/internal/store"
)

// DKIMKeyManager is the protoadmin-facing surface of keymgmt.Manager.
// It is defined here as an interface so the admin server does not import
// the keymgmt package directly, keeping import cycles flat. The concrete
// implementation is keymgmt.Manager; tests supply a stub.
//
// GenerateKey generates (or rotates to) a fresh signing key for domain
// using algorithm alg. It persists the new key as active, transitions
// any prior active key to retiring, and returns the chosen selector.
//
// PublishedRecord returns the v=DKIM1 TXT content for key, ready to
// paste into a DNS zone file.
type DKIMKeyManager interface {
	GenerateKey(ctx context.Context, domain string, alg store.DKIMAlgorithm) (string, error)
	PublishedRecord(ctx context.Context, key store.DKIMKey) (string, error)
}

// handleGenerateDKIMKey implements POST /api/v1/domains/{name}/dkim.
//
// Rotate-or-generate semantics: if an active key exists for the domain it
// is transitioned to retiring and a new key is installed as active; if no
// key exists the first key is generated. The operation is therefore always
// idempotent in the sense that it always produces exactly one active key,
// but it is NOT safe to call repeatedly without intent — each call
// generates a fresh selector and the caller should publish the new TXT
// before the old one is removed.
//
// REQ-ADM-11: /dkim is a documented subresource of /domains.
// REQ-OPS-60: explicit rotation knob (complement to the automatic key
// generated on domain add).
// REQ-OPS-62: old selector transitions to retiring; DNS grace-period
// removal is operator-driven via DELETE (or a follow-up wave).
func (s *Server) handleGenerateDKIMKey(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	if s.opts.DKIMKeyManager == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not_implemented",
			"DKIM key management is not configured on this server", "")
		return
	}
	name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	if name == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id",
			"domain name is required", "")
		return
	}
	// Verify the domain is known to the store before generating a key so the
	// response is a clean 404 rather than a key dangling without a domain row.
	if _, err := s.store.Meta().GetDomain(r.Context(), name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeProblem(w, r, http.StatusNotFound, "not_found",
				"domain not found", name)
			return
		}
		s.writeStoreError(w, r, err)
		return
	}

	// Default to Ed25519 (smaller, faster, RFC 8463). A later wave may expose
	// the algorithm as a request body parameter; for now it is fixed so the
	// endpoint stays zero-body (a bare POST triggers the rotate).
	alg := store.DKIMAlgorithmEd25519SHA256
	selector, err := s.opts.DKIMKeyManager.GenerateKey(r.Context(), name, alg)
	if err != nil {
		s.loggerFrom(r.Context()).Error("protoadmin.dkim.generate_failed",
			"domain", name, "err", err)
		writeProblem(w, r, http.StatusInternalServerError, "internal_error",
			"DKIM key generation failed", err.Error())
		return
	}

	// Re-read the generated key so we can build the TXT body for the caller.
	key, err := s.store.Meta().GetActiveDKIMKey(r.Context(), name)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	txt, err := s.opts.DKIMKeyManager.PublishedRecord(r.Context(), key)
	if err != nil {
		s.loggerFrom(r.Context()).Error("protoadmin.dkim.txt_build_failed",
			"domain", name, "selector", selector, "err", err)
		writeProblem(w, r, http.StatusInternalServerError, "internal_error",
			"DKIM TXT record build failed", err.Error())
		return
	}

	// TODO(autodns): wire this rotation event to autodns.Publisher so the
	// DNS plugin picks up the new selector automatically. For now operators
	// must publish the TXT manually (or via the DNS plugin's own reconcile
	// loop). The txt body in the response is ready to paste into a zone file.
	s.appendAudit(r.Context(), "dkim_rotate",
		fmt.Sprintf("domain:%s", name),
		store.OutcomeSuccess, "", map[string]string{
			"selector":  selector,
			"algorithm": alg.String(),
		})

	w.Header().Set("Location", fmt.Sprintf("/api/v1/domains/%s/dkim", name))
	writeJSON(w, http.StatusCreated, dkimKeyDTO{
		Selector:  selector,
		Algorithm: alg.String(),
		IsActive:  true,
		CreatedAt: key.CreatedAt,
		TXTRecord: txt,
	})
}

// handleListDKIMKeys implements GET /api/v1/domains/{name}/dkim.
//
// Returns every key for the domain (any status) plus the DKIM TXT body for
// each — the TXT body is the operator-visible artefact (REQ-ADM-310).
func (s *Server) handleListDKIMKeys(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	if s.opts.DKIMKeyManager == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not_implemented",
			"DKIM key management is not configured on this server", "")
		return
	}
	name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	if name == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id",
			"domain name is required", "")
		return
	}
	if _, err := s.store.Meta().GetDomain(r.Context(), name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeProblem(w, r, http.StatusNotFound, "not_found",
				"domain not found", name)
			return
		}
		s.writeStoreError(w, r, err)
		return
	}
	keys, err := s.store.Meta().ListDKIMKeys(r.Context(), name)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	items := make([]dkimKeyDTO, 0, len(keys))
	for _, k := range keys {
		txt, err := s.opts.DKIMKeyManager.PublishedRecord(r.Context(), k)
		if err != nil {
			// Log and skip the TXT rather than aborting the list.
			s.loggerFrom(r.Context()).Warn("protoadmin.dkim.txt_build_failed",
				"domain", name, "selector", k.Selector, "err", err)
		}
		items = append(items, toDKIMKeyDTO(k, txt))
	}
	writeJSON(w, http.StatusOK, pageDTO[dkimKeyDTO]{Items: items, Next: nil})
}

// handleDeleteDKIMKey implements DELETE /api/v1/domains/{name}/dkim/{selector}.
//
// Retires a specific selector (transitions it to DKIMKeyStatusRetired).
// Callers use this after the DNS grace period has elapsed following a
// rotation. The route is registered only when DKIMKeyManager is non-nil.
func (s *Server) handleDeleteDKIMKey(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	if s.opts.DKIMKeyManager == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not_implemented",
			"DKIM key management is not configured on this server", "")
		return
	}
	name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	selector := strings.TrimSpace(r.PathValue("selector"))
	if name == "" || selector == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id",
			"domain name and selector are required", "")
		return
	}

	keys, err := s.store.Meta().ListDKIMKeys(r.Context(), name)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	var target *store.DKIMKey
	for i := range keys {
		if keys[i].Selector == selector {
			target = &keys[i]
			break
		}
	}
	if target == nil {
		writeProblem(w, r, http.StatusNotFound, "not_found",
			"selector not found", selector)
		return
	}
	if target.Status == store.DKIMKeyStatusActive {
		writeProblem(w, r, http.StatusConflict, "conflict",
			"cannot retire the active key; generate a new key first to rotate, then delete", selector)
		return
	}
	target.Status = store.DKIMKeyStatusRetired
	if err := s.store.Meta().UpsertDKIMKey(r.Context(), *target); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	s.appendAudit(r.Context(), "dkim_retire",
		fmt.Sprintf("domain:%s", name),
		store.OutcomeSuccess, "", map[string]string{
			"selector": selector,
		})
	w.WriteHeader(http.StatusNoContent)
}
