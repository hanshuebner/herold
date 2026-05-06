package protoadmin

// acl.go implements the admin REST endpoints for mailbox ACL management:
//
//   GET    /api/v1/principals/{pid}/mailboxes
//   GET    /api/v1/principals/{pid}/mailboxes/{mailbox}/acl
//   PUT    /api/v1/principals/{pid}/mailboxes/{mailbox}/acl/{grantee}
//   DELETE /api/v1/principals/{pid}/mailboxes/{mailbox}/acl/{grantee}
//
// {pid} and {grantee} are numeric principal IDs (consistent with the rest
// of protoadmin's principal routing convention). {mailbox} is a numeric
// mailbox ID.
//
// REQ-PROTO-33: cross-mailbox IMAP ACL (RFC 4314) backing store.
// REQ-AUTH-63: admin API surface for granting shared-mailbox access.

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// mailboxSummaryDTO is the wire shape returned by the mailbox-listing
// endpoint.  Only the fields needed by the bootstrap fixture (id + name)
// are included; callers that need the full set use JMAP or IMAP.
type mailboxSummaryDTO struct {
	ID   uint64 `json:"id"`
	Name string `json:"name"`
}

// aclRowDTO is one row returned by GET .../acl.
type aclRowDTO struct {
	// GranteePrincipalID is the numeric ID of the grantee, or 0 for the
	// RFC 4314 "anyone" pseudo-identifier.
	GranteePrincipalID uint64 `json:"grantee_principal_id"`
	// Rights is the canonical RFC 4314 sorted letter string (lrswipkxtea
	// ordering); the empty string means zero rights.
	Rights string `json:"rights"`
}

// putACLRequest is the body accepted by PUT .../acl/{grantee}.
type putACLRequest struct {
	Rights string `json:"rights"`
}

// parseMailboxID reads the {mailbox} path parameter and returns it as a
// MailboxID. On failure the caller returns immediately after the 400 problem
// is written.
func parseMailboxID(w http.ResponseWriter, r *http.Request) (store.MailboxID, bool) {
	raw := r.PathValue("mailbox")
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || n == 0 {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id",
			"mailbox id must be a positive integer", raw)
		return 0, false
	}
	return store.MailboxID(n), true
}

// parseGranteePID reads the {grantee} path parameter and returns it as a
// PrincipalID. On failure the caller returns immediately after the 400 problem
// is written.
func parseGranteePID(w http.ResponseWriter, r *http.Request) (store.PrincipalID, bool) {
	raw := r.PathValue("grantee")
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || n == 0 {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id",
			"grantee principal id must be a positive integer", raw)
		return 0, false
	}
	return store.PrincipalID(n), true
}

// requirePrincipalExists loads the principal for pid and writes a 404 problem
// if it is not found. Returns the principal and true on success.
func (s *Server) requirePrincipalExists(w http.ResponseWriter, r *http.Request, pid store.PrincipalID) (store.Principal, bool) {
	p, err := s.store.Meta().GetPrincipalByID(r.Context(), pid)
	if err != nil {
		s.writeStoreError(w, r, err)
		return store.Principal{}, false
	}
	return p, true
}

// requireMailboxBelongsTo loads mailboxID and verifies it is owned by
// principalID. Writes 404 if not found or if the mailbox belongs to a
// different principal.
func (s *Server) requireMailboxBelongsTo(
	w http.ResponseWriter, r *http.Request,
	mailboxID store.MailboxID, principalID store.PrincipalID,
) (store.Mailbox, bool) {
	mb, err := s.store.Meta().GetMailboxByID(r.Context(), mailboxID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeProblem(w, r, http.StatusNotFound, "not_found",
				"mailbox not found", fmt.Sprintf("mailbox:%d", mailboxID))
			return store.Mailbox{}, false
		}
		s.writeStoreError(w, r, err)
		return store.Mailbox{}, false
	}
	if mb.PrincipalID != principalID {
		// Expose as 404 so callers cannot enumerate other principals'
		// mailbox IDs by probing the ACL surface.
		writeProblem(w, r, http.StatusNotFound, "not_found",
			"mailbox not found", fmt.Sprintf("mailbox:%d", mailboxID))
		return store.Mailbox{}, false
	}
	return mb, true
}

// handleListPrincipalMailboxes handles
//
//	GET /api/v1/principals/{pid}/mailboxes
//
// Returns the list of mailboxes owned by the principal, as id+name pairs.
// This lightweight listing is the primitive the interop bootstrap fixture
// uses to find INBOX without guessing its numeric ID.
func (s *Server) handleListPrincipalMailboxes(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	pid, ok := parsePID(w, r)
	if !ok {
		return
	}
	if _, ok := s.requirePrincipalExists(w, r, pid); !ok {
		return
	}
	mbs, err := s.store.Meta().ListMailboxes(r.Context(), pid)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	items := make([]mailboxSummaryDTO, 0, len(mbs))
	for _, mb := range mbs {
		items = append(items, mailboxSummaryDTO{
			ID:   uint64(mb.ID),
			Name: mb.Name,
		})
	}
	writeJSON(w, http.StatusOK, pageDTO[mailboxSummaryDTO]{Items: items, Next: nil})
}

