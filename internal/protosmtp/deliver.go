package protosmtp

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"

	"github.com/hanshuebner/herold/internal/categorise"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/queue"
	"github.com/hanshuebner/herold/internal/sieve"
	"github.com/hanshuebner/herold/internal/spam"
	"github.com/hanshuebner/herold/internal/store"
)

// finishMessage is called by DATA / BDAT LAST. body is the CRLF-
// normalised message content without the dot-stuffing. It runs the
// delivery pipeline and emits the final 250 or transient/perm error
// reply. On return the caller flushes and the command loop continues.
func (sess *session) finishMessage(body []byte) {
	ctx := sess.ctx
	// Build the full stored message: prepend Received + (for relay-in)
	// Authentication-Results. We delay AR computation until after the
	// verifiers run below so it lands on the stored blob and is visible
	// to the sieve pipeline.
	authResults, _ := sess.runMailAuth(ctx, body)
	// Spam classification.
	msg, perr := mailparse.Parse(bytes.NewReader(body), mailparse.NewParseOptions())
	if perr != nil {
		sess.log.InfoContext(ctx, "smtp data: parse failed",
			slog.String("activity", observe.ActivitySystem),
			slog.String("err", perr.Error()))
		observe.SMTPMessagesRejectedTotal.WithLabelValues(sess.mode.String(), "policy").Inc()
		sess.writeReply("554 5.6.0 message parse failed: " + perr.Error())
		sess.resetEnvelope()
		return
	}

	// REQ-FLOW-ATTPOL-01: header-only inbound attachment policy check.
	// Inspects the parsed top-level MIME structure between DATA accept
	// and 250 OK; refuses with 552 5.3.4 when ANY recipient has
	// inbound_attachment_policy = reject_at_data AND the top-level
	// shape carries an attachment.
	if sess.applyAttPolHeaderCheck(ctx, msg, body) {
		// All recipients refused at the message-wide level: reply
		// 552, drop, and return.
		sess.resetEnvelope()
		return
	}

	classification := sess.classify(ctx, msg, authResults)
	listenerLabel := sess.mode.String()
	// Track inbound DATA bytes (best-effort; counts the body bytes the
	// session received, not framing or commands).
	observe.SMTPDataBytesTotal.WithLabelValues(listenerLabel, "in").Add(float64(len(body)))
	// Stamp the spam verdict onto authResults so the renderer surfaces
	// it as an "x-herold-spam=<verdict>" method on the
	// Authentication-Results header (RFC 8601 §2.7 experimental method
	// prefix). Operators inspecting the stored message can therefore see
	// what the classifier decided. We only attach a SpamResult when the
	// classifier was wired up — a Verdict of Unclassified with no engine
	// name indicates "did not run" and is omitted.
	if sess.mode == RelayIn && sess.srv.spam != nil {
		authResults.Spam = &mailauth.SpamResult{
			Verdict: classification.Verdict.String(),
			Score:   classification.Score,
			Engine:  sess.srv.spamPlug,
		}
	}
	// Re-render with the spam method token included.
	authStr := ""
	if sess.mode == RelayIn {
		authStr = renderAuthResults(sess.srv.opts.AuthservID, authResults)
		authResults.Raw = authStr
	}
	// Assemble the raw message bytes we will store (prepend Received
	// and Authentication-Results headers).
	finalBytes := sess.assembleStoredBytes(body, authStr)

	// Persist the blob once; every recipient × mailbox refers to the
	// same BlobRef.
	blobRef, err := sess.srv.store.Blobs().Put(ctx, bytes.NewReader(finalBytes))
	if err != nil {
		sess.log.ErrorContext(ctx, "smtp data: blob put failed",
			slog.String("activity", observe.ActivitySystem),
			slog.String("err", err.Error()))
		sess.writeReply("451 4.3.0 temporary storage failure")
		sess.resetEnvelope()
		return
	}

	// Deliver per recipient.
	var anyOK bool
	var messageID string
	if id := msg.Envelope.MessageID; id != "" {
		messageID = id
	}
	for _, rc := range sess.envelope.rcpts {
		if rc.synthetic {
			// REQ-DIR-RCPT-07: synthetic recipient. Skip mailbox insert,
			// per-recipient Sieve, and (unless opted in) spam
			// classification. The message lands on the inbound webhook
			// path only.
			//
			// REQ-FLOW-ATTPOL-02: synthetic recipients still get the
			// post-acceptance walker so a webhook intake configured
			// for reject_at_data refuses nested attachments before
			// the dispatcher would otherwise hand them off. Synthetic
			// recipients' policy resolves through the recipient's
			// domain row (the matched webhook target's configured
			// domain); a future per-webhook-target attpol field
			// (REQ-HOOK-02 follow-up) can override per-synthetic-
			// target by writing a per-recipient row keyed on rc.addr.
			if sess.applyAttPolPostAcceptance(ctx, rc, msg, finalBytes) {
				anyOK = true
				continue
			}
			sess.auditAttPolPassed(ctx, rc, msg)
			sess.dispatchSynthetic(ctx, rc, msg, finalBytes, blobRef)
			anyOK = true
			continue
		}
		if rc.principalID == 0 {
			// Submission listener: non-local recipient is the normal
			// outbound case (Wave 3.1.6). Authenticated MUA-clients
			// hand off to the outbound queue here; the queue worker
			// dials the smart-host / MX. Inbound listener: non-local
			// recipient should have been refused at RCPT TO time, so
			// reaching this branch is a defensive log + continue.
			switch sess.mode {
			case SubmissionSTARTTLS, SubmissionImplicitTLS:
				if ok := sess.queueOutboundFromSubmission(ctx, rc, msg, finalBytes); ok {
					anyOK = true
				}
			default:
				sess.log.WarnContext(ctx, "phase1_rcpt_leak: non-local recipient on inbound listener slipped past RCPT TO",
					slog.String("activity", observe.ActivityInternal),
					slog.String("recipient", rc.addr),
					slog.String("mode", sess.mode.String()))
				_ = sess.srv.store.Meta().AppendAuditLog(ctx, store.AuditLogEntry{
					At:         sess.srv.clk.Now(),
					ActorKind:  store.ActorSystem,
					ActorID:    "smtp",
					Action:     "smtp.phase1_rcpt_leak",
					Subject:    "recipient:" + rc.addr,
					RemoteAddr: sess.remoteIP,
					Outcome:    store.OutcomeFailure,
					Message:    "non-local recipient on inbound listener slipped past RCPT TO",
					Metadata: map[string]string{
						"session_id": sess.sessID,
						"mode":       sess.mode.String(),
					},
				})
			}
			continue
		}
		// REQ-FLOW-ATTPOL-02: post-acceptance MIME walker. If the
		// recipient's policy is reject_at_data and the deep walker
		// catches an attachment that the header-only check missed
		// (e.g. nested under multipart/alternative), enqueue a
		// bounce DSN to the original sender and skip delivery for
		// this recipient. The message-wide DATA accept stands.
		if sess.applyAttPolPostAcceptance(ctx, rc, msg, finalBytes) {
			anyOK = true
			continue
		}
		ok, derr := sess.deliverOne(ctx, rc, finalBytes, blobRef, msg, authResults, classification)
		if derr != nil {
			sess.log.ErrorContext(ctx, "smtp delivery failed",
				slog.String("activity", observe.ActivitySystem),
				slog.String("recipient", rc.addr),
				slog.String("err", derr.Error()))
		}
		if ok {
			// Audit "passed" outcome for recipients with
			// reject_at_data set whose message survived both the
			// header-only check and the deep walker.
			sess.auditAttPolPassed(ctx, rc, msg)
			anyOK = true
		}
	}
	// Audit the accept (REQ-FLOW-03 durability).
	auditTimer := observe.StartStoreOp("append_audit")
	_ = sess.srv.store.Meta().AppendAuditLog(ctx, store.AuditLogEntry{
		At:         sess.srv.clk.Now(),
		ActorKind:  store.ActorSystem,
		ActorID:    "smtp",
		Action:     "smtp.accept",
		Subject:    "message:" + blobRef.Hash,
		RemoteAddr: sess.remoteIP,
		Outcome:    store.OutcomeSuccess,
		Message:    fmt.Sprintf("session=%s recipients=%d size=%d", sess.sessID, len(sess.envelope.rcpts), len(finalBytes)),
		Metadata: map[string]string{
			"hostname":  sess.srv.opts.Hostname,
			"authserv":  sess.srv.opts.AuthservID,
			"mode":      sess.mode.String(),
			"auth":      boolStr(sess.authenticated),
			"spam":      classification.Verdict.String(),
			"mail_from": sess.envelope.mailFrom,
		},
	})
	auditTimer.Done()
	if !anyOK {
		// Every recipient failed. Emit a transient error so the remote
		// can retry; this is rare in practice (store crash) but keeps
		// the contract "no silent drops".
		sess.writeReply("451 4.3.0 delivery failed for every recipient")
		sess.resetEnvelope()
		return
	}
	reply := "250 2.6.0 message accepted"
	if messageID != "" {
		reply = fmt.Sprintf("250 2.6.0 %s accepted", messageID)
	}
	observe.SMTPMessagesAcceptedTotal.WithLabelValues(listenerLabel).Inc()
	// Per REQ-OPS-86d: DATA accepted on submission → user/info;
	// relay/inbound → system/info.
	dataActivity := observe.ActivitySystem
	if sess.mode == SubmissionSTARTTLS || sess.mode == SubmissionImplicitTLS {
		dataActivity = observe.ActivityUser
	}
	sess.log.InfoContext(ctx, "smtp data accepted",
		slog.String("activity", dataActivity),
		slog.String("message_id", messageID),
		slog.Int("size_bytes", len(finalBytes)),
		slog.Int("recipients", len(sess.envelope.rcpts)),
		slog.Uint64("principal_id", uint64(sess.authPrincipal)))
	sess.writeReply(reply)
	sess.resetEnvelope()
}

