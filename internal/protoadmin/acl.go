package protoadmin

// acl.go — admin REST surface for managing IMAP mailbox ACLs (RFC 4314).
// REQ-PROTO-33 (shared mailboxes / IMAP ACL, phase 2) and REQ-AUTH-63
// (admin API surface for granting shared-mailbox access).
//
// Endpoint shape (all behind authAdmin):
//
//   GET    /api/v1/principals/{pid}/mailboxes
//   GET    /api/v1/principals/{pid}/mailboxes/{mailbox}/acl
//   PUT    /api/v1/principals/{pid}/mailboxes/{mailbox}/acl/{grantee}
//   DELETE /api/v1/principals/{pid}/mailboxes/{mailbox}/acl/{grantee}
//
// {pid} is the canonical owner principal id (numeric). {mailbox} is the
// canonical mailbox Name, URL-decoded by net/http's stdlib mux. {grantee}
// is either the literal token "anyone" (NULL-principal row) or a
// canonical email address.
//
// Rights letters are encoded as the canonical "lrswipkxtea" sequence
// from internal/aclcodec; unknown letters return 400 (the obsolete
// RFC 2086 "c"/"d" composites are accepted only on the IMAP wire — the
// REST surface is strict so admin scripts never accumulate aliases).
//
// Storage (epic #210): store.Meta()'s SetMailboxACL / GetMailboxACL /
// RemoveMailboxACL are a DTO layer over `mailbox`-kind rows in the unified
// grants table (internal/protoimap's SETACL/GETACL/MYRIGHTS use the same
// methods), not a separate mailbox_acl table -- this file's calls into
// store.Meta() are unchanged from before that migration.

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/hanshuebner/herold/internal/aclcodec"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// aclGranteeAnyone is the literal {grantee} path segment that addresses
// the RFC 4314 "anyone" pseudo-identifier (stored as NULL PrincipalID).
// Matched case-insensitively to mirror the IMAP wire surface
// (resolveACLIdentifier in protoimap).
const aclGranteeAnyone = "anyone"

// mailboxSummaryDTO is the wire shape returned by GET .../mailboxes.
// Kept narrow: callers that need the full mailbox row (UIDVALIDITY,
// flags, ...) go through JMAP or IMAP.
type mailboxSummaryDTO struct {
	ID          uint64 `json:"id"`
	Name        string `json:"name"`
	PrincipalID uint64 `json:"principal_id"`
}

// aclRowDTO is one row returned by GET .../acl and PUT .../acl/{grantee}.
//
// The owner row is surfaced with Grantee == owner's canonical email and
// Rights == "lrswipkxtea" (the implicit full-rights mask) so admin tools
// can render a single homogeneous list without special-casing the
// implicit owner row. An "anyone" row carries Grantee == "anyone".
type aclRowDTO struct {
	Grantee string `json:"grantee"`
	Rights  string `json:"rights"`
	// IsOwner is true on the synthetic owner row, false on every
	// stored ACL row. Lets admin UIs hide the owner row from "delete"
	// actions (it has no underlying grant row to delete).
	IsOwner bool `json:"is_owner,omitempty"`
}

// putACLRequest is the body accepted by PUT .../acl/{grantee}.
type putACLRequest struct {
	Rights string `json:"rights"`
}

