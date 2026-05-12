package protoadmin

// tagged_address_dismissals.go implements the REST surface for the
// tagged-address dismissal table (REQ-TAG-50..57, REQ-TAG-60..62).
//
// Dismissals are a private signalling mechanism for the SPA banner gate
// (REQ-TAG-71): the user has chosen to ignore a (base Identity, suffix)
// pair so the suite must NOT re-surface the per-message prompt for that
// suffix. Dismissals never appear on the JMAP wire — they are
// REST-only, scoped to the authenticated caller, mounted on the public
// listener.
//
// Endpoints:
//   - POST /api/v1/tagged-address-dismissals
//     Body: {"base_identity_id":"<id>","suffix":"<suffix>"}
//     Creates (or idempotently re-acknowledges) one dismissal row for
//     the caller. Returns 201 on first create, 200 on duplicate.
//     REQ-TAG-50..56, REQ-TAG-60.
//   - GET /api/v1/tagged-address-dismissals
//     Returns every dismissal owned by the caller (REQ-TAG-52).
//   - DELETE /api/v1/tagged-address-dismissals/{base_identity_id}/{suffix}
//     Idempotently removes the named dismissal (REQ-TAG-57, REQ-TAG-62).
//
// Authentication: self-only via requireAuth — the caller's principal
// scopes every read and write. There is no admin-impersonation path
// (mirroring the rest of the user-state surfaces).
//
// Audit: create + delete emit tagged_address_dismissal.{create,destroy}
// rows (REQ-TAG-90). Per-message banner gates do NOT generate audit
// events (consistent with REQ-TAG-21's per-message-silence stance).

import (
	"errors"
	"net/http"
	"strings"

	"github.com/hanshuebner/herold/internal/store"
)

// dismissalRequest is the POST body shape for a create call.
type dismissalRequest struct {
	BaseIdentityID string `json:"base_identity_id"`
	Suffix         string `json:"suffix"`
}

// dismissalResponse is the wire-form dismissal row returned by GET (and
// echoed back on a successful POST so the SPA can stamp its cache
// without a follow-up read).
type dismissalResponse struct {
	BaseIdentityID string `json:"base_identity_id"`
	Suffix         string `json:"suffix"`
	DismissedAt    string `json:"dismissed_at"`
}

// dismissalListResponse wraps the GET payload in an object envelope so
// future additions (paging, capability flags) can land without breaking
// the wire contract.
type dismissalListResponse struct {
	Dismissals []dismissalResponse `json:"dismissals"`
}

// validateDismissalSuffix mirrors the JMAP-side validateSuffix gate
// (internal/protojmap/mail/taggedaddress/types.go). The REST surface
// applies the same rules so a dismissal row's suffix cannot diverge
// from a filter row's stored shape: empty rejected, 64-byte cap,
// no control bytes, no '@'. Lower-casing is done by the store on
// insert (REQ-TAG-02); the handler accepts mixed-case input.
func validateDismissalSuffix(s string) error {
	if s == "" {
		return errors.New("suffix must not be empty")
	}
	if len(s) > 64 {
		return errors.New("suffix exceeds 64-byte limit")
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c == 0x7f || c == '@' {
			return errors.New("suffix contains forbidden byte")
		}
	}
	return nil
}

