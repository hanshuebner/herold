package protoadmin

// mlist_members.go — admin REST surface for hosted mailing list rosters
// (epic #183, REQ-MLIST-02/03/04, REQ-MLIST-40a).
//
// Endpoint shape (all behind authAdmin, then requireMlistListAccess on the
// parent list):
//
//	GET    /api/v1/lists/{id}/members            roster page
//	POST   /api/v1/lists/{id}/members            add one member
//	PATCH  /api/v1/lists/{id}/members/{mid}       update state/delivery_mode
//	                                              (state=active is a REQ-MLIST-55
//	                                              reactivation: also resets bounce_score)
//	DELETE /api/v1/lists/{id}/members/{mid}       remove one member
//	POST   /api/v1/lists/{id}/members/import      bulk add
//	GET    /api/v1/lists/{id}/members/export      full roster dump
//	GET    /api/v1/lists/{id}/members/summary     REQ-MLIST-54 per-list roster counts by state

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/hanshuebner/herold/internal/store"
)

// mlistMemberImportCap bounds a single POST .../members/import request so a
// misbehaving client cannot force one handler invocation to hold an
// unbounded number of rows in memory. Generous relative to S1's
// "domain-admin maintained small lists" scope (REQ-MLIST-05).
const mlistMemberImportCap = 5000

// mlistMemberExportCap bounds a single GET .../members/export response for
// the same reason: a roster larger than the cap is walked to completion
// across several requests via the same ?after= cursor every other
// collection endpoint uses (Next is set, exactly as it would be if the
// store's own 1000-row-per-call ceiling had been hit).
const mlistMemberExportCap = 50000

// parseMailingListMemberID reads the {mid} path parameter.
func parseMailingListMemberID(w http.ResponseWriter, r *http.Request) (store.MailingListMemberID, bool) {
	raw := r.PathValue("mid")
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || n == 0 {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id",
			"member id must be a positive integer", raw)
		return 0, false
	}
	return store.MailingListMemberID(n), true
}

// loadListAndCheckAccess is the common prologue for every roster handler:
// load the parent list, then apply the same per-list authorization as the
// list-config endpoints (REQ-MLIST-05). Callers MUST have already passed
// requireAdmin.
func (s *Server) loadListAndCheckAccess(w http.ResponseWriter, r *http.Request, caller store.Principal) (store.MailingList, bool) {
	l, ok := s.loadMailingList(w, r)
	if !ok {
		return store.MailingList{}, false
	}
	if !s.requireMlistListAccess(w, r, caller, l) {
		return store.MailingList{}, false
	}
	return l, true
}

// loadMemberInList loads a roster row by {mid} and verifies it belongs to
// l — member ids are a global sequence, so without this check a caller
// authorized on list A could address a member row belonging to list B via
// the URL alone.
func (s *Server) loadMemberInList(w http.ResponseWriter, r *http.Request, l store.MailingList) (store.MailingListMember, bool) {
	mid, ok := parseMailingListMemberID(w, r)
	if !ok {
		return store.MailingListMember{}, false
	}
	m, err := s.store.Meta().GetMailingListMember(r.Context(), mid)
	if err != nil {
		s.writeMlistError(w, r, err)
		return store.MailingListMember{}, false
	}
	if m.ListID != l.ID {
		writeProblem(w, r, http.StatusNotFound, "not_found",
			"member not found on this list", "")
		return store.MailingListMember{}, false
	}
	return m, true
}

