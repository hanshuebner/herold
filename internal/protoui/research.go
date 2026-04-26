package protoui

import (
	"net/http"
	"strings"

	"github.com/hanshuebner/herold/internal/store"
)

// researchData is the body payload for templates/research.html.
//
// The "research" view is a thin wrapper over Metadata.ListQueueItems
// plus the audit log. A complete implementation requires joining
// messages, state_changes, and queue rows for both inbound and
// outbound mail, and exposing per-principal scoping. The Phase 2
// minimum the docs/design/requirements/08 spec calls for is "queue rows
// matched by sender/recipient/state with date range"; richer joins
// land when the scheduler ships its result-aggregation surface.
//
// The form is GET-driven so the URL is shareable.
type researchData struct {
	Sender    string
	Recipient string
	State     string
	Items     []store.QueueItem
	Searched  bool
}

func (s *Server) handleResearch(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFromCtx(r.Context())

	q := r.URL.Query()
	body := researchData{
		Sender:    strings.TrimSpace(q.Get("sender")),
		Recipient: strings.TrimSpace(q.Get("recipient")),
		State:     strings.TrimSpace(q.Get("state")),
	}
	// Search runs only when at least one filter is set; otherwise
	// the empty form renders.
	if body.Sender == "" && body.Recipient == "" && body.State == "" {
		s.renderPage(w, r, http.StatusOK, &pageData{
			Title:    "Email research",
			Active:   "research",
			BodyTmpl: "research_body",
			Body:     body,
		})
		return
	}
	body.Searched = true

	filter := store.QueueFilter{Limit: 100}
	if !caller.Flags.Has(store.PrincipalFlagAdmin) {
		filter.PrincipalID = caller.ID
	}
	if body.Recipient != "" && strings.Contains(body.Recipient, "@") {
		// The store filter has RecipientDomain only; pull domain from
		// `local@domain` and post-filter on RcptTo for the local part.
		parts := strings.SplitN(body.Recipient, "@", 2)
		filter.RecipientDomain = strings.ToLower(parts[1])
	}
	switch body.State {
	case "queued":
		filter.State = store.QueueStateQueued
	case "deferred":
		filter.State = store.QueueStateDeferred
	case "done":
		filter.State = store.QueueStateDone
	case "failed":
		filter.State = store.QueueStateFailed
	case "held":
		filter.State = store.QueueStateHeld
	}
	rows, err := s.store.Meta().ListQueueItems(r.Context(), filter)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "Search failed: "+err.Error())
		return
	}
	// In-memory post-filters for sender / full recipient.
	if body.Sender != "" {
		needle := strings.ToLower(body.Sender)
		filtered := make([]store.QueueItem, 0, len(rows))
		for _, q := range rows {
			if strings.Contains(strings.ToLower(q.MailFrom), needle) {
				filtered = append(filtered, q)
			}
		}
		rows = filtered
	}
	if body.Recipient != "" {
		needle := strings.ToLower(body.Recipient)
		filtered := make([]store.QueueItem, 0, len(rows))
		for _, q := range rows {
			if strings.Contains(strings.ToLower(q.RcptTo), needle) {
				filtered = append(filtered, q)
			}
		}
		rows = filtered
	}
	body.Items = rows
	s.renderPage(w, r, http.StatusOK, &pageData{
		Title:    "Email research",
		Active:   "research",
		BodyTmpl: "research_body",
		Body:     body,
	})
}
