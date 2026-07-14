package protoadmin

// mlist_moderators.go — admin REST surface for assigning/revoking a
// list's list:moderator grant (REQ-AC-41, REQ-MLIST-80, issue #189:
// "Introduces the list:moderator grant... assignable by the
// list:owner"). Distinct from the held-post moderation actions
// themselves (mlist_moderation.go): this is list-CONFIG-level authority
// (who may moderate), so it is gated the same as the rest of the S1
// list-config surface (requireMlistListAccess: super-admin,
// domain:operator, or list:owner) rather than requireMlistModerateAccess
// -- a bare moderator may action held posts but must not be able to
// mint MORE moderators (REQ-AC-41: "without config... write authority").
//
// Endpoint shape:
//
//	GET    /api/v1/lists/{id}/moderators               current moderator grants
//	POST   /api/v1/lists/{id}/moderators                {principal_id} grant
//	DELETE /api/v1/lists/{id}/moderators/{principal_id}  revoke

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/hanshuebner/herold/internal/store"
)

// mailingListModeratorDTO is the wire representation of one
// list:moderator grant row.
type mailingListModeratorDTO struct {
	PrincipalID uint64 `json:"principal_id"`
	GrantedAt   string `json:"granted_at"`
}

// handleListMailingListModerators handles GET
// /api/v1/lists/{id}/moderators.
func (s *Server) handleListMailingListModerators(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	l, ok := s.loadListAndCheckAccess(w, r, caller)
	if !ok {
		return
	}
	grants, err := s.store.Meta().ListGrantsOnResource(r.Context(), store.GrantResourceList, mlistResourceID(l.ID))
	if err != nil {
		s.writeMlistError(w, r, err)
		return
	}
	items := make([]mailingListModeratorDTO, 0, len(grants))
	for _, g := range grants {
		if g.Level != store.GrantLevelModerator {
			continue
		}
		items = append(items, mailingListModeratorDTO{
			PrincipalID: g.SubjectID,
			GrantedAt:   g.GrantedAt.Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	writeJSON(w, http.StatusOK, pageDTO[mailingListModeratorDTO]{Items: items})
}

// addMailingListModeratorRequest is the POST
// /api/v1/lists/{id}/moderators body.
type addMailingListModeratorRequest struct {
	PrincipalID uint64 `json:"principal_id"`
}

// handleAddMailingListModerator handles POST
// /api/v1/lists/{id}/moderators.
func (s *Server) handleAddMailingListModerator(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	l, ok := s.loadListAndCheckAccess(w, r, caller)
	if !ok {
		return
	}
	var req addMailingListModeratorRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.PrincipalID == 0 {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed", "principal_id is required", "")
		return
	}
	target := store.PrincipalID(req.PrincipalID)
	if _, err := s.store.Meta().GetPrincipalByID(r.Context(), target); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"principal_id does not reference a known principal", "")
		return
	}
	if err := s.grantListModerator(r.Context(), l, target, caller.ID); err != nil {
		s.writeMlistError(w, r, err)
		return
	}
	s.appendAuditDomain(r.Context(), "mlist.moderator.grant",
		fmt.Sprintf("mlist:%d", l.ID),
		store.OutcomeSuccess, "", map[string]string{
			"principal_id": strconv.FormatUint(req.PrincipalID, 10),
		}, l.Domain)
	w.WriteHeader(http.StatusNoContent)
}

// handleRemoveMailingListModerator handles DELETE
// /api/v1/lists/{id}/moderators/{principal_id}.
func (s *Server) handleRemoveMailingListModerator(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	l, ok := s.loadListAndCheckAccess(w, r, caller)
	if !ok {
		return
	}
	raw := r.PathValue("pid")
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || n == 0 {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id", "principal id must be a positive integer", raw)
		return
	}
	if err := s.revokeListModerator(r.Context(), l, store.PrincipalID(n)); err != nil {
		s.writeMlistError(w, r, err)
		return
	}
	s.appendAuditDomain(r.Context(), "mlist.moderator.revoke",
		fmt.Sprintf("mlist:%d", l.ID),
		store.OutcomeSuccess, "", map[string]string{
			"principal_id": strconv.FormatUint(n, 10),
		}, l.Domain)
	w.WriteHeader(http.StatusNoContent)
}
