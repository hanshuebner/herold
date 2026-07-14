package protoadmin

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/store"
)

// handleMessageResearch serves GET /api/v1/admin/message-research.
//
// The endpoint is a retrospective per-message tracer (REQ-ADM-306). It joins
// three sources and returns a flat timeline sorted newest-first:
//
//   - "received": messages accepted and stored, with envelope, the owning
//     principal (principal_id and its resolved principal_email, re #226),
//     the recorded-at-ingest delivery disposition (store.MessageDeliveryDisposition,
//     re #143 -- immutable, never recomputed from current mailbox state),
//     the message's current mailbox/Junk membership (live state, for
//     "where is it now"), and spam verdict. No body content is ever
//     returned.
//   - "smtp_event": SMTP-time accept/reject/defer entries from system_events.
//     These cover messages that were rejected before storage.
//   - "send_outcome": outbound queue entries from the queue table. Beyond
//     the sender/recipient/domain-scope filters applied directly to the
//     queue table, a "send_outcome" entry also surfaces when its
//     message_id column (re #235) matches a "received" entry this same
//     search found: an alias-forward relay row's mail_from/rcpt_to are
//     SRS-rewritten and no longer contain the original sender/recipient
//     address as a substring, so this Message-ID join is what lets a
//     sender/recipient search reach a relayed message's outbound leg.
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
		// Newest-first: message research queries the full queue
		// history with no State restriction, so the default oldest-
		// first fetch would silently drop every recent send/forward
		// outcome once the queue table exceeds one page (re #143).
		Newest: true,
	}
	qItems, err := s.store.Meta().ListQueueItems(r.Context(), qFilter)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}

	// Correlate relay/forward queue rows back to the received message(s)
	// this search matched, by Message-ID (REQ-ADM-306, re #235). An
	// alias-forward relay row's mail_from/rcpt_to are SRS-rewritten and
	// no longer contain the original sender/recipient address as a
	// substring, so the address-filtered query above can miss it even
	// though the row carries the same Message-ID as the received message
	// it originated from. Collect every distinct Message-ID this search
	// touched -- the message_id query param itself, plus every matched
	// received message's envelope Message-ID -- and run a second,
	// message-id-only queue query to pick up rows the address filter
	// alone would drop, then merge (deduped by queue row id) with the
	// address-filtered result above. The query param is normalised
	// (mailparse.NormalizeMessageID) before comparison: the queue's
	// message_id column and the messages table's env_message_id column
	// both store the normalised form, but an operator may type the
	// Message-ID with angle brackets or mixed case.
	correlationIDs := make(map[string]struct{})
	if messageID != "" {
		correlationIDs[mailparse.NormalizeMessageID(messageID)] = struct{}{}
	}
	for _, m := range msgs {
		if m.Envelope.MessageID != "" {
			correlationIDs[m.Envelope.MessageID] = struct{}{}
		}
	}
	if len(correlationIDs) > 0 {
		ids := make([]string, 0, len(correlationIDs))
		for id := range correlationIDs {
			ids = append(ids, id)
		}
		corrFilter := store.QueueFilter{
			SenderDomains: senderDomains,
			MessageIDs:    ids,
			Limit:         fetchLimit,
			Newest:        true,
		}
		corrItems, err := s.store.Meta().ListQueueItems(r.Context(), corrFilter)
		if err != nil {
			s.writeStoreError(w, r, err)
			return
		}
		seen := make(map[store.QueueItemID]struct{}, len(qItems))
		for _, qi := range qItems {
			seen[qi.ID] = struct{}{}
		}
		for _, qi := range corrItems {
			if _, dup := seen[qi.ID]; dup {
				continue
			}
			seen[qi.ID] = struct{}{}
			qItems = append(qItems, qi)
		}
	}

	// --- Build unified timeline ---
	type entry struct {
		at   time.Time
		data map[string]any
	}

	var timeline []entry

	// Resolve PrincipalID -> CanonicalEmail once per distinct principal
	// rather than once per received row: a single operator search
	// commonly returns many messages for the same handful of mailboxes.
	principalEmails := make(map[store.PrincipalID]string)
	resolvePrincipalEmail := func(pid store.PrincipalID) string {
		if email, ok := principalEmails[pid]; ok {
			return email
		}
		email := ""
		if p, err := s.store.Meta().GetPrincipalByID(r.Context(), pid); err == nil {
			email = p.CanonicalEmail
		}
		principalEmails[pid] = email
		return email
	}

	for _, m := range msgs {
		e := map[string]any{
			"source":          "received",
			"at":              m.ReceivedAt.UTC().Format(time.RFC3339Nano),
			"principal_id":    uint64(m.PrincipalID),
			"principal_email": resolvePrincipalEmail(m.PrincipalID),
			// disposition is the recorded-at-ingest fact (re #143): what
			// the SMTP ingest path decided when the message was
			// accepted, immutable regardless of later moves. "" means
			// not recorded (row predates migration 0090, or was written
			// by a non-SMTP-ingest path); rendered as-is, never inferred.
			"disposition": string(m.Disposition),
			// mailbox_name / is_junk are LIVE state -- the message's
			// CURRENT mailbox membership, which can differ from
			// disposition after a later move/refile. Kept for "where is
			// it now"; disposition is the "what happened at delivery"
			// field.
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
		// Only surface genuine per-message SMTP/delivery decisions. system_events
		// also carries connection-lifecycle and subsystem-operational events
		// (imapimport's IDLE armed/woke, dial connected/closed, sync round, noop
		// tick debug events; protowebhook's dispatch-outcome events) that have no
		// envelope, sender, or recipient to match a per-message search on and do
		// not belong in this per-message tracer (re #214).
		if !isMessageTraceSystemEvent(ev.Action) {
			continue
		}
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
		if qi.MessageID != "" {
			e["message_id"] = qi.MessageID
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

// isMessageTraceSystemEvent reports whether a system_events Action identifies
// a genuine per-message SMTP/delivery decision, as opposed to a connection-
// lifecycle or subsystem-operational event with no envelope, sender, or
// recipient to match a per-message search on (re #214). Message research
// (REQ-ADM-306) is an allowlist rather than a blocklist: system_events is a
// shared ring-buffer fed by several subsystems, and a blocklist keyed on one
// known-bad prefix silently leaks every other non-message action a future
// AppendSystemEvent caller introduces.
//
// The allowlist was derived by enumerating every production AppendSystemEvent
// call site:
//
//   - internal/protosmtp/deliver.go: "smtp.accept" (Subject "message:<hash>"),
//     "smtp.synthetic_accept" (Subject "recipient:<addr>", carries message_id
//     in Metadata when parseable).
//   - internal/protosmtp/ingest.go: "<source>.accept" (Subject
//     "message:<hash>"; source is the IngestSource label -- "ingest",
//     "loopback", "ses_inbound" -- so the literal actions are "ingest.accept",
//     "loopback.accept", "ses_inbound.accept").
//   - internal/protosmtp/deliver_attpol.go: "smtp.attpol", the per-recipient
//     attachment-policy outcome (Subject "recipient:<addr>").
//   - internal/directory/resolve_rcpt.go: "smtp.rcpt.resolve", the SMTP-time
//     accept/reject/defer decision for one recipient (Subject
//     "rcpt:<addr>") -- this is what covers messages rejected before
//     storage; the verdict is carried in Message/Metadata, not the Action.
//   - internal/sesinbound/sesinbound.go: "ses_inbound_received" (Subject
//     "message:<id>").
//
// Excluded as not per-message:
//
//   - internal/imapimport/worker.go's emitDebugEvent: all actions are
//     prefixed "imapimport." (idle.entered/woke, dial.connected/closed,
//     sync.round.failed/completed, noop.tick); Subject is
//     "imapimport:<account-id>", tied to a connection, not a message.
//   - internal/protowebhook/delivery.go: "hook.dispatch.dropped_no_text";
//     Subject is "webhook:<id>", a webhook-dispatch operational outcome, not
//     an SMTP/delivery decision on the message itself.
//
// (internal/protosmtp/deliver.go's "smtp.phase1_rcpt_leak" and
// "smtp.inbound_submission_queued" go to AppendAuditLog, not
// AppendSystemEvent, so they can never reach ListSystemEvents and need no
// entry here.)
func isMessageTraceSystemEvent(action string) bool {
	switch action {
	case "smtp.accept", "smtp.synthetic_accept", "smtp.attpol",
		"smtp.rcpt.resolve", "ses_inbound_received":
		return true
	}
	return strings.HasSuffix(action, ".accept")
}
