package chat

import (
	"context"
	"strconv"

	"github.com/hanshuebner/herold/internal/store"
)

// currentConversationState returns the JMAP state string for the
// principal's Conversation datatype: the decimal of
// JMAPStates.Conversation.
func currentConversationState(ctx context.Context, meta store.Metadata, pid store.PrincipalID) (string, error) {
	st, err := meta.GetJMAPStates(ctx, pid)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(st.Conversation, 10), nil
}

// currentMessageState returns the JMAP state string for the principal's
// chat Message datatype (distinct from email-Email state per the
// dual-meaning split in store/types_chat.go).
func currentMessageState(ctx context.Context, meta store.Metadata, pid store.PrincipalID) (string, error) {
	st, err := meta.GetJMAPStates(ctx, pid)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(st.ChatMessage, 10), nil
}

// currentMembershipState returns the JMAP state string for the
// principal's Membership datatype.
func currentMembershipState(ctx context.Context, meta store.Metadata, pid store.PrincipalID) (string, error) {
	st, err := meta.GetJMAPStates(ctx, pid)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(st.Membership, 10), nil
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
