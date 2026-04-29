package protosmtp

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/spam"
	"github.com/hanshuebner/herold/internal/store"
)

// IngestRequest describes a pre-fetched raw RFC 5322 message delivered via
// an external ingest path (e.g. SES inbound via SNS + S3). The caller is
// responsible for resolving the recipient list from the store before calling
// IngestBytes.
type IngestRequest struct {
	// Body is the raw RFC 5322 message bytes (no dot-stuffing).
	Body []byte
	// MailFrom is the RFC 5321 MAIL FROM address (the envelope reverse-path).
	// Used for SPF evaluation.
	MailFrom string
	// SourceIP is the sending MTA's IP address as reported by SES (the
	// apparent sending host for SPF). Per REQ-HOOK-SES-04, SPF MUST use
	// this value, not the SES infrastructure IP.
	SourceIP string
	// Recipients is the pre-resolved list of local recipients with their
	// principal IDs. The caller must resolve these from the store before
	// calling; non-local recipients are silently skipped.
	Recipients []IngestRecipient
	// IngestSource is a short label embedded in Received: and audit rows
	// (e.g. "ses_inbound").
	IngestSource string
}

// IngestRecipient describes one resolved local recipient.
type IngestRecipient struct {
	// Addr is the RFC 5321 RCPT TO address.
	Addr string
	// PrincipalID is the owning principal resolved from the store.
	PrincipalID directory.PrincipalID
}

