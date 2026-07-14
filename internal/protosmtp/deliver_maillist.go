package protosmtp

import (
	"context"
	"log/slog"

	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/maillist"
	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// expandMailingList dispatches one DATA-accepted post addressed to a
// hosted mailing list (rc.mailingList set by cmdRCPT's REQ-MLIST-10
// lookup) to internal/maillist. It never returns an error to the
// caller: like the synthetic/forward branches above it in
// finishMessageWithBlob, a mailing-list recipient is always "handled"
// from the SMTP-reply point of view -- the guards inside Expand decide
// whether the post is fanned out or dropped-with-audit, and either
// outcome is a successful RCPT as far as the accepting MTA is
// concerned.
func (sess *session) expandMailingList(
	ctx context.Context,
	rc rcptEntry,
	msg mailparse.Message,
	finalBytes []byte,
	authResults mailauth.AuthResults,
) {
	ml := *rc.mailingList
	if sess.srv.mlistExpander == nil {
		sess.log.WarnContext(ctx, "maillist: no expander wired; post dropped",
			slog.String("activity", observe.ActivitySystem),
			slog.String("list", ml.PostingAddress))
		_ = sess.srv.store.Meta().AppendAuditLog(ctx, store.AuditLogEntry{
			At:         sess.srv.clk.Now(),
			ActorKind:  store.ActorSystem,
			ActorID:    "smtp",
			Action:     "maillist.post.dropped",
			Subject:    "mailing_list:" + ml.PostingAddress,
			Outcome:    store.OutcomeFailure,
			Message:    "no mailing-list expander wired",
			RemoteAddr: sess.remoteIP,
			Domain:     ml.Domain,
		})
		return
	}
	// REQ-MLIST-80 issue #189 security fix: thread the SASL/submission-
	// authenticated identity of THIS session (if any) through to the
	// posting-policy gate, so members-only/announce-only can trust it
	// outright instead of the forgeable RFC 5322 From: header. Zero
	// means "no AUTH on this session" (an anonymous/inbound MX
	// delivery), for which the policy gate falls back to the
	// DMARC/DKIM-backed From: check instead — see
	// internal/maillist/policy.go's authoritativePosterAddress.
	var submissionPrincipalID *store.PrincipalID
	if sess.authPrincipal != 0 {
		pid := sess.authPrincipal
		submissionPrincipalID = &pid
	}
	result, err := sess.srv.mlistExpander.Expand(ctx, maillist.ExpandInput{
		List:                     ml,
		Parsed:                   msg,
		Raw:                      finalBytes,
		Auth:                     authResults,
		DeploymentMaxMessageSize: sess.srv.opts.MaxMessageSize,
		SubmissionPrincipalID:    submissionPrincipalID,
	})
	if err != nil {
		sess.log.ErrorContext(ctx, "maillist: expand failed",
			slog.String("activity", observe.ActivitySystem),
			slog.String("list", ml.PostingAddress),
			slog.String("err", err.Error()))
		return
	}
	if result.Dropped {
		sess.log.InfoContext(ctx, "maillist: post dropped by guard",
			slog.String("activity", observe.ActivitySystem),
			slog.String("list", ml.PostingAddress),
			slog.String("reason", string(result.DropReason)))
		return
	}
	if result.Held {
		// REQ-MLIST-80: the `moderated` posting policy held this post for
		// an owner/moderator decision instead of fanning it out. As far
		// as the accepting MTA is concerned this is still a successful
		// RCPT (see the function doc comment above).
		sess.log.InfoContext(ctx, "maillist: post held for moderation",
			slog.String("activity", observe.ActivityUser),
			slog.String("list", ml.PostingAddress),
			slog.Uint64("held_post_id", uint64(result.HeldPostID)))
		return
	}
	sess.log.InfoContext(ctx, "maillist: post fanned out",
		slog.String("activity", observe.ActivityUser),
		slog.String("list", ml.PostingAddress),
		slog.Int("member_count", result.MemberCount))
	if result.Unsealed {
		sess.log.ErrorContext(ctx, "maillist: fan-out delivered unsealed (ARC seal failed)",
			slog.String("activity", observe.ActivitySystem),
			slog.String("list", ml.PostingAddress))
	}
}
