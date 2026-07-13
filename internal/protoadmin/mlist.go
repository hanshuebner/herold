package protoadmin

// mlist.go — admin REST surface for hosted mailing lists (epic #183,
// docs/design/server/requirements/28-mailing-lists.md REQ-MLIST-40a,
// docs/design/server/architecture/14-mailing-lists.md). CRUD for the list
// row itself; roster CRUD and bulk import/export live in mlist_members.go.
//
// Endpoint shape (all behind authAdmin, then the finer per-domain/per-list
// check in mlist_authz.go):
//
//	GET    /api/v1/lists              list (domain-scoped for an operator)
//	POST   /api/v1/lists              create
//	GET    /api/v1/lists/{id}         show
//	PATCH  /api/v1/lists/{id}         rename / set config / reassign owner
//	DELETE /api/v1/lists/{id}         delete (cascades the roster)
//
// A list is a Group principal (REQ-MLIST-01) plus this REST-visible
// configuration. Creating a list here also creates its backing Group
// principal and writes the owner's list:owner grant (REQ-MLIST-05); the
// backing principal is left in place on delete, matching
// store.DeleteMailingList's documented "the caller owns that lifecycle
// decision separately".

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/hanshuebner/herold/internal/store"
)

// parseMailingListID reads the {id} path parameter.
func parseMailingListID(w http.ResponseWriter, r *http.Request) (store.MailingListID, bool) {
	raw := r.PathValue("id")
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || n == 0 {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id",
			"list id must be a positive integer", raw)
		return 0, false
	}
	return store.MailingListID(n), true
}

// loadMailingList reads {id} and the list row, writing 400/404 on failure.
func (s *Server) loadMailingList(w http.ResponseWriter, r *http.Request) (store.MailingList, bool) {
	id, ok := parseMailingListID(w, r)
	if !ok {
		return store.MailingList{}, false
	}
	l, err := s.store.Meta().GetMailingList(r.Context(), id)
	if err != nil {
		s.writeMlistError(w, r, err)
		return store.MailingList{}, false
	}
	return l, true
}

// writeMlistError maps a mailing-list store error to an HTTP problem
// response, adding the ErrInvalidArgument case (malformed posting address,
// member XOR/nomail violations) on top of writeStoreError's set.
func (s *Server) writeMlistError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrInvalidArgument) {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed", err.Error(), "")
		return
	}
	s.writeStoreError(w, r, err)
}

// handleListMailingLists handles GET /api/v1/lists (REQ-MLIST-40a).
//
// A super-admin with no ?domain= filter gets the store's native
// keyset-paginated scan over every list. Everyone else is restricted to
// the domain(s) they hold at least domain:operator on (REQ-MLIST-05): an
// explicit ?domain= must be one of those domains (403 otherwise), and an
// omitted ?domain= merges one native, AfterID-filtered page per
// authorized domain, sorts by ID, and truncates to the requested page
// size — correct because MailingListID is a single global sequence, so
// filtering AfterID per-domain and merging yields exactly the same
// candidate set as filtering the union post-hoc. A caller with an empty
// authorized-domain set gets an empty page, never an error (fail-closed).
func (s *Server) handleListMailingLists(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	q := r.URL.Query()
	domain := strings.ToLower(strings.TrimSpace(q.Get("domain")))
	after, limit, ok := parseAfterLimit(w, r, 0, 100, 1000)
	if !ok {
		return
	}

	scope := s.mlistAuthorizedDomains(r.Context(), caller)

	if scope.SuperAdmin {
		rows, err := s.store.Meta().ListMailingLists(r.Context(), store.MailingListFilter{
			Domain: domain, AfterID: store.MailingListID(after), Limit: limit,
		})
		if err != nil {
			s.writeMlistError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, mailingListPage(rows, limit))
		return
	}

	if domain != "" {
		if !domainInSet(scope.Domains, domain) {
			writeProblem(w, r, http.StatusForbidden, "forbidden",
				"insufficient privileges on this domain", "")
			return
		}
		rows, err := s.store.Meta().ListMailingLists(r.Context(), store.MailingListFilter{
			Domain: domain, AfterID: store.MailingListID(after), Limit: limit,
		})
		if err != nil {
			s.writeMlistError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, mailingListPage(rows, limit))
		return
	}

	// No domain filter: merge one page per authorized domain (empty
	// Domains yields an empty merged result, never a store call).
	var merged []store.MailingList
	for _, d := range scope.Domains {
		rows, err := s.store.Meta().ListMailingLists(r.Context(), store.MailingListFilter{
			Domain: d, AfterID: store.MailingListID(after), Limit: limit,
		})
		if err != nil {
			s.writeMlistError(w, r, err)
			return
		}
		merged = append(merged, rows...)
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].ID < merged[j].ID })
	if len(merged) > limit {
		merged = merged[:limit]
	}
	writeJSON(w, http.StatusOK, mailingListPage(merged, limit))
}

