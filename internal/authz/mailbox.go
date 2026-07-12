package authz

// Mailbox resolution (epic #186, REQ-AC-50..53) — the "thin layer" the
// Phase A design (docs/design/server/implementation/06-access-control.md)
// anticipated: the generic Resolve already honours explicit `mailbox` grant
// rows (any resource kind works, see authz.go's rank/topLevel tables); the
// one structural piece #186 adds is implicit ownership (REQ-AC-51) plus the
// RFC 4314 rights <-> grant-tier bridge documented in
// architecture/15-access-control.md ("IMAP RFC 4314 mapping").

import (
	"context"
	"strconv"

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

// RightsForMailboxLevel expands a mailbox grant tier to the RFC 4314 letter
// rights it implies (architecture/15-access-control.md "IMAP RFC 4314
// mapping"):
//
//	read  (l r s)          -> mailbox:read
//	write (i p k x t e w)  -> mailbox:write   (implies read)
//	admin (a)               -> mailbox:admin   (implies write)
//
// The zero level (no access) expands to zero rights. This is the read
// direction of the collapse: a grant row only ever carries a tier, never a
// per-letter mask, so every letter within a tier is granted or withheld
// together (REQ-AC-50's documented fidelity simplification).
func RightsForMailboxLevel(level store.GrantLevel) store.ACLRights {
	switch level {
	case store.GrantLevelRead:
		return store.ACLRightLookup | store.ACLRightRead | store.ACLRightSeen
	case store.GrantLevelWrite:
		return RightsForMailboxLevel(store.GrantLevelRead) |
			store.ACLRightWrite | store.ACLRightInsert | store.ACLRightPost |
			store.ACLRightCreateMailbox | store.ACLRightDeleteMailbox |
			store.ACLRightDeleteMessage | store.ACLRightExpunge
	case store.GrantLevelAdmin:
		return RightsForMailboxLevel(store.GrantLevelWrite) | store.ACLRightAdmin
	default:
		return 0
	}
}

// MailboxLevelForRights collapses an RFC 4314 rights mask to the grant tier
// it implies, the write direction of the same table: the highest tier for
// which the mask contains at least one of that tier's letters. A mask with
// no recognised bits collapses to the zero level (no grant / delete).
//
// This is used when a caller wants to express letter-string input (e.g. an
// admin surface accepting a modifyrights string) as a mailbox grant.
func MailboxLevelForRights(r store.ACLRights) store.GrantLevel {
	if r&store.ACLRightAdmin != 0 {
		return store.GrantLevelAdmin
	}
	const writeBits = store.ACLRightWrite | store.ACLRightInsert | store.ACLRightPost |
		store.ACLRightCreateMailbox | store.ACLRightDeleteMailbox |
		store.ACLRightDeleteMessage | store.ACLRightExpunge
	if r&writeBits != 0 {
		return store.GrantLevelWrite
	}
	const readBits = store.ACLRightLookup | store.ACLRightRead | store.ACLRightSeen
	if r&readBits != 0 {
		return store.GrantLevelRead
	}
	return ""
}
