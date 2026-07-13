package protosmtp

import (
	"context"
	"log/slog"

	"github.com/hanshuebner/herold/internal/maillist"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// expandMailingListBounce dispatches one DATA-accepted post addressed
// to a hosted list's VERP bounce-address shape (rc.mailingListBounce
// set by cmdRCPT's REQ-MLIST-51 shape match) to internal/maillist's
// bounce processor. Like expandMailingList, this never returns an SMTP
// error to the caller: an unverifiable token is REQ-MLIST-52's
// "logged, not applied to any member" case, not a rejected RCPT --
// rejecting would let a probing sender learn which bounce addresses
// verify.
func (sess *session) expandMailingListBounce(
	ctx context.Context,
	rc rcptEntry,
	finalBytes []byte,
) {
	target := *rc.mailingListBounce
	if sess.srv.mlistBounce == nil {
		sess.log.WarnContext(ctx, "maillist: no bounce processor wired; bounce dropped",
			slog.String("activity", observe.ActivitySystem),
			slog.String("list", target.List.PostingAddress))
		_ = sess.srv.store.Meta().AppendAuditLog(ctx, store.AuditLogEntry{
			At:         sess.srv.clk.Now(),
			ActorKind:  store.ActorSystem,
			ActorID:    "smtp",
			Action:     "maillist.bounce.dropped",
			Subject:    "mailing_list:" + target.List.PostingAddress,
			Outcome:    store.OutcomeFailure,
			Message:    "no mailing-list bounce processor wired",
			RemoteAddr: sess.remoteIP,
			Domain:     target.List.Domain,
		})
		return
	}
	result, err := sess.srv.mlistBounce.ProcessBounce(ctx, maillist.BounceInput{
		List:  target.List,
		Token: target.Token,
		Raw:   finalBytes,
	})
	if err != nil {
		sess.log.ErrorContext(ctx, "maillist: process bounce failed",
			slog.String("activity", observe.ActivitySystem),
			slog.String("list", target.List.PostingAddress),
			slog.String("err", err.Error()))
		return
	}
	if !result.Attributed {
		// Already logged/audited inside ProcessBounce; nothing further
		// to do here (REQ-MLIST-52).
		return
	}
	sess.log.InfoContext(ctx, "maillist: bounce attributed",
		slog.String("activity", observe.ActivityUser),
		slog.String("list", target.List.PostingAddress),
		slog.Uint64("member_id", uint64(result.MemberID)),
		slog.String("classification", result.Classification.String()))
}
