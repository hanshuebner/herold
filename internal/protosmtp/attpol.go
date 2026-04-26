package protosmtp

import (
	"context"
	"fmt"
	"strings"

	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/store"
)

// attpolDefaultRejectText is appended after the "552 5.3.4 " prefix on
// a refusal when the operator has not configured an override.
const attpolDefaultRejectText = "attachments not accepted on this address"

// attpolEnhancedStatus is the RFC 3463 enhanced-status code surfaced on
// refusals. REQ-FLOW-ATTPOL-01 fixes "5.3.4"; the operator-overridable
// portion is the trailing text only, so the prefix stays stable for
// downstream MTAs / log pipelines.
const attpolEnhancedStatus = "5.3.4"

// attpolOutcome enumerates the audit-log + metrics outcome tokens the
// inbound pipeline emits per recipient (REQ-FLOW-ATTPOL-02).
type attpolOutcome string

const (
	attpolOutcomePassed                attpolOutcome = "passed"
	attpolOutcomeRefusedAtData         attpolOutcome = "refused_at_data"
	attpolOutcomeRefusedPostAcceptance attpolOutcome = "refused_post_acceptance"
)

// attpolHeaderCheck reports whether the parsed top-level MIME structure
// trips the REQ-FLOW-ATTPOL-01 reject criteria: ANY of (a) top-level
// Content-Type multipart/mixed, (b) a direct child part declaring
// Content-Disposition: attachment, (c) any direct child part whose
// Content-Type is neither text/* nor multipart/*.
//
// The check inspects the top-level part headers and the direct children
// only — it deliberately does NOT walk the whole tree. Nested
// attachments hidden under multipart/alternative are caught by
// attpolPostAcceptanceWalk after DATA accept.
//
// The returned reason is a short label suitable for audit-log metadata
// and operator log lines; empty when the message passes.
func attpolHeaderCheck(msg mailparse.Message) (rejected bool, reason string) {
	top := strings.ToLower(strings.TrimSpace(msg.Body.ContentType))
	// (a) top-level multipart/mixed.
	if strings.HasPrefix(top, "multipart/mixed") {
		return true, "top_level_multipart_mixed"
	}
	// (c) top-level neither text/* nor multipart/* (covers a top-level
	// application/pdf, image/*, etc.).
	if !strings.HasPrefix(top, "text/") && !strings.HasPrefix(top, "multipart/") && top != "" {
		return true, "top_level_non_text_non_multipart"
	}
	// (b) direct child has Content-Disposition: attachment, or
	// (c) direct child has Content-Type that is neither text/* nor
	// multipart/* — both criteria evaluated together so the cheaper
	// shape wins.
	//
	// Exception: direct children explicitly marked
	// Content-Disposition: inline are body-referenced media (e.g. the
	// inline image leg of a multipart/related), NOT attachments — REQ
	// fixture (f) requires multipart/related with inline images to
	// pass. We therefore exempt inline children from the type-based
	// reject criterion; the disposition-based criterion above already
	// excluded inline children since attachment != inline.
	for _, c := range msg.Body.Children {
		if c.Disposition == mailparse.DispositionAttachment {
			return true, "direct_child_attachment_disposition"
		}
		if c.Disposition == mailparse.DispositionInline {
			continue
		}
		ct := strings.ToLower(strings.TrimSpace(c.ContentType))
		if ct == "" {
			continue
		}
		if !strings.HasPrefix(ct, "text/") && !strings.HasPrefix(ct, "multipart/") {
			return true, "direct_child_non_text_non_multipart"
		}
	}
	return false, ""
}

// attpolPostAcceptanceWalk runs the deep MIME walk that defends against
// nested-attachment edge cases (REQ-FLOW-ATTPOL-02). It reuses
// mailparse.Attachments — the same walker the FTS pipeline consumes
// through storefts.MailparseExtractor — so the two callers stay in
// agreement on what counts as an attachment.
//
// The walker treats Content-Disposition: attachment AND any non-text /
// non-multipart leaf with non-Inline disposition as an attachment.
// Inline images (multipart/related embedded images) carry
// Content-Disposition: inline and are explicitly NOT classified as
// attachments — see TestAttPol_PostAcceptanceWalker_RelatedInlinePasses
// for the canonical expected shape.
func attpolPostAcceptanceWalk(msg mailparse.Message) (rejected bool, reason string) {
	atts := mailparse.Attachments(msg)
	if len(atts) == 0 {
		return false, ""
	}
	// First attachment wins as the diagnostic; any attachment trips the
	// refusal. The reason tag carries the part's content-type so audit
	// readers can see what the deep walk found.
	first := atts[0]
	ct := strings.TrimSpace(first.ContentType)
	if ct == "" {
		ct = "application/octet-stream"
	}
	return true, "post_acceptance_walker:" + ct
}

// attpolRejectReply renders the SMTP refusal reply line for the row's
// configured policy. RejectText falls back to attpolDefaultRejectText
// when empty so a recipient who chose reject_at_data without overriding
// the text still gets a sensible reply.
func attpolRejectReply(row store.InboundAttachmentPolicyRow) string {
	text := strings.TrimSpace(row.RejectText)
	if text == "" {
		text = attpolDefaultRejectText
	}
	return fmt.Sprintf("552 %s %s", attpolEnhancedStatus, text)
}

// attpolDiagnosticCode renders the RFC 3464 §2.3.6 Diagnostic-Code
// representation used on the bounce DSN (REQ-FLOW-ATTPOL-02).
func attpolDiagnosticCode(row store.InboundAttachmentPolicyRow) string {
	text := strings.TrimSpace(row.RejectText)
	if text == "" {
		text = attpolDefaultRejectText
	}
	return fmt.Sprintf("smtp; 552 %s %s", attpolEnhancedStatus, text)
}

// attpolDomainOf returns the lowercased domain part of an SMTP
// forward-path. Used to label the metrics counter and to look up the
// per-domain fallback row.
func attpolDomainOf(addr string) string {
	addr = strings.ToLower(strings.TrimSpace(addr))
	at := strings.LastIndex(addr, "@")
	if at <= 0 || at == len(addr)-1 {
		return ""
	}
	return addr[at+1:]
}

// BouncePoster enqueues a DSN bounce to the original sender after a
// post-acceptance attachment-policy refusal (REQ-FLOW-ATTPOL-02). The
// implementation is plugged in by the cmd/herold wiring so the protosmtp
// package does not import the queue subsystem directly. PostBounce MUST
// be safe to call from a session goroutine; failures are logged and
// absorbed (the refusal still completes — the message is not delivered).
type BouncePoster interface {
	// PostBounce enqueues a 5xx DSN to mailFrom (the original sender)
	// describing the refusal of finalRcpt with the given diagnostic
	// code (already including the SMTP / enhanced-status prefix). The
	// originalHeaders blob is the parsed message headers section of
	// the refused message; pass nil when not available. The optional
	// originalEnvID is echoed into the DSN's Original-Envelope-Id
	// field. messageID is the original RFC 5322 Message-ID for log
	// correlation.
	PostBounce(ctx context.Context, in BounceInput) error
}

// BounceInput is the shape PostBounce consumes; kept narrow so the
// queue-side adapter can map it onto its existing dsnInput without a
// brittle struct conversion.
type BounceInput struct {
	MailFrom        string
	FinalRcpt       string
	OriginalRcpt    string
	OriginalEnvID   string
	OriginalHeaders []byte
	MessageID       string
	DiagnosticCode  string
	StatusCode      string
}
