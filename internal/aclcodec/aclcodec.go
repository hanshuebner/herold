// Package aclcodec is the single source of truth for the RFC 4314 letter
// encoding of store.ACLRights. Both the IMAP wire surface (SETACL /
// GETACL / MYRIGHTS / LISTRIGHTS in internal/protoimap) and the admin
// REST surface (/api/v1/principals/{pid}/mailboxes/{mailbox}/acl* in
// internal/protoadmin) consume this package so the two never drift on
// which letter maps to which bit.
//
// Scope: the package handles the canonical 11-letter vocabulary
// "lrswipkxtea" defined by RFC 4314 §2.1. The obsolete RFC 2086
// composite letters "c" (create) and "d" (delete) are NOT handled
// here; callers that need to accept them on input (the IMAP SETACL
// command, for back-compat with older clients) implement that
// translation themselves before calling Decode. The REST surface
// rejects unknown letters.
package aclcodec

import (
	"fmt"
	"strings"

	"github.com/hanshuebner/herold/internal/store"
)

// LetterTable is the canonical RFC 4314 §2.1 letter ordering exported
// for callers that need to iterate the vocabulary (e.g. IMAP
// LISTRIGHTS, which renders one entry per independently-grantable
// right). Encode and Decode iterate this same table so the two stay
// in lockstep.
var LetterTable = []struct {
	Letter byte
	Bit    store.ACLRights
}{
	{'l', store.ACLRightLookup},
	{'r', store.ACLRightRead},
	{'s', store.ACLRightSeen},
	{'w', store.ACLRightWrite},
	{'i', store.ACLRightInsert},
	{'p', store.ACLRightPost},
	{'k', store.ACLRightCreateMailbox},
	{'x', store.ACLRightDeleteMailbox},
	{'t', store.ACLRightDeleteMessage},
	{'e', store.ACLRightExpunge},
	{'a', store.ACLRightAdmin},
}

// Encode renders a rights mask as the canonical RFC 4314 letter
// sequence. The letters always appear in the "lrswipkxtea" order
// regardless of the bit positions in the mask, so test assertions
// stay stable as new bits are added. Bits outside the known set are
// silently ignored — the store owns validation of which bits may be
// set; the renderer should never panic on a stray bit.
func Encode(r store.ACLRights) string {
	var sb strings.Builder
	for _, e := range LetterTable {
		if r&e.Bit != 0 {
			sb.WriteByte(e.Letter)
		}
	}
	return sb.String()
}

// Decode parses an RFC 4314 letter string into an ACLRights bitmask.
// Unknown letters return an error so callers can surface a clean
// "invalid input" rather than silently dropping bits. Duplicate
// letters are accepted (idempotent OR) — RFC 4314 §2.2.1 says
// "duplicate letters MUST be treated as a single occurrence". An
// empty string is valid and yields zero rights.
func Decode(s string) (store.ACLRights, error) {
	var out store.ACLRights
	for i := 0; i < len(s); i++ {
		c := s[i]
		matched := false
		for _, e := range LetterTable {
			if e.Letter == c {
				out |= e.Bit
				matched = true
				break
			}
		}
		if !matched {
			return 0, fmt.Errorf("aclcodec: unknown ACL right %q", c)
		}
	}
	return out, nil
}

// ExpandTier expands a coarse mailbox grant tier (store.GrantLevelRead /
// Write / Admin) to the RFC 4314 rights it implies (epic #210,
// architecture/15-access-control.md "IMAP RFC 4314 mapping"):
//
//	read  (l r s)          -> mailbox:read
//	write (i p k x t e w)  -> mailbox:write   (implies read)
//	admin (a)              -> mailbox:admin   (implies write)
//
// The zero level (no access) expands to zero rights. This is the coarse
// tier's projection into rights, used for a mailbox grant whose Level holds
// a tier word rather than a letter-set -- the only representation a
// non-ACL-wire grant (e.g. a future IdP claim-mapping rule) can assert.
func ExpandTier(level store.GrantLevel) store.ACLRights {
	switch level {
	case store.GrantLevelRead:
		return store.ACLRightLookup | store.ACLRightRead | store.ACLRightSeen
	case store.GrantLevelWrite:
		return ExpandTier(store.GrantLevelRead) |
			store.ACLRightWrite | store.ACLRightInsert | store.ACLRightPost |
			store.ACLRightCreateMailbox | store.ACLRightDeleteMailbox |
			store.ACLRightDeleteMessage | store.ACLRightExpunge
	case store.GrantLevelAdmin:
		return ExpandTier(store.GrantLevelWrite) | store.ACLRightAdmin
	default:
		return 0
	}
}

// CollapseTier collapses an RFC 4314 rights mask to the coarse grant tier it
// implies, the inverse direction of ExpandTier: the highest tier for which
// the mask contains at least one of that tier's letters. A mask with no
// recognised bits collapses to the zero level (no grant). Used when a
// caller wants to express letter-string input as a coarse tier (e.g. a UI
// convenience badge), never for the grant row itself.
func CollapseTier(r store.ACLRights) store.GrantLevel {
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

// DecodeGrantLevel decodes a mailbox grant's Level column into the RFC 4314
// rights it conveys (epic #210). Two representations are accepted: the
// canonical per-letter encoding this package emits (Encode/Decode) for
// every mailbox grant written through SETACL or the admin ACL REST surface,
// and the three coarse tier words (store.GrantLevelRead/Write/Admin) a
// non-ACL-wire grant may carry instead -- "the coarse tiers remain a
// convenience projection" (docs/design/server/architecture/15-access-control.md).
// An unrecognised level decodes to (0, false) so a caller enumerating grant
// rows can skip a corrupt one without failing the whole resolution.
func DecodeGrantLevel(level store.GrantLevel) (store.ACLRights, bool) {
	switch level {
	case store.GrantLevelRead, store.GrantLevelWrite, store.GrantLevelAdmin:
		return ExpandTier(level), true
	}
	r, err := Decode(string(level))
	if err != nil {
		return 0, false
	}
	return r, true
}
