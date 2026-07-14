package maillist

// archive.go implements Stage 4's archive mailbox (epic #187,
// docs/design/server/requirements/28-mailing-lists.md REQ-MLIST-70..74;
// docs/design/server/architecture/14-mailing-lists.md "Archive mailbox
// and access"):
//
//   - fileArchive files one fanned-out post into the list's archive
//     mailbox exactly once, via the normal content-addressed blob store
//     (REQ-MLIST-70) -- the SAME bytes (and therefore the same BLAKE3
//     hash) every "each" member's copy is built from, so the archive
//     copy references the identical blob rather than a fresh one
//     (REQ-MLIST-11's dedup guarantee extended to the archive).
//   - SyncMemberArchiveGrant / RevokeAllArchiveGrants /
//     GrantExistingActiveMembersArchiveAccess maintain the REQ-MLIST-72
//     mailbox:read grant on the archive as the roster changes, reusing
//     the existing cross-principal mailbox-grant substrate
//     (internal/store's SetMailboxACL/RemoveMailboxACL,
//     docs/design/server/requirements/07-access-control.md REQ-AC-50..53)
//     -- there is no list-specific ACL type (REQ-AC-52).
//
// No protocol change was needed for read-only IMAP/JMAP/Suite access to
// light up once the grant exists: internal/authz.ResolveMailboxRights and
// store.Meta().ListMailboxesAccessibleBy already treat every `mailbox`
// grant row uniformly regardless of who wrote it (SETACL, the admin ACL
// REST surface, or this file) -- see internal/protoimap/acl.go's own
// header comment, written when that substrate landed (epic #210), naming
// "the mailing-list archive read grant, REQ-AC-52" as an anticipated
// consumer.

import (
	"bytes"
	"context"
	"fmt"
	"net/mail"
	"strings"

	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/store"
)

// ArchiveMailboxName returns the canonical mailbox Name used for ml's
// archive (REQ-MLIST-70), when created via GrantExistingActiveMembersArchiveAccess's
// caller (the admin REST archive-enable path). Namespaced under "Lists/"
// and keyed by the list's own globally-unique PostingAddress so it can
// never collide with a member's own personal mailbox of the same name
// (e.g. a member's own special-use \Archive folder) -- a plain "Archive"
// name would be permanently shadowed by GetMailboxByName's own-principal-
// first lookup (internal/protoimap's SELECT/lookupACLMailbox), hiding the
// shared mailbox behind the member's personal one whenever the names
// collide.
func ArchiveMailboxName(ml store.MailingList) string {
	return ArchiveMailboxNameForAddress(ml.PostingAddress)
}

// ArchiveMailboxNameForAddress is ArchiveMailboxName for a caller that
// only has the list's PostingAddress at hand -- e.g. the admin REST
// create-list handler, which names the archive mailbox before the
// store.MailingList row exists (the mailbox must be created first so its
// id can be passed to InsertMailingList).
func ArchiveMailboxNameForAddress(postingAddress string) string {
	return "Lists/" + postingAddress
}

// ListArchiveHeaderValue renders the REQ-MLIST-20 List-Archive header
// value for a list with an archive mailbox configured: an RFC 5092 IMAP
// URL naming the archive mailbox on the list's own domain. herold does
// not publish a public HTTP archive browser (28-mailing-lists.md
// Non-goals), so an imap:// URL is the accurate pointer -- it names
// exactly the authenticated, ACL-gated surface REQ-MLIST-73 actually
// exposes, rather than implying an anonymous web page that does not
// exist.
func ListArchiveHeaderValue(ml store.MailingList) string {
	return fmt.Sprintf("<imap://%s/%s>", ml.Domain, imapURLPathEscape(ArchiveMailboxName(ml)))
}

