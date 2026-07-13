package protoadmin

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/hanshuebner/herold/internal/maillist"
	"github.com/hanshuebner/herold/internal/store"
)

// Hosted mailing lists, Stage 1 admin REST (epic #183,
// docs/design/server/requirements/28-mailing-lists.md REQ-MLIST-40a).
// This file is the DTO layer over store.MailingList / store.MailingListMember;
// mlist.go and mlist_members.go hold the handlers and authorization.

// mailingListDTO is the wire representation of a MailingList row.
type mailingListDTO struct {
	ID               uint64 `json:"id"`
	PrincipalID      uint64 `json:"principal_id"`
	PostingAddress   string `json:"posting_address"`
	Domain           string `json:"domain"`
	DisplayName      string `json:"display_name"`
	OwnerPrincipalID uint64 `json:"owner_principal_id"`
	SubjectTag       string `json:"subject_tag,omitempty"`
	ARCSeal          bool   `json:"arc_seal"`
	// DKIMKeyMissing is true when ARCSeal is enabled but Domain currently
	// has no active DKIM key (REQ-MLIST-21, issue #183): sealing will fail
	// at fan-out time and copies go out unsealed. Only ever set when
	// ARCSeal is true; omitted (false) otherwise. Computed at read time
	// from store.GetActiveDKIMKey, not stored on the row, so it always
	// reflects the domain's current key state even if the key was
	// generated or revoked after the list was configured.
	DKIMKeyMissing      bool   `json:"dkim_key_missing,omitempty"`
	PostingPolicy       string `json:"posting_policy"`
	SubscribePolicy     string `json:"subscribe_policy"`
	MaxMessageSizeBytes int64  `json:"max_message_size_bytes,omitempty"`
	// UnsubscribeEnabled gates the REQ-MLIST-56/57/58 List-Unsubscribe /
	// RFC 8058 one-click header pair and the self-service management
	// page (epic #184).
	UnsubscribeEnabled bool `json:"unsubscribe_enabled"`
	// BouncePolicy is the REQ-MLIST-53 per-list bounce-scoring policy,
	// RESOLVED: every field always reflects the value actually in effect
	// (the operator's override, or the deployment default), never a
	// sparse partial. This is what the operator bounce/suspension view
	// (issue #184) reads.
	BouncePolicy bouncePolicyDTO `json:"bounce_policy"`
	// ArchiveEnabled is true when the list owns an archive mailbox
	// (Stage 4, REQ-MLIST-70). Read-only here; toggled via PATCH
	// archive_enabled.
	ArchiveEnabled bool `json:"archive_enabled"`
	// ArchiveMailboxName is the archive's canonical mailbox Name
	// (maillist.ArchiveMailboxName), the string a member SELECTs over
	// IMAP or looks up by id over JMAP. Empty when ArchiveEnabled is
	// false.
	ArchiveMailboxName string `json:"archive_mailbox_name,omitempty"`
	// ArchiveRetentionDays / ArchiveRetentionMaxMessages are the S4
	// archive age/count retention bounds (REQ-MLIST-74). 0 means
	// unbounded.
	ArchiveRetentionDays        int64     `json:"archive_retention_days,omitempty"`
	ArchiveRetentionMaxMessages int64     `json:"archive_retention_max_messages,omitempty"`
	CreatedAt                   time.Time `json:"created_at"`
	UpdatedAt                   time.Time `json:"updated_at"`
}