// handleCreateTaggedAddressDismissal implements POST
// /api/v1/tagged-address-dismissals (REQ-TAG-60). The body names a
// (base_identity_id, suffix) pair; the store inserts (or idempotently
// no-ops on duplicate) one row scoped to the authenticated principal.
// Cap enforcement (REQ-TAG-11, 500 dismissals per principal) lives in
// the store; the handler maps store.ErrTooManyDismissals to 422 with a
// clear reason.
func (s *Server) handleCreateTaggedAddressDismissal(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	caller, ok := principalFrom(ctx)
	if !ok {
		writeProblem(w, r, http.StatusUnauthorized, "unauthenticated",
			"session required", "")
		return
	}

	var body dismissalRequest
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.BaseIdentityID) == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_body",
			"base_identity_id is required", "")
		return
	}
	if err := validateDismissalSuffix(body.Suffix); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid_suffix",
			err.Error(), "")
		return
	}

	// The wire-form id "default" denotes the caller's synthesised
	// default identity. It is owned by the principal by construction
	// but has no jmap_identities row until first referenced; the
	// tagged_address_dismissals FK requires a real row, so we
	// materialise it on demand and rewrite the body in place so the
	// rest of the handler (insert + response echo) uses the persisted
	// numeric id rather than the literal "default".
	if body.BaseIdentityID == "default" {
		matID, mErr := s.store.Meta().MaterializeDefaultIdentity(ctx, caller.ID)
		if mErr != nil {
			s.writeStoreError(w, r, mErr)
			return
		}
		body.BaseIdentityID = matID
	}

	// Verify the identity exists AND belongs to the caller. The store
	// FK enforces existence, but we surface ownership as 404 rather
	// than letting the insert fall through with a foreign-key error.
	ident, err := s.store.Meta().GetJMAPIdentity(ctx, body.BaseIdentityID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeProblem(w, r, http.StatusNotFound, "not_found",
				"identity not found", body.BaseIdentityID)
			return
		}
		s.writeStoreError(w, r, err)
		return
	}
	if ident.PrincipalID != caller.ID {
		writeProblem(w, r, http.StatusNotFound, "not_found",
			"identity not found", body.BaseIdentityID)
		return
	}

	// Probe for an existing row so we can distinguish 201 from 200 on
	// the return path. The store insert is idempotent (REQ-TAG-60); we
	// re-read after the insert to fetch the stable dismissed_at stamp.
	_, alreadyDismissed, err := s.store.Meta().HasTaggedAddressFilterOrDismissal(ctx, caller.ID, body.BaseIdentityID, body.Suffix)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}

	d := store.TaggedAddressDismissal{
		PrincipalID:    caller.ID,
		BaseIdentityID: body.BaseIdentityID,
		Suffix:         body.Suffix,
	}
	if err := s.store.Meta().InsertTaggedAddressDismissal(ctx, d); err != nil {
		if errors.Is(err, store.ErrTooManyDismissals) {
			writeProblem(w, r, http.StatusUnprocessableEntity, "too_many_dismissals",
				"per-principal dismissal cap reached", "")
			return
		}
		if errors.Is(err, store.ErrInvalidArgument) {
			writeProblem(w, r, http.StatusBadRequest, "invalid_argument",
				err.Error(), "")
			return
		}
		s.writeStoreError(w, r, err)
		return
	}

	// Re-read to get the canonical dismissed_at stamp the store
	// assigned. The list-query is the only read path; one round trip
	// is acceptable because dismissal mutations are rare (user-driven,
	// one click per suffix in the SPA banner).
	rows, err := s.store.Meta().ListTaggedAddressDismissalsForPrincipal(ctx, caller.ID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	suffix := strings.ToLower(body.Suffix)
	var resp dismissalResponse
	for _, row := range rows {
		if row.BaseIdentityID == body.BaseIdentityID && row.Suffix == suffix {
			resp = dismissalResponse{
				BaseIdentityID: row.BaseIdentityID,
				Suffix:         row.Suffix,
				DismissedAt:    row.DismissedAt.UTC().Format("2006-01-02T15:04:05Z"),
			}
			break
		}
	}

	// Audit only on first create — a duplicate POST is a no-op from
	// the user's perspective and emitting an audit row each time
	// would let a buggy SPA's retry loop spam the log.
	if !alreadyDismissed {
		s.appendAudit(ctx, "tagged_address_dismissal.create",
			body.BaseIdentityID+"+"+suffix,
			store.OutcomeSuccess, "", map[string]string{
				"base_identity_id": body.BaseIdentityID,
				"suffix":           suffix,
			})
		// REQ-TAG-72: dismissal mutations bump the JMAP Identity state
		// token so the SPA's Identity/changes push invalidates the
		// per-tab cache for other tabs / clients. Best-effort: a
		// failure here is logged but does not invalidate the user's
		// mutation, which has already landed (mirrors the audit-log
		// error pattern).
		if _, err := s.store.Meta().IncrementJMAPState(ctx, caller.ID,
			store.JMAPStateKindIdentity); err != nil {
			s.logger.WarnContext(ctx, "tagged_address_dismissals.create: state bump failed",
				"err", err.Error(),
				"principal_id", uint64(caller.ID))
		}
		writeJSON(w, http.StatusCreated, resp)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleListTaggedAddressDismissals implements GET
// /api/v1/tagged-address-dismissals (REQ-TAG-52). Returns every
// dismissal owned by the caller in (base_identity_id, suffix) order.
func (s *Server) handleListTaggedAddressDismissals(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	caller, ok := principalFrom(ctx)
	if !ok {
		writeProblem(w, r, http.StatusUnauthorized, "unauthenticated",
			"session required", "")
		return
	}
	rows, err := s.store.Meta().ListTaggedAddressDismissalsForPrincipal(ctx, caller.ID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	out := dismissalListResponse{Dismissals: make([]dismissalResponse, 0, len(rows))}
	for _, row := range rows {
		out.Dismissals = append(out.Dismissals, dismissalResponse{
			BaseIdentityID: row.BaseIdentityID,
			Suffix:         row.Suffix,
			DismissedAt:    row.DismissedAt.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDeleteTaggedAddressDismissal implements DELETE
// /api/v1/tagged-address-dismissals/{base_identity_id}/{suffix}
// (REQ-TAG-57, REQ-TAG-62). Idempotent: a missing row returns 204 with
// no audit entry so retries are silent.
func (s *Server) handleDeleteTaggedAddressDismissal(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	caller, ok := principalFrom(ctx)
	if !ok {
		writeProblem(w, r, http.StatusUnauthorized, "unauthenticated",
			"session required", "")
		return
	}
	baseIdentityID := r.PathValue("base_identity_id")
	suffix := r.PathValue("suffix")
	if baseIdentityID == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id",
			"base_identity_id is required", "")
		return
	}
	if err := validateDismissalSuffix(suffix); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid_suffix",
			err.Error(), "")
		return
	}
	// Symmetry with the POST path: a tab whose cache still holds the
	// pre-materialisation "default" wire-form id MUST be able to
	// delete its dismissal. Materialise on demand and rewrite to the
	// persisted numeric id before the store lookup / delete.
	if baseIdentityID == "default" {
		matID, mErr := s.store.Meta().MaterializeDefaultIdentity(ctx, caller.ID)
		if mErr != nil {
			s.writeStoreError(w, r, mErr)
			return
		}
		baseIdentityID = matID
	}

	// Probe for the row before deleting so we know whether to emit an
	// audit entry. The store-side delete is idempotent (no row -> no
	// error), which is the right semantics for retry-safety; we just
	// don't want to spam audit on retries.
	_, hadDismissal, err := s.store.Meta().HasTaggedAddressFilterOrDismissal(ctx, caller.ID, baseIdentityID, suffix)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	if err := s.store.Meta().DeleteTaggedAddressDismissal(ctx, caller.ID, baseIdentityID, suffix); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	if hadDismissal {
		lower := strings.ToLower(suffix)
		s.appendAudit(ctx, "tagged_address_dismissal.destroy",
			baseIdentityID+"+"+lower,
			store.OutcomeSuccess, "", map[string]string{
				"base_identity_id": baseIdentityID,
				"suffix":           lower,
			})
		// REQ-TAG-72: bump JMAP Identity state on a destroy that
		// actually removed a row so the SPA's Identity/changes push
		// invalidates other tabs' caches. Idempotent re-DELETE on a
		// missing row does NOT advance the state (the user's view did
		// not change). Best-effort error handling parallels the
		// create branch above.
		if _, err := s.store.Meta().IncrementJMAPState(ctx, caller.ID,
			store.JMAPStateKindIdentity); err != nil {
			s.logger.WarnContext(ctx, "tagged_address_dismissals.destroy: state bump failed",
				"err", err.Error(),
				"principal_id", uint64(caller.ID))
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