// deliverOne handles the per-recipient filter + persist path.
// Returns (ok, err) — ok indicates the recipient's message reached a
// mailbox or was intentionally discarded / redirected. err carries a
// non-fatal diagnostic for the log.
func (sess *session) deliverOne(
	ctx context.Context,
	rc rcptEntry,
	finalBytes []byte,
	blobRef store.BlobRef,
	msg mailparse.Message,
	authResults mailauth.AuthResults,
	classification spam.Classification,
) (bool, error) {
	// Run sieve.
	outcome, serr := sess.runSieve(ctx, rc, msg, authResults, classification)
	if serr != nil {
		// Per REQ-FLOW-22: fatal sieve error on a user script MUST NOT
		// lose the message; fall back to keep-to-Inbox.
		sess.log.WarnContext(ctx, "sieve evaluation failed; falling back to INBOX",
			slog.String("activity", observe.ActivitySystem),
			slog.String("recipient", rc.addr),
			slog.String("err", serr.Error()))
		outcome = sieve.Outcome{ImplicitKeep: true}
	}
	// Decide target mailboxes.
	targets, discarded, rejected, rejectReason := resolveSieveTargets(outcome)

	if rejected {
		// RFC 5429 reject: in a relay-in context, emit the message to
		// the operator log only. We do NOT bounce here because Phase 2
		// owns outbound; we instead treat the message as accepted-
		// then-dropped per REQ-FLOW-22 relaxation.
		sess.log.InfoContext(ctx, "sieve reject",
			slog.String("activity", observe.ActivitySystem),
			slog.String("recipient", rc.addr),
			slog.String("reason", rejectReason))
		return true, nil
	}
	if discarded && len(targets) == 0 {
		// Successfully consumed.
		return true, nil
	}
	if len(targets) == 0 {
		targets = []string{"INBOX"}
	}

	// REQ-FLOW-104..107: try to consume the message as an inbound
	// reaction email.  Runs AFTER spam classification (already in
	// classification) and AFTER sieve (so the junk target is already
	// resolved before this check).  REQ-FLOW-108: spam falls through.
	// The check only runs for local principals (principalID != 0).
	if rc.principalID != 0 {
		if sess.tryConsumeReaction(ctx, rc.principalID, msg, classification) {
			return true, nil
		}
	}

	// Resolve / create each target mailbox and insert the message.
	for _, mbName := range targets {
		mb, err := sess.ensureMailbox(ctx, rc.principalID, mbName)
		if err != nil {
			return false, fmt.Errorf("ensure mailbox %q: %w", mbName, err)
		}
		storeMsg := store.Message{
			PrincipalID:  rc.principalID,
			Size:         int64(len(finalBytes)),
			Blob:         blobRef,
			ReceivedAt:   sess.srv.clk.Now(),
			InternalDate: sess.srv.clk.Now(),
			Envelope:     envelopeFromParsed(msg),
		}
		// Propagate sieve-added flags onto system flags where possible.
		msgFlags := sieveFlagsFromOutcome(outcome)
		// REQ-FILT-200: only categorise messages destined for the
		// inbox, after Sieve fileinto + spam classification. Spam
		// suppresses the call. Categorisation NEVER blocks delivery
		// (REQ-FILT-230); a failure here returns "" and we proceed.
		var msgKeywords []string
		var catResult categorise.CategorisationResult
		if sess.srv.categorise != nil &&
			classification.Verdict != spam.Spam &&
			strings.EqualFold(mb.Name, "INBOX") {
			catResult, _ = sess.srv.categorise.CategoriseRich(
				ctx, rc.principalID, msg, &authResults, classification.Verdict)
			if catResult.Category != "" {
				msgKeywords = append(msgKeywords, "$category-"+catResult.Category)
			}
		}
		target := store.MessageMailbox{
			MailboxID: mb.ID,
			Flags:     msgFlags,
			Keywords:  msgKeywords,
		}
		insertTimer := observe.StartStoreOp("insert_message")
		_, _, ierr := sess.srv.store.Meta().InsertMessage(ctx, storeMsg, []store.MessageMailbox{target})
		insertTimer.Done()
		if ierr != nil {
			if errors.Is(ierr, store.ErrQuotaExceeded) {
				sess.log.InfoContext(ctx, "delivery over quota",
					slog.String("activity", observe.ActivitySystem),
					slog.String("recipient", rc.addr))
				// REQ-FLOW-11 default behaviour: defer (4.2.2). We
				// already emitted 354; re-emit 452 for the whole
				// message (simpler: return failure).
				return false, ierr
			}
			return false, ierr
		}
		// Persist the LLM classification record for transparency (REQ-FILT-66 /
		// REQ-FILT-216 / G14). Only when at least one LLM was invoked.
		// The record is fire-and-forget: a failure here is logged but never
		// blocks delivery (REQ-FILT-230 / REQ-FILT-40).
		if rc.principalID != 0 && (classification.Verdict != spam.Unclassified || catResult.PromptApplied != "") {
			sess.persistLLMRecord(ctx, rc.principalID, storeMsg.Envelope.MessageID, msg, authResults, classification, catResult)
		}

		// Seed-on-receive (REQ-MAIL-11h): record the From address in the
		// principal's SeenAddress history when the sender is not spam,
		// not a mailing list, not an identity or contact of the principal,
		// and the principal has seen_addresses_enabled = true.
		// Fire-and-forget — seeding never blocks delivery.
		if rc.principalID != 0 && classification.Verdict != spam.Spam {
			go seedFromAddress(context.Background(), sess.srv.store, sess.srv.log,
				rc.principalID, sess.envelope.mailFrom, msg)
		}
	}
	return true, nil
}