// mailingListPage converts rows to the pageDTO envelope. Next carries the
// last row's id when the page is full (len(rows) == limit), the same
// "maybe more" heuristic handleListPrincipals uses.
func mailingListPage(rows []store.MailingList, limit int) pageDTO[mailingListDTO] {
	items := make([]mailingListDTO, 0, len(rows))
	for _, l := range rows {
		items = append(items, toMailingListDTO(l))
	}
	var next *string
	if len(rows) == limit && len(rows) > 0 {
		tok := strconv.FormatUint(uint64(rows[len(rows)-1].ID), 10)
		next = &tok
	}
	return pageDTO[mailingListDTO]{Items: items, Next: next}
}

// parseAfterLimit parses the ?after= / ?limit= query params shared by
// every mailing-list collection endpoint. Returns ok=false after writing a
// 400 problem on a malformed value.
func parseAfterLimit(w http.ResponseWriter, r *http.Request, defaultAfter uint64, defaultLimit, maxLimit int) (after uint64, limit int, ok bool) {
	after = defaultAfter
	limit = defaultLimit
	q := r.URL.Query()
	if raw := q.Get("after"); raw != "" {
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			writeProblem(w, r, http.StatusBadRequest, "invalid_cursor",
				"after is not a valid id", raw)
			return 0, 0, false
		}
		after = n
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			writeProblem(w, r, http.StatusBadRequest, "invalid_limit",
				"limit must be a positive integer", raw)
			return 0, 0, false
		}
		if n > maxLimit {
			n = maxLimit
		}
		limit = n
	}
	return after, limit, true
}

func domainInSet(domains []string, d string) bool {
	for _, x := range domains {
		if x == d {
			return true
		}
	}
	return false
}

// createMailingListRequest is the POST /api/v1/lists body.
type createMailingListRequest struct {
	PostingAddress string `json:"posting_address"`
	DisplayName    string `json:"display_name"`
	OwnerPrincipal uint64 `json:"owner_principal_id,omitempty"`
	SubjectTag     string `json:"subject_tag,omitempty"`
	ARCSeal        *bool  `json:"arc_seal,omitempty"`
	MaxMessageSize int64  `json:"max_message_size_bytes,omitempty"`
}

