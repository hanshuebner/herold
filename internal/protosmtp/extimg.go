package protosmtp

import (
	"context"
	"log/slog"

	"github.com/hanshuebner/herold/internal/extimg"
	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/observe"
)

// REQ-EXTIMG-40 / REQ-EXTIMG-43: translate the inbound mailauth DKIM
// verdicts into the extimg-shaped DKIMVerdict the rebuilder uses to
// stamp the server-emitted Authentication-Results header. A message
// may be signed by multiple DKIM signatures; we pick the first
// passing one when present, else the first non-empty result. Empty
// input is rendered as "dkim=none" by extimg, matching RFC 8601.
func dkimVerdictFromAuth(results []mailauth.DKIMResult) extimg.DKIMVerdict {
	if len(results) == 0 {
		return extimg.DKIMVerdict{}
	}
	pick := results[0]
	for _, r := range results {
		if r.Status == mailauth.AuthPass {
			pick = r
			break
		}
	}
	if pick.Status == mailauth.AuthUnknown {
		return extimg.DKIMVerdict{}
	}
	return extimg.DKIMVerdict{
		Result:        pick.Status.String(),
		SigningDomain: pick.Domain,
		Selector:      pick.Selector,
	}
}

// logExtImgOutcome emits a single per-message INFO log line summarising
// what the rewriter did (REQ-EXTIMG-81). The log line is stable and
// aggregable: one entry per Internalize call regardless of result. The
// audit map is unrolled into individual fields so log aggregators can
// alert on e.g. a sustained blocked_ssrf rate without parsing JSON.
func (sess *session) logExtImgOutcome(ctx context.Context, sum extimg.AuditSummary, err error) {
	attrs := []slog.Attr{
		slog.String("activity", observe.ActivitySystem),
		slog.String("subsystem", "protosmtp"),
		slog.String("mode", string(sum.Mode)),
		slog.Bool("modified", sum.Modified),
		slog.Int("html_parts", sum.HTMLPartsScanned),
		slog.Int("candidates", sum.Candidates),
		slog.Int("internalized", sum.Internalized),
		slog.Int("failed", sum.Failed),
		slog.Int("original_size", sum.OriginalSize),
		slog.Int("rewritten_size", sum.RewrittenSize),
		slog.Duration("wall", sum.WallClock),
	}
	if sum.NotEligibleReason != "" {
		attrs = append(attrs, slog.String("not_eligible", sum.NotEligibleReason))
	}
	if sum.ParseError != "" {
		attrs = append(attrs, slog.String("parse_error", sum.ParseError))
	}
	if sum.TruncatedAtImage > 0 {
		attrs = append(attrs, slog.Int("truncated_at", sum.TruncatedAtImage))
	}
	for outcome, n := range sum.FailureCounts {
		if n > 0 {
			attrs = append(attrs, slog.Int("fail_"+string(outcome), n))
		}
	}
	level := slog.LevelInfo
	msg := "smtp data: extimg rewrite"
	if err != nil {
		level = slog.LevelWarn
		msg = "smtp data: extimg error"
		attrs = append(attrs, slog.String("err", err.Error()))
	}
	sess.log.LogAttrs(ctx, level, msg, attrs...)
}
