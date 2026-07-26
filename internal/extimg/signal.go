package extimg

// Issue #271: the JMAP Email object exposes two values derived from a
// message's per-outcome image-fetch failure tally
// (AuditSummary.FailureCounts / RetryResult.FailureCounts) --
// retryableFailedImageCount and failedImageReason. This file defines
// the failedImageReason category enum and the reduction that produces
// both values from a tally.

// FailedImageReason is the stable, client-facing category derived
// from a message's permanent (non-retryable) image-fetch failures.
// FailedImageReasonNone means every failure -- if any -- is
// retryable, so no permanent reason applies.
type FailedImageReason string

const (
	FailedImageReasonNone        FailedImageReason = ""
	FailedImageReasonBlocked     FailedImageReason = "blocked_by_policy"
	FailedImageReasonNotFound    FailedImageReason = "not_found"
	FailedImageReasonUnsupported FailedImageReason = "unsupported"
	FailedImageReasonTooLarge    FailedImageReason = "too_large"
	FailedImageReasonOther       FailedImageReason = "other"
)

// reasonPriority is the tie-break order applied when a message's
// permanent failures span more than one category at the same count:
// the earlier entry in this list wins.
var reasonPriority = []FailedImageReason{
	FailedImageReasonBlocked,
	FailedImageReasonNotFound,
	FailedImageReasonUnsupported,
	FailedImageReasonTooLarge,
	FailedImageReasonOther,
}

// categoryFor maps one permanent FetchOutcome to its FailedImageReason
// category (issue #271 contract):
//
//	blocked_by_policy <- blocked_ssrf, blocked_scheme
//	not_found         <- http_4xx
//	unsupported       <- not_image
//	too_large         <- too_large, empty
//	other             <- redirect_loop and any other permanent outcome
//
// Callers only invoke this for outcomes that are not Retryable().
func categoryFor(o FetchOutcome) FailedImageReason {
	switch o {
	case FetchBlockedSSRF, FetchBlockedScheme:
		return FailedImageReasonBlocked
	case FetchHTTP4xx:
		return FailedImageReasonNotFound
	case FetchNotImage:
		return FailedImageReasonUnsupported
	case FetchTooLarge, FetchEmpty:
		return FailedImageReasonTooLarge
	default:
		return FailedImageReasonOther
	}
}

// DeriveFailureSignal reduces a per-outcome failure tally into the two
// values the JMAP Email object exposes: retryable is the count of
// failures whose FetchOutcome.Retryable() is true; reason is the
// FailedImageReason with the highest count among the remaining
// (permanent) failures, ties broken by reasonPriority order. Returns
// (0, FailedImageReasonNone) for a nil, empty, or all-retryable tally.
func DeriveFailureSignal(counts map[FetchOutcome]int) (retryable int, reason FailedImageReason) {
	categoryCounts := make(map[FailedImageReason]int, len(reasonPriority))
	for outcome, n := range counts {
		if n <= 0 {
			continue
		}
		if outcome.Retryable() {
			retryable += n
			continue
		}
		categoryCounts[categoryFor(outcome)] += n
	}
	best := FailedImageReasonNone
	bestCount := 0
	for _, cat := range reasonPriority {
		if c := categoryCounts[cat]; c > bestCount {
			best = cat
			bestCount = c
		}
	}
	return retryable, best
}