func toMailingListDTO(l store.MailingList) mailingListDTO {
	dto := mailingListDTO{
		ID:                          uint64(l.ID),
		PrincipalID:                 uint64(l.PrincipalID),
		PostingAddress:              l.PostingAddress,
		Domain:                      l.Domain,
		DisplayName:                 l.DisplayName,
		OwnerPrincipalID:            uint64(l.OwnerID),
		ARCSeal:                     l.ARCSeal,
		PostingPolicy:               string(l.PostingPolicy),
		SubscribePolicy:             string(l.SubscribePolicy),
		MaxMessageSizeBytes:         l.MaxMessageSizeBytes,
		UnsubscribeEnabled:          l.UnsubscribeEnabled,
		BouncePolicy:                toBouncePolicyDTO(l.BouncePolicyJSON),
		ArchiveRetentionDays:        l.ArchiveRetentionDays,
		ArchiveRetentionMaxMessages: l.ArchiveRetentionMaxMessages,
		CreatedAt:                   l.CreatedAt,
		UpdatedAt:                   l.UpdatedAt,
	}
	if l.SubjectTag != nil {
		dto.SubjectTag = *l.SubjectTag
	}
	if l.ArchiveMailboxID != nil {
		dto.ArchiveEnabled = true
		dto.ArchiveMailboxName = maillist.ArchiveMailboxName(l)
	}
	return dto
}

// bouncePolicyDTO is the resolved (defaults-applied) REQ-MLIST-53
// per-list bounce-scoring policy wire shape.
type bouncePolicyDTO struct {
	HardWeight         float64 `json:"hard_weight"`
	SoftWeight         float64 `json:"soft_weight"`
	DecayWindowSeconds int64   `json:"decay_window_seconds"`
	SuspendThreshold   float64 `json:"suspend_threshold"`
}

// toBouncePolicyDTO resolves raw (store.MailingList.BouncePolicyJSON)
// against the deployment defaults. A malformed stored value (should not
// occur -- writes are validated, see applyBouncePolicyPatch) falls back
// to an all-defaults policy rather than failing the whole list read.
func toBouncePolicyDTO(raw string) bouncePolicyDTO {
	pol, err := maillist.ParseBouncePolicy(raw)
	if err != nil {
		pol = maillist.BouncePolicy{}
	}
	r := pol.Resolve()
	return bouncePolicyDTO{
		HardWeight:         r.HardWeight,
		SoftWeight:         r.SoftWeight,
		DecayWindowSeconds: int64(r.DecayWindow / time.Second),
		SuspendThreshold:   r.SuspendThreshold,
	}
}

// bouncePolicyPatchDTO is the create/patch request shape for
// bounce_policy: every field is optional, and only the fields present
// override the list's current (or default) policy -- the same partial-
// update convention every other mailing-list PATCH field already uses.
type bouncePolicyPatchDTO struct {
	HardWeight         *float64 `json:"hard_weight,omitempty"`
	SoftWeight         *float64 `json:"soft_weight,omitempty"`
	DecayWindowSeconds *int64   `json:"decay_window_seconds,omitempty"`
	SuspendThreshold   *float64 `json:"suspend_threshold,omitempty"`
}

// applyBouncePolicyPatch merges patch onto the policy currently stored
// as currentRaw and returns the new BouncePolicyJSON value. Returns a
// user-facing validation error string (never touches the store) if any
// provided field is out of range: weights must be non-negative, and
// the threshold and decay window must be strictly positive (a
// suspend_threshold of 0 or negative would suspend on the very first
// classified bounce regardless of weight, and a decay_window <= 0 has
// its own explicit "never decay" meaning at the store layer that a
// per-list config field should not silently produce).
func applyBouncePolicyPatch(currentRaw string, patch bouncePolicyPatchDTO) (string, string) {
	pol, err := maillist.ParseBouncePolicy(currentRaw)
	if err != nil {
		pol = maillist.BouncePolicy{}
	}
	if patch.HardWeight != nil {
		if *patch.HardWeight < 0 {
			return "", "bounce_policy.hard_weight must be non-negative"
		}
		pol.HardWeight = *patch.HardWeight
	}
	if patch.SoftWeight != nil {
		if *patch.SoftWeight < 0 {
			return "", "bounce_policy.soft_weight must be non-negative"
		}
		pol.SoftWeight = *patch.SoftWeight
	}
	if patch.DecayWindowSeconds != nil {
		if *patch.DecayWindowSeconds <= 0 {
			return "", "bounce_policy.decay_window_seconds must be positive"
		}
		pol.DecayWindowSeconds = *patch.DecayWindowSeconds
	}
	if patch.SuspendThreshold != nil {
		if *patch.SuspendThreshold <= 0 {
			return "", "bounce_policy.suspend_threshold must be positive"
		}
		pol.SuspendThreshold = *patch.SuspendThreshold
	}
	b, err := json.Marshal(pol)
	if err != nil {
		// Marshalling a struct of plain numeric fields cannot fail; kept
		// as a defensive fallback rather than a panic.
		return "", fmt.Sprintf("encode bounce_policy: %v", err)
	}
	return string(b), ""
}

