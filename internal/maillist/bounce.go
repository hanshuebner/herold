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

// BounceProcessor implements REQ-MLIST-51/52/53/54 (issue #184): given
// an inbound message accepted at a hosted list's VERP bounce address, it
// verifies the per-member token, parses the message as a DSN / non-
// delivery report (internal/dsn), classifies the failure, and scores
// the classified bounce against the list's bounce policy -- auto-
// suspending, auditing, notifying the owner, and updating metrics when
// the score crosses the configured threshold.
type BounceProcessor struct {
	Meta   store.Metadata
	Tokens *TokenSigner
	// Blobs persists the owner-notification message body on auto-
	// suspend (REQ-MLIST-54). May be nil (e.g. a caller that only wants
	// classification/scoring without the notification side effect, or a
	// deployment still being wired up): notifyOwner then logs a warning
	// and skips the notification rather than failing the whole bounce.
	Blobs  store.Blobs
	Clock  clock.Clock
	Logger *slog.Logger
}

// NewBounceProcessor constructs a BounceProcessor. clk defaults to
// clock.NewReal and logger to slog.Default when nil. tokens may be nil
// (e.g. no deployment data key configured yet); every bounce is then
// logged and dropped without attribution, matching what an unverifiable
// token produces (REQ-MLIST-52) -- a missing signer is not distinguished
// from a bad token, since both mean "cannot verify". blobs may also be
// nil; see the Blobs field doc.
func NewBounceProcessor(meta store.Metadata, blobs store.Blobs, tokens *TokenSigner, clk clock.Clock, logger *slog.Logger) *BounceProcessor {
	if clk == nil {
		clk = clock.NewReal()
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &BounceProcessor{Meta: meta, Blobs: blobs, Tokens: tokens, Clock: clk, Logger: logger}
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
		p.score(ctx, in.List, memberID, dsn.ClassificationUnknown)
		return BounceResult{Attributed: true, List: in.List, MemberID: memberID, Classification: dsn.ClassificationUnknown}, nil
	}

	p.Logger.InfoContext(ctx, "maillist: bounce attributed",
		slog.String("activity", observe.ActivityUser),
		slog.String("list", listLabel),
		slog.Uint64("member_id", uint64(memberID)),
		slog.String("classification", report.Classification.String()))
	p.score(ctx, in.List, memberID, report.Classification)
	return BounceResult{Attributed: true, List: in.List, MemberID: memberID, Classification: report.Classification, Report: report}, nil
}

