package protoadmin

// mlist_moderation.go — admin REST surface for hosted mailing-list
// moderation (v2 milestone, issue #189, REQ-MLIST-80, REQ-AC-41): the
// held-post queue and the owner/moderator approve/reject/discard
// decisions. Business logic (fan-out on approve, the never-fan-out
// disposal on reject/discard, audit) lives in internal/maillist's
// Expander (hold.go); this file is the thin REST/authz/DTO layer over
// it, mirroring mlist.go / mlist_members.go's own split.
//
// Endpoint shape (all behind authAdmin, then requireMlistModerateAccess
// on the parent list -- list:owner OR list:moderator OR domain:operator
// OR super-admin; a bare roster member with neither grant gets 403):
//
//	GET    /api/v1/lists/{id}/held                 held-post queue (optionally ?status=)
//	GET    /api/v1/lists/{id}/held/{hid}            one held post's metadata
//	GET    /api/v1/lists/{id}/held/{hid}/raw        the held post's raw message/rfc822 bytes
//	POST   /api/v1/lists/{id}/held/{hid}/approve    fan out through the normal S1 path
//	POST   /api/v1/lists/{id}/held/{hid}/reject     {note?} -- never fanned out
//	POST   /api/v1/lists/{id}/held/{hid}/discard    {note?} -- never fanned out

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/hanshuebner/herold/internal/maillist"
	"github.com/hanshuebner/herold/internal/store"
)

// MailingListModerator is the narrow internal/maillist.Expander surface
// the held-post moderation endpoints need: fanning an approved post out
// through the normal S1 path, and the never-fan-out reject/discard
// disposals. *maillist.Expander satisfies this directly (the same
// instance internal/admin/server.go wires as the SMTP server's mailing-
// list expander).
type MailingListModerator interface {
	ApproveHeldPost(ctx context.Context, id store.MailingListHeldPostID, approver store.PrincipalID) (maillist.ExpandResult, error)
	RejectHeldPost(ctx context.Context, id store.MailingListHeldPostID, actor store.PrincipalID, note string) (store.MailingListHeldPost, error)
	DiscardHeldPost(ctx context.Context, id store.MailingListHeldPostID, actor store.PrincipalID, note string) (store.MailingListHeldPost, error)
}

// parseMailingListHeldPostID reads the {hid} path parameter.
func parseMailingListHeldPostID(w http.ResponseWriter, r *http.Request) (store.MailingListHeldPostID, bool) {
	raw := r.PathValue("hid")
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || n == 0 {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id",
			"held post id must be a positive integer", raw)
		return 0, false
	}
	return store.MailingListHeldPostID(n), true
}

// loadListAndCheckModerateAccess is the common prologue for every held-
// post handler: load the parent list, then apply
// requireMlistModerateAccess (REQ-AC-41's wider grant set than the S1
// config/roster surface accepts). Callers MUST have already passed
// requireAdmin.
func (s *Server) loadListAndCheckModerateAccess(w http.ResponseWriter, r *http.Request, caller store.Principal) (store.MailingList, bool) {
	l, ok := s.loadMailingList(w, r)
	if !ok {
		return store.MailingList{}, false
	}
	if !s.requireMlistModerateAccess(w, r, caller, l) {
		return store.MailingList{}, false
	}
	return l, true
}

// loadHeldPostInList loads a held-post row by {hid} and verifies it
// belongs to l — held-post ids are a global sequence, so without this
// check a caller authorized to moderate list A could address a held
// post belonging to list B via the URL alone.
func (s *Server) loadHeldPostInList(w http.ResponseWriter, r *http.Request, l store.MailingList) (store.MailingListHeldPost, bool) {
	hid, ok := parseMailingListHeldPostID(w, r)
	if !ok {
		return store.MailingListHeldPost{}, false
	}
	h, err := s.store.Meta().GetMailingListHeldPost(r.Context(), hid)
	if err != nil {
		s.writeMlistError(w, r, err)
		return store.MailingListHeldPost{}, false
	}
	if h.ListID != l.ID {
		writeProblem(w, r, http.StatusNotFound, "not_found",
			"held post not found on this list", "")
		return store.MailingListHeldPost{}, false
	}
	return h, true
}