// handleCreateMailingList handles POST /api/v1/lists (REQ-MLIST-01,
// REQ-MLIST-05). Creates the backing Group principal, the list row, and
// the owner's list:owner grant. owner_principal_id defaults to the
// caller.
func (s *Server) handleCreateMailingList(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	var req createMailingListRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.DisplayName) == "" {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"display_name is required", "")
		return
	}
	address, domain, err := store.NormalizeMailingListAddress(req.PostingAddress)
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed", err.Error(), "")
		return
	}
	d, err := s.store.Meta().GetDomain(r.Context(), domain)
	if err != nil || !d.IsLocal {
		writeProblem(w, r, http.StatusBadRequest, "unknown_domain",
			"posting_address must be on a locally hosted domain", domain)
		return
	}
	if !s.requireMlistDomainAccess(w, r, caller, domain) {
		return
	}
	ownerID := caller.ID
	if req.OwnerPrincipal != 0 {
		owner, err := s.store.Meta().GetPrincipalByID(r.Context(), store.PrincipalID(req.OwnerPrincipal))
		if err != nil {
			writeProblem(w, r, http.StatusBadRequest, "validation_failed",
				"owner_principal_id does not reference a known principal", "")
			return
		}
		ownerID = owner.ID
	}
	arcSeal := true
	if req.ARCSeal != nil {
		arcSeal = *req.ARCSeal
	}

	group, err := s.store.Meta().InsertPrincipal(r.Context(), store.Principal{
		Kind:           store.PrincipalKindGroup,
		CanonicalEmail: address,
		DisplayName:    req.DisplayName,
	})
	if err != nil {
		s.writeMlistError(w, r, err)
		return
	}

	var subjectTag *string
	if strings.TrimSpace(req.SubjectTag) != "" {
		tag := req.SubjectTag
		subjectTag = &tag
	}
	l, err := s.store.Meta().InsertMailingList(r.Context(), store.MailingList{
		PrincipalID:         group.ID,
		PostingAddress:      address,
		DisplayName:         req.DisplayName,
		OwnerID:             ownerID,
		SubjectTag:          subjectTag,
		ARCSeal:             arcSeal,
		MaxMessageSizeBytes: req.MaxMessageSize,
	})
	if err != nil {
		s.writeMlistError(w, r, err)
		return
	}
	if err := s.grantListOwner(r.Context(), l, ownerID, caller.ID); err != nil {
		s.writeMlistError(w, r, err)
		return
	}
	s.appendAudit(r.Context(), "mlist.create",
		fmt.Sprintf("mlist:%d", l.ID),
		store.OutcomeSuccess, "", map[string]string{
			"posting_address": l.PostingAddress,
			"owner_id":        strconv.FormatUint(uint64(ownerID), 10),
		})
	w.Header().Set("Location", fmt.Sprintf("/api/v1/lists/%d", l.ID))
	writeJSON(w, http.StatusCreated, toMailingListDTO(l))
}

// handleGetMailingList handles GET /api/v1/lists/{id}.
func (s *Server) handleGetMailingList(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	l, ok := s.loadMailingList(w, r)
	if !ok {
		return
	}
	if !s.requireMlistListAccess(w, r, caller, l) {
		return
	}
	writeJSON(w, http.StatusOK, toMailingListDTO(l))
}

// patchMailingListRequest is the PATCH /api/v1/lists/{id} body. Every
// field is optional; only fields present are applied. subject_tag uses ""
// to mean "clear the tag" (a tag is never meaningfully the empty string).
type patchMailingListRequest struct {
	PostingAddress      *string `json:"posting_address,omitempty"`
	DisplayName         *string `json:"display_name,omitempty"`
	OwnerPrincipal      *uint64 `json:"owner_principal_id,omitempty"`
	SubjectTag          *string `json:"subject_tag,omitempty"`
	ARCSeal             *bool   `json:"arc_seal,omitempty"`
	MaxMessageSizeBytes *int64  `json:"max_message_size_bytes,omitempty"`
}