// resolveOwnerAndMailbox parses {pid} + {mailbox} and loads the
// matching mailbox. Returns 404 if either is unknown; returns 400 if
// {pid} is malformed.
func (s *Server) resolveOwnerAndMailbox(w http.ResponseWriter, r *http.Request) (store.Principal, store.Mailbox, bool) {
	pid, ok := parsePID(w, r)
	if !ok {
		return store.Principal{}, store.Mailbox{}, false
	}
	owner, err := s.store.Meta().GetPrincipalByID(r.Context(), pid)
	if err != nil {
		s.writeStoreError(w, r, err)
		return store.Principal{}, store.Mailbox{}, false
	}
	name := r.PathValue("mailbox")
	if name == "" {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"mailbox name is required", "")
		return store.Principal{}, store.Mailbox{}, false
	}
	// Canonicalise INBOX case-insensitively to match the IMAP layer
	// (canonicalMailboxName in internal/protoimap/session_mailbox.go).
	// Other names are preserved verbatim — RFC 3501 mailboxes are
	// modified UTF-7 octet strings and the store does the exact match.
	if strings.EqualFold(name, "INBOX") {
		name = "INBOX"
	}
	mb, err := s.store.Meta().GetMailboxByName(r.Context(), pid, name)
	if err != nil {
		s.writeStoreError(w, r, err)
		return store.Principal{}, store.Mailbox{}, false
	}
	return owner, mb, true
}

// resolveGrantee maps the {grantee} path segment to a *PrincipalID and
// a canonical display label. The literal token "anyone" (case-
// insensitive) returns (nil, "anyone"). Any other value is treated as
// a canonical email and looked up in the directory.
//
// Returns false (and writes a 404) when the grantee email is unknown
// so callers do not accidentally create dangling ACL rows.
func (s *Server) resolveGrantee(w http.ResponseWriter, r *http.Request) (*store.PrincipalID, string, bool) {
	raw := r.PathValue("grantee")
	if raw == "" {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"grantee is required", "")
		return nil, "", false
	}
	if strings.EqualFold(raw, aclGranteeAnyone) {
		return nil, aclGranteeAnyone, true
	}
	email := strings.ToLower(raw)
	p, err := s.store.Meta().GetPrincipalByEmail(r.Context(), email)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeProblem(w, r, http.StatusNotFound, "not_found",
				"grantee principal not found", email)
			return nil, "", false
		}
		s.writeStoreError(w, r, err)
		return nil, "", false
	}
	pid := p.ID
	return &pid, p.CanonicalEmail, true
}

// handleListPrincipalMailboxes handles GET /api/v1/principals/{pid}/mailboxes.
// Returns every mailbox owned by the principal as id+name+principal_id.
// Admin UI calls this to pick the target mailbox before granting ACLs.
func (s *Server) handleListPrincipalMailboxes(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	pid, ok := parsePID(w, r)
	if !ok {
		return
	}
	if _, err := s.store.Meta().GetPrincipalByID(r.Context(), pid); err != nil {
		s.writeStoreError(w, r, err)
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
			ID:          uint64(mb.ID),
			Name:        mb.Name,
			PrincipalID: uint64(mb.PrincipalID),
		})
	}
	writeJSON(w, http.StatusOK, pageDTO[mailboxSummaryDTO]{Items: items, Next: nil})
}

