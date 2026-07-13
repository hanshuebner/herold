package maillist

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/dsn"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// BounceProcessor implements REQ-MLIST-51/52 (issue #184): given an
// inbound message accepted at a hosted list's VERP bounce address, it
// verifies the per-member token, parses the message as a DSN / non-
// delivery report (internal/dsn), and classifies the failure. It does
// NOT update membership state, score bounces, or auto-suspend --
// BounceResult is the seam a later stage (scoring/auto-suspend,
// REQ-MLIST-53/54) scores against.
type BounceProcessor struct {
	Meta   store.Metadata
	Tokens *TokenSigner
	Clock  clock.Clock
	Logger *slog.Logger
}

// NewBounceProcessor constructs a BounceProcessor. clk defaults to
// clock.NewReal and logger to slog.Default when nil. tokens may be nil
// (e.g. no deployment data key configured yet); every bounce is then
// logged and dropped without attribution, matching what an unverifiable
// token produces (REQ-MLIST-52) -- a missing signer is not distinguished
// from a bad token, since both mean "cannot verify".
func NewBounceProcessor(meta store.Metadata, tokens *TokenSigner, clk clock.Clock, logger *slog.Logger) *BounceProcessor {
	if clk == nil {
		clk = clock.NewReal()
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &BounceProcessor{Meta: meta, Tokens: tokens, Clock: clk, Logger: logger}
}

// BounceInput is the per-message payload protosmtp's DATA-finish hands
// to ProcessBounce for one accepted post to a VERP bounce address.
type BounceInput struct {
	// List is the mailing list whose posting-address prefix the
	// recipient's local-part matched (RCPT-time shape resolution,
	// maillist.ParseVERPBounceLocalPart).
	List store.MailingList
	// Token is the opaque per-member token recovered from the
	// local-part suffix; not yet verified.
	Token string
	// Raw is the accepted message bytes -- the DSN / bounce report
	// itself.
	Raw []byte
}

// BounceResult is the classified, member-attributed outcome of one
// bounce delivery. Attributed is false whenever the token failed to
// verify (REQ-MLIST-52): callers MUST NOT read List/MemberID in that
// case (both are the zero value).
type BounceResult struct {
	Attributed     bool
	List           store.MailingList
	MemberID       store.MailingListMemberID
	Classification dsn.Classification
	Report         dsn.Report
}

// ProcessBounce verifies in.Token against in.List (REQ-MLIST-50) and,
// only on success, parses in.Raw as a DSN and classifies it
// (REQ-MLIST-51). An unverifiable token -- forged, tampered, expired, or
// signed for a different list or purpose -- is logged and audited
// WITHOUT attribution, returning a zero-value, Attributed=false
// BounceResult and a nil error: REQ-MLIST-52 requires this fail-closed
// behavior over any guess, and an unverifiable bounce token is an
// expected, non-exceptional inbound event (spam, a stale token, a
// forged probe), not a processing failure worth surfacing as an error
// to the caller.
//
// A DSN that fails to parse as a well-formed RFC 5322 message
// (vanishingly rare -- the inbound pipeline already accepted it once)
// still returns Attributed=true with Classification set to
// dsn.ClassificationUnknown: the token itself verified, and declining
// attribution here would be worse than an unknown-severity attribution
// that a later scoring stage is free to ignore.
func (p *BounceProcessor) ProcessBounce(ctx context.Context, in BounceInput) (BounceResult, error) {
	listLabel := in.List.PostingAddress

	if p.Tokens == nil {
		p.Logger.ErrorContext(ctx, "maillist: bounce received but no TokenSigner wired; dropped without attribution",
			slog.String("activity", observe.ActivitySystem),
			slog.String("list", listLabel))
		p.auditUnverified(ctx, in.List, "no TokenSigner wired")
		return BounceResult{}, nil
	}

	memberID, verr := p.Tokens.Verify(TokenPurposeVERP, in.List.ID, in.Token, p.Clock.Now())
	if verr != nil {
		p.Logger.WarnContext(ctx, "maillist: bounce token did not verify; dropped without attribution",
			slog.String("activity", observe.ActivitySystem),
			slog.String("list", listLabel))
		p.auditUnverified(ctx, in.List, "token verification failed")
		return BounceResult{}, nil
	}

	report, perr := dsn.Parse(in.Raw)
	if perr != nil {
		p.Logger.WarnContext(ctx, "maillist: bounce message did not parse as a DSN; classifying unknown",
			slog.String("activity", observe.ActivitySystem),
			slog.String("list", listLabel),
			slog.Uint64("member_id", uint64(memberID)),
			slog.String("err", perr.Error()))
		return BounceResult{Attributed: true, List: in.List, MemberID: memberID, Classification: dsn.ClassificationUnknown}, nil
	}

	p.Logger.InfoContext(ctx, "maillist: bounce attributed",
		slog.String("activity", observe.ActivityUser),
		slog.String("list", listLabel),
		slog.Uint64("member_id", uint64(memberID)),
		slog.String("classification", report.Classification.String()))
	return BounceResult{Attributed: true, List: in.List, MemberID: memberID, Classification: report.Classification, Report: report}, nil
}

// auditUnverified records an unverifiable bounce token (REQ-MLIST-52:
// "logged, not applied to any member"). Best-effort: a store error here
// is logged but never blocks ProcessBounce's own decision.
func (p *BounceProcessor) auditUnverified(ctx context.Context, ml store.MailingList, reason string) {
	if p.Meta == nil {
		return
	}
	if err := p.Meta.AppendAuditLog(ctx, store.AuditLogEntry{
		At:        p.Clock.Now(),
		ActorKind: store.ActorSystem,
		ActorID:   "maillist",
		Action:    "maillist.bounce.unverified",
		Subject:   fmt.Sprintf("mailing_list:%d", ml.ID),
		Outcome:   store.OutcomeFailure,
		Message:   reason,
		Domain:    ml.Domain,
	}); err != nil {
		p.Logger.WarnContext(ctx, "maillist: unverified-bounce audit log append failed",
			slog.String("activity", observe.ActivitySystem),
			slog.String("list", ml.PostingAddress),
			slog.String("err", err.Error()))
	}
}
