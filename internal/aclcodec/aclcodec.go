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
