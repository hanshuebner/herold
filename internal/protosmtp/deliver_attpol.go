package protosmtp

import (
	"bytes"
	"context"
	"log/slog"

	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// applyAttPolHeaderCheck runs the REQ-FLOW-ATTPOL-01 header-only check
// against the parsed top-level MIME structure and the per-recipient
// policy rows. The function returns true when EVERY recipient with a
// resolvable principal has reject_at_data set AND the header-only
// check trips, in which case the caller emits 552 5.3.4 for the whole
// DATA and aborts. Otherwise the function returns false and may have
// mutated sess.envelope.rcpts to drop recipients that individually
// refused (their slot is removed so the per-recipient delivery loop
// downstream skips them and the audit log records refused_at_data).
//
// On the all-reject path the function emits ONE 552 reply and
// increments the per-domain refused_at_data metric for each refused
// recipient. The audit log entry carries the rejection reason, the
// recipient list, and the parsed top-level Content-Type so an operator
// can correlate refusals against sender shape.
func (sess *session) applyAttPolHeaderCheck(
	ctx context.Context,
	msg mailparse.Message,
	body []byte,
) bool {
	if len(sess.envelope.rcpts) == 0 {
		return false
	}
	headerRejected, headerReason := attpolHeaderCheck(msg)
	if !headerRejected {
		// Nothing to do at the header-only stage; per-recipient
		// post-acceptance walking happens later.
		return false
	}

	// Resolve per-recipient policies. Recipients whose policy is
	// AttPolicyAccept (default or explicit) keep their slot; recipients
	// whose policy is reject_at_data are dropped from the envelope and
	// their per-recipient audit + metric is recorded.
	type kept struct {
		rc rcptEntry
	}
	var keep []kept
	var rejectedReply string
	allRejected := true
	for _, rc := range sess.envelope.rcpts {
		row, err := sess.lookupAttPol(ctx, rc)
		if err != nil {
			sess.log.WarnContext(ctx, "attpol lookup failed; treating as accept",
				slog.String("activity", observe.ActivitySystem),
				slog.String("recipient", rc.addr),
				slog.String("err", err.Error()))
			keep = append(keep, kept{rc: rc})
			allRejected = false
			continue
		}
		if row.Policy != store.AttPolicyRejectAtData {
			keep = append(keep, kept{rc: rc})
			allRejected = false
			continue
		}
		// This recipient refuses.
		domain := attpolDomainOf(rc.addr)
		if observe.SMTPInboundAttachmentPolicyTotal != nil {
			observe.SMTPInboundAttachmentPolicyTotal.
				WithLabelValues(domain, string(attpolOutcomeRefusedAtData)).Inc()
		}
		sess.auditAttPol(ctx, rc, msg, attpolOutcomeRefusedAtData, headerReason)
		if rejectedReply == "" {
			rejectedReply = attpolRejectReply(row)
		}
	}

	if allRejected {
		if rejectedReply == "" {
			rejectedReply = attpolRejectReply(store.InboundAttachmentPolicyRow{
				Policy: store.AttPolicyRejectAtData,
			})
		}
		observe.SMTPMessagesRejectedTotal.WithLabelValues(sess.mode.String(), "policy").Inc()
		sess.writeReply(rejectedReply)
		return true
	}

	// Mixed acceptance — drop the rejected recipients from the
	// envelope so the downstream delivery loop walks only the kept
	// rows. The 552 surfaces as a per-recipient bounce DSN: see
	// emitAttPolBounce called from applyAttPolPostAcceptance for the
	// post-acceptance path; for the header-only mixed-refusal case we
	// also enqueue a 5.3.4 DSN per refused recipient so the sender
	// learns about the refusal even though the message was accepted.
	for _, rc := range sess.envelope.rcpts {
		row, err := sess.lookupAttPol(ctx, rc)
		if err != nil || row.Policy != store.AttPolicyRejectAtData {
			continue
		}
		// We already audited and metered above; emit the bounce DSN
		// now using the same diagnostic shape as the post-acceptance
		// path.
		sess.emitAttPolBounce(ctx, rc, msg, body, row, attpolDiagnosticCode(row))
	}
	newRcpts := make([]rcptEntry, 0, len(keep))
	for _, k := range keep {
		newRcpts = append(newRcpts, k.rc)
	}
	sess.envelope.rcpts = newRcpts
	return false
}

// applyAttPolPostAcceptance runs the deep MIME walker for one recipient
// after the message has been accepted at the protocol layer. Returns
// true when the recipient is refused: the message is NOT inserted into
// the recipient's mailbox, a bounce DSN is enqueued to the original
// sender, and the audit log records refused_post_acceptance. Returns
// false when the recipient's policy is accept (the default) OR the
// recipient's policy is reject_at_data and the deep walker found no
// attachment.
func (sess *session) applyAttPolPostAcceptance(
	ctx context.Context,
	rc rcptEntry,
	msg mailparse.Message,
	body []byte,
) bool {
	row, err := sess.lookupAttPol(ctx, rc)
	if err != nil {
		sess.log.WarnContext(ctx, "attpol lookup failed; treating as accept",
			slog.String("activity", observe.ActivitySystem),
			slog.String("recipient", rc.addr),
			slog.String("err", err.Error()))
		return false
	}
	if row.Policy != store.AttPolicyRejectAtData {
		return false
	}
	rejected, reason := attpolPostAcceptanceWalk(msg)
	if !rejected {
		// Recipient has reject_at_data set but the deep walker found
		// nothing; outcome is "passed" — emitted by the caller via
		// auditAttPolPassed once the deliver succeeds.
		return false
	}
	domain := attpolDomainOf(rc.addr)
	if observe.SMTPInboundAttachmentPolicyTotal != nil {
		observe.SMTPInboundAttachmentPolicyTotal.
			WithLabelValues(domain, string(attpolOutcomeRefusedPostAcceptance)).Inc()
	}
	sess.auditAttPol(ctx, rc, msg, attpolOutcomeRefusedPostAcceptance, reason)
	sess.emitAttPolBounce(ctx, rc, msg, body, row, attpolDiagnosticCode(row))
	return true
}

// auditAttPolPassed records the "passed" outcome for a recipient whose
// policy is reject_at_data and whose message survived both the
// header-only check and the deep walker (REQ-FLOW-ATTPOL-02). Recipients
// without an attpol row are not audited (would be noise).
func (sess *session) auditAttPolPassed(
	ctx context.Context,
	rc rcptEntry,
	msg mailparse.Message,
) {
	row, err := sess.lookupAttPol(ctx, rc)
	if err != nil || row.Policy != store.AttPolicyRejectAtData {
		return
	}
	domain := attpolDomainOf(rc.addr)
	if observe.SMTPInboundAttachmentPolicyTotal != nil {
		observe.SMTPInboundAttachmentPolicyTotal.
			WithLabelValues(domain, string(attpolOutcomePassed)).Inc()
	}
	sess.auditAttPol(ctx, rc, msg, attpolOutcomePassed, "")
}

// lookupAttPol resolves the effective policy for one recipient. The
// store handles the recipient > domain > default precedence; for
// synthetic recipients this falls back to the recipient's domain row
// since no per-recipient row is on file (Track C may populate the
// per-recipient row when the webhook subscription wires through).
//
// TODO(3.5c-coord): synthetic recipients currently inherit only via
// the address's domain part. Track C's webhook-target attpol field
// (when defined) overrides this and is wired in at this seam by
// keying on rc.routeTag rather than the address domain.
func (sess *session) lookupAttPol(
	ctx context.Context,
	rc rcptEntry,
) (store.InboundAttachmentPolicyRow, error) {
	return sess.srv.store.Meta().GetInboundAttachmentPolicy(ctx, rc.addr)
}

// auditAttPol writes one attpol_outcome audit-log row per recipient.
// Action is "smtp.attpol"; Subject names the recipient. Metadata
// carries the message-id (if parseable), the top-level Content-Type,
// the outcome, and the rejection reason ("" for passed). Failures to
// write the audit row are logged at warn but do not break delivery.
func (sess *session) auditAttPol(
	ctx context.Context,
	rc rcptEntry,
	msg mailparse.Message,
	outcome attpolOutcome,
	reason string,
) {
	auditTimer := observe.StartStoreOp("append_audit")
	defer auditTimer.Done()
	md := map[string]string{
		"recipient":        rc.addr,
		"sender":           sess.envelope.mailFrom,
		"attpol_outcome":   string(outcome),
		"top_content_type": msg.Body.ContentType,
		"recipient_domain": attpolDomainOf(rc.addr),
	}
	if msg.Envelope.MessageID != "" {
		md["message_id"] = msg.Envelope.MessageID
	}
	if reason != "" {
		md["reason"] = reason
	}
	if rc.synthetic {
		md["synthetic"] = "true"
	}
	subject := "recipient:" + rc.addr
	auditOutcome := store.OutcomeSuccess
	if outcome != attpolOutcomePassed {
		auditOutcome = store.OutcomeFailure
	}
	if err := sess.srv.store.Meta().AppendAuditLog(ctx, store.AuditLogEntry{
		At:         sess.srv.clk.Now(),
		ActorKind:  store.ActorSystem,
		ActorID:    "smtp",
		Action:     "smtp.attpol",
		Subject:    subject,
		RemoteAddr: sess.remoteIP,
		Outcome:    auditOutcome,
		Message:    "session=" + sess.sessID,
		Metadata:   md,
	}); err != nil {
		sess.log.WarnContext(ctx, "attpol audit append failed",
			slog.String("activity", observe.ActivityInternal),
			slog.String("err", err.Error()))
	}
}

// emitAttPolBounce enqueues a 5.3.4 DSN to the original sender via the
// configured BouncePoster. nil sender (MAIL FROM:<>) suppresses the
// bounce per RFC 3464 (no DSN-of-DSN). A nil BouncePoster collapses to
// a warn-level log line so a deployment without an outbound queue
// (e.g. a relay-only test fixture) still benefits from refusal +
// audit + metrics — the operator just does not get the postmaster
// notification.
func (sess *session) emitAttPolBounce(
	ctx context.Context,
	rc rcptEntry,
	msg mailparse.Message,
	body []byte,
	row store.InboundAttachmentPolicyRow,
	diagnostic string,
) {
	_ = row
	if sess.envelope.mailFrom == "" {
		sess.log.InfoContext(ctx, "attpol bounce suppressed (null sender)",
			slog.String("activity", observe.ActivitySystem),
			slog.String("recipient", rc.addr))
		return
	}
	if sess.srv.bouncePoster == nil {
		sess.log.WarnContext(ctx, "attpol bounce skipped (no BouncePoster wired)",
			slog.String("activity", observe.ActivitySystem),
			slog.String("recipient", rc.addr))
		return
	}
	in := BounceInput{
		MailFrom:        sess.envelope.mailFrom,
		FinalRcpt:       rc.addr,
		OriginalRcpt:    rc.addr,
		MessageID:       msg.Envelope.MessageID,
		DiagnosticCode:  diagnostic,
		StatusCode:      attpolEnhancedStatus,
		OriginalHeaders: extractHeaderBlock(body),
	}
	if err := sess.srv.bouncePoster.PostBounce(ctx, in); err != nil {
		sess.log.WarnContext(ctx, "attpol bounce enqueue failed",
			slog.String("activity", observe.ActivitySystem),
			slog.String("recipient", rc.addr),
			slog.String("err", err.Error()))
	}
}

// extractHeaderBlock returns the raw header section of body (everything
// before the first empty line). Returns nil when no header/body
// boundary is found. Used to populate the Original-Headers part of the
// generated DSN.
func extractHeaderBlock(body []byte) []byte {
	if i := bytes.Index(body, []byte("\r\n\r\n")); i >= 0 {
		return append([]byte(nil), body[:i+2]...)
	}
	if i := bytes.Index(body, []byte("\n\n")); i >= 0 {
		return append([]byte(nil), body[:i+1]...)
	}
	return nil
}
