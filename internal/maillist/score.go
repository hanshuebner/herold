package maillist

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/hanshuebner/herold/internal/dsn"
)

// Bounce scoring policy (issue #184, REQ-MLIST-53): the weights, decay
// window, and suspend threshold that BounceProcessor.ProcessBounce
// applies after internal/dsn classifies a bounce. The policy is
// per-list (store.MailingList.BouncePolicyJSON); every field follows
// the "zero means use the deployment default" convention already used
// by store.MailingList.MaxMessageSizeBytes, so an operator only needs
// to set the field(s) they want to override.

// DefaultHardBounceWeight is the score a single hard (5.x.x, Action:
// failed) bounce contributes. Combined with DefaultSuspendThreshold,
// one hard bounce alone crosses the threshold (REQ-MLIST-53's stated
// default: "one hard bounce ... crosses the threshold").
const DefaultHardBounceWeight = 1.0

// DefaultSoftBounceWeight is the score a single soft (4.x.x, transient)
// bounce contributes. At the default threshold this means four soft
// bounces are needed to cross it, as long as no gap between them exceeds
// DefaultDecayWindow.
const DefaultSoftBounceWeight = 0.25

// DefaultDecayWindow bounds how long an accumulated soft-bounce score
// survives without a fresh bounce: if the gap since the member's last
// bounce exceeds this window, the prior score is discarded rather than
// carried forward (REQ-MLIST-53: "a soft bounce from months ago must
// not accumulate forever"). One week means a list that only sees
// sporadic, isolated soft bounces never accumulates toward suspension,
// while a member whose mail keeps failing every fan-out (typically
// days apart) keeps accumulating until the threshold is crossed --
// operationalising "soft bounces spanning more than a configured
// window crosses the threshold".
const DefaultDecayWindow = 7 * 24 * time.Hour

// DefaultSuspendThreshold is the bounce_score at or above which a member
// is auto-suspended (REQ-MLIST-54).
const DefaultSuspendThreshold = 1.0

// BouncePolicy is the JSON shape persisted in
// store.MailingList.BouncePolicyJSON (REQ-MLIST-53). Every field is
// optional; a zero value means "use the deployment default" (see the
// Default* constants and Resolve). This mirrors
// store.MailingList.MaxMessageSizeBytes's existing "zero means
// deployment default" convention for consistency across the package.
type BouncePolicy struct {
	// HardWeight is the score one hard bounce contributes. Zero means
	// DefaultHardBounceWeight.
	HardWeight float64 `json:"hard_weight,omitempty"`
	// SoftWeight is the score one soft bounce contributes. Zero means
	// DefaultSoftBounceWeight.
	SoftWeight float64 `json:"soft_weight,omitempty"`
	// DecayWindowSeconds bounds how long an accumulated score survives a
	// gap with no bounce (see DefaultDecayWindow). Zero means
	// DefaultDecayWindow. Stored as seconds (not time.Duration's
	// nanosecond JSON encoding) so the persisted JSON is human-readable
	// and stable across any future unit change to time.Duration's own
	// marshalling.
	DecayWindowSeconds int64 `json:"decay_window_seconds,omitempty"`
	// SuspendThreshold is the bounce_score at or above which a member is
	// auto-suspended. Zero means DefaultSuspendThreshold.
	SuspendThreshold float64 `json:"suspend_threshold,omitempty"`
}

// ResolvedBouncePolicy is BouncePolicy with every default substituted,
// ready for BounceProcessor's scoring arithmetic and for the admin
// surface's read-time display (issue #184: the operator bounce view
// always shows the value actually in effect, not a sparse override).
type ResolvedBouncePolicy struct {
	HardWeight       float64
	SoftWeight       float64
	DecayWindow      time.Duration
	SuspendThreshold float64
}

// Resolve substitutes the Default* constant for every zero-valued field.
func (p BouncePolicy) Resolve() ResolvedBouncePolicy {
	r := ResolvedBouncePolicy{
		HardWeight:       p.HardWeight,
		SoftWeight:       p.SoftWeight,
		DecayWindow:      time.Duration(p.DecayWindowSeconds) * time.Second,
		SuspendThreshold: p.SuspendThreshold,
	}
	if r.HardWeight <= 0 {
		r.HardWeight = DefaultHardBounceWeight
	}
	if r.SoftWeight <= 0 {
		r.SoftWeight = DefaultSoftBounceWeight
	}
	if r.DecayWindow <= 0 {
		r.DecayWindow = DefaultDecayWindow
	}
	if r.SuspendThreshold <= 0 {
		r.SuspendThreshold = DefaultSuspendThreshold
	}
	return r
}

// ParseBouncePolicy decodes a store.MailingList.BouncePolicyJSON value.
// An empty string or "{}" (the store's own default, InsertMailingList)
// is the zero BouncePolicy -- every field resolves to its deployment
// default. Returns an error only for structurally malformed JSON;
// callers MUST treat that fail-closed (skip scoring entirely) rather
// than guess a policy, matching the conservative posture the DSN parser
// already established for classification.
func ParseBouncePolicy(raw string) (BouncePolicy, error) {
	if raw == "" || raw == "{}" {
		return BouncePolicy{}, nil
	}
	var p BouncePolicy
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return BouncePolicy{}, fmt.Errorf("maillist: parse bounce policy: %w", err)
	}
	return p, nil
}

// bounceWeight returns the ResolvedBouncePolicy weight and metric-label
// reason for one classified bounce. ok is false for
// dsn.ClassificationUnknown: REQ-MLIST-53 scores classified bounces
// only, and an Unknown classification is exactly the DSN parser's "no
// confident signal" outcome (internal/dsn's stated conservatism) --
// scoring it would defeat that conservatism, so it is skipped entirely
// rather than guessed as either hard or soft.
func bounceWeight(r ResolvedBouncePolicy, c dsn.Classification) (weight float64, reason string, ok bool) {
	switch c {
	case dsn.ClassificationHard:
		return r.HardWeight, "hard", true
	case dsn.ClassificationSoft:
		return r.SoftWeight, "soft", true
	default:
		return 0, "", false
	}
}