// handlePatchMailingList handles PATCH /api/v1/lists/{id}: rename (change
// posting_address), reassign owner, or update config fields. The CLI's
// `list rename` and `list set` both map onto this one endpoint (`rename`
// sends only posting_address, `set` sends the rest), the same way
// `principal disable/enable/quota/grant-admin` all map onto one PATCH.
func (s *Server) handlePatchMailingList(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	l, ok := s.loadMailingList(w, r)
	if !ok {
		return
	}
	if !s.requireMlistListAccess(w, r, caller, l) {
		return
	}
	var req patchMailingListRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}

	oldOwner := l.OwnerID
	newOwner := l.OwnerID

	if req.PostingAddress != nil {
		address, domain, err := store.NormalizeMailingListAddress(*req.PostingAddress)
		if err != nil {
			writeProblem(w, r, http.StatusBadRequest, "validation_failed", err.Error(), "")
			return
		}
		if domain != l.Domain {
			// Moving the list to a different domain: the caller must be
			// authorized on the DESTINATION domain too, or a domain
			// operator could relocate a list into a domain they do not
			// manage and keep managing it there.
			d, derr := s.store.Meta().GetDomain(r.Context(), domain)
			if derr != nil || !d.IsLocal {
				writeProblem(w, r, http.StatusBadRequest, "unknown_domain",
					"posting_address must be on a locally hosted domain", domain)
				return
			}
			if !s.requireMlistDomainAccess(w, r, caller, domain) {
				return
			}
		}
		l.PostingAddress = address
	}
	if req.DisplayName != nil {
		if strings.TrimSpace(*req.DisplayName) == "" {
			writeProblem(w, r, http.StatusBadRequest, "validation_failed",
				"display_name must not be empty", "")
			return
		}
		l.DisplayName = *req.DisplayName
	}
	if req.OwnerPrincipal != nil {
		owner, err := s.store.Meta().GetPrincipalByID(r.Context(), store.PrincipalID(*req.OwnerPrincipal))
		if err != nil {
			writeProblem(w, r, http.StatusBadRequest, "validation_failed",
				"owner_principal_id does not reference a known principal", "")
			return
		}
		newOwner = owner.ID
		l.OwnerID = newOwner
	}
	if req.SubjectTag != nil {
		if *req.SubjectTag == "" {
			l.SubjectTag = nil
		} else {
			tag := *req.SubjectTag
			l.SubjectTag = &tag
		}
	}
	if req.ARCSeal != nil {
		l.ARCSeal = *req.ARCSeal
	}
	if req.MaxMessageSizeBytes != nil {
		if *req.MaxMessageSizeBytes < 0 {
			writeProblem(w, r, http.StatusBadRequest, "validation_failed",
				"max_message_size_bytes must be non-negative", "")
			return
		}
		l.MaxMessageSizeBytes = *req.MaxMessageSizeBytes
	}

	if err := s.store.Meta().UpdateMailingList(r.Context(), l); err != nil {
		s.writeMlistError(w, r, err)
		return
	}
	if newOwner != oldOwner {
		if err := s.revokeListOwner(r.Context(), l, oldOwner); err != nil {
			s.writeMlistError(w, r, err)
			return
		}
		if err := s.grantListOwner(r.Context(), l, newOwner, caller.ID); err != nil {
			s.writeMlistError(w, r, err)
			return
		}
	}
	updated, err := s.store.Meta().GetMailingList(r.Context(), l.ID)
	if err != nil {
		s.writeMlistError(w, r, err)
		return
	}
	s.appendAudit(r.Context(), "mlist.update",
		fmt.Sprintf("mlist:%d", l.ID),
		store.OutcomeSuccess, "", map[string]string{
			"posting_address": updated.PostingAddress,
			"owner_id":        strconv.FormatUint(uint64(updated.OwnerID), 10),
		})
	writeJSON(w, http.StatusOK, toMailingListDTO(updated))
}

// handleDeleteMailingList handles DELETE /api/v1/lists/{id}. Cascades the
// roster (store.DeleteMailingList) and best-effort removes any grants
// scoped to the list resource; the backing Group principal is left in
// place (store's documented "the caller owns that lifecycle decision
// separately").
func (s *Server) handleDeleteMailingList(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	l, ok := s.loadMailingList(w, r)
	if !ok {
		return
	}
	if !s.requireMlistListAccess(w, r, caller, l) {
		return
	}
	if err := s.store.Meta().DeleteMailingList(r.Context(), l.ID); err != nil {
		s.writeMlistError(w, r, err)
		return
	}
	grants, err := s.store.Meta().ListGrantsOnResource(r.Context(), store.GrantResourceList, mlistResourceID(l.ID))
	if err == nil {
		for _, g := range grants {
			_ = s.store.Meta().DeleteGrant(r.Context(), g)
		}
	}
	s.appendAudit(r.Context(), "mlist.delete",
		fmt.Sprintf("mlist:%d", l.ID),
		store.OutcomeSuccess, "", map[string]string{"posting_address": l.PostingAddress})
	w.WriteHeader(http.StatusNoContent)
}