// mailingListMemberDTO is the wire representation of a roster row
// (REQ-MLIST-02). Exactly one of PrincipalID / ExternalAddress is
// non-zero, mirroring the store's XOR constraint.
type mailingListMemberDTO struct {
	ID              uint64     `json:"id"`
	ListID          uint64     `json:"list_id"`
	PrincipalID     uint64     `json:"principal_id,omitempty"`
	ExternalAddress string     `json:"external_address,omitempty"`
	State           string     `json:"state"`
	DeliveryMode    string     `json:"delivery_mode"`
	BounceScore     float64    `json:"bounce_score,omitempty"`
	LastBounceAt    *time.Time `json:"last_bounce_at,omitempty"`
	AddedAt         time.Time  `json:"added_at"`
	AddedBy         uint64     `json:"added_by,omitempty"`
}

func toMailingListMemberDTO(m store.MailingListMember) mailingListMemberDTO {
	dto := mailingListMemberDTO{
		ID:           uint64(m.ID),
		ListID:       uint64(m.ListID),
		State:        string(m.State),
		DeliveryMode: string(m.DeliveryMode),
		BounceScore:  m.BounceScore,
		LastBounceAt: m.LastBounceAt,
		AddedAt:      m.AddedAt,
	}
	if m.PrincipalID != nil {
		dto.PrincipalID = uint64(*m.PrincipalID)
	}
	if m.ExternalAddress != nil {
		dto.ExternalAddress = *m.ExternalAddress
	}
	if m.AddedBy != nil {
		dto.AddedBy = uint64(*m.AddedBy)
	}
	return dto
}

// mlistStateFromString maps the wire vocabulary to store.MailingListMemberState.
// An empty string is accepted and means "no filter" / "use the store default".
func mlistStateFromString(s string) (store.MailingListMemberState, bool) {
	switch s {
	case "":
		return "", true
	case string(store.MailingListMemberActive):
		return store.MailingListMemberActive, true
	case string(store.MailingListMemberSuspended):
		return store.MailingListMemberSuspended, true
	case string(store.MailingListMemberUnsubscribed):
		return store.MailingListMemberUnsubscribed, true
	case string(store.MailingListMemberPending):
		return store.MailingListMemberPending, true
	case string(store.MailingListMemberAwaitingApproval):
		return store.MailingListMemberAwaitingApproval, true
	default:
		return "", false
	}
}

// mlistSubscribePolicyFromString maps the wire vocabulary to
// store.MailingListSubscribePolicy (Stage 3, REQ-MLIST-60, issue #185).
// An empty string is accepted and means "leave unset" (caller resolves
// the store default, closed).
func mlistSubscribePolicyFromString(s string) (store.MailingListSubscribePolicy, bool) {
	switch s {
	case "":
		return "", true
	case string(store.MailingListSubscribeClosed):
		return store.MailingListSubscribeClosed, true
	case string(store.MailingListSubscribeRequestApproval):
		return store.MailingListSubscribeRequestApproval, true
	case string(store.MailingListSubscribeOpen):
		return store.MailingListSubscribeOpen, true
	default:
		return "", false
	}
}

// mlistDeliveryModeFromString maps the wire vocabulary to
// store.MailingListDeliveryMode. An empty string means "no filter" / "use
// the store default (each)".
func mlistDeliveryModeFromString(s string) (store.MailingListDeliveryMode, bool) {
	switch s {
	case "":
		return "", true
	case string(store.MailingListDeliveryEach):
		return store.MailingListDeliveryEach, true
	case string(store.MailingListDeliveryNoMail):
		return store.MailingListDeliveryNoMail, true
	default:
		return "", false
	}
}
