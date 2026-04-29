package protosend

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/auth/sendpolicy"
	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/queue"
	"github.com/hanshuebner/herold/internal/store"
)

// handleSend implements POST /api/v1/mail/send (REQ-SEND-01).
func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	var req sendRequest
	if !decodeJSONBody(w, r, s.opts.MaxBodySize, &req) {
		return
	}
	resp, problem := s.processSend(r.Context(), r, req)
	if problem != nil {
		s.writeProblemDoc(w, problem)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleSendRaw implements POST /api/v1/mail/send-raw (REQ-SEND-02).
func (s *Server) handleSendRaw(w http.ResponseWriter, r *http.Request) {
	var req sendRawRequest
	if !decodeJSONBody(w, r, s.opts.MaxBodySize, &req) {
		return
	}
	resp, problem := s.processSendRaw(r.Context(), r, req)
	if problem != nil {
		s.writeProblemDoc(w, problem)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleSendBatch implements POST /api/v1/mail/send-batch (REQ-SEND-03).
// Per-item failures are returned as RFC 7807 problem details inside the
// 200 OK response; only request-wide failures (bad JSON, too many items)
// produce a non-2xx status.
func (s *Server) handleSendBatch(w http.ResponseWriter, r *http.Request) {
	var items []batchItem
	if !decodeJSONBody(w, r, s.opts.MaxBodySize, &items) {
		return
	}
	if len(items) == 0 {
		writeProblem(w, r, http.StatusBadRequest, "validation-failed",
			"batch must contain at least one item", "")
		return
	}
	if len(items) > s.opts.MaxBatchItems {
		writeProblem(w, r, http.StatusBadRequest, "validation-failed",
			fmt.Sprintf("batch exceeds %d items", s.opts.MaxBatchItems), "")
		return
	}
	resp := batchResponse{Items: make([]batchEntry, len(items))}
	for i, it := range items {
		switch {
		case it.Send != nil && it.SendRaw != nil:
			resp.Items[i].Problem = newProblem(r, http.StatusBadRequest,
				"validation-failed", "exactly one of send / sendRaw required", "")
		case it.Send != nil:
			out, problem := s.processSend(r.Context(), r, *it.Send)
			if problem != nil {
				resp.Items[i].Problem = problem
				continue
			}
			resp.Items[i].MessageID = out.MessageID
			resp.Items[i].SubmissionID = out.SubmissionID
		case it.SendRaw != nil:
			out, problem := s.processSendRaw(r.Context(), r, *it.SendRaw)
			if problem != nil {
				resp.Items[i].Problem = problem
				continue
			}
			resp.Items[i].MessageID = out.MessageID
			resp.Items[i].SubmissionID = out.SubmissionID
		default:
			resp.Items[i].Problem = newProblem(r, http.StatusBadRequest,
				"validation-failed", "exactly one of send / sendRaw required", "")
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleQuota implements GET /api/v1/mail/quota (REQ-SEND-10/04).
//
// QuotaBytes on the principal is reused as the per-day send count cap
// (an explicit quota field is a future store schema migration). Zero
// means "unlimited"; we surface -1 so consumers can distinguish
// "limited and 0 remaining" from "unlimited".
func (s *Server) handleQuota(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	key, _ := apiKeyFrom(r.Context())
	used := s.dailyUsed(p.ID)
	limit := p.QuotaBytes // see comment above; reused as per-day count.
	remain := int64(-1)
	if limit > 0 {
		remain = limit - used
		if remain < 0 {
			remain = 0
		}
	}
	rlKey := fmt.Sprintf("apikey:%d", key.ID)
	rlCount := s.rl.count(rlKey)
	resp := quotaResponse{
		DailyLimit:      limit,
		DailyUsed:       used,
		DailyRemaining:  remain,
		PerMinuteLimit:  s.opts.RateLimitPerKey,
		PerMinuteUsed:   rlCount,
		PerMinuteRemain: max0(s.opts.RateLimitPerKey - rlCount),
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleStats implements GET /api/v1/mail/stats (REQ-SEND-20/05).
//
// Aggregates over the queue items associated with the calling principal
// in the last 24 hours. Bounce counts come from the failed-state row
// count (a richer breakdown by SMTP enhanced-code is a future wave).
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	rows, err := s.store.Meta().ListQueueItems(r.Context(), store.QueueFilter{
		PrincipalID: p.ID,
		Limit:       1000,
	})
	if err != nil {
		s.loggerFrom(r.Context()).Error("protosend.stats.list_queue",
			"activity", observe.ActivityInternal, "err", err)
		writeProblem(w, r, http.StatusInternalServerError, "internal-error",
			"could not read queue", "")
		return
	}
	cutoff := s.clk.Now().Add(-24 * time.Hour)
	resp := statsResponse{WindowSeconds: int64((24 * time.Hour).Seconds())}
	for _, q := range rows {
		if q.CreatedAt.Before(cutoff) {
			continue
		}
		resp.Submitted++
		switch q.State {
		case store.QueueStateDone:
			resp.Delivered++
		case store.QueueStateFailed:
			resp.Failed++
			resp.Bounced++
		case store.QueueStateInflight, store.QueueStateQueued, store.QueueStateDeferred:
			resp.InFlight++
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// processSend is the shared core of /send and /send-batch send-items.
// Returns either a sendResponse (200) or a problem document (caller
// translates to status). The function never panics; validation errors
// surface as 4xx problems and queue errors as 5xx unless they map to
// the idempotency-replay path.
func (s *Server) processSend(ctx context.Context, r *http.Request, req sendRequest) (*sendResponse, *problemDoc) {
	if problem := s.validateSendRequest(r, req); problem != nil {
		return nil, problem
	}
	p, _ := principalFrom(ctx)
	key, _ := apiKeyFrom(ctx)
	if problem := s.checkFromPolicy(ctx, r, p, &key, req.Source); problem != nil {
		return nil, problem
	}
	built, err := buildStructuredMessage(req, s.opts.Hostname, s.clk.Now())
	if err != nil {
		s.loggerFrom(ctx).Error("protosend.build_message",
			"activity", observe.ActivityInternal, "err", err)
		return nil, newProblem(r, http.StatusInternalServerError, "internal-error",
			"could not build message", "")
	}
	rcpts := allRecipients(req.Destination)
	idempKey := composeIdempotencyKey(key.ID, req.IdempotencyKey)
	signingDomain := domainOf(req.Source)
	envID, err := s.queue.Submit(ctx, queue.Submission{
		PrincipalID:    pidPtr(p.ID),
		MailFrom:       req.Source,
		Recipients:     rcpts,
		Body:           bytes.NewReader(built.bytes),
		IdempotencyKey: idempKey,
		Sign:           true,
		SigningDomain:  signingDomain,
		DSNNotify:      parseDSNNotify(req.DSNNotify),
	})
	if err != nil && !errors.Is(err, queue.ErrConflict) {
		s.loggerFrom(ctx).Error("protosend.queue.submit",
			"activity", observe.ActivityInternal, "err", err)
		return nil, newProblem(r, http.StatusInternalServerError, "queue-error",
			"could not submit to queue", err.Error())
	}
	// On ErrConflict the existing envelope id is returned; treat as
	// idempotent-success.
	idempotent := errors.Is(err, queue.ErrConflict)
	s.noteSubmitted(p.ID, int64(len(rcpts)))
	s.appendAudit(ctx, "send.api.submit", built.messageID, store.OutcomeSuccess,
		"", map[string]string{
			"submission_id": string(envID),
			"recipient_n":   strconv.Itoa(len(rcpts)),
			"tags":          formatTagsList(req.Tags),
			"config_set":    req.ConfigurationSet,
			"idempotent":    boolStr(idempotent),
		})
	s.loggerFrom(ctx).InfoContext(ctx, "protosend.send.accepted",
		"activity", observe.ActivityUser,
		"api_key_id", func() uint64 {
			if k, ok := apiKeyFrom(ctx); ok {
				return uint64(k.ID)
			}
			return 0
		}(),
		"msg_id", built.messageID,
		"submission_id", string(envID),
		"recipient_count", len(rcpts),
		"size_bytes", len(built.bytes),
		"idempotent", idempotent,
	)
	return &sendResponse{
		MessageID:    "<" + built.messageID + ">",
		SubmissionID: string(envID),
	}, nil
}

// processSendRaw is the shared core of /send-raw and batch-raw items.
func (s *Server) processSendRaw(ctx context.Context, r *http.Request, req sendRawRequest) (*sendResponse, *problemDoc) {
	if len(req.Destinations) == 0 {
		return nil, newProblem(r, http.StatusBadRequest, "validation-failed",
			"destinations is required", "")
	}
	if len(req.Destinations) > s.opts.MaxRecipients {
		return nil, newProblem(r, http.StatusBadRequest, "validation-failed",
			fmt.Sprintf("destinations exceeds %d", s.opts.MaxRecipients), "")
	}
	raw, err := base64.StdEncoding.DecodeString(req.RawMessage)
	if err != nil {
		return nil, newProblem(r, http.StatusBadRequest, "invalid-body",
			"rawMessage is not valid base64", err.Error())
	}
	if int64(len(raw)) > s.opts.MaxBodySize {
		return nil, newProblem(r, http.StatusRequestEntityTooLarge, "payload-too-large",
			"raw message exceeds size cap", "")
	}
	// Parse for sanity (size + structural caps). We don't keep the
	// parsed value; mailparse.Parse returns an error on cap breach.
	if _, err := mailparse.Parse(bytes.NewReader(raw), mailparse.NewParseOptions()); err != nil {
		return nil, newProblem(r, http.StatusBadRequest, "invalid-message",
			"raw message failed sanity parse", err.Error())
	}
	// Determine source for ownership check + signing. The first parsed
	// From header wins; reject when absent (we can't sign anonymously).
	parsed, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, newProblem(r, http.StatusBadRequest, "invalid-message",
			"could not read raw message", err.Error())
	}
	fromHdr := parsed.Header.Get("From")
	source := extractFirstAddress(fromHdr)
	if source == "" {
		return nil, newProblem(r, http.StatusBadRequest, "validation-failed",
			"raw message missing From: address", "")
	}
	p, _ := principalFrom(ctx)
	key, _ := apiKeyFrom(ctx)
	if problem := s.checkFromPolicy(ctx, r, p, &key, source); problem != nil {
		return nil, problem
	}
	final, msgID, err := inspectRawMessage(raw, s.opts.Hostname, source, s.clk.Now())
	if err != nil {
		return nil, newProblem(r, http.StatusBadRequest, "invalid-message",
			err.Error(), "")
	}
	idempKey := composeIdempotencyKey(key.ID, req.IdempotencyKey)
	envID, err := s.queue.Submit(ctx, queue.Submission{
		PrincipalID:    pidPtr(p.ID),
		MailFrom:       source,
		Recipients:     req.Destinations,
		Body:           bytes.NewReader(final),
		IdempotencyKey: idempKey,
		Sign:           true,
		SigningDomain:  domainOf(source),
		DSNNotify:      parseDSNNotify(req.DSNNotify),
	})
	if err != nil && !errors.Is(err, queue.ErrConflict) {
		s.loggerFrom(ctx).Error("protosend.queue.submit_raw",
			"activity", observe.ActivityInternal, "err", err)
		return nil, newProblem(r, http.StatusInternalServerError, "queue-error",
			"could not submit to queue", err.Error())
	}
	idempotent := errors.Is(err, queue.ErrConflict)
	s.noteSubmitted(p.ID, int64(len(req.Destinations)))
	s.appendAudit(ctx, "send.api.submit", msgID, store.OutcomeSuccess, "",
		map[string]string{
			"submission_id": string(envID),
			"recipient_n":   strconv.Itoa(len(req.Destinations)),
			"raw":           "true",
			"config_set":    req.ConfigurationSet,
			"idempotent":    boolStr(idempotent),
		})
	s.loggerFrom(ctx).InfoContext(ctx, "protosend.send_raw.accepted",
		"activity", observe.ActivityUser,
		"api_key_id", func() uint64 {
			if k, ok := apiKeyFrom(ctx); ok {
				return uint64(k.ID)
			}
			return 0
		}(),
		"msg_id", msgID,
		"submission_id", string(envID),
		"recipient_count", len(req.Destinations),
		"idempotent", idempotent,
	)
	return &sendResponse{
		MessageID:    "<" + msgID + ">",
		SubmissionID: string(envID),
	}, nil
}

// validateSendRequest checks the structured form's mandatory fields.
func (s *Server) validateSendRequest(r *http.Request, req sendRequest) *problemDoc {
	if strings.TrimSpace(req.Source) == "" {
		return newProblem(r, http.StatusBadRequest, "validation-failed",
			"source is required", "")
	}
	if _, err := mail.ParseAddress(req.Source); err != nil {
		return newProblem(r, http.StatusBadRequest, "validation-failed",
			"source is not a valid email address", err.Error())
	}
	if strings.TrimSpace(req.Message.Subject) == "" {
		return newProblem(r, http.StatusBadRequest, "validation-failed",
			"message.subject is required", "")
	}
	if req.Message.Body.Text == "" && req.Message.Body.HTML == "" {
		return newProblem(r, http.StatusBadRequest, "validation-failed",
			"at least one of message.body.text or message.body.html is required", "")
	}
	rcpts := allRecipients(req.Destination)
	if len(rcpts) == 0 {
		return newProblem(r, http.StatusBadRequest, "validation-failed",
			"destination must contain at least one address", "")
	}
	if len(rcpts) > s.opts.MaxRecipients {
		return newProblem(r, http.StatusBadRequest, "validation-failed",
			fmt.Sprintf("destination exceeds %d recipients", s.opts.MaxRecipients), "")
	}
	for _, rcpt := range rcpts {
		if _, err := mail.ParseAddress(rcpt); err != nil {
			return newProblem(r, http.StatusBadRequest, "validation-failed",
				"recipient is not a valid email address: "+rcpt, err.Error())
		}
	}
	return nil
}

// checkFromPolicy enforces REQ-SEND-12 / REQ-FLOW-41: the from address
// must be owned by the authenticated principal.  On denial it writes the
// audit log, increments the metric, and returns a 403 problem document.
// key may be nil when the session was not authenticated via an API key.
func (s *Server) checkFromPolicy(ctx context.Context, r *http.Request, p store.Principal, key *store.APIKey, from string) *problemDoc {
	observe.RegisterSendPolicyMetrics()
	chk := sendpolicy.StoreChecker{Meta: s.store.Meta()}
	dec, err := sendpolicy.CheckFrom(ctx, chk, p, key, strings.ToLower(from))
	if err != nil {
		s.loggerFrom(ctx).Error("protosend.checkfrom.store_error",
			"activity", observe.ActivityInternal, "err", err, "from", from)
		return newProblem(r, http.StatusInternalServerError, "internal-error",
			"from-address ownership check failed", err.Error())
	}
	if !dec.Allowed {
		observe.SendForbiddenFromTotal.WithLabelValues(string(sendpolicy.SourceHTTP)).Inc()
		s.appendAudit(ctx, "mail.send.forbidden_from", from, store.OutcomeFailure,
			"from address not owned by principal",
			map[string]string{"from": from, "source": string(sendpolicy.SourceHTTP), "reason": string(dec.Reason)})
		s.loggerFrom(ctx).WarnContext(ctx, "protosend.send.rejected_forbidden_from",
			"activity", observe.ActivityUser,
			"from", from,
			"reason", string(dec.Reason))
		return newProblem(r, http.StatusForbidden, "forbidden-from",
			"from address is not owned by the authenticated principal", from)
	}
	return nil
}

// composeIdempotencyKey ties the client-supplied key to the API key id
// so two principals using the same UUID don't collide.
func composeIdempotencyKey(keyID store.APIKeyID, clientKey string) string {
	if clientKey == "" {
		return ""
	}
	return fmt.Sprintf("%d:%s", keyID, clientKey)
}

// allRecipients flattens to/cc/bcc into one slice in declaration order.
func allRecipients(d destination) []string {
	n := len(d.ToAddresses) + len(d.CCAddresses) + len(d.BCCAddresses)
	if n == 0 {
		return nil
	}
	out := make([]string, 0, n)
	out = append(out, d.ToAddresses...)
	out = append(out, d.CCAddresses...)
	out = append(out, d.BCCAddresses...)
	return out
}

func domainOf(addr string) string {
	at := strings.LastIndexByte(addr, '@')
	if at < 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(addr[at+1:]))
}

func extractFirstAddress(s string) string {
	addrs, err := mail.ParseAddressList(s)
	if err != nil || len(addrs) == 0 {
		// Fallback: try ParseAddress on the trimmed input.
		if a, err2 := mail.ParseAddress(strings.TrimSpace(s)); err2 == nil {
			return a.Address
		}
		return ""
	}
	return addrs[0].Address
}

func parseDSNNotify(in []string) store.DSNNotifyFlags {
	var flags store.DSNNotifyFlags
	for _, t := range in {
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "success":
			flags |= store.DSNNotifySuccess
		case "failure":
			flags |= store.DSNNotifyFailure
		case "delay":
			flags |= store.DSNNotifyDelay
		case "never":
			flags |= store.DSNNotifyNever
		}
	}
	return flags
}

func formatTagsList(tags []messageTag) string {
	if len(tags) == 0 {
		return ""
	}
	var b strings.Builder
	for i, t := range tags {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(t.Name)
		b.WriteByte('=')
		b.WriteString(t.Value)
	}
	return b.String()
}

func pidPtr(id store.PrincipalID) *store.PrincipalID { return &id }

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// writeProblemDoc writes an existing problem document with the right
// status and content type.
func (s *Server) writeProblemDoc(w http.ResponseWriter, p *problemDoc) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}

// appendAudit writes a send-API audit entry.
func (s *Server) appendAudit(ctx context.Context, action, subject string,
	outcome store.AuditOutcome, message string, metadata map[string]string,
) {
	actorKind := store.ActorAPIKey
	actorID := "system"
	if k, ok := apiKeyFrom(ctx); ok {
		actorID = strconv.FormatUint(uint64(k.ID), 10)
	}
	if metadata == nil {
		metadata = map[string]string{}
	}
	if rid := requestID(ctx); rid != "" {
		metadata["request_id"] = rid
	}
	remote := ""
	if v, ok := ctx.Value(ctxKeyRemoteAddr).(string); ok {
		remote = v
	}
	entry := store.AuditLogEntry{
		At:         s.clk.Now(),
		ActorKind:  actorKind,
		ActorID:    actorID,
		Action:     action,
		Subject:    subject,
		RemoteAddr: remote,
		Outcome:    outcome,
		Message:    message,
		Metadata:   metadata,
	}
	if err := s.store.Meta().AppendAuditLog(ctx, entry); err != nil {
		s.loggerFrom(ctx).Warn("protosend.audit.append_failed",
			"activity", observe.ActivityInternal,
			"err", err, "action", action, "subject", subject)
	}
}