// handleListMailingListMembers handles GET /api/v1/lists/{id}/members.
func (s *Server) handleListMailingListMembers(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	l, ok := s.loadListAndCheckAccess(w, r, caller)
	if !ok {
		return
	}
	q := r.URL.Query()
	state, ok := mlistStateFromString(q.Get("state"))
	if !ok {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"state must be one of active, suspended, unsubscribed, pending, awaiting-approval", q.Get("state"))
		return
	}
	mode, ok := mlistDeliveryModeFromString(q.Get("delivery_mode"))
	if !ok {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"delivery_mode must be one of each, nomail", q.Get("delivery_mode"))
		return
	}
	after, limit, ok := parseAfterLimit(w, r, 0, 100, 1000)
	if !ok {
		return
	}
	rows, err := s.store.Meta().ListMailingListMembers(r.Context(), store.MailingListRosterFilter{
		ListID:       l.ID,
		State:        state,
		DeliveryMode: mode,
		AfterID:      store.MailingListMemberID(after),
		Limit:        limit,
	})
	if err != nil {
		s.writeMlistError(w, r, err)
		return
	}
	items := make([]mailingListMemberDTO, 0, len(rows))
	for _, m := range rows {
		items = append(items, toMailingListMemberDTO(m))
	}
	var next *string
	if len(rows) == limit && len(rows) > 0 {
		tok := strconv.FormatUint(uint64(rows[len(rows)-1].ID), 10)
		next = &tok
	}
	writeJSON(w, http.StatusOK, pageDTO[mailingListMemberDTO]{Items: items, Next: next})
}

// createMemberRequest is the shape of one roster entry: POST
// /api/v1/lists/{id}/members's whole body, or one element of the
// /import request's "members" array. Exactly one of PrincipalID /
// ExternalAddress must be set (REQ-MLIST-02).
type createMemberRequest struct {
	PrincipalID     uint64 `json:"principal_id,omitempty"`
	ExternalAddress string `json:"external_address,omitempty"`
	State           string `json:"state,omitempty"`
	DeliveryMode    string `json:"delivery_mode,omitempty"`
}

// toMailingListMember validates req and converts it to a store row for
// listID. Returns a user-facing validation error string on failure (never
// touches the store).
func (req createMemberRequest) toMailingListMember(listID store.MailingListID) (store.MailingListMember, string) {
	hasPrincipal := req.PrincipalID != 0
	hasAddress := req.ExternalAddress != ""
	if hasPrincipal == hasAddress {
		return store.MailingListMember{}, "exactly one of principal_id / external_address must be set"
	}
	state, ok := mlistStateFromString(req.State)
	if !ok || state == "" {
		if req.State == "" {
			state = store.MailingListMemberActive
		} else {
			return store.MailingListMember{}, "state must be one of active, suspended, unsubscribed, pending, awaiting-approval"
		}
	}
	mode, ok := mlistDeliveryModeFromString(req.DeliveryMode)
	if !ok || mode == "" {
		if req.DeliveryMode == "" {
			mode = store.MailingListDeliveryEach
		} else {
			return store.MailingListMember{}, "delivery_mode must be one of each, nomail"
		}
	}
	m := store.MailingListMember{
		ListID:       listID,
		State:        state,
		DeliveryMode: mode,
	}
	if hasPrincipal {
		pid := store.PrincipalID(req.PrincipalID)
		m.PrincipalID = &pid
	} else {
		addr := req.ExternalAddress
		m.ExternalAddress = &addr
	}
	return m, ""
}

// handleAddMailingListMember handles POST /api/v1/lists/{id}/members.
func (s *Server) handleAddMailingListMember(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	l, ok := s.loadListAndCheckAccess(w, r, caller)
	if !ok {
		return
	}
	var req createMemberRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	m, verr := req.toMailingListMember(l.ID)
	if verr != "" {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed", verr, "")
		return
	}
	if caller.ID != 0 {
		by := caller.ID
		m.AddedBy = &by
	}
	added, err := s.store.Meta().AddMailingListMember(r.Context(), m)
	if err != nil {
		s.writeMlistError(w, r, err)
		return
	}
	s.appendAudit(r.Context(), "mlist.member.add",
		fmt.Sprintf("mlist:%d", l.ID),
		store.OutcomeSuccess, "", map[string]string{
			"member_id": strconv.FormatUint(uint64(added.ID), 10),
			"address":   mlistMemberAddressLabel(added),
		})
	w.Header().Set("Location", fmt.Sprintf("/api/v1/lists/%d/members/%d", l.ID, added.ID))
	writeJSON(w, http.StatusCreated, toMailingListMemberDTO(added))
}

