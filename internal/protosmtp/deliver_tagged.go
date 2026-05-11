package protosmtp

import (
	"context"
	"errors"
	"log/slog"

	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/sieve"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/taggedaddr"
)

// taggedFilterEffect describes the per-recipient side-effects derived
// from a matched tagged_address_filters row (REQ-TAG-30..32). Zero
// value means "no tag filter applied" — the inbound pipeline acts as
// if the tagged-address subsystem were absent.
type taggedFilterEffect struct {
	// labelName is the mailbox/label the message MUST land in.
	// Always non-empty when this effect is set.
	labelName string
	// suppressImplicitKeep clears outcome.ImplicitKeep before sieve
	// target resolution (REQ-TAG-30 / REQ-TAG-31). True for
	// label_archive and label_archive_read; false for plain `label`.
	suppressImplicitKeep bool
	// markSeen ORs MessageFlagSeen onto the final flags on every
	// mailbox the message is filed into (REQ-TAG-30
	// label_archive_read). Sieve's own setflag is applied alongside.
	markSeen bool
	// action is the raw string from the store (`label`,
	// `label_archive`, `label_archive_read`). Carried purely for
	// metrics / logging.
	action string
}

// resolveTaggedAddress probes the store for a tagged-address filter
// that matches rc.addr's `+`-suffix on rc.principalID and returns the
// resulting effect. The returned bool is true when a filter matched;
// false means "no filter, no effect, fall through unchanged".
//
// REQ-TAG-20 step 1+2: suffix extraction + filter lookup. The lookup
// joins jmap_identities by lower(email) so the synthesised default
// identity (no row) is correctly skipped — filters against the default
// are unrepresentable until REST/JMAP materialises a row for it.
//
// All failure modes (no suffix, no filter, store error) collapse to
// (zero effect, false, nil) so the caller can branch on a single bool;
// the error path is logged but never blocks delivery (REQ-FLOW-22 /
// REQ-TAG-21 spirit: the per-message hot path must not surface
// transient store errors as bounces).
func (sess *session) resolveTaggedAddress(
	ctx context.Context,
	rc rcptEntry,
) (taggedFilterEffect, bool) {
	if !sess.srv.taggedAddrEnabled {
		return taggedFilterEffect{}, false
	}
	if rc.principalID == 0 {
		// Submission / synthetic / unresolved recipient — no inbound
		// filing decision to make.
		return taggedFilterEffect{}, false
	}
	_, suffix, baseEmail := taggedaddr.Split(rc.addr)
	if suffix == "" {
		return taggedFilterEffect{}, false
	}
	f, err := sess.srv.store.Meta().LookupTaggedAddressFilterForRecipient(
		ctx, rc.principalID, baseEmail, suffix)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return taggedFilterEffect{}, false
		}
		sess.log.WarnContext(ctx, "tagged-address: lookup failed; falling through to Sieve",
			slog.String("activity", observe.ActivitySystem),
			slog.String("recipient", rc.addr),
			slog.String("err", err.Error()))
		return taggedFilterEffect{}, false
	}
	eff := taggedFilterEffect{
		labelName: f.LabelName,
		action:    f.Action,
	}
	switch f.Action {
	case store.TaggedAddressActionLabel:
		// REQ-TAG-30: ADD to label_name, implicit keep is NOT
		// suppressed; INBOX still lands unless a later stage files
		// the message elsewhere or discards.
	case store.TaggedAddressActionLabelArchive:
		// REQ-TAG-30: ADD to label_name, suppress implicit keep so
		// INBOX is NOT added (unless Sieve later does fileinto Inbox
		// explicitly).
		eff.suppressImplicitKeep = true
	case store.TaggedAddressActionLabelArchiveRead:
		// REQ-TAG-30: same as label_archive PLUS the \Seen flag.
		eff.suppressImplicitKeep = true
		eff.markSeen = true
	default:
		// Defensive: a stale row with an action token outside the
		// allowed set should never reach here (the store CHECK +
		// validateTaggedAddressAction gate insert + update). Log and
		// fall through with no effect so the message still delivers.
		sess.log.WarnContext(ctx, "tagged-address: unknown action; falling through",
			slog.String("activity", observe.ActivityInternal),
			slog.String("recipient", rc.addr),
			slog.String("action", f.Action))
		return taggedFilterEffect{}, false
	}
	observe.TaggedAddressFilterMatchTotal.WithLabelValues(f.Action).Inc()
	return eff, true
}

// applyTaggedFilterPreSieve folds the tag-filter effect into the
// outcome BEFORE resolveSieveTargets reads it. This is the
// REQ-TAG-20-step-2-runs-before-step-4 contract: by clearing
// ImplicitKeep on the outcome up front, the existing sieve target
// resolver naturally honours the tag-filter's keep-suppression.
//
// Note: this runs on the outcome AFTER Sieve has produced it.
// resolveSieveTargets then transforms outcome -> target list. The
// effect: tag-filter's suppress-implicit-keep + Sieve's lack of own
// fileinto = no INBOX. Sieve's explicit `fileinto "Inbox"` survives
// because resolveSieveTargets honours ActionFileInto directly.
func (eff taggedFilterEffect) applyTaggedFilterPreSieve(out sieve.Outcome) sieve.Outcome {
	if eff.suppressImplicitKeep {
		out.ImplicitKeep = false
	}
	return out
}

// extraSeenFlag returns the MessageFlagSeen bit that label_archive_read
// adds on every mailbox membership row this delivery produces. Other
// actions return 0. This is OR'd alongside the existing
// sieveFlagsFromOutcome (so a Sieve setflag "\Seen" + a tag-filter
// label_archive_read both produce one Seen bit, not two).
func (eff taggedFilterEffect) extraSeenFlag() store.MessageFlags {
	if eff.markSeen {
		return store.MessageFlagSeen
	}
	return 0
}
