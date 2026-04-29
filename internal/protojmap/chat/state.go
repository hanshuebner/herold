package chat

import (
	"context"
	"strconv"

	"github.com/hanshuebner/herold/internal/store"
)

// Chat state values are derived from the per-principal change-feed max
// seq for each entity kind, mirroring the Email/Mailbox/Thread pattern
// (see internal/protojmap/mail/email/state.go). This matters for the
// EventSource push: collectStateMap in protojmap/push.go also reports
// the change-feed seq for these types, so a client comparing the
// pushed state against its cached state from Conversation/get will see
// the values move in lockstep. The previous implementation read the
// jmap_states.conversation_state column, which nothing increments, so
// every chat /get response advertised state="0" forever and the push
// loop's matched-but-state-unchanged early-return masked every advance.

func currentConversationState(ctx context.Context, meta store.Metadata, pid store.PrincipalID) (string, error) {
	seq, err := meta.GetMaxChangeSeqForKind(ctx, pid, store.EntityKindConversation)
	if err != nil {
		return "", err
	}
	return strconv.FormatUint(uint64(seq), 10), nil
}

func currentMessageState(ctx context.Context, meta store.Metadata, pid store.PrincipalID) (string, error) {
	seq, err := meta.GetMaxChangeSeqForKind(ctx, pid, store.EntityKindChatMessage)
	if err != nil {
		return "", err
	}
	return strconv.FormatUint(uint64(seq), 10), nil
}

func currentMembershipState(ctx context.Context, meta store.Metadata, pid store.PrincipalID) (string, error) {
	seq, err := meta.GetMaxChangeSeqForKind(ctx, pid, store.EntityKindMembership)
	if err != nil {
		return "", err
	}
	return strconv.FormatUint(uint64(seq), 10), nil
}

// parseState decodes a client-supplied state string into the integer
// counter form. Returns ok=false on unparseable input; callers map that
// to RFC 8620 §5.2 "cannotCalculateChanges".
func parseState(s string) (int64, bool) {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return v, true
}

// stateFromCounter renders a state counter into the wire form.
func stateFromCounter(v int64) string {
	return strconv.FormatInt(v, 10)
}
