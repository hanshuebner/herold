package protoui

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

type auditData struct {
	Items   []store.AuditLogEntry
	Action  string
	Actor   string
	HasNext bool
	NextCur uint64
	Limit   int
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFromCtx(r.Context())
	if !caller.Flags.Has(store.PrincipalFlagAdmin) {
		s.renderError(w, r, http.StatusForbidden, "Admin privileges required.")
		return
	}
	q := r.URL.Query()
	limit := 50
	if raw := q.Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	filter := store.AuditLogFilter{
		Action: strings.TrimSpace(q.Get("action")),
		Limit:  limit,
	}
	if raw := q.Get("after"); raw != "" {
		if n, err := strconv.ParseUint(raw, 10, 64); err == nil {
			filter.AfterID = store.AuditLogID(n)
		}
	}
	if raw := q.Get("since"); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			filter.Since = t
		}
	}
	if raw := q.Get("until"); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			filter.Until = t
		}
	}
	if raw := strings.TrimSpace(q.Get("actor")); raw != "" {
		// The store filters by principal-id, not free-form actor; we
		// translate when the form gives us a numeric id.
		if n, err := strconv.ParseUint(raw, 10, 64); err == nil {
			filter.PrincipalID = store.PrincipalID(n)
		}
	}
	rows, err := s.store.Meta().ListAuditLog(r.Context(), filter)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "Audit list failed: "+err.Error())
		return
	}
	body := auditData{Items: rows, Action: filter.Action, Limit: limit}
	if len(rows) == limit && len(rows) > 0 {
		body.HasNext = true
		body.NextCur = uint64(rows[len(rows)-1].ID)
	}
	s.renderPage(w, r, http.StatusOK, &pageData{
		Title:    "Audit log",
		Active:   "audit",
		BodyTmpl: "audit_body",
		Body:     body,
	})
}
