package protoui

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/hanshuebner/herold/internal/store"
)

type queueListData struct {
	Items   []store.QueueItem
	State   string
	Domain  string
	HasNext bool
	NextCur uint64
	Limit   int
}

type queueDetailData struct {
	Item store.QueueItem
}

// retryBackoffOnRetry is the small static deferral the retry button
// applies — we move the row to deferred at "now", letting the
// scheduler pick it up immediately.
//
// Keeping this in code (not template) so the future scheduler-aware
// logic that lives here can read the operator's chosen policy.

func (s *Server) handleQueueList(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFromCtx(r.Context())
	if !caller.Flags.Has(store.PrincipalFlagAdmin) {
		s.renderError(w, r, http.StatusForbidden, "Admin privileges required.")
		return
	}
	q := r.URL.Query()
	stateStr := q.Get("state")
	limit := 50
	if raw := q.Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	filter := store.QueueFilter{
		Limit:           limit,
		RecipientDomain: strings.TrimSpace(q.Get("domain")),
	}
	if raw := q.Get("after"); raw != "" {
		if n, err := strconv.ParseUint(raw, 10, 64); err == nil {
			filter.AfterID = store.QueueItemID(n)
		}
	}
	switch stateStr {
	case "queued":
		filter.State = store.QueueStateQueued
	case "deferred":
		filter.State = store.QueueStateDeferred
	case "inflight":
		filter.State = store.QueueStateInflight
	case "done":
		filter.State = store.QueueStateDone
	case "failed":
		filter.State = store.QueueStateFailed
	case "held":
		filter.State = store.QueueStateHeld
	}
	rows, err := s.store.Meta().ListQueueItems(r.Context(), filter)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "Queue list failed: "+err.Error())
		return
	}
	body := queueListData{Items: rows, State: stateStr, Domain: filter.RecipientDomain, Limit: limit}
	if len(rows) == limit {
		body.HasNext = true
		body.NextCur = uint64(rows[len(rows)-1].ID)
	}
	flash := flashFromQuery(r)
	s.renderPage(w, r, http.StatusOK, &pageData{
		Title:    "Queue",
		Active:   "queue",
		Flash:    flash,
		BodyTmpl: "queue_list_body",
		Body:     body,
	})
}

func (s *Server) handleQueueDetail(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFromCtx(r.Context())
	if !caller.Flags.Has(store.PrincipalFlagAdmin) {
		s.renderError(w, r, http.StatusForbidden, "Admin privileges required.")
		return
	}
	id, ok := s.parseQueueID(w, r)
	if !ok {
		return
	}
	item, err := s.store.Meta().GetQueueItem(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.renderError(w, r, http.StatusNotFound, "Queue item not found.")
			return
		}
		s.renderError(w, r, http.StatusInternalServerError, "Queue lookup failed: "+err.Error())
		return
	}
	flash := flashFromQuery(r)
	s.renderPage(w, r, http.StatusOK, &pageData{
		Title:    "Queue item",
		Active:   "queue",
		Flash:    flash,
		BodyTmpl: "queue_detail_body",
		Body:     queueDetailData{Item: item},
	})
}

func (s *Server) handleQueueRetry(w http.ResponseWriter, r *http.Request) {
	id, ok := s.queueOpGate(w, r)
	if !ok {
		return
	}
	if err := s.store.Meta().RescheduleQueueItem(r.Context(), id, s.clk.Now(), "operator-retry"); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "Retry failed: "+err.Error())
		return
	}
	http.Redirect(w, r, s.queueRedirect(id), http.StatusSeeOther)
}

func (s *Server) handleQueueHold(w http.ResponseWriter, r *http.Request) {
	id, ok := s.queueOpGate(w, r)
	if !ok {
		return
	}
	if err := s.store.Meta().HoldQueueItem(r.Context(), id); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "Hold failed: "+err.Error())
		return
	}
	http.Redirect(w, r, s.queueRedirect(id), http.StatusSeeOther)
}

func (s *Server) handleQueueRelease(w http.ResponseWriter, r *http.Request) {
	id, ok := s.queueOpGate(w, r)
	if !ok {
		return
	}
	if err := s.store.Meta().ReleaseQueueItem(r.Context(), id); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "Release failed: "+err.Error())
		return
	}
	http.Redirect(w, r, s.queueRedirect(id), http.StatusSeeOther)
}

func (s *Server) handleQueueDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := s.queueOpGate(w, r)
	if !ok {
		return
	}
	if r.PostForm.Get("confirm") != "DELETE" {
		s.renderError(w, r, http.StatusBadRequest, "Type DELETE to confirm.")
		return
	}
	if err := s.store.Meta().DeleteQueueItem(r.Context(), id); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "Delete failed: "+err.Error())
		return
	}
	http.Redirect(w, r, s.pathPrefix+"/queue?flash=queue_action", http.StatusSeeOther)
}

// queueOpGate enforces admin + parses the queue id; returns false on
// any failure so the handler simply returns.
func (s *Server) queueOpGate(w http.ResponseWriter, r *http.Request) (store.QueueItemID, bool) {
	caller, _ := principalFromCtx(r.Context())
	if !caller.Flags.Has(store.PrincipalFlagAdmin) {
		s.renderError(w, r, http.StatusForbidden, "Admin privileges required.")
		return 0, false
	}
	return s.parseQueueID(w, r)
}

func (s *Server) parseQueueID(w http.ResponseWriter, r *http.Request) (store.QueueItemID, bool) {
	raw := r.PathValue("id")
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || n == 0 {
		s.renderError(w, r, http.StatusBadRequest, "Invalid queue id.")
		return 0, false
	}
	return store.QueueItemID(n), true
}

func (s *Server) queueRedirect(id store.QueueItemID) string {
	return s.pathPrefix + "/queue/" + strconv.FormatUint(uint64(id), 10) + "?flash=queue_action"
}
