package protoadmin

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// handleMessageResearch serves GET /api/v1/admin/message-research.
//
// The endpoint is a retrospective per-message tracer (REQ-ADM-306). It joins
// three sources and returns a flat timeline sorted newest-first:
//
//   - "received": messages accepted and stored, with envelope + disposition
//     (mailbox, Junk flag, spam verdict). No body content is ever returned.
//   - "smtp_event": SMTP-time accept/reject/defer entries from system_events.
//     These cover messages that were rejected before storage.
//   - "send_outcome": outbound queue entries from the queue table.
//
// Operator scope (REQ-ADM-307): super-admins see everything; domain-scoped
// operators see only messages whose local participant's domain is in their
// managed set; callers with an unresolvable scope receive an empty result.
//
// Audit-logged per REQ-ADM-300.
//
// Query parameters:
//
//	sender       — substring match on envelope From (case-insensitive)
//	recipient    — substring match on envelope To (case-insensitive)
//	message_id   — exact match on Message-ID header (case-insensitive)
//	subject      — substring match on Subject header (case-insensitive)
//	date_from    — RFC3339 lower bound on event time (inclusive)
//	date_to      — RFC3339 upper bound on event time (exclusive)
//	limit        — page size (default 100, max 1000)
//	before_us    — keyset cursor: Unix microseconds; return entries older than this
func (s *Server) handleMessageResearch(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}

	scope := ResolveOperatorScope(r.Context(), s.store.Meta(), caller)

	q := r.URL.Query()

	// Parse limit.
	limit := 100
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			writeProblem(w, r, http.StatusBadRequest, "invalid_limit",
				"limit must be a positive integer", raw)
			return
		}
		if n > 1000 {
			n = 1000
		}
		limit = n
	}

	// Parse before_us cursor.
	var beforeUS int64
	if raw := q.Get("before_us"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n <= 0 {
			writeProblem(w, r, http.StatusBadRequest, "invalid_cursor",
				"before_us must be a positive integer (Unix microseconds)", raw)
			return
		}
		beforeUS = n
	}

	// Parse date range.
	var dateFrom, dateTo time.Time
	if raw := q.Get("date_from"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeProblem(w, r, http.StatusBadRequest, "invalid_param",
				"date_from must be RFC3339", raw)
			return
		}
		dateFrom = t
	}
	if raw := q.Get("date_to"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeProblem(w, r, http.StatusBadRequest, "invalid_param",
				"date_to must be RFC3339", raw)
			return
		}
		dateTo = t
	}

	sender := q.Get("sender")
	recipient := q.Get("recipient")
	messageID := q.Get("message_id")
	subject := q.Get("subject")

	// Resolve domain filters per REQ-ADM-307.
	// nil Domains = super-admin (unrestricted).
	// non-nil empty = fail-closed (no results).
	var msgDomains []string    // for SearchAdminMessages
	var evtDomains []string    // for ListSystemEvents
	var senderDomains []string // for ListQueueItems
	if !scope.SuperAdmin {
		msgDomains = scope.Domains // may be nil (fail-closed below) or []string{...}
		evtDomains = scope.Domains
		senderDomains = scope.Domains
		if scope.Domains == nil {
			// No admin: treat as empty (fail-closed).
			msgDomains = []string{}
			evtDomains = []string{}
			senderDomains = []string{}
		}
	}

	// Fetch limit+1 from each source to detect whether a next page exists.
	fetchLimit := limit + 1

	// --- Source 1: received messages ---
	msgFilter := store.AdminMessageFilter{
		Sender:           sender,
		Recipient:        recipient,
		MessageID:        messageID,
		Subject:          subject,
		DateFrom:         dateFrom,
		DateTo:           dateTo,
		Domains:          msgDomains,
		Limit:            fetchLimit,
		BeforeReceivedUs: beforeUS,
	}
	msgs, err := s.store.Meta().SearchAdminMessages(r.Context(), msgFilter)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}

	// --- Source 2: SMTP system events ---
	evtFilter := store.SystemEventFilter{
		Domains: evtDomains,
		Limit:   fetchLimit,
	}
	if !dateFrom.IsZero() {
		evtFilter.Since = dateFrom
	}
	if !dateTo.IsZero() {
		evtFilter.Until = dateTo
	}
	if beforeUS != 0 {
		evtFilter.Until = time.UnixMicro(beforeUS)
	}
	evts, err := s.store.Meta().ListSystemEvents(r.Context(), evtFilter)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}

	// --- Source 3: outbound queue items ---
	qFilter := store.QueueFilter{
		SenderDomains:    senderDomains,
		MailFromContains: sender,
		RcptToContains:   recipient,
		Limit:            fetchLimit,
	}
	qItems, err := s.store.Meta().ListQueueItems(r.Context(), qFilter)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}

	// --- Build unified timeline ---
	type entry struct {
		at   time.Time
		data map[string]any
	}

	var timeline []entry

	for _, m := range msgs {
		e := map[string]any{
			"source":       "received",
			"at":           m.ReceivedAt.UTC().Format(time.RFC3339Nano),
			"principal_id": uint64(m.PrincipalID),
			"mailbox_name": m.MailboxName,
			"is_junk":      m.IsJunk,
			"envelope": map[string]any{
				"subject":     m.Envelope.Subject,
				"from":        m.Envelope.From,
				"to":          m.Envelope.To,
				"cc":          m.Envelope.Cc,
				"bcc":         m.Envelope.Bcc,
				"reply_to":    m.Envelope.ReplyTo,
				"message_id":  m.Envelope.MessageID,
				"in_reply_to": m.Envelope.InReplyTo,
				"references":  m.Envelope.References,
			},
		}
		if !m.Envelope.Date.IsZero() {
			e["envelope"].(map[string]any)["date"] = m.Envelope.Date.UTC().Format(time.RFC3339)
		}
		if m.SpamVerdict != nil {
			e["spam_verdict"] = *m.SpamVerdict
		}
		if m.SpamConfidence != nil {
			e["spam_confidence"] = *m.SpamConfidence
		}
		timeline = append(timeline, entry{at: m.ReceivedAt, data: e})
	}

	for _, ev := range evts {
		e := map[string]any{
			"source":      "smtp_event",
			"at":          ev.At.UTC().Format(time.RFC3339Nano),
			"action":      ev.Action,
			"actor_id":    ev.ActorID,
			"subject":     ev.Subject,
			"remote_addr": ev.RemoteAddr,
			"outcome":     outcomeStr(ev.Outcome),
			"message":     ev.Message,
			"domain":      ev.Domain,
		}
		if len(ev.Metadata) > 0 {
			e["metadata"] = ev.Metadata
		}
		// Apply before_us time filter to system events (since ListSystemEvents
		// uses Until as exclusive upper bound on ts, which is rounded to microsecond
		// precision, this re-filters events that match the cursor exactly).
		if beforeUS != 0 && ev.At.UnixMicro() >= beforeUS {
			continue
		}
		// Apply date_from filter (ListSystemEvents Since is inclusive).
		if !dateFrom.IsZero() && ev.At.Before(dateFrom) {
			continue
		}
		timeline = append(timeline, entry{at: ev.At, data: e})
	}

	for _, qi := range qItems {
		// Apply time cursor to queue items (ListQueueItems has no time cursor).
		if beforeUS != 0 && qi.CreatedAt.UnixMicro() >= beforeUS {
			continue
		}
		if !dateFrom.IsZero() && qi.CreatedAt.Before(dateFrom) {
			continue
		}
		if !dateTo.IsZero() && !qi.CreatedAt.Before(dateTo) {
			continue
		}
		e := map[string]any{
			"source":      "send_outcome",
			"at":          qi.CreatedAt.UTC().Format(time.RFC3339Nano),
			"queue_id":    uint64(qi.ID),
			"mail_from":   qi.MailFrom,
			"rcpt_to":     qi.RcptTo,
			"envelope_id": string(qi.EnvelopeID),
			"state":       qi.State.String(),
			"attempts":    qi.Attempts,
		}
		if qi.LastError != "" {
			e["last_error"] = qi.LastError
		}
		if !qi.LastAttemptAt.IsZero() {
			e["last_attempt_at"] = qi.LastAttemptAt.UTC().Format(time.RFC3339)
		}
		timeline = append(timeline, entry{at: qi.CreatedAt, data: e})
	}

	// Sort newest-first.
	sort.Slice(timeline, func(i, j int) bool {
		return timeline[i].at.After(timeline[j].at)
	})

	// Paginate: take first limit entries; compute next cursor from the oldest
	// entry on the page.
	var next *string
	if len(timeline) > limit {
		timeline = timeline[:limit]
		oldest := timeline[limit-1].at.UnixMicro()
		tok := strconv.FormatInt(oldest, 10)
		next = &tok
	}

	items := make([]map[string]any, len(timeline))
	for i, e := range timeline {
		items[i] = e.data
	}

	// Audit-log the read access (REQ-ADM-300).
	s.appendAudit(r.Context(),
		"admin.message_research.read", "",
		store.OutcomeSuccess, "message research query",
		map[string]string{
			"sender":    sender,
			"recipient": recipient,
		})

	writeJSON(w, http.StatusOK, map[string]any{
		"items": items,
		"next":  next,
	})
}