// IngestBytes runs the inbound delivery pipeline for pre-fetched message
// bytes. It mirrors the DATA-phase finishMessage path: mail-auth
// (DKIM/SPF/DMARC/ARC), spam classification, Sieve, and mailbox delivery.
// No TCP / SMTP session is involved; the bytes are treated as if they had
// arrived via SMTP relay-in.
//
// This is the entry point for the SES inbound path (REQ-HOOK-SES-04).
// All of REQ-FLOW-01..32 apply once the bytes enter this function.
//
// Returns an error only when no recipient delivery was attempted at all
// (e.g. empty recipient list, blob storage failure). Per-recipient failures
// are logged and metered but do not surface as a returned error.
func (s *Server) IngestBytes(ctx context.Context, req IngestRequest) error {
	if len(req.Recipients) == 0 {
		return fmt.Errorf("protosmtp.IngestBytes: no recipients")
	}
	source := req.IngestSource
	if source == "" {
		source = "ingest"
	}

	// Mail-auth: DKIM / SPF (using SES-reported source IP per REQ-HOOK-SES-04)
	// / DMARC / ARC.
	var authResults mailauth.AuthResults
	if s.dkim != nil {
		if dkimRes, err := s.dkim.Verify(ctx, req.Body); err == nil {
			authResults.DKIM = dkimRes
		} else {
			s.log.WarnContext(ctx, "ingest: dkim verify error",
				slog.String("activity", observe.ActivitySystem),
				slog.String("subsystem", "protosmtp"),
				slog.String("err", err.Error()))
		}
	}
	if s.spf != nil {
		// REQ-HOOK-SES-04: SPF uses the SES-reported source IP, not the
		// IP the SNS notification arrived from.
		if spfRes, err := s.spf.Check(ctx, req.MailFrom, "", req.SourceIP); err == nil {
			authResults.SPF = spfRes
		} else {
			s.log.WarnContext(ctx, "ingest: spf check error",
				slog.String("activity", observe.ActivitySystem),
				slog.String("subsystem", "protosmtp"),
				slog.String("err", err.Error()))
		}
	}
	if s.dmarc != nil {
		headerFrom := extractHeaderFrom(req.Body)
		if dres, err := s.dmarc.Evaluate(ctx, headerFrom, authResults.SPF, authResults.DKIM); err == nil {
			authResults.DMARC = dres
		} else {
			s.log.WarnContext(ctx, "ingest: dmarc evaluate error",
				slog.String("activity", observe.ActivitySystem),
				slog.String("subsystem", "protosmtp"),
				slog.String("err", err.Error()))
		}
	}
	if s.arc != nil {
		if arcRes, err := s.arc.Verify(ctx, req.Body); err == nil {
			authResults.ARC = arcRes
		}
	}

	authStr := renderAuthResults(s.opts.Hostname, authResults)
	authResults.Raw = authStr

	// Prepend a Received: header in the same style as the SMTP session
	// path to maintain pipeline uniformity.
	received := fmt.Sprintf("Received: from %s via %s; %s",
		sanitizeHeaderValue(req.SourceIP),
		sanitizeHeaderValue(source),
		s.clk.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 -0700"))

	var finalBuf bytes.Buffer
	finalBuf.Grow(len(req.Body) + len(received) + 512)
	finalBuf.WriteString(received)
	finalBuf.WriteString("\r\n")
	if authStr != "" {
		finalBuf.WriteString("Authentication-Results: ")
		finalBuf.WriteString(authStr)
		finalBuf.WriteString("\r\n")
	}
	finalBuf.Write(req.Body)
	finalBytes := finalBuf.Bytes()

	// Parse message once.
	msg, perr := mailparse.Parse(bytes.NewReader(finalBytes), mailparse.NewParseOptions())
	if perr != nil {
		s.log.InfoContext(ctx, "ingest: parse failed",
			slog.String("activity", observe.ActivitySystem),
			slog.String("subsystem", "protosmtp"),
			slog.String("source", source),
			slog.String("err", perr.Error()))
		return fmt.Errorf("protosmtp.IngestBytes: parse: %w", perr)
	}

	// Spam classification (same as relay-in path).
	var classification spam.Classification
	if s.spam != nil {
		cls, err := s.spam.Classify(ctx, msg, &authResults, s.spamPlug)
		if err != nil {
			s.log.InfoContext(ctx, "ingest: spam classify error",
				slog.String("activity", observe.ActivitySystem),
				slog.String("subsystem", "protosmtp"),
				slog.String("err", err.Error()))
			classification = spam.Classification{Verdict: spam.Unclassified, Score: -1}
		} else {
			classification = cls
		}
		// Stamp spam verdict onto authResults for audit consistency.
		authResults.Spam = &mailauth.SpamResult{
			Verdict: classification.Verdict.String(),
			Score:   classification.Score,
			Engine:  s.spamPlug,
		}
	}

	// Persist the blob once; every recipient references the same BlobRef.
	blobRef, err := s.store.Blobs().Put(ctx, bytes.NewReader(finalBytes))
	if err != nil {
		s.log.ErrorContext(ctx, "ingest: blob put failed",
			slog.String("activity", observe.ActivitySystem),
			slog.String("subsystem", "protosmtp"),
			slog.String("err", err.Error()))
		return fmt.Errorf("protosmtp.IngestBytes: blob put: %w", err)
	}

	// Deliver per recipient.
	var anyOK bool
	for _, rc := range req.Recipients {
		// Re-use the synthetic session helper by constructing a minimal
		// session. All methods called on it (runSieve, deliverOne,
		// ensureMailbox) use only the session.srv pointer and rc.
		fakeSess := &session{
			srv:      s,
			mode:     RelayIn,
			remoteIP: req.SourceIP,
			sessID:   source,
			log: s.log.With(
				slog.String("subsystem", "protosmtp"),
				slog.String("session_id", source),
				slog.String("remote_addr", req.SourceIP),
			),
			envelope: envelope{
				mailFrom: req.MailFrom,
			},
		}
		rcEntry := rcptEntry{
			addr:        rc.Addr,
			principalID: rc.PrincipalID,
			domain:      domainOfRecipient(rc.Addr),
		}
		ok, derr := fakeSess.deliverOne(ctx, rcEntry, finalBytes, blobRef, msg, authResults, classification)
		if derr != nil {
			s.log.ErrorContext(ctx, "ingest: delivery failed",
				slog.String("activity", observe.ActivitySystem),
				slog.String("subsystem", "protosmtp"),
				slog.String("recipient", rc.Addr),
				slog.String("err", derr.Error()))
		}
		if ok {
			anyOK = true
		}
	}

	// Audit the acceptance (REQ-FLOW-03 durability).
	auditTimer := observe.StartStoreOp("append_audit")
	_ = s.store.Meta().AppendAuditLog(ctx, store.AuditLogEntry{
		At:         s.clk.Now(),
		ActorKind:  store.ActorSystem,
		ActorID:    source,
		Action:     source + ".accept",
		Subject:    "message:" + blobRef.Hash,
		RemoteAddr: req.SourceIP,
		Outcome:    store.OutcomeSuccess,
		Message: fmt.Sprintf("source=%s recipients=%d size=%d",
			source, len(req.Recipients), len(finalBytes)),
		Metadata: map[string]string{
			"hostname":  s.opts.Hostname,
			"mail_from": req.MailFrom,
			"source_ip": req.SourceIP,
			"spam":      classification.Verdict.String(),
		},
	})
	auditTimer.Done()

	if !anyOK {
		return fmt.Errorf("protosmtp.IngestBytes: delivery failed for every recipient")
	}
	return nil
}

// domainOfRecipient extracts the domain portion of an RFC 5321 address.
func domainOfRecipient(addr string) string {
	if i := strings.LastIndex(addr, "@"); i >= 0 {
		return strings.ToLower(addr[i+1:])
	}
	return ""
}
