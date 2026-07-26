package protosmtp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/extimg"
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
	// / DMARC / ARC. The IngestBytes caller holds req.Body as a []byte
	// (SES path); the verifiers accept io.Reader so we wrap with
	// bytes.NewReader. The body is not held twice: each verifier streams the
	// reader forward and does not buffer the body.
	var authResults mailauth.AuthResults
	if s.dkim != nil {
		if dkimRes, err := s.dkim.Verify(ctx, bytes.NewReader(req.Body)); err == nil {
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
		if arcRes, err := s.arc.Verify(ctx, bytes.NewReader(req.Body)); err == nil {
			authResults.ARC = arcRes
		}
	}

	authStr := renderAuthResults(s.opts.Hostname, authResults)
	authResults.Raw = authStr

	// Prepend a Received: header in the same style as the SMTP session
	// path to maintain pipeline uniformity. Build the header prefix as a
	// separate []byte so we can stream it together with req.Body via an
	// io.MultiReader — eliminating the full finalBuf copy for the blob Put
	// (REQ-STORE-18). IngestBytes.Body is a []byte from the caller (SES
	// path) so we cannot eliminate that allocation in Phase 1; the benefit
	// here is removing the extra buffer that concatenated prefix + body.
	received := fmt.Sprintf("Received: from %s via %s; %s",
		sanitizeHeaderValue(req.SourceIP),
		sanitizeHeaderValue(source),
		s.clk.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 -0700"))
	headerPrefix := buildHeaderPrefix(received, authStr)

	// TODO(REQ-STORE-19, Phase 2): read from the seekable blob handle once
	// mailparse/maildkim/extimg are reader-based; bounded by MaxMessageSize
	// until then.
	// Parse message once (operate on the raw body, not the assembled bytes;
	// parsing from headerPrefix+body avoids the double copy).
	assembledBytes := append(headerPrefix, req.Body...)
	msg, perr := mailparse.Parse(bytes.NewReader(assembledBytes), mailparse.NewParseOptions())
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

	// finalBytes holds the assembled message used by downstream pipeline
	// stages and for the blob Put. Initially it is the combined header
	// prefix + body; extimg may replace it with a rewritten copy.
	finalBytes := assembledBytes
	extimgRewritten := false
	var failedImageCount int
	var failedImageState string
	var retryableFailedImageCount int
	var failedImageReason string

	// REQ-EXTIMG-02: external-image internalization. Same hook as the
	// SMTP DATA path; ingest is always inbound so no mode check.
	// TODO(REQ-STORE-19, Phase 2): extimg.Internalize should accept a
	// seekable reader; bounded by MaxMessageSize until then.
	if s.extImg.Mode == extimg.ModeInternalize {
		verdict := dkimVerdictFromAuth(authResults.DKIM)
		rewritten, sum, ierr := extimg.Internalize(ctx, finalBytes, s.extImg, verdict)
		if ierr == nil && sum.Modified {
			finalBytes = rewritten
			extimgRewritten = true
			if msg2, perr := mailparse.Parse(bytes.NewReader(finalBytes), mailparse.NewParseOptions()); perr == nil {
				msg = msg2
			}
		}
		// issue #162: retain failed-image state (URLs + splice-back
		// template + DKIM verdict) server-side so a later JMAP
		// Email/retryImages call can re-attempt exactly these URLs.
		// Best-effort; an encode failure only costs the retry
		// affordance, not the already-correct placeholdered body.
		if len(sum.FailedURLs) > 0 {
			if encoded, eerr := extimg.EncodeRetainedState(extimg.RetainedState{
				URLs:     sum.FailedURLs,
				Template: sum.FailedImageTemplate,
				DKIM:     verdict,
			}); eerr == nil {
				failedImageCount = len(sum.FailedURLs)
				failedImageState = encoded
				retryable, reason := extimg.DeriveFailureSignal(sum.FailureCounts)
				retryableFailedImageCount = retryable
				failedImageReason = string(reason)
			} else {
				s.log.WarnContext(ctx, "ingest: encode retained failed-image state failed",
					slog.String("activity", observe.ActivitySystem),
					slog.String("err", eerr.Error()))
			}
		}
		// Server-level log (no session here; emit directly).
		level := slog.LevelInfo
		msgText := "ingest: extimg rewrite"
		if ierr != nil {
			level = slog.LevelWarn
			msgText = "ingest: extimg error"
		}
		s.log.LogAttrs(ctx, level, msgText,
			slog.String("activity", observe.ActivitySystem),
			slog.String("subsystem", "protosmtp"),
			slog.String("source", source),
			slog.Bool("modified", sum.Modified),
			slog.Int("candidates", sum.Candidates),
			slog.Int("internalized", sum.Internalized),
			slog.Int("failed", sum.Failed))
	}

	// Persist the blob. When extimg did not rewrite the message, stream
	// the header prefix and the body through an io.MultiReader to avoid
	// holding assembled finalBytes in RAM twice (REQ-STORE-18). The
	// headerPrefix is a separate allocation that shares no backing array
	// with req.Body, so the MultiReader is effectively zero-copy for the
	// Put path. IngestBytes.Body arrives as a []byte from the caller (SES
	// path, Phase 1) so we cannot eliminate that caller-side allocation;
	// the benefit here is not building a second concatenated copy.
	var blobRef store.BlobRef
	var blobErr error
	if !extimgRewritten {
		// No rewrite: stream header prefix + body via MultiReader.
		blobRef, blobErr = s.store.Blobs().Put(ctx,
			io.MultiReader(bytes.NewReader(headerPrefix), bytes.NewReader(req.Body)))
	} else {
		// extimg rewrote: finalBytes is a fresh allocation; put directly.
		blobRef, blobErr = s.store.Blobs().Put(ctx, bytes.NewReader(finalBytes))
	}
	if blobErr != nil {
		s.log.ErrorContext(ctx, "ingest: blob put failed",
			slog.String("activity", observe.ActivitySystem),
			slog.String("subsystem", "protosmtp"),
			slog.String("err", blobErr.Error()))
		return fmt.Errorf("protosmtp.IngestBytes: blob put: %w", blobErr)
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
				mailFrom:                  req.MailFrom,
				failedImageCount:          failedImageCount,
				failedImageState:          failedImageState,
				retryableFailedImageCount: retryableFailedImageCount,
				failedImageReason:         failedImageReason,
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

	// Record the acceptance in the system events ring-buffer (REQ-ADM-304).
	// Derive domain from the first recipient for REQ-ADM-307 scoping.
	var ingestDomain string
	if len(req.Recipients) > 0 {
		ingestDomain = domainOfRecipient(req.Recipients[0].Addr)
	}
	auditTimer := observe.StartStoreOp("append_audit")
	_ = s.store.Meta().AppendSystemEvent(ctx, store.SystemEvent{
		At:         s.clk.Now(),
		ActorID:    source,
		Action:     source + ".accept",
		Subject:    "message:" + blobRef.Hash,
		RemoteAddr: req.SourceIP,
		Outcome:    store.OutcomeSuccess,
		Message: fmt.Sprintf("source=%s recipients=%d size=%d",
			source, len(req.Recipients), len(finalBytes)),
		Domain: ingestDomain,
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
