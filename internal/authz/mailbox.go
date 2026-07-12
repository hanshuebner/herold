package authz

// Mailbox resolution (epic #186, extended to full RFC 4314 fidelity by epic
// #210). Two resolvers exist side by side:
//
//   - ResolveMailbox / RightsForMailboxLevel / MailboxLevelForRights: the
//     coarse read/write/admin tier view, still meaningful as a convenience
//     projection (REQ-AC-50) for a non-ACL-wire grant (e.g. a future IdP
//     claim-mapping rule targeting a mailbox) and for UI badges. Backed by
//     the generic Resolve over explicit `mailbox` grant rows plus implicit
//     ownership (REQ-AC-51).
//   - ResolveMailboxRights: the authoritative enforcement path (epic #210).
//     It reads every `mailbox` grant row on the resource directly (letter-set
//     or coarse-tier Level, decoded via internal/aclcodec.DecodeGrantLevel)
//     rather than collapsing through the tier ranking, so a fine-grained
//     grant (e.g. insert without delete) round-trips exactly. This is the
//     sole path internal/protoimap's SELECT/STATUS/APPEND/STORE/EXPUNGE/ACL
//     gating uses; there is no second (mailbox_acl-backed) source to union.

import (
	"context"
	"strconv"

	"github.com/hanshuebner/herold/internal/aclcodec"
	"github.com/hanshuebner/herold/internal/store"
)

// MailboxResource returns the Resource identifying mb for authz.Resolve. The
// resource id is the mailbox's numeric id rendered as decimal text, matching
// the `resource_id TEXT` column (architecture/15-access-control.md storage
// section).
func MailboxResource(mb store.Mailbox) Resource {
	return MailboxResourceID(mb.ID)
}

// MailboxResourceID is MailboxResource for callers that only have the
// mailbox id at hand (e.g. a rights check already scoped to a known,
// non-owned mailbox).
func MailboxResourceID(id store.MailboxID) Resource {
	return Resource{Kind: store.GrantResourceMailbox, ID: strconv.FormatUint(uint64(id), 10)}
}

// ResolveMailbox returns p's effective grant-substrate level on mb. A
// principal always holds implicit mailbox:admin on its own mailboxes
// (REQ-AC-51) ahead of any explicit grant lookup; grants express access
// delegated to *other* principals, resolved by the generic Resolve over the
// mailbox resource kind (explicit `mailbox` grant rows, REQ-AC-50).
//
// Fail-closed: propagates Resolve's store error unchanged (REQ-AC-12).
func ResolveMailbox(ctx context.Context, meta grantReader, p store.Principal, mb store.Mailbox) (store.GrantLevel, error) {
	if mb.PrincipalID == p.ID {
		return store.GrantLevelAdmin, nil
	}
	return Resolve(ctx, meta, p, MailboxResource(mb))
}

// RightsForMailboxLevel expands a coarse mailbox grant tier to the RFC 4314
// letter rights it implies (architecture/15-access-control.md "IMAP RFC 4314
// mapping"). Thin wrapper over internal/aclcodec.ExpandTier, which is the
// canonical home of the tier<->rights table (epic #210) so
// internal/storesqlite / internal/storepg can decode a mailbox grant's Level
// without importing this package.
func RightsForMailboxLevel(level store.GrantLevel) store.ACLRights {
	return aclcodec.ExpandTier(level)
}

// MailboxLevelForRights collapses an RFC 4314 rights mask to the coarse
// grant tier it implies. Thin wrapper over internal/aclcodec.CollapseTier
// (see RightsForMailboxLevel).
func MailboxLevelForRights(r store.ACLRights) store.GrantLevel {
	return aclcodec.CollapseTier(r)
}

// mailboxRightsReader is the slice of store.Metadata ResolveMailboxRights
// depends on: grantReader (for the superadmin check, via the generic
// Resolve) plus ListGrantsOnResource to enumerate every grant row on the
// mailbox itself (both the caller's own row and any "anyone" row).
type mailboxRightsReader interface {
	grantReader
	ListGrantsOnResource(ctx context.Context, resourceKind store.GrantResourceKind, resourceID string) ([]store.Grant, error)
}

// mailboxGrantApplies reports whether grant row g on a mailbox resource
// applies to principal pid: either g is pid's own explicit row, or g is the
// RFC 4314 "anyone" pseudo-row (GrantSubjectAnyone), which applies to every
// authenticated principal.
func mailboxGrantApplies(g store.Grant, pid store.PrincipalID) bool {
	if g.SubjectKind == store.GrantSubjectAnyone {
		return true
	}
	return g.SubjectKind == store.GrantSubjectPrincipal && store.PrincipalID(g.SubjectID) == pid
}

// ResolveMailboxRights returns p's full RFC 4314 rights mask on mb -- the
// authoritative enforcement path (epic #210, REQ-AC-50/51/53), replacing the
// #186 bridge that unioned this with a separate mailbox_acl table (now
// retired). The mask is the OR of:
//
//   - implicit ownership: mb.PrincipalID == p.ID grants every right
//     (REQ-AC-51), no store read needed;
//   - the superadmin short-circuit (flag-based, or a grant-based
//     server:superadmin row), which also grants every right;
//   - every `mailbox` grant row on mb applicable to p (p's own row, or an
//     "anyone" row), each decoded via internal/aclcodec.DecodeGrantLevel so
//     both the letter-set and coarse-tier Level encodings are honoured.
//
// Fail-closed: a store error resolving the superadmin check or the grant
// rows is returned unchanged (REQ-AC-12); callers must treat it as a deny.
func ResolveMailboxRights(ctx context.Context, meta mailboxRightsReader, p store.Principal, mb store.Mailbox) (store.ACLRights, error) {
	if mb.PrincipalID == p.ID {
		return store.ACLRightsAll, nil
	}
	lvl, err := Resolve(ctx, meta, p, Resource{Kind: store.GrantResourceServer})
	if err != nil {
		return 0, err
	}
	if AtLeast(store.GrantResourceServer, lvl, store.GrantLevelSuperadmin) {
		return store.ACLRightsAll, nil
	}
	rows, err := meta.ListGrantsOnResource(ctx, store.GrantResourceMailbox, MailboxResourceID(mb.ID).ID)
	if err != nil {
		return 0, err
	}
	var have store.ACLRights
	for _, g := range rows {
		if g.ResourceKind != store.GrantResourceMailbox || !mailboxGrantApplies(g, p.ID) {
			continue
		}
		r, ok := aclcodec.DecodeGrantLevel(g.Level)
		if !ok {
			continue
		}
		have |= r
	}
	return have, nil
}
