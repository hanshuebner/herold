package maillist

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/queue"
	"github.com/hanshuebner/herold/internal/store"
)

// Submitter is the narrow outbound-queue dependency Expand needs: one
// call per fan-out copy. Structurally identical to
// protosmtp.SubmissionQueue and to *queue.Queue itself, so either
// satisfies this interface without an adapter.
type Submitter interface {
	Submit(ctx context.Context, msg queue.Submission) (queue.EnvelopeID, error)
}

// Sealer performs REQ-MLIST-21 ARC-sealing. *mailarc.Sealer satisfies
// this directly.
type Sealer interface {
	Seal(ctx context.Context, msg []byte, prior mailauth.AuthResults, signingDomain string) ([]byte, error)
}

// Expander performs REQ-MLIST-10..12/20..24/30..32 for one accepted
// post to a list's posting_address: the loop/abuse guards, the
// render-time fan-out shaping, and the incremental, roster-streamed
// enqueue.
type Expander struct {
	Meta      store.Metadata
	Submitter Submitter
	// Sealer performs ARC-sealing. May be nil; when a list has
	// ARCSeal=true and Sealer is nil (or Sealer.Seal errors, e.g. no
	// active DKIM key for the domain), Expand logs an ERROR, records a
	// distinct "unsealed" fan-out outcome and audit row, and still fans
	// out unsealed rather than dropping the post -- dropping mail is the
	// worse failure, but the degradation must never be silent (REQ-MLIST-21,
	// issue #183).
	Sealer Sealer
	// TokenSigner mints the REQ-MLIST-50 per-member VERP envelope MAIL
	// FROM, and (when set) the REQ-MLIST-56 per-member List-Unsubscribe
	// token. May be nil (e.g. no deployment data key configured yet, or a
	// test that does not exercise VERP): Expand then falls back to the
	// S1 list-wide BounceAddress and logs a loud ERROR once per Expand
	// call, matching the Sealer-nil degrade-not-drop posture above --
	// dropping the post would be the worse failure, but a deployment
	// running without VERP attribution must be impossible to miss.
	TokenSigner *TokenSigner
	// PublicBaseURL is the externally-reachable base URL of the public
	// HTTP listener PublicServer is mounted on (e.g.
	// "https://mail.example.com"), used to render the REQ-MLIST-56
	// List-Unsubscribe token URL. Required only when a list has
	// UnsubscribeEnabled set; a list with UnsubscribeEnabled=false never
	// reads it. When UnsubscribeEnabled is true but PublicBaseURL is
	// empty (or TokenSigner is nil), Expand degrades by omitting the
	// header pair from that post's fan-out and logs a loud ERROR once
	// per Expand call, mirroring the TokenSigner-nil VERP degradation
	// above -- a list configured for unsubscribe support must not
	// silently stop advertising it.
	PublicBaseURL string
	// Blobs is the content-addressed blob store used to file a fanned-
	// out post into the list's archive mailbox (Stage 4, REQ-MLIST-70).
	// May be nil when no list ever has ArchiveMailboxID set (e.g. a
	// deployment or test that does not exercise archiving); fileArchive
	// degrades by logging loudly rather than dropping the post when a
	// list DOES have an archive configured but Blobs is nil.
	Blobs  store.Blobs
	Clock  clock.Clock
	Logger *slog.Logger
}

// unsealedOutcome is the fan-out-metric outcome label recorded when
// ARCSeal is enabled but sealing failed (REQ-MLIST-21, issue #183): the
// post is still delivered (dropping mail is the worse failure), but the
// outcome is distinct from a normal sealed "delivered" fan-out so a
// dashboard/alert can see the degradation.
const unsealedOutcome = "unsealed"

// deliveredOutcome is the fan-out-metric outcome label for a normal
// fan-out: either ARCSeal is disabled, or it is enabled and sealing
// succeeded.
const deliveredOutcome = "delivered"