// handleGetMailboxACL handles
//
//	GET /api/v1/principals/{pid}/mailboxes/{mailbox}/acl
//
// Returns every ACL row for mailboxID as a JSON array.
func (s *Server) handleGetMailboxACL(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	pid, ok := parsePID(w, r)
	if !ok {
		return
	}
	if _, ok := s.requirePrincipalExists(w, r, pid); !ok {
		return
	}
	mailboxID, ok := parseMailboxID(w, r)
	if !ok {
		return
	}
	if _, ok := s.requireMailboxBelongsTo(w, r, mailboxID, pid); !ok {
		return
	}
	rows, err := s.store.Meta().GetMailboxACL(r.Context(), mailboxID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	items := make([]aclRowDTO, 0, len(rows))
	for _, row := range rows {
		dto := aclRowDTO{Rights: formatACLRights(row.Rights)}
		if row.PrincipalID != nil {
			dto.GranteePrincipalID = uint64(*row.PrincipalID)
		}
		items = append(items, dto)
	}
	writeJSON(w, http.StatusOK, items)
}

// handlePutMailboxACL handles
//
//	PUT /api/v1/principals/{pid}/mailboxes/{mailbox}/acl/{grantee}
//
// Upserts the ACL row for (mailboxID, granteePID). The body must contain
// a rights string; an empty rights string is accepted and stores zero rights.
func (s *Server) handlePutMailboxACL(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	pid, ok := parsePID(w, r)
	if !ok {
		return
	}
	if _, ok := s.requirePrincipalExists(w, r, pid); !ok {
		return
	}
	mailboxID, ok := parseMailboxID(w, r)
	if !ok {
		return
	}
	if _, ok := s.requireMailboxBelongsTo(w, r, mailboxID, pid); !ok {
		return
	}
	granteePID, ok := parseGranteePID(w, r)
	if !ok {
		return
	}
	// Ensure the grantee exists so we return 404 rather than a store
	// constraint error on a foreign-key violation.
	if _, ok := s.requirePrincipalExists(w, r, granteePID); !ok {
		return
	}
	var req putACLRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	rights, err := parseACLRights(req.Rights)
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid_rights",
			"rights string contains unknown or duplicate letters", err.Error())
		return
	}
	if err := s.store.Meta().SetMailboxACL(r.Context(), mailboxID, &granteePID, rights, caller.ID); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	s.appendAudit(r.Context(), "mailbox_acl.set",
		fmt.Sprintf("mailbox:%d", mailboxID),
		store.OutcomeSuccess, "",
		map[string]string{
			"grantee":    fmt.Sprintf("%d", granteePID),
			"rights":     formatACLRights(rights),
			"mailbox_id": fmt.Sprintf("%d", mailboxID),
		})
	s.logger.InfoContext(r.Context(), "protoadmin.acl.put",
		"activity", observe.ActivityAudit,
		"mailbox_id", mailboxID,
		"grantee_pid", granteePID,
		"rights", formatACLRights(rights))
	writeJSON(w, http.StatusOK, aclRowDTO{
		GranteePrincipalID: uint64(granteePID),
		Rights:             formatACLRights(rights),
	})
}

// handleDeleteMailboxACL handles
//
//	DELETE /api/v1/principals/{pid}/mailboxes/{mailbox}/acl/{grantee}
//
// Removes the ACL row for (mailboxID, granteePID). Returns 204 on success,
// 404 when no row existed.
func (s *Server) handleDeleteMailboxACL(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	pid, ok := parsePID(w, r)
	if !ok {
		return
	}
	if _, ok := s.requirePrincipalExists(w, r, pid); !ok {
		return
	}
	mailboxID, ok := parseMailboxID(w, r)
	if !ok {
		return
	}
	if _, ok := s.requireMailboxBelongsTo(w, r, mailboxID, pid); !ok {
		return
	}
	granteePID, ok := parseGranteePID(w, r)
	if !ok {
		return
	}
	if err := s.store.Meta().RemoveMailboxACL(r.Context(), mailboxID, &granteePID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeProblem(w, r, http.StatusNotFound, "not_found",
				"no ACL row for this grantee on this mailbox", "")
			return
		}
		s.writeStoreError(w, r, err)
		return
	}
	s.appendAudit(r.Context(), "mailbox_acl.remove",
		fmt.Sprintf("mailbox:%d", mailboxID),
		store.OutcomeSuccess, "",
		map[string]string{
			"grantee":    fmt.Sprintf("%d", granteePID),
			"mailbox_id": fmt.Sprintf("%d", mailboxID),
		})
	s.logger.InfoContext(r.Context(), "protoadmin.acl.delete",
		"activity", observe.ActivityAudit,
		"mailbox_id", mailboxID,
		"grantee_pid", granteePID)
	w.WriteHeader(http.StatusNoContent)
}