// mlistMemberAddressLabel is the audit-metadata label for a roster row:
// the external address, or "principal:<id>" for an internal member.
func mlistMemberAddressLabel(m store.MailingListMember) string {
	if m.ExternalAddress != nil {
		return *m.ExternalAddress
	}
	if m.PrincipalID != nil {
		return fmt.Sprintf("principal:%d", *m.PrincipalID)
	}
	return ""
}

// patchMemberRequest is the PATCH /api/v1/lists/{id}/members/{mid} body.
type patchMemberRequest struct {
	State        *string `json:"state,omitempty"`
	DeliveryMode *string `json:"delivery_mode,omitempty"`
}

// handlePatchMailingListMember handles PATCH
// /api/v1/lists/{id}/members/{mid}: transitions State and/or DeliveryMode
// (REQ-MLIST-03/04).
func (s *Server) handlePatchMailingListMember(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	l, ok := s.loadListAndCheckAccess(w, r, caller)
	if !ok {
		return
	}
	m, ok := s.loadMemberInList(w, r, l)
	if !ok {
		return
	}
	var req patchMemberRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	wasSuspended := m.State == store.MailingListMemberSuspended
	wasAwaitingApproval := m.State == store.MailingListMemberAwaitingApproval
	reactivated := false
	rejected := false
	if req.State != nil {
		state, ok := mlistStateFromString(*req.State)
		if !ok || state == "" {
			writeProblem(w, r, http.StatusBadRequest, "validation_failed",
				"state must be one of active, suspended, unsubscribed, pending, awaiting-approval", *req.State)
			return
		}
		if state == store.MailingListMemberActive {
			// REQ-MLIST-55: transitioning TO active is a reactivation,
			// which also resets bounce_score/last_bounce_at -- not a bare
			// state overwrite, since a member's bounce history should not
			// survive being brought back onto the roster (whether it was
			// suspended, or e.g. an operator reversing an unsubscribe).
			// From awaiting-approval, this is ALSO the REQ-MLIST-62 owner
			// approval action.
			if err := s.store.Meta().ReactivateMailingListMember(r.Context(), m.ID); err != nil {
				s.writeMlistError(w, r, err)
				return
			}
			reactivated = true
		} else {
			if wasAwaitingApproval && state == store.MailingListMemberUnsubscribed {
				// REQ-MLIST-62's owner rejection: an awaiting-approval
				// request the owner declines is recorded as unsubscribed
				// (never delivers, keeps roster history) rather than a
				// bare state overwrite going unlabeled in the audit trail.
				rejected = true
			}
			if err := s.store.Meta().UpdateMailingListMemberState(r.Context(), m.ID, state); err != nil {
				s.writeMlistError(w, r, err)
				return
			}
		}
	}
	if req.DeliveryMode != nil {
		mode, ok := mlistDeliveryModeFromString(*req.DeliveryMode)
		if !ok || mode == "" {
			writeProblem(w, r, http.StatusBadRequest, "validation_failed",
				"delivery_mode must be one of each, nomail", *req.DeliveryMode)
			return
		}
		if err := s.store.Meta().UpdateMailingListMemberDeliveryMode(r.Context(), m.ID, mode); err != nil {
			s.writeMlistError(w, r, err)
			return
		}
	}
	updated, err := s.store.Meta().GetMailingListMember(r.Context(), m.ID)
	if err != nil {
		s.writeMlistError(w, r, err)
		return
	}
	action := "mlist.member.update"
	switch {
	case reactivated && wasAwaitingApproval:
		// REQ-MLIST-62 owner approval: distinct from a plain
		// bounce-recovery reactivation so the operator approval queue can
		// filter on it directly.
		action = "mlist.member.approve"
	case reactivated:
		// A distinct action (REQ-MLIST-55) so the operator bounce/
		// suspension view can filter reactivations without guessing them
		// out of the generic update audit trail; wasSuspended records
		// what state it was reactivated FROM for that view.
		action = "mlist.member.reactivate"
	case rejected:
		action = "mlist.member.reject"
	}
	s.appendAudit(r.Context(), action,
		fmt.Sprintf("mlist:%d", l.ID),
		store.OutcomeSuccess, "", map[string]string{
			"member_id":             strconv.FormatUint(uint64(updated.ID), 10),
			"state":                 string(updated.State),
			"delivery_mode":         string(updated.DeliveryMode),
			"was_suspended":         strconv.FormatBool(wasSuspended),
			"was_awaiting_approval": strconv.FormatBool(wasAwaitingApproval),
		})
	writeJSON(w, http.StatusOK, toMailingListMemberDTO(updated))
}

