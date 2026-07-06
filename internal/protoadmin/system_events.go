package protoadmin

import (
	"net/http"
	"strconv"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// handleSystemEvents serves GET /api/v1/admin/system-events.
//
// Super-admins see all events. Domain-scoped operators see only events for
// their managed domains (REQ-ADM-307). The response is fail-closed: an
// operator whose managed-domain set cannot be resolved receives an empty page.
//
// Query parameters:
//
//	action   — exact match on the Action field
//	actor_id — exact match on the ActorID field
//	since    — RFC3339 lower bound on event time (inclusive)
//	until    — RFC3339 upper bound on event time (exclusive)
//	limit    — page size (default 100, max 1000)
//	before_id — keyset cursor: return rows with ID < before_id
func (s *Server) handleSystemEvents(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}

	scope := ResolveOperatorScope(r.Context(), s.store.Meta(), caller)

	q := r.URL.Query()
	filter := store.SystemEventFilter{
		Action:  q.Get("action"),
		ActorID: q.Get("actor_id"),
	}

	// Apply domain scope per REQ-ADM-307.
	if !scope.SuperAdmin {
		// Domains may be nil (non-admin caller slipped through somehow) or
		// empty (operator with no assigned domains). Both cases are
		// fail-closed: return nothing.
		filter.Domains = scope.Domains
	}

	if raw := q.Get("since"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeProblem(w, r, http.StatusBadRequest, "invalid_param",
				"since must be RFC3339", raw)
			return
		}
		filter.Since = t
	}
	if raw := q.Get("until"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeProblem(w, r, http.StatusBadRequest, "invalid_param",
				"until must be RFC3339", raw)
			return
		}
		filter.Until = t
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			writeProblem(w, r, http.StatusBadRequest, "invalid_limit",
				"limit must be a positive integer", raw)
			return
		}
		filter.Limit = n
	}
	if raw := q.Get("before_id"); raw != "" {
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			writeProblem(w, r, http.StatusBadRequest, "invalid_cursor",
				"before_id must be a positive integer", raw)
			return
		}
		filter.BeforeID = store.SystemEventID(n)
	}

	evs, err := s.store.Meta().ListSystemEvents(r.Context(), filter)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}

	items := make([]map[string]any, 0, len(evs))
	for _, e := range evs {
		items = append(items, systemEventToMap(e))
	}
	var next *string
	if filter.Limit > 0 && len(evs) == filter.Limit && len(evs) > 0 {
		tok := strconv.FormatUint(uint64(evs[len(evs)-1].ID), 10)
		next = &tok
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items,
		"next":  next,
	})
}

func systemEventToMap(e store.SystemEvent) map[string]any {
	m := map[string]any{
		"id":          uint64(e.ID),
		"at":          e.At.UTC().Format(time.RFC3339Nano),
		"action":      e.Action,
		"actor_id":    e.ActorID,
		"subject":     e.Subject,
		"remote_addr": e.RemoteAddr,
		"outcome":     outcomeStr(e.Outcome),
		"message":     e.Message,
		"domain":      e.Domain,
	}
	if len(e.Metadata) > 0 {
		m["metadata"] = e.Metadata
	}
	return m
}

// outcomeStr converts AuditOutcome to its string representation.
func outcomeStr(o store.AuditOutcome) string {
	switch o {
	case store.OutcomeSuccess:
		return "success"
	case store.OutcomeFailure:
		return "failure"
	default:
		return "unknown"
	}
}
