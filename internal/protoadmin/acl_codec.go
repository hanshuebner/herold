package protoadmin

import (
	"fmt"
	"strings"

	"github.com/hanshuebner/herold/internal/store"
)

// aclLetterOrder is the canonical RFC 4314 §2.1 letter ordering used for
// both parsing and serialisation. The order is the one RFC 4314 defines:
// l r s w i p k x t e a.
//
// Future call sites (CLI, shared library) can lift this table rather than
// re-implementing the codec; it lives here because protoadmin is the first
// consumer. If a second caller appears in a different package, move this
// table and the two helpers to internal/store or a new internal/aclcodec
// package.
var aclLetterOrder = []struct {
	letter byte
	bit    store.ACLRights
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

// parseACLRights converts an RFC 4314 rights string (e.g. "lrswipkxtea")
// into an ACLRights bitmask.  It rejects:
//   - letters outside the 11-letter RFC 4314 vocabulary, and
//   - duplicate letters within the same string.
//
// An empty string is valid and yields ACLRights(0) (no rights).
func parseACLRights(s string) (store.ACLRights, error) {
	var rights store.ACLRights
	for i := 0; i < len(s); i++ {
		ch := s[i]
		found := false
		for _, entry := range aclLetterOrder {
			if entry.letter == ch {
				if rights&entry.bit != 0 {
					return 0, fmt.Errorf("duplicate right letter %q in rights string", ch)
				}
				rights |= entry.bit
				found = true
				break
			}
		}
		if !found {
			return 0, fmt.Errorf("unknown right letter %q; valid letters are lrswipkxtea", ch)
		}
	}
	return rights, nil
}

// formatACLRights converts an ACLRights bitmask to the canonical RFC 4314
// sorted letter string (l r s w i p k x t e a order). Bits not in the known
// set are silently ignored rather than panicking — the store owns validation
// of which bits may be set.
func formatACLRights(r store.ACLRights) string {
	var b strings.Builder
	for _, entry := range aclLetterOrder {
		if r&entry.bit != 0 {
			b.WriteByte(entry.letter)
		}
	}
	return b.String()
}