// mailingListHeldPostDTO is the wire representation of a held-post row.
type mailingListHeldPostDTO struct {
	ID           uint64     `json:"id"`
	ListID       uint64     `json:"list_id"`
	FromAddress  string     `json:"from_address"`
	Subject      string     `json:"subject"`
	MessageID    string     `json:"message_id,omitempty"`
	Reason       string     `json:"reason"`
	Status       string     `json:"status"`
	BlobSize     int64      `json:"blob_size"`
	HeldAt       time.Time  `json:"held_at"`
	DecidedAt    *time.Time `json:"decided_at,omitempty"`
	DecidedBy    uint64     `json:"decided_by,omitempty"`
	DecisionNote string     `json:"decision_note,omitempty"`
}

func toMailingListHeldPostDTO(h store.MailingListHeldPost) mailingListHeldPostDTO {
	dto := mailingListHeldPostDTO{
		ID:           uint64(h.ID),
		ListID:       uint64(h.ListID),
		FromAddress:  h.FromAddress,
		Subject:      h.Subject,
		MessageID:    h.MessageID,
		Reason:       string(h.Reason),
		Status:       string(h.Status),
		BlobSize:     h.BlobSize,
		HeldAt:       h.HeldAt,
		DecidedAt:    h.DecidedAt,
		DecisionNote: h.DecisionNote,
	}
	if h.DecidedBy != nil {
		dto.DecidedBy = uint64(*h.DecidedBy)
	}
	return dto
}

// handleListMailingListHeldPosts handles GET /api/v1/lists/{id}/held.
// ?status= restricts to one status (pending/approved/rejected/discarded);
// omitted means every held post regardless of status, matching the
// roster collection endpoint's own ?state= convention.
func (s *Server) handleListMailingListHeldPosts(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	l, ok := s.loadListAndCheckModerateAccess(w, r, caller)
	if !ok {
		return
	}
	status := r.URL.Query().Get("status")
	if status != "" && !validHeldPostStatus(status) {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"status must be one of pending, approved, rejected, discarded", status)
		return
	}
	after, limit, ok := parseAfterLimit(w, r, 0, 100, 1000)
	if !ok {
		return
	}
	rows, err := s.store.Meta().ListMailingListHeldPosts(r.Context(), store.MailingListHeldPostFilter{
		ListID:  l.ID,
		Status:  store.MailingListHeldPostStatus(status),
		AfterID: store.MailingListHeldPostID(after),
		Limit:   limit,
	})
	if err != nil {
		s.writeMlistError(w, r, err)
		return
	}
	items := make([]mailingListHeldPostDTO, 0, len(rows))
	for _, h := range rows {
		items = append(items, toMailingListHeldPostDTO(h))
	}
	var next *string
	if len(rows) == limit && len(rows) > 0 {
		tok := strconv.FormatUint(uint64(rows[len(rows)-1].ID), 10)
		next = &tok
	}
	writeJSON(w, http.StatusOK, pageDTO[mailingListHeldPostDTO]{Items: items, Next: next})
}

func validHeldPostStatus(s string) bool {
	switch store.MailingListHeldPostStatus(s) {
	case store.MailingListHeldPostPending, store.MailingListHeldPostApproved,
		store.MailingListHeldPostRejected, store.MailingListHeldPostDiscarded:
		return true
	default:
		return false
	}
}

// handleGetMailingListHeldPost handles GET /api/v1/lists/{id}/held/{hid}.
func (s *Server) handleGetMailingListHeldPost(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	l, ok := s.loadListAndCheckModerateAccess(w, r, caller)
	if !ok {
		return
	}
	h, ok := s.loadHeldPostInList(w, r, l)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, toMailingListHeldPostDTO(h))
}

// handleGetMailingListHeldPostRaw handles GET
// /api/v1/lists/{id}/held/{hid}/raw: the held message's raw bytes
// (header block + body, CRLF-terminated), served as message/rfc822 so a
// moderator's client (or the operator SPA) can render the actual post
// content, not just its denormalised From/Subject.
func (s *Server) handleGetMailingListHeldPostRaw(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	l, ok := s.loadListAndCheckModerateAccess(w, r, caller)
	if !ok {
		return
	}
	h, ok := s.loadHeldPostInList(w, r, l)
	if !ok {
		return
	}
	rc, err := s.store.Blobs().Get(r.Context(), h.BlobHash)
	if err != nil {
		s.writeMlistError(w, r, err)
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", "message/rfc822")
	w.Header().Set("Content-Length", strconv.FormatInt(h.BlobSize, 10))
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, rc); err != nil {
		s.loggerFrom(r.Context()).Warn("protoadmin.mlist_held_raw_copy_failed",
			"err", err, "held_post_id", h.ID)
	}
}

