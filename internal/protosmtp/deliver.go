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

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/mailparse"
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
	authResults, authStr := sess.runMailAuth(ctx, body)
	// Spam classification.
	msg, perr := mailparse.Parse(bytes.NewReader(body), mailparse.NewParseOptions())
	if perr != nil {
		sess.srv.log.InfoContext(ctx, "smtp data: parse failed",
			slog.String("session_id", sess.sessID),
			slog.String("remote_ip", sess.remoteIP),
			slog.String("err", perr.Error()))
		sess.writeReply("554 5.6.0 message parse failed: " + perr.Error())
		sess.resetEnvelope()
		return
	}
	classification := sess.classify(ctx, msg, authResults)
	// Assemble the raw message bytes we will store (prepend Received
	// and Authentication-Results headers).
	finalBytes := sess.assembleStoredBytes(body, authStr)

	// Persist the blob once; every recipient × mailbox refers to the
	// same BlobRef.
	blobRef, err := sess.srv.store.Blobs().Put(ctx, bytes.NewReader(finalBytes))
	if err != nil {
		sess.srv.log.ErrorContext(ctx, "smtp data: blob put failed",
			slog.String("session_id", sess.sessID),
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
		if rc.principalID == 0 {
			// Non-local recipient on submission — Phase 2 queues
			// outbound. Phase 1 already rejected at RCPT time; defensive.
			continue
		}
		ok, derr := sess.deliverOne(ctx, rc, finalBytes, blobRef, msg, authResults, classification)
		if derr != nil {
			sess.srv.log.ErrorContext(ctx, "smtp delivery failed",
				slog.String("session_id", sess.sessID),
				slog.String("recipient", rc.addr),
				slog.String("err", derr.Error()))
		}
		if ok {
			anyOK = true
		}
	}
	// Audit the accept (REQ-FLOW-03 durability).
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
		sess.srv.log.WarnContext(ctx, "sieve evaluation failed; falling back to INBOX",
			slog.String("session_id", sess.sessID),
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
		sess.srv.log.InfoContext(ctx, "sieve reject",
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

	// Resolve / create each target mailbox and insert the message.
	for _, mbName := range targets {
		mb, err := sess.ensureMailbox(ctx, rc.principalID, mbName)
		if err != nil {
			return false, fmt.Errorf("ensure mailbox %q: %w", mbName, err)
		}
		storeMsg := store.Message{
			MailboxID:    mb.ID,
			Size:         int64(len(finalBytes)),
			Blob:         blobRef,
			ReceivedAt:   sess.srv.clk.Now(),
			InternalDate: sess.srv.clk.Now(),
			Envelope:     envelopeFromParsed(msg),
		}
		// Propagate sieve-added flags onto system flags where possible.
		storeMsg.Flags = sieveFlagsFromOutcome(outcome)
		if _, _, err := sess.srv.store.Meta().InsertMessage(ctx, storeMsg); err != nil {
			if errors.Is(err, store.ErrQuotaExceeded) {
				sess.srv.log.InfoContext(ctx, "delivery over quota",
					slog.String("recipient", rc.addr))
				// REQ-FLOW-11 default behaviour: defer (4.2.2). We
				// already emitted 354; re-emit 452 for the whole
				// message (simpler: return failure).
				return false, err
			}
			return false, err
		}
	}
	return true, nil
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
			sess.srv.log.WarnContext(ctx, "dkim verify error", slog.String("err", err.Error()))
		}
	}
	if sess.srv.spf != nil {
		if spfRes, err := sess.srv.spf.Check(ctx, sess.envelope.mailFrom, sess.helo, sess.remoteIP); err == nil {
			res.SPF = spfRes
		} else {
			sess.srv.log.WarnContext(ctx, "spf check error", slog.String("err", err.Error()))
		}
	}
	if sess.srv.dmarc != nil {
		headerFrom := extractHeaderFrom(body)
		if dres, err := sess.srv.dmarc.Evaluate(ctx, headerFrom, res.SPF, res.DKIM); err == nil {
			res.DMARC = dres
		} else {
			sess.srv.log.WarnContext(ctx, "dmarc evaluate error", slog.String("err", err.Error()))
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
	fmt.Fprintf(&b, "Received: from %s", sess.helo)
	if sess.remoteIP != "" && sess.remoteIP != "-" {
		fmt.Fprintf(&b, " ([%s])", sess.remoteIP)
	}
	fmt.Fprintf(&b, " by %s with %s%s", sess.srv.opts.Hostname, proto, enc)
	if sess.tlsEstablished {
		fmt.Fprintf(&b, " (%s:%s)", tlsVersionName(sess.tlsVersion), tls.CipherSuiteName(sess.tlsCipherSuite))
	}
	fmt.Fprintf(&b, "; %s", sess.srv.clk.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 -0700"))
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
		sess.srv.log.InfoContext(ctx, "spam classification error",
			slog.String("err", err.Error()))
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
func renderAuthResults(authservID string, r mailauth.AuthResults) string {
	var parts []string
	parts = append(parts, authservID)
	// SPF.
	if r.SPF.Status != mailauth.AuthUnknown {
		p := fmt.Sprintf("spf=%s", r.SPF.Status.String())
		if r.SPF.From != "" {
			p += fmt.Sprintf(" smtp.mailfrom=%s", r.SPF.From)
		} else if r.SPF.HELO != "" {
			p += fmt.Sprintf(" smtp.helo=%s", r.SPF.HELO)
		}
		parts = append(parts, p)
	}
	// DKIM — one method entry per signature.
	for _, d := range r.DKIM {
		p := fmt.Sprintf("dkim=%s", d.Status.String())
		if d.Domain != "" {
			p += fmt.Sprintf(" header.d=%s", d.Domain)
		}
		if d.Selector != "" {
			p += fmt.Sprintf(" header.s=%s", d.Selector)
		}
		parts = append(parts, p)
	}
	// DMARC.
	if r.DMARC.Status != mailauth.AuthUnknown {
		p := fmt.Sprintf("dmarc=%s", r.DMARC.Status.String())
		if r.DMARC.HeaderFrom != "" {
			p += fmt.Sprintf(" header.from=%s", r.DMARC.HeaderFrom)
		}
		parts = append(parts, p)
	}
	// ARC.
	if r.ARC.Status != mailauth.AuthUnknown && r.ARC.Status != mailauth.AuthNone {
		parts = append(parts, fmt.Sprintf("arc=%s", r.ARC.Status.String()))
	}
	return strings.Join(parts, "; ")
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