// persistLLMRecord stores the LLM classification transparency record for a
// newly-delivered message. It is fire-and-forget: any error is logged at
// warn level but never propagated to the delivery caller.
func (sess *session) persistLLMRecord(
	ctx context.Context,
	principalID store.PrincipalID,
	msgIDHeader string,
	msg mailparse.Message,
	authResults mailauth.AuthResults,
	classification spam.Classification,
	catResult categorise.CategorisationResult,
) {
	// Retrieve the message ID by Message-ID header lookup. This is the only
	// way to get the store-assigned MessageID without changing InsertMessage's
	// return type. The overhead is one indexed lookup per delivered message
	// when LLM classification ran.
	m, err := sess.srv.store.Meta().GetMessageByMessageIDHeader(ctx, principalID, msgIDHeader)
	if err != nil {
		sess.log.WarnContext(ctx, "llm-transparency: lookup message ID for record",
			slog.String("activity", observe.ActivityInternal),
			slog.String("msg_id_header", msgIDHeader),
			slog.String("err", err.Error()))
		return
	}
	rec := store.LLMClassificationRecord{
		MessageID:   m.ID,
		PrincipalID: principalID,
	}
	// Spam sub-record.
	if classification.Verdict != spam.Unclassified {
		v := classification.Verdict.String()
		rec.SpamVerdict = &v
		score := classification.Score
		rec.SpamConfidence = &score
		if raw := classification.RawResponse; raw != nil {
			if reason, ok := raw["reason"].(string); ok && reason != "" {
				rec.SpamReason = &reason
			}
			if mdl, ok := raw["model"].(string); ok && mdl != "" {
				rec.SpamModel = &mdl
			}
		}
		// Build the user-visible prompt-as-applied from the spam.Request.
		// The spam.Request is the structured context sent to the plugin —
		// this is the content visible to users. The plugin's system prompt
		// is not in herold's scope; SpamPolicy.SystemPromptOverride is
		// the per-account user-editable text returned by LLMTransparency/get.
		req := spam.BuildRequest(msg, &authResults)
		if b, jerr := req.Canonical(); jerr == nil {
			s := string(b)
			rec.SpamPromptApplied = &s
		}
		t := sess.srv.clk.Now()
		rec.SpamClassifiedAt = &t
	}
	// Categorisation sub-record.
	if catResult.PromptApplied != "" {
		rec.CategoryPromptApplied = &catResult.PromptApplied
		rec.CategoryModel = &catResult.Model
		if catResult.Category != "" {
			rec.CategoryAssigned = &catResult.Category
		}
		t := sess.srv.clk.Now()
		rec.CategoryClassifiedAt = &t
	}
	if err := sess.srv.store.Meta().SetLLMClassification(ctx, rec); err != nil {
		sess.log.WarnContext(ctx, "llm-transparency: persist classification record",
			slog.String("activity", observe.ActivityInternal),
			slog.String("msg_id_header", msgIDHeader),
			slog.String("err", err.Error()))
	}
}