// imapURLPathEscape percent-encodes the handful of bytes that would
// otherwise break the imap:// URL's path segment (RFC 5092 §5 reuses
// RFC 3986 generic syntax); mailbox names here are "Lists/<addr-spec>",
// so only '@' realistically needs escaping, but the loop is general.
func imapURLPathEscape(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '.', c == '_', c == '~', c == '/':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// fileArchive persists shaped (the same, list-wide-identical, optionally
// ARC-sealed byte slice every "each" member's fan-out copy is built from)
// into ml's archive mailbox exactly once (REQ-MLIST-70), via the normal
// content-addressed blob store: e.Blobs.Put dedups by content hash, so
// this call references the SAME blob every member Submit call in Expand
// persists -- REQ-MLIST-11's dedup guarantee extended to the archive
// copy, not a second stored body.
//
// heldPostID, when non-zero (fanOut was called for a held post via
// ApproveHeldPost), gates InsertMessage behind
// store.Metadata.ClaimMailingListHeldPostArchive: only the FIRST call
// that ever reaches here for a given held post wins that claim and
// actually files the archive copy. A crash-resumed approval or a
// stale-lease reclaim (see store.MailingListHeldPostApprovalLeaseTTL)
// calls fanOut, and therefore fileArchive, a second time for the SAME
// held post; without this gate that second call would file a second,
// duplicate archive copy every time (issue #189 verification fix,
// second hardening pass -- the member Submit path already had this
// exactly-once discipline via its own per-member idempotency key, but
// fileArchive's InsertMessage call had none). Zero (the normal,
// never-held Expand path, which calls fanOut exactly once ever) skips
// the claim entirely.
//
// Best-effort like the Sealer/TokenSigner degradation paths in Expand:
// a failure here is logged loudly and does not drop or fail the post
// (dropping mail, or blocking fan-out on an archive-only failure, is the
// worse failure). Returns true when the post is (now, or already)
// archived.
func (e *Expander) fileArchive(ctx context.Context, ml store.MailingList, shaped []byte, parsed mailparse.Message, heldPostID store.MailingListHeldPostID) bool {
	if ml.ArchiveMailboxID == nil {
		return false
	}
	if e.Blobs == nil || e.Meta == nil {
		e.Logger.ErrorContext(ctx, "maillist: archive_mailbox_id set but no Blobs/Meta wired; post not archived",
			"list", ml.PostingAddress)
		return false
	}
	if heldPostID != 0 {
		claimed, cerr := e.Meta.ClaimMailingListHeldPostArchive(ctx, heldPostID, e.Clock.Now())
		if cerr != nil {
			e.Logger.ErrorContext(ctx, "maillist: archive: claim held post archive slot failed; post not archived",
				"list", ml.PostingAddress, "held_post_id", uint64(heldPostID), "err", cerr.Error())
			return false
		}
		if !claimed {
			// An earlier attempt at fanning out this SAME held post
			// (a crash-resumed approval, or a losing concurrent
			// resumer) already filed the archive copy. Report the post
			// as archived -- it is -- without filing a second copy.
			return true
		}
	}
	ref, err := e.Blobs.Put(ctx, bytes.NewReader(shaped))
	if err != nil {
		e.Logger.ErrorContext(ctx, "maillist: archive: persist blob failed",
			"list", ml.PostingAddress, "err", err.Error())
		return false
	}
	now := e.Clock.Now()
	msg := store.Message{
		PrincipalID:  ml.PrincipalID,
		Size:         ref.Size,
		Blob:         ref,
		ReceivedAt:   now,
		InternalDate: now,
		Envelope:     envelopeFromParsedMessage(parsed),
	}
	if _, _, err := e.Meta.InsertMessage(ctx, msg, []store.MessageMailbox{{MailboxID: *ml.ArchiveMailboxID}}); err != nil {
		e.Logger.ErrorContext(ctx, "maillist: archive: file message failed",
			"list", ml.PostingAddress, "err", err.Error())
		return false
	}
	return true
}

// envelopeFromParsedMessage extracts the cached envelope fields the
// store expects on a Message row. A package-local twin of
// protosmtp.envelopeFromParsed: protosmtp already depends on maillist
// (for the Expander it wires as its MailingListExpander seam), so the
// dependency cannot run the other way; the function is ~20 lines of pure
// mailparse.Envelope -> store.Envelope field mapping, small enough that
// duplicating it here is simpler than promoting a shared helper package
// for one call site on each side.
func envelopeFromParsedMessage(msg mailparse.Message) store.Envelope {
	join := func(addrs []mail.Address) string {
		parts := make([]string, 0, len(addrs))
		for _, a := range addrs {
			parts = append(parts, a.String())
		}
		return strings.Join(parts, ", ")
	}
	var refs string
	if len(msg.Envelope.References) > 0 {
		parts := make([]string, len(msg.Envelope.References))
		for i, r := range msg.Envelope.References {
			parts[i] = "<" + r + ">"
		}
		refs = strings.Join(parts, " ")
	}
	return store.Envelope{
		Subject:    msg.Envelope.Subject,
		From:       join(msg.Envelope.From),
		To:         join(msg.Envelope.To),
		Cc:         join(msg.Envelope.Cc),
		Bcc:        join(msg.Envelope.Bcc),
		MessageID:  msg.Envelope.MessageID,
		InReplyTo:  strings.Join(msg.Envelope.InReplyTo, " "),
		References: refs,
	}
}

// archiveReadRights is the RFC 4314 rights mask an archive read grant
// conveys (REQ-MLIST-72: "read-only access; they cannot delete, expunge,
// or write into the archive"): lookup + read only -- select/fetch/search,
// nothing else. Deliberately narrower than the store.GrantLevelRead
// coarse tier (which also carries 's'/Seen): internal/protojmap's
// Email/set gates on "caller holds ANY of write/seen/deleteMessage/
// expunge/insert" as its first-pass mutate check (mail/email/set.go), so
// a grant that included 's' would let a JMAP client set arbitrary
// keywords (not just $seen) on an archived message -- a real mutation
// REQ-MLIST-72's "otherwise mutate the archive" rules out. Lookup+Read
// alone denies that first-pass check outright on both protocols, at the
// cost of a member being unable to track per-message \Seen state in the
// archive (an acceptable trade for a shared, authoritative read-only
// archive -- mirrors a conventional list-archive UX, where "read state"
// is not a meaningful per-message concept to begin with).
var archiveReadRights = store.ACLRightLookup | store.ACLRightRead

// SyncMemberArchiveGrant ensures member's mailbox:read grant on ml's
// archive mailbox (REQ-MLIST-72) matches whether member should currently
// hold it: an internal-principal member (PrincipalID set -- REQ-AC-52's
// "nomail/reading member principal"; both `each` and `nomail` internal
// members may read the archive, external-address members never can,
// having no principal to grant) in MailingListMemberActive state. Idempotent
// and safe to call unconditionally after every roster mutation:
// no-ops when ml has no archive, or when member has no PrincipalID.
//
// grantedBy attributes the grant/revoke to ml's OwnerID -- the list
// owner is the closest "who is delegating this access" concept for a
// system-maintained grant that has no per-call human actor.
func SyncMemberArchiveGrant(ctx context.Context, meta store.Metadata, ml store.MailingList, member store.MailingListMember) error {
	if ml.ArchiveMailboxID == nil || member.PrincipalID == nil {
		return nil
	}
	if member.State == store.MailingListMemberActive {
		return meta.SetMailboxACL(ctx, *ml.ArchiveMailboxID, member.PrincipalID, archiveReadRights, ml.OwnerID)
	}
	return removeArchiveACL(ctx, meta, *ml.ArchiveMailboxID, member.PrincipalID)
}

// RevokeMemberArchiveGrant unconditionally removes member's grant on
// ml's archive mailbox, regardless of member's current State. Used on
// roster removal (DELETE .../members/{mid}), where the member row is
// already gone by the time the grant is cleaned up so
// SyncMemberArchiveGrant's state check has nothing left to read.
// No-op when ml has no archive or member has no PrincipalID.
func RevokeMemberArchiveGrant(ctx context.Context, meta store.Metadata, ml store.MailingList, member store.MailingListMember) error {
	if ml.ArchiveMailboxID == nil || member.PrincipalID == nil {
		return nil
	}
	return removeArchiveACL(ctx, meta, *ml.ArchiveMailboxID, member.PrincipalID)
}

// removeArchiveACL is RemoveMailboxACL with ErrNotFound treated as
// success -- every call site here is idempotent maintenance, not a user
// command reporting "nothing to remove".
func removeArchiveACL(ctx context.Context, meta store.Metadata, archiveID store.MailboxID, principalID *store.PrincipalID) error {
	if err := meta.RemoveMailboxACL(ctx, archiveID, principalID); err != nil && err != store.ErrNotFound {
		return err
	}
	return nil
}

// archiveGrantRosterPage bounds one ListMailingListMembers page inside
// GrantExistingActiveMembersArchiveAccess / RevokeAllArchiveGrants, the
// same fixed, constant-size streaming discipline
// store.StreamActiveEachMembers uses (REQ-STORE-06: large-mailbox target
// applies equally to large lists).
const archiveGrantRosterPage = 500

// GrantExistingActiveMembersArchiveAccess grants every current active,
// internal-principal member read access to ml's archive mailbox. Called
// once, right after an operator enables the archive on a list that
// already has a roster (PATCH .../lists/{id} with archive_enabled=true),
// so existing members do not have to be individually re-added to gain
// access. Streams the roster in fixed-size pages rather than loading it
// whole.
func GrantExistingActiveMembersArchiveAccess(ctx context.Context, meta store.Metadata, ml store.MailingList) error {
	if ml.ArchiveMailboxID == nil {
		return nil
	}
	after := store.MailingListMemberID(0)
	for {
		page, err := meta.ListMailingListMembers(ctx, store.MailingListRosterFilter{
			ListID:  ml.ID,
			State:   store.MailingListMemberActive,
			AfterID: after,
			Limit:   archiveGrantRosterPage,
		})
		if err != nil {
			return err
		}
		for _, mem := range page {
			if mem.PrincipalID == nil {
				after = mem.ID
				continue
			}
			if err := SyncMemberArchiveGrant(ctx, meta, ml, mem); err != nil {
				return err
			}
			after = mem.ID
		}
		if len(page) < archiveGrantRosterPage {
			return nil
		}
	}
}

// RevokeAllArchiveGrants removes every member's read grant on
// archiveMailboxID. Called when an operator disables a list's archive
// (PATCH .../lists/{id} with archive_enabled=false): the mailbox and its
// already-archived messages are left in place (an operator can always
// re-enable and reuse it), but read access is revoked immediately --
// REQ-AC-52's grant is what actually gates access, not the list config
// field, so leaving stale grants behind after "disabling" the archive
// would silently keep it readable.
func RevokeAllArchiveGrants(ctx context.Context, meta store.Metadata, archiveMailboxID store.MailboxID) error {
	rows, err := meta.GetMailboxACL(ctx, archiveMailboxID)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.PrincipalID == nil {
			// The RFC 4314 "anyone" pseudo-row: never written by this
			// package, but tolerated defensively.
			if err := removeArchiveACL(ctx, meta, archiveMailboxID, nil); err != nil {
				return err
			}
			continue
		}
		if err := removeArchiveACL(ctx, meta, archiveMailboxID, row.PrincipalID); err != nil {
			return err
		}
	}
	return nil
}