// handleRemoveMailingListMember handles DELETE
// /api/v1/lists/{id}/members/{mid}.
func (s *Server) handleRemoveMailingListMember(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	l, ok := s.loadListAndCheckAccess(w, r, caller)
	if !ok {
		return
	}
	m, ok := s.loadMemberInList(w, r, l)
	if !ok {
		return
	}
	if err := s.store.Meta().RemoveMailingListMember(r.Context(), m.ID); err != nil {
		s.writeMlistError(w, r, err)
		return
	}
	s.appendAudit(r.Context(), "mlist.member.remove",
		fmt.Sprintf("mlist:%d", l.ID),
		store.OutcomeSuccess, "", map[string]string{
			"member_id": strconv.FormatUint(uint64(m.ID), 10),
			"address":   mlistMemberAddressLabel(m),
		})
	w.WriteHeader(http.StatusNoContent)
}

// importMembersRequest is the POST /api/v1/lists/{id}/members/import body.
type importMembersRequest struct {
	Members []createMemberRequest `json:"members"`
}

// importResultItem reports the outcome of one entry of an import request.
type importResultItem struct {
	Index  int    `json:"index"`
	Status string `json:"status"` // "added" | "conflict" | "invalid"
	ID     uint64 `json:"id,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// importMembersResponse is the POST .../members/import response.
type importMembersResponse struct {
	Added   int                `json:"added"`
	Skipped int                `json:"skipped"`
	Results []importResultItem `json:"results"`
}

// handleImportMailingListMembers handles POST
// /api/v1/lists/{id}/members/import (REQ-MLIST-40a bulk import). Every
// entry is attempted independently: a duplicate (ErrConflict) or
// malformed (ErrInvalidArgument) entry is recorded as skipped rather than
// aborting the whole batch, so one bad row in a large CSV-derived import
// does not lose the rows around it.
func (s *Server) handleImportMailingListMembers(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	l, ok := s.loadListAndCheckAccess(w, r, caller)
	if !ok {
		return
	}
	var req importMembersRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if len(req.Members) > mlistMemberImportCap {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			fmt.Sprintf("import request exceeds the %d-member cap; split into multiple requests", mlistMemberImportCap), "")
		return
	}
	resp := importMembersResponse{Results: make([]importResultItem, 0, len(req.Members))}
	var by *store.PrincipalID
	if caller.ID != 0 {
		cid := caller.ID
		by = &cid
	}
	for i, entry := range req.Members {
		m, verr := entry.toMailingListMember(l.ID)
		if verr != "" {
			resp.Skipped++
			resp.Results = append(resp.Results, importResultItem{Index: i, Status: "invalid", Detail: verr})
			continue
		}
		m.AddedBy = by
		added, err := s.store.Meta().AddMailingListMember(r.Context(), m)
		if err != nil {
			resp.Skipped++
			status := "invalid"
			if errors.Is(err, store.ErrConflict) {
				status = "conflict"
			}
			resp.Results = append(resp.Results, importResultItem{Index: i, Status: status, Detail: err.Error()})
			continue
		}
		resp.Added++
		resp.Results = append(resp.Results, importResultItem{Index: i, Status: "added", ID: uint64(added.ID)})
	}
	s.appendAudit(r.Context(), "mlist.member.import",
		fmt.Sprintf("mlist:%d", l.ID),
		store.OutcomeSuccess, "", map[string]string{
			"added":   strconv.Itoa(resp.Added),
			"skipped": strconv.Itoa(resp.Skipped),
		})
	writeJSON(w, http.StatusOK, resp)
}

// handleExportMailingListMembers handles GET
// /api/v1/lists/{id}/members/export (REQ-MLIST-40a bulk export): the
// roster in the same pageDTO envelope every other collection endpoint
// uses, but reading in mlistMemberExportCap-sized chunks internally
// (the store itself caps a single ListMailingListMembers call at 1000
// rows) so one request can return far more than the usual default page.
// An optional ?after= continues from a previous response's Next, exactly
// like GET .../members; a roster larger than the cap sets Next instead of
// silently dropping the tail.
func (s *Server) handleExportMailingListMembers(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	l, ok := s.loadListAndCheckAccess(w, r, caller)
	if !ok {
		return
	}
	startAfter, exportLimit, ok := parseAfterLimit(w, r, 0, mlistMemberExportCap, mlistMemberExportCap)
	if !ok {
		return
	}
	var all []store.MailingListMember
	after := store.MailingListMemberID(startAfter)
	const innerPage = 1000 // the store's own per-call ceiling
	more := false
	for {
		rows, err := s.store.Meta().ListMailingListMembers(r.Context(), store.MailingListRosterFilter{
			ListID: l.ID, AfterID: after, Limit: innerPage,
		})
		if err != nil {
			s.writeMlistError(w, r, err)
			return
		}
		for _, m := range rows {
			if len(all) >= exportLimit {
				more = true
				break
			}
			all = append(all, m)
			after = m.ID
		}
		if len(rows) < innerPage || len(all) >= exportLimit {
			more = more || len(rows) == innerPage
			break
		}
	}
	items := make([]mailingListMemberDTO, 0, len(all))
	for _, m := range all {
		items = append(items, toMailingListMemberDTO(m))
	}
	var next *string
	if more && len(all) > 0 {
		tok := strconv.FormatUint(uint64(all[len(all)-1].ID), 10)
		next = &tok
	}
	writeJSON(w, http.StatusOK, pageDTO[mailingListMemberDTO]{Items: items, Next: next})
}

// mailingListMemberSummaryDTO is the GET .../members/summary response
// (REQ-MLIST-54): the list's roster population grouped by
// store.MailingListMemberState, as a scalar summary rather than a
// pageDTO collection -- an operator bounce/suspension view needs this
// count once per list render, not a paginated roster walk.
type mailingListMemberSummaryDTO struct {
	Active           int `json:"active"`
	Suspended        int `json:"suspended"`
	Unsubscribed     int `json:"unsubscribed"`
	Pending          int `json:"pending"`
	AwaitingApproval int `json:"awaiting_approval"`
}

// handleMailingListMemberSummary handles GET
// /api/v1/lists/{id}/members/summary (REQ-MLIST-54: "an operator-visible
// per-list bounce/suspension view"). Combined with the per-member
// bounce_score/last_bounce_at already on mailingListMemberDTO (GET
// .../members?state=suspended), this is the whole read surface issue
// #184 exposes for that view.
func (s *Server) handleMailingListMemberSummary(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	l, ok := s.loadListAndCheckAccess(w, r, caller)
	if !ok {
		return
	}
	counts, err := s.store.Meta().CountMailingListMembersByState(r.Context(), l.ID)
	if err != nil {
		s.writeMlistError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, mailingListMemberSummaryDTO{
		Active:           counts[store.MailingListMemberActive],
		Suspended:        counts[store.MailingListMemberSuspended],
		Unsubscribed:     counts[store.MailingListMemberUnsubscribed],
		Pending:          counts[store.MailingListMemberPending],
		AwaitingApproval: counts[store.MailingListMemberAwaitingApproval],
	})
}
