package protoui

import (
	"net/http"

	"github.com/hanshuebner/herold/internal/store"
)

// dashboardData is the body payload for templates/dashboard.html.
type dashboardData struct {
	QueueCounts  map[string]int
	RecentAudit  []store.AuditLogEntry
	DomainsCount int
}

func (s *Server) handleDashboardRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, s.pathPrefix+"/dashboard", http.StatusSeeOther)
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	body := dashboardData{
		QueueCounts: map[string]int{},
	}
	// Queue counts. We tolerate a partial failure: if the call fails
	// the panel renders with zeros and a flash banner explains why.
	if counts, err := s.store.Meta().CountQueueByState(r.Context()); err == nil {
		for k, v := range counts {
			body.QueueCounts[k.String()] = v
		}
	}
	// Recent audit, capped at 20 rows.
	if entries, err := s.store.Meta().ListAuditLog(r.Context(), store.AuditLogFilter{Limit: 20}); err == nil {
		body.RecentAudit = entries
	}
	if domains, err := s.store.Meta().ListLocalDomains(r.Context()); err == nil {
		body.DomainsCount = len(domains)
	}
	s.renderPage(w, r, http.StatusOK, &pageData{
		Title:    "Dashboard",
		Active:   "dashboard",
		BodyTmpl: "dashboard_body",
		Body:     body,
	})
}