// handleGetMailboxACL handles
//
//	GET /api/v1/principals/{pid}/mailboxes/{mailbox}/acl
//
// Returns the implicit owner row followed by every stored ACL row
// (including any "anyone" row). Rows are sorted by grantee label so
// the response is deterministic for test assertions and UI rendering.
func (s *Server) handleGetMailboxACL(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	owner, mb, ok := s.resolveOwnerAndMailbox(w, r)
	if !ok {
		return
	}
	rows, err := s.store.Meta().GetMailboxACL(r.Context(), mb.ID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	out := make([]aclRowDTO, 0, len(rows)+1)
	// Owner row first — implicit full rights per RFC 4314 §2.
	out = append(out, aclRowDTO{
		Grantee: owner.CanonicalEmail,
		Rights:  aclcodec.Encode(store.ACLRightsAll),
		IsOwner: true,
	})
	for _, row := range rows {
		dto := aclRowDTO{Rights: aclcodec.Encode(row.Rights)}
		if row.PrincipalID == nil {
			dto.Grantee = aclGranteeAnyone
		} else {
			p, perr := s.store.Meta().GetPrincipalByID(r.Context(), *row.PrincipalID)
			if perr != nil {
				// Stale ACL row pointing at a deleted principal: skip
				// silently so we do not leak the deletion mid-list. A
				// future GC sweep can purge orphan rows.
				continue
			}
			dto.Grantee = p.CanonicalEmail
		}
		out = append(out, dto)
	}
	writeJSON(w, http.StatusOK, pageDTO[aclRowDTO]{Items: out, Next: nil})
}

// handlePutMailboxACL handles
//
//	PUT /api/v1/principals/{pid}/mailboxes/{mailbox}/acl/{grantee}
//
// Upserts the ACL row for (mailbox, grantee) with the rights encoded by
// the canonical letter sequence in the request body. Refusing the
// owner-grants-self case (grantee == owner email) avoids writing rows
// that would shadow the implicit owner-has-all-rights behaviour.
func (s *Server) handlePutMailboxACL(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	owner, mb, ok := s.resolveOwnerAndMailbox(w, r)
	if !ok {
		return
	}
	granteePID, granteeLabel, ok := s.resolveGrantee(w, r)
	if !ok {
		return
	}
	var req putACLRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	rights, err := aclcodec.Decode(req.Rights)
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid_rights",
			"rights string contains unknown letters", err.Error())
		return
	}
	if granteePID != nil && *granteePID == owner.ID {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"cannot store an ACL row for the mailbox owner; the owner has implicit full rights", "")
		return
	}
	if err := s.store.Meta().SetMailboxACL(r.Context(), mb.ID, granteePID, rights, caller.ID); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	encoded := aclcodec.Encode(rights)
	s.appendAudit(r.Context(), "mailbox.acl.set",
		fmt.Sprintf("mailbox:%d", mb.ID),
		store.OutcomeSuccess, "",
		map[string]string{
			"owner_pid":    fmt.Sprintf("%d", owner.ID),
			"mailbox_name": mb.Name,
			"grantee":      granteeLabel,
			"rights":       encoded,
		})
	s.logger.InfoContext(r.Context(), "protoadmin.acl.put",
		"activity", observe.ActivityAudit,
		"owner_pid", owner.ID,
		"mailbox_id", mb.ID,
		"mailbox_name", mb.Name,
		"grantee", granteeLabel,
		"rights", encoded,
	)
	writeJSON(w, http.StatusOK, aclRowDTO{
		Grantee: granteeLabel,
		Rights:  encoded,
	})
}

// handleDeleteMailboxACL handles
//
//	DELETE /api/v1/principals/{pid}/mailboxes/{mailbox}/acl/{grantee}
//
// Removes the ACL row for (mailbox, grantee). Idempotent: missing rows
// still return 204 so admin scripts can re-run the delete without
// special-casing 404 on cleanup. (The grantee email must still resolve
// to a known principal — that gates against typo-driven silent no-ops.)
func (s *Server) handleDeleteMailboxACL(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	owner, mb, ok := s.resolveOwnerAndMailbox(w, r)
	if !ok {
		return
	}
	granteePID, granteeLabel, ok := s.resolveGrantee(w, r)
	if !ok {
		return
	}
	if err := s.store.Meta().RemoveMailboxACL(r.Context(), mb.ID, granteePID); err != nil &&
		!errors.Is(err, store.ErrNotFound) {
		s.writeStoreError(w, r, err)
		return
	}
	s.appendAudit(r.Context(), "mailbox.acl.remove",
		fmt.Sprintf("mailbox:%d", mb.ID),
		store.OutcomeSuccess, "",
		map[string]string{
			"owner_pid":    fmt.Sprintf("%d", owner.ID),
			"mailbox_name": mb.Name,
			"grantee":      granteeLabel,
		})
	s.logger.InfoContext(r.Context(), "protoadmin.acl.delete",
		"activity", observe.ActivityAudit,
		"owner_pid", owner.ID,
		"mailbox_id", mb.ID,
		"mailbox_name", mb.Name,
		"grantee", granteeLabel,
	)
	w.WriteHeader(http.StatusNoContent)
}