// score applies REQ-MLIST-53/54 to one attributed, classified bounce: it
// updates the member's bounce_score/last_bounce_at under the list's
// bounce policy and, on threshold crossing, atomically suspends the
// member, audits the transition, notifies the owner, and updates
// metrics. classification == dsn.ClassificationUnknown is a deliberate
// no-op (bounceWeight's ok=false): REQ-MLIST-52/53's conservatism means
// an ambiguous bounce never touches a member's score, matching the DSN
// parser's own refusal to guess Hard.
//
// Every failure path here is best-effort and logged, never returned:
// classification/attribution already succeeded by the time score runs,
// and a scoring-side error (a malformed stored policy, a store outage)
// must not turn an otherwise-successful ProcessBounce call into an
// error for its caller.
func (p *BounceProcessor) score(ctx context.Context, ml store.MailingList, memberID store.MailingListMemberID, classification dsn.Classification) {
	listLabel := ml.PostingAddress
	policy, perr := ParseBouncePolicy(ml.BouncePolicyJSON)
	if perr != nil {
		p.Logger.ErrorContext(ctx, "maillist: stored bounce policy is malformed; skipping scoring",
			slog.String("activity", observe.ActivitySystem),
			slog.String("list", listLabel),
			slog.String("err", perr.Error()))
		return
	}
	resolved := policy.Resolve()
	weight, reason, ok := bounceWeight(resolved, classification)
	if !ok {
		return
	}

	now := p.Clock.Now()
	newScore, err := p.Meta.RecordMailingListMemberBounce(ctx, memberID, now, weight, resolved.DecayWindow)
	if err != nil {
		p.Logger.ErrorContext(ctx, "maillist: record bounce score failed",
			slog.String("activity", observe.ActivitySystem),
			slog.String("list", listLabel),
			slog.Uint64("member_id", uint64(memberID)),
			slog.String("err", err.Error()))
		return
	}
	observe.RegisterMailingListMetrics()
	p.updateBounceRateGauge(ctx, ml)

	if newScore < resolved.SuspendThreshold {
		return
	}
	transitioned, err := p.Meta.SuspendMailingListMemberIfActive(ctx, memberID)
	if err != nil {
		p.Logger.ErrorContext(ctx, "maillist: auto-suspend failed",
			slog.String("activity", observe.ActivitySystem),
			slog.String("list", listLabel),
			slog.Uint64("member_id", uint64(memberID)),
			slog.String("err", err.Error()))
		return
	}
	if !transitioned {
		// Already suspended (or in a state auto-suspend does not apply
		// to, e.g. unsubscribed/pending) -- another call already handled
		// (or will handle) the audit/metric/notify side effects, so this
		// call must not repeat them.
		return
	}

	member, err := p.Meta.GetMailingListMember(ctx, memberID)
	if err != nil {
		p.Logger.ErrorContext(ctx, "maillist: load suspended member for audit/notify failed",
			slog.String("activity", observe.ActivitySystem),
			slog.String("list", listLabel),
			slog.Uint64("member_id", uint64(memberID)),
			slog.String("err", err.Error()))
		return
	}

	observe.MailingListSuspendedTotal.WithLabelValues(listLabel, reason).Inc()
	p.updateBounceRateGauge(ctx, ml)
	p.auditSuspend(ctx, ml, member, classification, newScore, resolved.SuspendThreshold)
	p.notifyOwner(ctx, ml, member, classification, newScore, resolved.SuspendThreshold)
	p.Logger.WarnContext(ctx, "maillist: member auto-suspended on bounce threshold",
		slog.String("activity", observe.ActivityUser),
		slog.String("list", listLabel),
		slog.Uint64("member_id", uint64(memberID)),
		slog.String("classification", classification.String()),
		slog.Float64("score", newScore),
		slog.Float64("threshold", resolved.SuspendThreshold))
}

// updateBounceRateGauge recomputes and publishes the REQ-MLIST-54
// per-list bounce-rate metric: the fraction of the list's active+
// suspended roster that is currently suspended. Best-effort: a count
// error is logged, not returned, and leaves the gauge at its last
// published value rather than blocking the scoring decision it
// accompanies.
func (p *BounceProcessor) updateBounceRateGauge(ctx context.Context, ml store.MailingList) {
	counts, err := p.Meta.CountMailingListMembersByState(ctx, ml.ID)
	if err != nil {
		p.Logger.WarnContext(ctx, "maillist: count members by state failed; bounce-rate gauge not updated",
			slog.String("activity", observe.ActivitySystem),
			slog.String("list", ml.PostingAddress),
			slog.String("err", err.Error()))
		return
	}
	total := counts[store.MailingListMemberActive] + counts[store.MailingListMemberSuspended]
	if total == 0 {
		return
	}
	rate := float64(counts[store.MailingListMemberSuspended]) / float64(total)
	observe.MailingListBounceRate.WithLabelValues(ml.PostingAddress).Set(rate)
}

// auditSuspend records the REQ-MLIST-54 auto-suspend audit event.
// Best-effort: a store error here is logged but never blocks the
// suspension itself, which has already committed by the time this runs.
func (p *BounceProcessor) auditSuspend(ctx context.Context, ml store.MailingList, member store.MailingListMember, c dsn.Classification, score, threshold float64) {
	if p.Meta == nil {
		return
	}
	if err := p.Meta.AppendAuditLog(ctx, store.AuditLogEntry{
		At:        p.Clock.Now(),
		ActorKind: store.ActorSystem,
		ActorID:   "maillist",
		Action:    "maillist.member.autosuspend",
		Subject:   fmt.Sprintf("mailing_list:%d", ml.ID),
		Outcome:   store.OutcomeSuccess,
		Message: fmt.Sprintf("member_id=%d address=%s classification=%s score=%.3f threshold=%.3f",
			member.ID, memberAddressLabel(member), c.String(), score, threshold),
		Domain: ml.Domain,
	}); err != nil {
		p.Logger.WarnContext(ctx, "maillist: auto-suspend audit log append failed",
			slog.String("activity", observe.ActivitySystem),
			slog.String("list", ml.PostingAddress),
			slog.String("err", err.Error()))
	}
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
