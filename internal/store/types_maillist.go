package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Hosted mailing lists, Stage 1 storage foundation (epic #183,
// docs/design/server/requirements/28-mailing-lists.md REQ-MLIST-01..12;
// docs/design/server/architecture/14-mailing-lists.md "Storage"). Schema
// commentary lives in storesqlite/migrations/0085_mailing_lists.sql.
//
// A list is a Group principal (REQ-MLIST-01) plus this list-specific
// configuration and a membership roster. Only Stage 1 semantics are
// exercised by the callers of this package today: a static,
// admin-maintained roster and fan-out to active/each members. The
// Stage 2-4 fields (BounceScore, LastBounceAt, MailingListMemberPending,
// MailingListDeliveryNoMail, ArchiveMailboxID, SubscribePolicy) are part
// of the schema now, per the requirements doc's stated intent to choose
// the data model once, but are not enforced or interpreted beyond
// storage by this package.

// MailingListID identifies a mailing_list row.
type MailingListID uint64

// MailingListMemberID identifies a mailing_list_member row.
type MailingListMemberID uint64

// MailingListPostingPolicy governs who may post to a list. S1 supports
// only MailingListPostingOpen; members-only / announce-only / moderated
// are reserved for a later stage (REQ-MLIST-80) and not enforced here.
type MailingListPostingPolicy string

// MailingListPostingOpen is the S1 posting policy: any address may post
// (subject to the loop/abuse guards, REQ-MLIST-30..32).
const MailingListPostingOpen MailingListPostingPolicy = "open"

// MailingListSubscribePolicy governs self-service subscription (Stage 3,
// REQ-MLIST-60). S1 lists are always MailingListSubscribeClosed; the
// other values are stored but not acted upon until Stage 3 ships.
type MailingListSubscribePolicy string

const (
	// MailingListSubscribeClosed is the S1 default: only an admin can
	// add members (no public subscribe endpoint).
	MailingListSubscribeClosed MailingListSubscribePolicy = "closed"
	// MailingListSubscribeRequestApproval exposes a public subscribe
	// endpoint whose requests need owner approval (Stage 3).
	MailingListSubscribeRequestApproval MailingListSubscribePolicy = "request-approval"
	// MailingListSubscribeOpen exposes a public subscribe endpoint with
	// double opt-in confirmation and no approval step (Stage 3).
	MailingListSubscribeOpen MailingListSubscribePolicy = "open"
)

// MailingListMemberState is a roster row's lifecycle state (REQ-MLIST-03).
// Only MailingListMemberActive members receive fan-out and count as
// recipients.
type MailingListMemberState string

const (
	// MailingListMemberActive receives fan-out (subject to DeliveryMode)
	// and counts as a recipient.
	MailingListMemberActive MailingListMemberState = "active"
	// MailingListMemberSuspended has delivery stopped (Stage 2
	// auto-suspend on bounce threshold, or an operator action) but
	// remains on the roster pending reactivation.
	MailingListMemberSuspended MailingListMemberState = "suspended"
	// MailingListMemberUnsubscribed opted out (Stage 2/3 unsubscribe
	// flow, or an operator removal that keeps history) and receives no
	// further mail.
	MailingListMemberUnsubscribed MailingListMemberState = "unsubscribed"
	// MailingListMemberPending awaits double opt-in confirmation
	// (Stage 3) or owner approval. Receives no mail while pending.
	MailingListMemberPending MailingListMemberState = "pending"
)

// MailingListDeliveryMode is a roster row's delivery mode (REQ-MLIST-04).
type MailingListDeliveryMode string

const (
	// MailingListDeliveryEach delivers one copy per fanned-out message.
	// The only mode fan-out acts on in S1.
	MailingListDeliveryEach MailingListDeliveryMode = "each"
	// MailingListDeliveryNoMail delivers no copy; the member reads posts
	// via the list's archive mailbox (Stage 4). Requires PrincipalID
	// (REQ-MLIST-04): a nomail member must be an internal, authenticatable
	// principal so it can be granted read access to the archive.
	MailingListDeliveryNoMail MailingListDeliveryMode = "nomail"
)

// MailingList is one row of the mailing_list table: a Group principal
// extended with list configuration (REQ-MLIST-01).
type MailingList struct {
	// ID is the assigned primary key.
	ID MailingListID
	// PrincipalID is the backing Group principal (store.PrincipalKindGroup).
	// The caller creates this principal beforehand; the store does not
	// itself validate its Kind. Immutable after Insert.
	PrincipalID PrincipalID
	// PostingAddress is the canonical list address (e.g.
	// "announce@example.com"), stored lowercased. Unique across the
	// deployment; this is the hot inbound-routing lookup key
	// (REQ-MLIST-10, GetMailingListByPostingAddress).
	PostingAddress string
	// Domain is the lowercased address-spec domain part of
	// PostingAddress, maintained by the store (any caller-supplied value
	// is ignored and recomputed on Insert/Update) so
	// domain-scoped-operator listing (REQ-MLIST-05) is an indexed
	// equality lookup.
	Domain string
	// DisplayName is used in the List-ID header (REQ-MLIST-20).
	DisplayName string
	// OwnerID is the owning principal (REQ-MLIST-05, REQ-ADM-307):
	// the list's domain-scoped operator or a super-admin. Deleting a
	// principal that still owns a list is rejected at the schema level
	// (ON DELETE RESTRICT) until ownership is reassigned.
	OwnerID PrincipalID
	// SubjectTag, when non-nil, is prepended ("[tag] ") to the Subject
	// of fanned-out copies (REQ-MLIST-23). Nil means unset (default).
	SubjectTag *string
	// ARCSeal controls whether fanned-out copies are ARC-sealed
	// (REQ-MLIST-21). The schema column defaults to true, but Insert
	// always writes this field's actual value (a bool cannot
	// distinguish "unset" from "explicitly false"), so callers that
	// want the REQ-MLIST-01 "default on" behaviour must set it to true
	// themselves before calling InsertMailingList.
	ARCSeal bool
	// PostingPolicy governs who may post. S1 always MailingListPostingOpen.
	PostingPolicy MailingListPostingPolicy
	// SubscribePolicy governs self-service subscription (Stage 3). S1
	// lists are always MailingListSubscribeClosed.
	SubscribePolicy MailingListSubscribePolicy
	// BouncePolicyJSON is the opaque, per-list Stage 2 bounce-scoring
	// policy (hard/soft weights, decay window, suspend threshold) as a
	// JSON object. The store treats it as opaque text; "{}" means
	// "deployment defaults" (REQ-MLIST-53). Not interpreted in S1.
	BouncePolicyJSON string
	// ArchiveMailboxID is the list's archive mailbox (Stage 4,
	// REQ-MLIST-70), or nil when the list has none. Not used in S1.
	ArchiveMailboxID *MailboxID
	// MaxMessageSizeBytes is the configurable per-list post-size ceiling
	// (REQ-MLIST-32). Zero means "use the deployment's default
	// MaxMessageSize" — the caller resolves that default, not this
	// package.
	MaxMessageSizeBytes int64
	// UnsubscribeEnabled gates the REQ-MLIST-56/57/58 List-Unsubscribe /
	// RFC 8058 one-click header pair and the token-authorised
	// self-service management page: when true, every fanned-out copy
	// carries a per-member token URL. Mirrors ARCSeal's own convention
	// (see that field's doc comment): the schema column defaults to
	// true (migration 0086), but InsertMailingList always writes this
	// field's actual Go value, so a caller that wants the default-on
	// behaviour must set it to true itself before calling
	// InsertMailingList.
	UnsubscribeEnabled bool
	// CreatedAt / UpdatedAt are the row lifecycle timestamps.
	CreatedAt time.Time
	UpdatedAt time.Time
}

// MailingListFilter narrows a ListMailingLists read.
type MailingListFilter struct {
	// Domain, when non-empty, restricts to lists whose Domain equals
	// this value (case-insensitive; the store lowercases before
	// comparing). Empty means no domain constraint.
	Domain string
	// Limit caps the result set (default and max 1000).
	Limit int
	// AfterID is the keyset cursor: rows whose ID > AfterID. Zero starts
	// at the first row.
	AfterID MailingListID
}

// MailingListMember is one row of the mailing_list_member table
// (REQ-MLIST-02). Exactly one of PrincipalID / ExternalAddress is set
// (enforced by a CHECK constraint as well as by AddMailingListMember).
type MailingListMember struct {
	// ID is the assigned primary key.
	ID MailingListMemberID
	// ListID is the owning list.
	ListID MailingListID
	// PrincipalID is set for an internal-principal member; nil for an
	// external-address member. Exactly one of PrincipalID /
	// ExternalAddress is non-nil.
	PrincipalID *PrincipalID
	// ExternalAddress is set for an external-address member, stored
	// canonicalised (lowercased, trimmed). Nil for a principal member.
	ExternalAddress *string
	// State is the roster lifecycle state (REQ-MLIST-03).
	State MailingListMemberState
	// DeliveryMode is each (S1) or nomail (Stage 4, requires PrincipalID).
	DeliveryMode MailingListDeliveryMode
	// BounceScore is the Stage 2 per-member bounce score. Zero in S1
	// (never updated by this package).
	BounceScore float64
	// LastBounceAt is the Stage 2 timestamp of the member's most recent
	// classified bounce, or nil. Never set by this package in S1.
	LastBounceAt *time.Time
	// AddedAt is the instant the row was inserted.
	AddedAt time.Time
	// AddedBy is the acting principal that added the member (an
	// operator, for S1's admin-only roster), or nil when unknown /
	// system-added.
	AddedBy *PrincipalID
}

// MailingListRosterFilter narrows a ListMailingListMembers read. It is
// the shape both admin roster browsing and the REQ-MLIST-12 fan-out scan
// use: fan-out sets State=MailingListMemberActive and
// DeliveryMode=MailingListDeliveryEach and pages via AfterID (or calls
// StreamActiveEachMembers, which does this looping for it). A large
// roster is never returned in one unbounded call: Limit is always capped
// server-side.
type MailingListRosterFilter struct {
	// ListID restricts to one list. Always required by callers in
	// practice; the zero value matches no rows (list ids start at 1).
	ListID MailingListID
	// State, when non-empty, restricts to that state. Empty means no
	// state constraint.
	State MailingListMemberState
	// DeliveryMode, when non-empty, restricts to that mode. Empty means
	// no delivery-mode constraint.
	DeliveryMode MailingListDeliveryMode
	// Limit caps the result set (default and max 1000).
	Limit int
	// AfterID is the keyset cursor: rows whose ID > AfterID. Zero starts
	// at the first row.
	AfterID MailingListMemberID
}

// NormalizeMailingListAddress lowercases and validates a mailing list's
// posting address, returning the canonicalised address and its
// address-spec domain part. Both storesqlite and storepg call this on
// Insert/Update so MailingList.Domain is always derived the same way
// regardless of backend, rather than trusting a caller-supplied value.
// Returns ErrInvalidArgument if addr is not a single-@ addr-spec.
func NormalizeMailingListAddress(addr string) (address, domain string, err error) {
	addr = strings.ToLower(strings.TrimSpace(addr))
	at := strings.LastIndexByte(addr, '@')
	if at <= 0 || at == len(addr)-1 {
		return "", "", fmt.Errorf("%w: posting address %q must be a single addr-spec", ErrInvalidArgument, addr)
	}
	return addr, addr[at+1:], nil
}

// CanonicalizeExternalAddress lowercases and trims a mailing-list member's
// external address (REQ-MLIST-02: "external_address (nullable,
// canonicalised)"), so two members that differ only in case or
// surrounding whitespace collide as the same address.
func CanonicalizeExternalAddress(addr string) string {
	return strings.ToLower(strings.TrimSpace(addr))
}

// maillistStreamPageSize bounds the per-page read StreamActiveEachMembers
// issues. Deliberately small and fixed so a roster of any size (REQ-STORE-06
// large-mailbox target applies equally to large lists) is walked with a
// bounded, constant-size working set rather than growing with roster size.
const maillistStreamPageSize = 500

// StreamActiveEachMembers walks listID's active, each-mode roster in
// ascending ID order, calling fn once per member. It is the fan-out entry
// point REQ-MLIST-12 requires: a large roster is read in fixed-size pages
// via ListMailingListMembers rather than materialised whole in memory.
// Stops and returns the first error fn returns, or the first store read
// error. Fan-out MUST use this (or an equivalent AfterID-paged loop over
// ListMailingListMembers) instead of any unbounded read.
func StreamActiveEachMembers(ctx context.Context, m Metadata, listID MailingListID, fn func(MailingListMember) error) error {
	after := MailingListMemberID(0)
	for {
		page, err := m.ListMailingListMembers(ctx, MailingListRosterFilter{
			ListID:       listID,
			State:        MailingListMemberActive,
			DeliveryMode: MailingListDeliveryEach,
			AfterID:      after,
			Limit:        maillistStreamPageSize,
		})
		if err != nil {
			return err
		}
		for _, mem := range page {
			if err := fn(mem); err != nil {
				return err
			}
			after = mem.ID
		}
		if len(page) < maillistStreamPageSize {
			return nil
		}
	}
}