// runMailAuth evaluates DKIM/SPF/DMARC/ARC on inbound (relay-in) mail
// and returns both the typed results and the rendered
// Authentication-Results header value (without the header name +
// colon). Submissions skip verification (authenticated + outbound).
func (sess *session) runMailAuth(ctx context.Context, body []byte) (mailauth.AuthResults, string) {
	if sess.mode != RelayIn {
		return mailauth.AuthResults{}, ""
	}
	var res mailauth.AuthResults
	if sess.srv.dkim != nil {
		if dkimRes, err := sess.srv.dkim.Verify(ctx, body); err == nil {
			res.DKIM = dkimRes
		} else {
			sess.log.WarnContext(ctx, "dkim verify error",
				slog.String("activity", observe.ActivitySystem),
				slog.String("err", err.Error()))
		}
	}
	if sess.srv.spf != nil {
		if spfRes, err := sess.srv.spf.Check(ctx, sess.envelope.mailFrom, sess.helo, sess.remoteIP); err == nil {
			res.SPF = spfRes
		} else {
			sess.log.WarnContext(ctx, "spf check error",
				slog.String("activity", observe.ActivitySystem),
				slog.String("err", err.Error()))
		}
	}
	if sess.srv.dmarc != nil {
		headerFrom := extractHeaderFrom(body)
		if dres, err := sess.srv.dmarc.Evaluate(ctx, headerFrom, res.SPF, res.DKIM); err == nil {
			res.DMARC = dres
		} else {
			sess.log.WarnContext(ctx, "dmarc evaluate error",
				slog.String("activity", observe.ActivitySystem),
				slog.String("err", err.Error()))
		}
	}
	if sess.srv.arc != nil {
		if arcRes, err := sess.srv.arc.Verify(ctx, body); err == nil {
			res.ARC = arcRes
		}
	}
	ar := renderAuthResults(sess.srv.opts.AuthservID, res)
	res.Raw = ar
	return res, ar
}

// assembleStoredBytes builds the final message bytes we persist. For
// relay-in we prepend Received + Authentication-Results. For submission
// we prepend Received only; Authentication-Results is meaningless
// there because the message has not been externally authenticated.
func (sess *session) assembleStoredBytes(body []byte, authResults string) []byte {
	var b bytes.Buffer
	b.Grow(len(body) + 512)
	b.WriteString(sess.renderReceived())
	b.WriteString("\r\n")
	if authResults != "" {
		b.WriteString("Authentication-Results: ")
		b.WriteString(authResults)
		b.WriteString("\r\n")
	}
	b.Write(body)
	return b.Bytes()
}

