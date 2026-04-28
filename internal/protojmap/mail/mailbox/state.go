package mailbox

import (
	"context"
	"fmt"
	"strconv"

	"github.com/hanshuebner/herold/internal/store"
)

// currentState returns the JMAP state string for the principal's
// Mailbox datatype. The state string is the decimal encoding of the
// maximum change-feed seq for EntityKindMailbox entries for this
// principal (0 when no mailbox mutations have been recorded yet).
//
// Using the change-feed seq directly (rather than a separate
// jmap_states counter) means that mailbox mutations made outside the
// JMAP layer — e.g. auto-provisioned folders during CreatePrincipal,
// IMAP renames — are visible in Mailbox/changes without a separate
// bookkeeping pass.
func currentState(ctx context.Context, meta store.Metadata, pid store.PrincipalID) (string, error) {
	seq, err := meta.GetMaxChangeSeqForKind(ctx, pid, store.EntityKindMailbox)
	if err != nil {
		return "", fmt.Errorf("mailbox currentState: %w", err)
	}
	return strconv.FormatUint(uint64(seq), 10), nil
}

// parseState decodes a client-supplied state string into a ChangeSeq.
// Returns ok=false on unparseable input; callers map that to RFC 8620
// §5.2 "cannotCalculateChanges".
func parseState(s string) (store.ChangeSeq, bool) {
	if s == "" {
		return 0, true
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return store.ChangeSeq(v), true
}

// stateFromSeq renders a ChangeSeq into the wire form.
func stateFromSeq(v store.ChangeSeq) string {
	return strconv.FormatUint(uint64(v), 10)
}