// NewExpander constructs an Expander. clk defaults to clock.NewReal and
// logger to slog.Default when nil.
func NewExpander(meta store.Metadata, submitter Submitter, sealer Sealer, clk clock.Clock, logger *slog.Logger) *Expander {
	if clk == nil {
		clk = clock.NewReal()
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Expander{Meta: meta, Submitter: submitter, Sealer: sealer, Clock: clk, Logger: logger}
}

// ExpandInput is the per-post payload Expand consumes.
type ExpandInput struct {
	// List is the resolved mailing_list row the post targeted.
	List store.MailingList
	// Parsed is the already-parsed original message; Expand reads
	// List-ID / Auto-Submitted / Subject from its Headers.
	Parsed mailparse.Message
	// Raw is the original accepted message bytes (header block + body,
	// CRLF-terminated), UNMODIFIED. Expand never mutates or persists
	// this slice; ShapeMessage builds a fresh copy for fan-out.
	Raw []byte
	// Auth is the message's own verified authentication result (DKIM /
	// SPF / DMARC / ARC), computed once at inbound acceptance. Passed
	// to the Sealer as the "prior" hop so the ARC-Authentication-
	// Results reflects what this list observed on receipt.
	Auth mailauth.AuthResults
	// DeploymentMaxMessageSize is the accepting listener's own
	// MaxMessageSize, used as the REQ-MLIST-32 fallback when the list
	// itself has no MaxMessageSizeBytes override. Zero means "no
	// deployment-wide limit".
	DeploymentMaxMessageSize int64
}

// DropReason enumerates why Expand declined to fan out a post.
type DropReason string

const (
	// DropReasonNone is the zero value: the post was fanned out.
	DropReasonNone DropReason = ""
	// DropReasonLoop is REQ-MLIST-30: the message already carries this
	// list's own List-ID.
	DropReasonLoop DropReason = "loop"
	// DropReasonAutoSubmitted is REQ-MLIST-31.
	DropReasonAutoSubmitted DropReason = "auto_submitted"
	// DropReasonOversize is REQ-MLIST-32.
	DropReasonOversize DropReason = "oversize"
)

// ExpandResult reports the outcome of one Expand call.
type ExpandResult struct {
	// Dropped is true when a loop/abuse guard blocked the post; no
	// member received a copy.
	Dropped bool
	// DropReason names the guard that fired; DropReasonNone when
	// Dropped is false.
	DropReason DropReason
	// MemberCount is the number of active/each members successfully
	// submitted to the outbound queue.
	MemberCount int
	// Unsealed is true when List.ARCSeal is enabled but this post's
	// fan-out copies went out WITHOUT an ARC set because sealing failed
	// (no Sealer wired, or Sealer.Seal errored -- most commonly no active
	// DKIM key for the domain, e.g. after a mid-lifecycle key revocation).
	// The post is still fanned out (dropping mail is the worse failure);
	// Unsealed lets a caller observe the degradation beyond the ERROR log
	// line and the distinct metric/audit outcome Expand also records.
	Unsealed bool
	// Archived is true when the post was filed into the list's archive
	// mailbox (REQ-MLIST-70). Always false when List.ArchiveMailboxID is
	// nil; also false (with a loud ERROR log) if archiving was configured
	// but filing failed -- filing failure never drops or blocks fan-out.
	Archived bool
}

// Expand runs the REQ-MLIST-30..32 loop/abuse guards and, if the post
// passes, shapes one fan-out copy (REQ-MLIST-20..24) and submits it to
// the outbound queue once per active/each member, streamed incrementally
// from the roster via store.StreamActiveEachMembers (REQ-MLIST-12): a
// large roster is never held in memory as N full copies, only as the
// one shaped byte slice reused across every Submit call. The queue's
// content-addressed blob store dedups that identical slice down to one
// stored blob (REQ-MLIST-11) regardless of how many Submit calls
// reference it.
func (e *Expander) Expand(ctx context.Context, in ExpandInput) (ExpandResult, error) {
	observe.RegisterMailingListMetrics()
	start := e.Clock.Now()
	ml := in.List
	listLabel := ml.PostingAddress

	if LoopDetected(in.Parsed.Headers.Get("List-ID"), ml) {
		e.audit(ctx, ml, "maillist.post.dropped", "reason=loop")
		observe.MailingListFanoutTotal.WithLabelValues(listLabel, string(DropReasonLoop)).Inc()
		return ExpandResult{Dropped: true, DropReason: DropReasonLoop}, nil
	}
	if AutoSubmittedBlocks(in.Parsed.Headers.Get("Auto-Submitted")) {
		e.audit(ctx, ml, "maillist.post.dropped", "reason=auto_submitted")
		observe.MailingListFanoutTotal.WithLabelValues(listLabel, string(DropReasonAutoSubmitted)).Inc()
		return ExpandResult{Dropped: true, DropReason: DropReasonAutoSubmitted}, nil
	}
	if SizeExceeds(int64(len(in.Raw)), ml, in.DeploymentMaxMessageSize) {
		e.audit(ctx, ml, "maillist.post.dropped", fmt.Sprintf("reason=oversize size=%d", len(in.Raw)))
		observe.MailingListFanoutTotal.WithLabelValues(listLabel, string(DropReasonOversize)).Inc()
		return ExpandResult{Dropped: true, DropReason: DropReasonOversize}, nil
	}

	shaped := ShapeMessage(in.Raw, ml, in.Parsed.Headers.Get("Subject"))
	unsealed := false
	if ml.ARCSeal {
		switch {
		case e.Sealer == nil:
			e.Logger.ErrorContext(ctx, "maillist: arc_seal enabled but no Sealer wired; fanning out unsealed",
				slog.String("activity", observe.ActivitySystem),
				slog.String("list", listLabel))
			unsealed = true
			e.auditUnsealed(ctx, ml, "no Sealer wired")
		default:
			sealed, serr := e.Sealer.Seal(ctx, shaped, in.Auth, ml.Domain)
			if serr != nil {
				e.Logger.ErrorContext(ctx, "maillist: arc seal failed; fanning out unsealed",
					slog.String("activity", observe.ActivitySystem),
					slog.String("list", listLabel),
					slog.String("err", serr.Error()))
				unsealed = true
				e.auditUnsealed(ctx, ml, serr.Error())
			} else {
				shaped = sealed
			}
		}
	}

	// REQ-MLIST-70: file the post into the archive exactly once, using
	// the same shaped (optionally ARC-sealed) bytes every "each" member's
	// copy is built from -- independent of the member loop below, so a
	// list with only nomail members (or none at all) still archives.
	archived := e.fileArchive(ctx, ml, shaped, in.Parsed)

	if e.TokenSigner == nil {
		e.Logger.ErrorContext(ctx, "maillist: no TokenSigner wired; fanning out with the S1 list-wide bounce address instead of a per-member VERP token",
			slog.String("activity", observe.ActivitySystem),
			slog.String("list", listLabel))
	}

	// REQ-MLIST-56: per-member List-Unsubscribe / List-Unsubscribe-Post.
	// canUnsubscribe gates whether Submit calls below get a per-member
	// header pair spliced onto shaped; computed once per Expand call
	// (not per member) so the "missing capability" ERROR log fires once,
	// not once per roster row.
	canUnsubscribe := ml.UnsubscribeEnabled && e.TokenSigner != nil && e.PublicBaseURL != ""
	if ml.UnsubscribeEnabled && !canUnsubscribe {
		e.Logger.ErrorContext(ctx, "maillist: unsubscribe_enabled but no TokenSigner/PublicBaseURL wired; fanning out without List-Unsubscribe",
			slog.String("activity", observe.ActivitySystem),
			slog.String("list", listLabel))
	}

	memberCount := 0
	err := store.StreamActiveEachMembers(ctx, e.Meta, ml.ID, func(m store.MailingListMember) error {
		addr, aerr := e.memberAddress(ctx, m)
		if aerr != nil {
			e.Logger.WarnContext(ctx, "maillist: skip member with unresolved address",
				slog.String("activity", observe.ActivitySystem),
				slog.String("list", listLabel),
				slog.Uint64("member_id", uint64(m.ID)),
				slog.String("err", aerr.Error()))
			return nil
		}
		mailFrom, mfErr := e.bounceMailFrom(ml, m.ID)
		if mfErr != nil {
			e.Logger.WarnContext(ctx, "maillist: skip member; verp token sign failed",
				slog.String("activity", observe.ActivitySystem),
				slog.String("list", listLabel),
				slog.Uint64("member_id", uint64(m.ID)),
				slog.String("err", mfErr.Error()))
			return nil
		}
		var headerOverlay []byte
		if canUnsubscribe {
			unsubURL, uErr := UnsubscribeURL(e.PublicBaseURL, e.TokenSigner, ml, m.ID, e.Clock.Now())
			if uErr != nil {
				e.Logger.WarnContext(ctx, "maillist: skip List-Unsubscribe header; unsubscribe token sign failed",
					slog.String("activity", observe.ActivitySystem),
					slog.String("list", listLabel),
					slog.Uint64("member_id", uint64(m.ID)),
					slog.String("err", uErr.Error()))
			} else {
				headerOverlay = unsubscribeHeaderOverlay(unsubURL)
			}
		}
		// REQ-MLIST-11: shaped is the one list-wide-identical, already
		// (optionally) ARC-sealed byte slice reused across every member;
		// the per-member List-Unsubscribe pair rides on HeaderOverlay
		// instead of being spliced into Body, so the queue's
		// content-addressed blob store dedups every Submit call below
		// down to one stored blob regardless of roster size.
		if _, serr := e.Submitter.Submit(ctx, queue.Submission{
			MailFrom:      mailFrom,
			Recipients:    []string{addr},
			Body:          bytes.NewReader(shaped),
			HeaderOverlay: headerOverlay,
			Sign:          true,
			SigningDomain: ml.Domain,
			DSNNotify:     store.DSNNotifyFailure,
		}); serr != nil {
			e.Logger.WarnContext(ctx, "maillist: submit failed for member",
				slog.String("activity", observe.ActivitySystem),
				slog.String("list", listLabel),
				slog.Uint64("member_id", uint64(m.ID)),
				slog.String("err", serr.Error()))
			return nil
		}
		memberCount++
		return nil
	})
	if err != nil {
		return ExpandResult{}, fmt.Errorf("maillist: stream members: %w", err)
	}

	outcome := deliveredOutcome
	if unsealed {
		outcome = unsealedOutcome
	}
	observe.MailingListFanoutTotal.WithLabelValues(listLabel, outcome).Add(float64(memberCount))
	observe.MailingListMembers.WithLabelValues(listLabel, string(store.MailingListMemberActive)).Set(float64(memberCount))
	observe.MailingListExpandSeconds.WithLabelValues(listLabel).Observe(e.Clock.Now().Sub(start).Seconds())
	e.Logger.InfoContext(ctx, "maillist: fan-out complete",
		slog.String("activity", observe.ActivityUser),
		slog.String("list", listLabel),
		slog.Int("member_count", memberCount),
		slog.String("outcome", outcome),
		slog.Bool("archived", archived))
	return ExpandResult{MemberCount: memberCount, Unsealed: unsealed, Archived: archived}, nil
}

// memberAddress resolves the address one roster row fans out to: the
// external address verbatim, or the CanonicalEmail of the referenced
// principal (REQ-MLIST-02: member_ref is exactly one of the two).
func (e *Expander) memberAddress(ctx context.Context, m store.MailingListMember) (string, error) {
	return resolveMemberAddress(ctx, e.Meta, m)
}

// resolveMemberAddress is the package-level form of Expander.memberAddress,
// shared with PublicServer's resubscribe-confirmation email (publicserver.go),
// which needs the same member_ref -> deliverable-address resolution outside
// a fan-out Expand call.
func resolveMemberAddress(ctx context.Context, meta store.Metadata, m store.MailingListMember) (string, error) {
	if m.ExternalAddress != nil && *m.ExternalAddress != "" {
		return *m.ExternalAddress, nil
	}
	if m.PrincipalID != nil {
		p, err := meta.GetPrincipalByID(ctx, *m.PrincipalID)
		if err != nil {
			return "", fmt.Errorf("resolve principal %d: %w", *m.PrincipalID, err)
		}
		return p.CanonicalEmail, nil
	}
	return "", fmt.Errorf("member %d has neither external address nor principal id", m.ID)
}

// bounceMailFrom computes one fan-out copy's envelope MAIL FROM
// (REQ-MLIST-22/50): the per-member VERP token address when a
// TokenSigner is wired (Stage 2), falling back to the S1 list-wide
// BounceAddress otherwise.
func (e *Expander) bounceMailFrom(ml store.MailingList, memberID store.MailingListMemberID) (string, error) {
	if e.TokenSigner == nil {
		return BounceAddress(ml), nil
	}
	return VERPBounceAddress(e.TokenSigner, ml, memberID)
}

// audit records a loop/abuse-guard drop (REQ-MLIST-30: "dropped with an
// audit record"). Best-effort: a store error here is logged but never
// blocks the guard's own decision.
func (e *Expander) audit(ctx context.Context, ml store.MailingList, action, msg string) {
	if e.Meta == nil {
		return
	}
	if err := e.Meta.AppendAuditLog(ctx, store.AuditLogEntry{
		At:        e.Clock.Now(),
		ActorKind: store.ActorSystem,
		ActorID:   "maillist",
		Action:    action,
		Subject:   fmt.Sprintf("mailing_list:%d", ml.ID),
		Outcome:   store.OutcomeSuccess,
		Message:   msg,
		Domain:    ml.Domain,
	}); err != nil {
		e.Logger.WarnContext(ctx, "maillist: audit log append failed",
			slog.String("activity", observe.ActivitySystem),
			slog.String("list", ml.PostingAddress),
			slog.String("err", err.Error()))
	}
}

// auditUnsealed records REQ-MLIST-21's runtime-degradation case (issue
// #183): a list configured with ARCSeal=true failed to seal a fan-out
// copy -- most commonly, no active DKIM key for the domain, e.g. after a
// mid-lifecycle key revocation the config-time gate in protoadmin cannot
// catch. The post is still fanned out (dropping mail is the worse
// failure), but this audit row's Outcome is OutcomeFailure, distinct from
// every other maillist audit action's OutcomeSuccess, so the operator can
// tell an unsealed fan-out apart from a normal delivered one.
func (e *Expander) auditUnsealed(ctx context.Context, ml store.MailingList, reason string) {
	if e.Meta == nil {
		return
	}
	if err := e.Meta.AppendAuditLog(ctx, store.AuditLogEntry{
		At:        e.Clock.Now(),
		ActorKind: store.ActorSystem,
		ActorID:   "maillist",
		Action:    "maillist.fanout.unsealed",
		Subject:   fmt.Sprintf("mailing_list:%d", ml.ID),
		Outcome:   store.OutcomeFailure,
		Message:   fmt.Sprintf("arc_seal enabled but sealing failed; fanned out unsealed: %s", reason),
		Domain:    ml.Domain,
	}); err != nil {
		e.Logger.ErrorContext(ctx, "maillist: unsealed-fanout audit log append failed",
			slog.String("activity", observe.ActivitySystem),
			slog.String("list", ml.PostingAddress),
			slog.String("err", err.Error()))
	}
}