// renderReceived produces the Received: header value for this message.
// It follows REQ-FLOW-20: protocol, encryption status, EHLO, client IP,
// message ID. The line is emitted without the trailing CRLF (caller
// adds one).
//
// Every field that originates from the wire — HELO, remote IP, TLS
// version/cipher, and our own hostname/AuthservID echoed back into
// headers — is run through sanitizeHeaderValue so a malicious peer
// cannot inject CR/LF or other header-shape bytes into the stored
// message. The hostname and TLS labels come from operator config but
// the helper is cheap and applying it uniformly removes the chance of
// a future caller forgetting.
func (sess *session) renderReceived() string {
	proto := "SMTP"
	if sess.isEHLO {
		proto = "ESMTP"
	}
	enc := ""
	if sess.tlsEstablished {
		enc = "S" // ESMTPS
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Received: from %s", sanitizeHeaderValue(sess.helo))
	if sess.remoteIP != "" && sess.remoteIP != "-" {
		fmt.Fprintf(&b, " ([%s])", sanitizeHeaderValue(sess.remoteIP))
	}
	fmt.Fprintf(&b, " by %s with %s%s",
		sanitizeHeaderValue(sess.srv.opts.Hostname),
		sanitizeHeaderValue(proto),
		sanitizeHeaderValue(enc))
	if sess.tlsEstablished {
		fmt.Fprintf(&b, " (%s:%s)",
			sanitizeHeaderValue(tlsVersionName(sess.tlsVersion)),
			sanitizeHeaderValue(tls.CipherSuiteName(sess.tlsCipherSuite)))
	}
	fmt.Fprintf(&b, "; %s", sess.srv.clk.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 -0700"))
	return b.String()
}

// maxHeaderFieldLen caps any single sanitized field at a sensible
// upper bound. RFC 5322 has no per-field maximum but real-world MUAs
// choke past a few KiB; we cap at 1 KiB which is well clear of the
// longest legitimate HELO domain (255 octets per RFC 5321) and the
// longest reasonable TLS cipher name.
const maxHeaderFieldLen = 1024

// sanitizeHeaderValue makes s safe to embed into a structured header
// (Received:, Authentication-Results:, etc.). Any byte outside the
// printable ASCII safe set [0x20..0x7E] minus the structural bytes
// CR / LF / NUL is replaced with '_'. Output is capped at
// maxHeaderFieldLen so an attacker cannot pad a stored message with a
// gigabyte of HELO bytes.
//
// Policy: this is intentionally aggressive. Mail headers are 7-bit
// US-ASCII per RFC 5322 §2.2; non-ASCII or control bytes in
// operator-rendered fields are either an attacker forging header
// shape or a misconfiguration we surface by mangling the byte. We do
// not attempt RFC 2047 encoded-word emission — this header is
// machine-read by downstream MTAs/log pipelines, not a UI string.
func sanitizeHeaderValue(s string) string {
	if len(s) > maxHeaderFieldLen {
		s = s[:maxHeaderFieldLen]
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c > 0x7E {
			b.WriteByte('_')
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// classify runs the spam classifier. On any error (plugin missing,
// timeout, parse failure) we collapse to Unclassified and continue —
// the filter step is not a gate for accept/reject by itself.
func (sess *session) classify(ctx context.Context, msg mailparse.Message, authResults mailauth.AuthResults) spam.Classification {
	if sess.srv.spam == nil {
		return spam.Classification{Verdict: spam.Unclassified, Score: -1}
	}
	cls, err := sess.srv.spam.Classify(ctx, msg, &authResults, sess.srv.spamPlug)
	if err != nil {
		// Classifier.Classify already emits a warn-level
		// "spam classifier error" with the plugin name and err
		// before returning. Logging again here would duplicate the
		// same record at INFO; let the classifier own that line.
		return spam.Classification{Verdict: spam.Unclassified, Score: -1}
	}
	return cls
}

// runSieve loads the recipient's script via
// store.Metadata.GetSieveScript and evaluates it. An absent / empty
// script falls back to the default "keep to Inbox" outcome without
// executing the interpreter.
func (sess *session) runSieve(
	ctx context.Context,
	rc rcptEntry,
	msg mailparse.Message,
	authResults mailauth.AuthResults,
	classification spam.Classification,
) (sieve.Outcome, error) {
	if sess.srv.sieve == nil {
		return sieve.Outcome{ImplicitKeep: true}, nil
	}
	scriptText, err := sess.srv.store.Meta().GetSieveScript(ctx, rc.principalID)
	if err != nil {
		// Failure to load the script reverts to default delivery.
		return sieve.Outcome{ImplicitKeep: true}, err
	}
	if strings.TrimSpace(scriptText) == "" {
		return sieve.Outcome{ImplicitKeep: true}, nil
	}
	script, perr := sieve.Parse([]byte(scriptText))
	if perr != nil {
		return sieve.Outcome{}, fmt.Errorf("parse: %w", perr)
	}
	env := sieve.Environment{
		Recipient:   rc.addr,
		Sender:      sess.envelope.mailFrom,
		Auth:        &authResults,
		SpamScore:   classification.Score,
		SpamVerdict: classification.Verdict.String(),
		Clock:       sess.srv.clk,
	}
	return sess.srv.sieve.Evaluate(ctx, script, msg, env)
}

// resolveSieveTargets flattens a sieve.Outcome into the mailbox-name
// list the delivery code acts on. discarded is true when discard was
// taken and no fileinto folder was chosen; rejected + reason map the
// Sieve reject action to the operator-visible log.
func resolveSieveTargets(out sieve.Outcome) (targets []string, discarded, rejected bool, reason string) {
	if out.ImplicitKeep {
		targets = append(targets, "INBOX")
	}
	for _, a := range out.Actions {
		switch a.Kind {
		case sieve.ActionKeep:
			if !contains(targets, "INBOX") {
				targets = append(targets, "INBOX")
			}
		case sieve.ActionFileInto:
			if a.Mailbox != "" && !contains(targets, a.Mailbox) {
				targets = append(targets, a.Mailbox)
			}
		case sieve.ActionDiscard:
			discarded = true
		case sieve.ActionReject:
			rejected = true
			reason = a.Reason
		case sieve.ActionRedirect:
			// Phase 1: redirect is recorded but not queued; the
			// outbound queue lands in Phase 2.
		}
	}
	return targets, discarded, rejected, reason
}

// sieveFlagsFromOutcome maps Sieve setflag / addflag actions onto the
// store's MessageFlags bitfield. Keywords stay on the message via
// UpdateMessageFlags in a later pass; Phase 1 wires system flags only.
func sieveFlagsFromOutcome(out sieve.Outcome) store.MessageFlags {
	var f store.MessageFlags
	for _, a := range out.Actions {
		if a.Kind != sieve.ActionAddFlag && a.Kind != sieve.ActionSetFlag {
			continue
		}
		switch strings.ToLower(a.Flag) {
		case "\\seen", "seen":
			f |= store.MessageFlagSeen
		case "\\answered", "answered":
			f |= store.MessageFlagAnswered
		case "\\flagged", "flagged":
			f |= store.MessageFlagFlagged
		case "\\draft", "draft":
			f |= store.MessageFlagDraft
		case "\\deleted", "deleted":
			f |= store.MessageFlagDeleted
		}
	}
	return f
}

// ensureMailbox returns the Mailbox named mbName owned by pid,
// creating it on the fly when absent. Mailbox names map 1:1 to the
// store schema (no hierarchy parsing in Wave 2).
func (sess *session) ensureMailbox(ctx context.Context, pid directory.PrincipalID, mbName string) (store.Mailbox, error) {
	mbs, err := sess.srv.store.Meta().ListMailboxes(ctx, pid)
	if err != nil {
		return store.Mailbox{}, err
	}
	for _, mb := range mbs {
		if strings.EqualFold(mb.Name, mbName) {
			return mb, nil
		}
	}
	attr := store.MailboxAttributes(0)
	switch strings.ToUpper(mbName) {
	case "INBOX":
		attr |= store.MailboxAttrInbox
	case "SENT":
		attr |= store.MailboxAttrSent
	case "DRAFTS":
		attr |= store.MailboxAttrDrafts
	case "TRASH":
		attr |= store.MailboxAttrTrash
	case "JUNK":
		attr |= store.MailboxAttrJunk
	case "ARCHIVE":
		attr |= store.MailboxAttrArchive
	}
	mb, err := sess.srv.store.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: pid,
		Name:        mbName,
		Attributes:  attr,
	})
	if err != nil {
		// Race: another delivery may have just inserted the same name.
		if errors.Is(err, store.ErrConflict) {
			mbs, _ = sess.srv.store.Meta().ListMailboxes(ctx, pid)
			for _, mb := range mbs {
				if strings.EqualFold(mb.Name, mbName) {
					return mb, nil
				}
			}
		}
		return store.Mailbox{}, err
	}
	return mb, nil
}

// envelopeFromParsed extracts the cached envelope fields the store
// expects on a Message row.
func envelopeFromParsed(msg mailparse.Message) store.Envelope {
	join := func(addrs []mail.Address) string {
		parts := make([]string, 0, len(addrs))
		for _, a := range addrs {
			parts = append(parts, a.String())
		}
		return strings.Join(parts, ", ")
	}
	return store.Envelope{
		Subject:   msg.Envelope.Subject,
		From:      join(msg.Envelope.From),
		To:        join(msg.Envelope.To),
		Cc:        join(msg.Envelope.Cc),
		Bcc:       join(msg.Envelope.Bcc),
		MessageID: msg.Envelope.MessageID,
		InReplyTo: strings.Join(msg.Envelope.InReplyTo, " "),
	}
}

// renderAuthResults emits the value portion of the
// Authentication-Results header per RFC 8601. authserv-id is the
// server's advertised identity (REQ-FLOW-21).
//
// Every value field that flows from the wire (SPF mailfrom/HELO,
// DKIM header.d / header.s, DMARC header.from) is run through
// sanitizeAuthToken so an attacker-controlled domain cannot inject
// new method tokens or close the header with a CRLF.
func renderAuthResults(authservID string, r mailauth.AuthResults) string {
	var parts []string
	parts = append(parts, sanitizeAuthToken(authservID))
	// SPF.
	if r.SPF.Status != mailauth.AuthUnknown {
		p := fmt.Sprintf("spf=%s", r.SPF.Status.String())
		if r.SPF.From != "" {
			p += fmt.Sprintf(" smtp.mailfrom=%s", sanitizeAuthToken(r.SPF.From))
		} else if r.SPF.HELO != "" {
			p += fmt.Sprintf(" smtp.helo=%s", sanitizeAuthToken(r.SPF.HELO))
		}
		parts = append(parts, p)
	}
	// DKIM — one method entry per signature.
	for _, d := range r.DKIM {
		p := fmt.Sprintf("dkim=%s", d.Status.String())
		if d.Domain != "" {
			p += fmt.Sprintf(" header.d=%s", sanitizeAuthToken(d.Domain))
		}
		if d.Selector != "" {
			p += fmt.Sprintf(" header.s=%s", sanitizeAuthToken(d.Selector))
		}
		parts = append(parts, p)
	}
	// DMARC.
	if r.DMARC.Status != mailauth.AuthUnknown {
		p := fmt.Sprintf("dmarc=%s", r.DMARC.Status.String())
		if r.DMARC.HeaderFrom != "" {
			p += fmt.Sprintf(" header.from=%s", sanitizeAuthToken(r.DMARC.HeaderFrom))
		}
		parts = append(parts, p)
	}
	// ARC.
	if r.ARC.Status != mailauth.AuthUnknown && r.ARC.Status != mailauth.AuthNone {
		parts = append(parts, fmt.Sprintf("arc=%s", r.ARC.Status.String()))
	}
	// Spam classifier verdict. Per RFC 8601 §2.7 the "x-" prefix marks
	// this as an experimental method; the rendered header therefore
	// looks like "...; x-herold-spam=spam (score=0.90)".
	// Operators inspecting the stored message can read the verdict
	// directly without reaching into the audit log. A nil Spam pointer
	// or an empty verdict suppresses the token (the classifier did not
	// run).
	if r.Spam != nil && r.Spam.Verdict != "" {
		p := fmt.Sprintf("x-herold-spam=%s", sanitizeAuthToken(r.Spam.Verdict))
		if r.Spam.Score >= 0 {
			p += fmt.Sprintf(" (score=%.2f)", r.Spam.Score)
		}
		if r.Spam.Engine != "" {
			p += fmt.Sprintf(" x-herold-spam-engine=%s", sanitizeAuthToken(r.Spam.Engine))
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, "; ")
}

// sanitizeAuthToken makes a value safe to embed in an
// Authentication-Results method/value. We strip whitespace and any
// character that would break the RFC 8601 grammar — semicolon, equals,
// CR, LF, and parenthesis — replacing them with '_'. Unsanitised input
// would let a malicious classifier name forge additional method tokens.
func sanitizeAuthToken(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case ' ', '\t', '\r', '\n', ';', '=', '(', ')', ',', '"', '<', '>', '@', ':', '\\':
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// extractHeaderFrom returns the RFC 5322 From: header value from the
// raw message bytes, or the empty string. Minimal scan; a full
// RFC 5322 parser is overkill here because the DMARC evaluator parses
// the address itself.
func extractHeaderFrom(raw []byte) string {
	// Find end of headers.
	end := len(raw)
	if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 {
		end = i
	} else if i := bytes.Index(raw, []byte("\n\n")); i >= 0 {
		end = i
	}
	// Un-fold header section.
	lines := bytes.Split(raw[:end], []byte("\n"))
	var cur bytes.Buffer
	var out []string
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, ln := range lines {
		trimmed := bytes.TrimRight(ln, "\r")
		if len(trimmed) == 0 {
			continue
		}
		if trimmed[0] == ' ' || trimmed[0] == '\t' {
			cur.WriteByte(' ')
			cur.Write(bytes.TrimLeft(trimmed, " \t"))
			continue
		}
		flush()
		cur.Write(trimmed)
	}
	flush()
	for _, line := range out {
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(name), "From") {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// contains reports whether haystack contains needle, case-insensitive.
func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if strings.EqualFold(h, needle) {
			return true
		}
	}
	return false
}

// boolStr encodes a bool as "true"/"false" for audit metadata.
func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// dispatchSynthetic hands a synthetic-recipient delivery off to the
// configured WebhookDispatcher (Wave 3.5c-Z, REQ-DIR-RCPT-07 +
// REQ-HOOK-02). When no dispatcher is wired or no subscription matches,
// the recipient is still treated as accepted at the SMTP layer (the
// 250 reply already accommodates partial misconfiguration); the audit
// log carries the operator-visible signal.
func (sess *session) dispatchSynthetic(
	ctx context.Context,
	rc rcptEntry,
	msg mailparse.Message,
	finalBytes []byte,
	blobRef store.BlobRef,
) {
	disp := sess.srv.webhookDisp
	if disp == nil {
		sess.log.WarnContext(ctx, "synthetic recipient accepted but no webhook dispatcher wired",
			slog.String("activity", observe.ActivitySystem),
			slog.String("recipient", rc.addr),
			slog.String("route_tag", rc.routeTag))
		sess.auditSyntheticAccepted(ctx, rc, msg, 0, "no_dispatcher_wired")
		return
	}
	domain := strings.ToLower(strings.TrimSpace(rc.domain))
	hooks := disp.MatchingSyntheticHooks(ctx, domain)
	if len(hooks) == 0 {
		sess.log.InfoContext(ctx, "synthetic recipient accepted but no webhook subscriber",
			slog.String("activity", observe.ActivitySystem),
			slog.String("recipient", rc.addr),
			slog.String("route_tag", rc.routeTag))
		sess.auditSyntheticAccepted(ctx, rc, msg, 0, "no_subscription")
		return
	}
	in := SyntheticDispatch{
		Domain:    domain,
		Recipient: rc.addr,
		MailFrom:  sess.envelope.mailFrom,
		RouteTag:  rc.routeTag,
		BlobHash:  blobRef.Hash,
		Size:      int64(len(finalBytes)),
		Parsed:    msg,
	}
	if err := disp.DispatchSynthetic(ctx, in, hooks); err != nil {
		sess.log.WarnContext(ctx, "synthetic dispatch enqueue failed",
			slog.String("activity", observe.ActivitySystem),
			slog.String("recipient", rc.addr),
			slog.String("err", err.Error()))
		sess.auditSyntheticAccepted(ctx, rc, msg, len(hooks), "dispatch_error")
		return
	}
	sess.log.InfoContext(ctx, "synthetic recipient dispatched to webhooks",
		slog.String("activity", observe.ActivitySystem),
		slog.String("recipient", rc.addr),
		slog.String("route_tag", rc.routeTag),
		slog.Int("subscribers", len(hooks)))
	sess.auditSyntheticAccepted(ctx, rc, msg, len(hooks), "dispatched")
}

// auditSyntheticAccepted writes the REQ-DIR-RCPT-09 audit row for a
// synthetic-recipient acceptance: action=smtp.synthetic_accept, with
// recipient, route_tag, dispatched-to-webhooks count, and the per-
// session decision-source label so an operator can correlate
// connection-time and DATA-time outcomes.
func (sess *session) auditSyntheticAccepted(
	ctx context.Context,
	rc rcptEntry,
	msg mailparse.Message,
	dispatched int,
	outcome string,
) {
	md := map[string]string{
		"recipient":        rc.addr,
		"recipient_domain": strings.ToLower(rc.domain),
		"sender":           sess.envelope.mailFrom,
		"route_tag":        rc.routeTag,
		"decision_source":  rc.decisionSource,
		"dispatched_hooks": fmt.Sprintf("%d", dispatched),
		"dispatch_outcome": outcome,
		"plugin_name":      sess.srv.rcptPluginNm,
		"top_content_type": msg.Body.ContentType,
	}
	if msg.Envelope.MessageID != "" {
		md["message_id"] = msg.Envelope.MessageID
	}
	auditOutcome := store.OutcomeSuccess
	if outcome == "no_dispatcher_wired" || outcome == "dispatch_error" {
		auditOutcome = store.OutcomeFailure
	}
	if err := sess.srv.store.Meta().AppendAuditLog(ctx, store.AuditLogEntry{
		At:         sess.srv.clk.Now(),
		ActorKind:  store.ActorSystem,
		ActorID:    "smtp",
		Action:     "smtp.synthetic_accept",
		Subject:    "recipient:" + rc.addr,
		RemoteAddr: sess.remoteIP,
		Outcome:    auditOutcome,
		Message:    "session=" + sess.sessID,
		Metadata:   md,
	}); err != nil {
		sess.log.WarnContext(ctx, "synthetic audit append failed",
			slog.String("activity", observe.ActivityInternal),
			slog.String("err", err.Error()))
	}
}

// queueOutboundFromSubmission enqueues one outbound queue row for an
// authenticated submission-listener RCPT TO that resolved to a non-
// local recipient (Wave 3.1.6). Returns true when the queue accepted
// the row so the per-recipient outcome counts as "ok" for the SMTP
// transaction; false on a queue failure (the SMTP layer surfaces the
// error to other recipients via the loop's `anyOK == false` path).
//
// The submission path mirrors the JMAP EmailSubmission and the HTTP
// send-API queue.Submit shapes (REQ-PROTO-42, REQ-FLOW-63): identical
// fields, identical idempotency-key composition. The DKIM-signing
// domain is derived from MAIL FROM's domain; if the principal lacks a
// signing key for that domain the queue worker logs and sends unsigned
// (the existing queue.Run behaviour).
func (sess *session) queueOutboundFromSubmission(
	ctx context.Context,
	rc rcptEntry,
	msg mailparse.Message,
	finalBytes []byte,
) bool {
	q := sess.srv.subQueue
	if q == nil {
		sess.log.WarnContext(ctx, "submission listener: outbound queue not wired; recipient dropped",
			slog.String("activity", observe.ActivitySystem),
			slog.String("recipient", rc.addr))
		return false
	}
	if !sess.authenticated {
		// Defensive: the cmdMAIL handler already required AUTH on
		// submission listeners. Reaching this branch unauthenticated
		// is a bug.
		sess.log.ErrorContext(ctx, "submission listener: unauthenticated session reached outbound queue path",
			slog.String("activity", observe.ActivityInternal),
			slog.String("recipient", rc.addr))
		return false
	}
	pid := sess.authPrincipal
	signingDomain, _ := domainOfAddress(sess.envelope.mailFrom)
	idemKey := ""
	if mid := msg.Envelope.MessageID; mid != "" {
		idemKey = mid + ":" + rc.addr
	}
	requireTLS := sess.envelope.mailFromParams.requireTLS
	envID, err := q.Submit(ctx, queue.Submission{
		PrincipalID:    &pid,
		MailFrom:       sess.envelope.mailFrom,
		Recipients:     []string{rc.addr},
		Body:           bytes.NewReader(finalBytes),
		Sign:           true,
		SigningDomain:  signingDomain,
		DSNNotify:      parseDSNNotifyFlags(rc.notify),
		DSNRet:         parseDSNRet(sess.envelope.mailFromParams.ret),
		DSNEnvelopeID:  sess.envelope.mailFromParams.envid,
		IdempotencyKey: idemKey,
		REQUIRETLS:     requireTLS,
	})
	if err != nil {
		sess.log.WarnContext(ctx, "submission listener: queue.Submit failed",
			slog.String("activity", observe.ActivitySystem),
			slog.String("recipient", rc.addr),
			slog.String("err", err.Error()))
		return false
	}
	sess.log.InfoContext(ctx, "submission listener: outbound queued",
		slog.String("activity", observe.ActivityUser),
		slog.String("recipient", rc.addr),
		slog.String("envelope_id", string(envID)),
		slog.String("signing_domain", signingDomain))
	_ = sess.srv.store.Meta().AppendAuditLog(ctx, store.AuditLogEntry{
		At:         sess.srv.clk.Now(),
		ActorKind:  store.ActorPrincipal,
		ActorID:    fmt.Sprintf("%d", uint64(pid)),
		Action:     "smtp.inbound_submission_queued",
		Subject:    "envelope:" + string(envID),
		RemoteAddr: sess.remoteIP,
		Outcome:    store.OutcomeSuccess,
		Message:    "session=" + sess.sessID,
		Metadata: map[string]string{
			"recipient":      rc.addr,
			"mail_from":      sess.envelope.mailFrom,
			"signing_domain": signingDomain,
			"mode":           sess.mode.String(),
		},
	})
	return true
}

// parseDSNNotifyFlags maps the RCPT NOTIFY parameter token list onto
// the typed store.DSNNotifyFlags bitfield. The token vocabulary is
// RFC 3461 §4.1: NEVER | SUCCESS,FAILURE,DELAY (any subset, comma-sep).
func parseDSNNotifyFlags(notify string) store.DSNNotifyFlags {
	notify = strings.ToUpper(strings.TrimSpace(notify))
	if notify == "" {
		return store.DSNNotifyNone
	}
	if notify == "NEVER" {
		return store.DSNNotifyNever
	}
	var f store.DSNNotifyFlags
	for _, tok := range strings.Split(notify, ",") {
		switch strings.TrimSpace(tok) {
		case "SUCCESS":
			f |= store.DSNNotifySuccess
		case "FAILURE":
			f |= store.DSNNotifyFailure
		case "DELAY":
			f |= store.DSNNotifyDelay
		}
	}
	if f == 0 {
		return store.DSNNotifyNone
	}
	return f
}

// parseDSNRet maps the MAIL FROM RET parameter ("FULL" | "HDRS" | "")
// onto the typed store.DSNRet enumeration.
func parseDSNRet(ret string) store.DSNRet {
	switch strings.ToUpper(strings.TrimSpace(ret)) {
	case "FULL":
		return store.DSNRetFull
	case "HDRS":
		return store.DSNRetHeaders
	default:
		return store.DSNRetUnspecified
	}
}

// tlsVersionName returns a readable name for a tls.Version constant.
func tlsVersionName(v uint16) string {
	switch v {
	case tls.VersionTLS13:
		return "TLSv1.3"
	case tls.VersionTLS12:
		return "TLSv1.2"
	case tls.VersionTLS11:
		return "TLSv1.1"
	case tls.VersionTLS10:
		return "TLSv1.0"
	default:
		return "TLS"
	}
}