// handleApproveMailingListHeldPost handles POST
// /api/v1/lists/{id}/held/{hid}/approve (REQ-MLIST-80: "an approved
// held post fans out normally"). 501 when no MailingListModerator is
// wired (mirrors every other optional-capability endpoint's nil-501
// convention, e.g. CertRenewer/DKIMKeyManager).
func (s *Server) handleApproveMailingListHeldPost(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	l, ok := s.loadListAndCheckModerateAccess(w, r, caller)
	if !ok {
		return
	}
	h, ok := s.loadHeldPostInList(w, r, l)
	if !ok {
		return
	}
	if s.opts.MailingListModerator == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not_implemented",
			"mailing-list moderation is not configured on this server", "")
		return
	}
	res, err := s.opts.MailingListModerator.ApproveHeldPost(r.Context(), h.ID, caller.ID)
	if err != nil {
		s.writeMlistError(w, r, err)
		return
	}
	s.appendAuditDomain(r.Context(), "mlist.held.approved",
		fmt.Sprintf("held_post:%d", h.ID),
		store.OutcomeSuccess, "", map[string]string{
			"list_id":      strconv.FormatUint(uint64(l.ID), 10),
			"member_count": strconv.Itoa(res.MemberCount),
		}, l.Domain)
	decided, err := s.store.Meta().GetMailingListHeldPost(r.Context(), h.ID)
	if err != nil {
		s.writeMlistError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toMailingListHeldPostDTO(decided))
}

// decideHeldPostRequest is the POST .../held/{hid}/reject and
// .../held/{hid}/discard body: an optional free-text moderator note.
type decideHeldPostRequest struct {
	Note string `json:"note,omitempty"`
}

// handleRejectMailingListHeldPost handles POST
// /api/v1/lists/{id}/held/{hid}/reject: never fanned out.
func (s *Server) handleRejectMailingListHeldPost(w http.ResponseWriter, r *http.Request) {
	s.decideHeldPostHandler(w, r, "mlist.held.rejected", func(ctx context.Context, id store.MailingListHeldPostID, actor store.PrincipalID, note string) (store.MailingListHeldPost, error) {
		return s.opts.MailingListModerator.RejectHeldPost(ctx, id, actor, note)
	})
}

// handleDiscardMailingListHeldPost handles POST
// /api/v1/lists/{id}/held/{hid}/discard: never fanned out. Distinct
// from reject only in the operator-visible action label.
func (s *Server) handleDiscardMailingListHeldPost(w http.ResponseWriter, r *http.Request) {
	s.decideHeldPostHandler(w, r, "mlist.held.discarded", func(ctx context.Context, id store.MailingListHeldPostID, actor store.PrincipalID, note string) (store.MailingListHeldPost, error) {
		return s.opts.MailingListModerator.DiscardHeldPost(ctx, id, actor, note)
	})
}

func (s *Server) decideHeldPostHandler(
	w http.ResponseWriter, r *http.Request, auditAction string,
	decide func(ctx context.Context, id store.MailingListHeldPostID, actor store.PrincipalID, note string) (store.MailingListHeldPost, error),
) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	l, ok := s.loadListAndCheckModerateAccess(w, r, caller)
	if !ok {
		return
	}
	h, ok := s.loadHeldPostInList(w, r, l)
	if !ok {
		return
	}
	if s.opts.MailingListModerator == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not_implemented",
			"mailing-list moderation is not configured on this server", "")
		return
	}
	var req decideHeldPostRequest
	if r.ContentLength != 0 {
		if !decodeJSONBody(w, r, &req) {
			return
		}
	}
	decided, err := decide(r.Context(), h.ID, caller.ID, req.Note)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeProblem(w, r, http.StatusConflict, "conflict",
				"held post has already been decided", "")
			return
		}
		s.writeMlistError(w, r, err)
		return
	}
	s.appendAuditDomain(r.Context(), auditAction,
		fmt.Sprintf("held_post:%d", h.ID),
		store.OutcomeSuccess, "", map[string]string{
			"list_id": strconv.FormatUint(uint64(l.ID), 10),
		}, l.Domain)
	writeJSON(w, http.StatusOK, toMailingListHeldPostDTO(decided))
}
