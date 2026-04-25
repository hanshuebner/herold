package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// requirePrincipal pulls the authenticated principal id out of ctx.
// Returns a MethodError if the request reached the handler without
// authentication.
func requirePrincipal(ctx context.Context) (store.PrincipalID, *protojmap.MethodError) {
	p, ok := principalFor(ctx)
	if !ok || p.ID == 0 {
		return 0, protojmap.NewMethodError("forbidden", "no authenticated principal")
	}
	return p.ID, nil
}

// requireAccount validates the JMAP accountId against the authenticated
// principal. v1 maps one principal -> one account; cross-principal
// access is rejected with "accountNotFound" per RFC 8620 §3.6.2.
//
// An empty accountId in the request is treated as "the requesting
// principal" — JMAP technically requires the field, but several real-
// world clients omit it for single-account servers and we accept that.
func requireAccount(reqAccountID jmapID, pid store.PrincipalID) *protojmap.MethodError {
	if reqAccountID == "" {
		return nil
	}
	if reqAccountID != string(protojmap.AccountIDForPrincipal(pid)) {
		return protojmap.NewMethodError("accountNotFound",
			"account "+reqAccountID+" is not accessible to this principal")
	}
	return nil
}

// serverFail wraps an internal Go error into a JMAP method-error
// envelope. RFC 8620 §3.6.2 reserves "serverFail" for "an unexpected
// error occurred during the processing of the call".
func serverFail(err error) *protojmap.MethodError {
	if err == nil {
		return nil
	}
	return protojmap.NewMethodError("serverFail", err.Error())
}

// findMembership returns the requesting principal's current membership
// row in the conversation, or (zero, false) if absent.
func findMembership(memberships []store.ChatMembership, pid store.PrincipalID) (store.ChatMembership, bool) {
	for _, m := range memberships {
		if m.PrincipalID == pid {
			return m, true
		}
	}
	return store.ChatMembership{}, false
}

// principalIsMember reports whether the principal is a current member
// of the conversation.
func principalIsMember(memberships []store.ChatMembership, pid store.PrincipalID) bool {
	_, ok := findMembership(memberships, pid)
	return ok
}

// canManageConversation reports whether the principal's role is owner
// or admin (REQ-CHAT-14: admin/owner-only mutations).
func canManageConversation(memberships []store.ChatMembership, pid store.PrincipalID) bool {
	m, ok := findMembership(memberships, pid)
	if !ok {
		return false
	}
	return m.Role == store.ChatRoleOwner || m.Role == store.ChatRoleAdmin
}

// canDestroyConversation reports whether the principal owns the
// conversation. Owner-only per the REQ-CHAT-15 destruction rule. The
// "owner" semantic in v1 is "the conversation creator"; the schema's
// CreatedByPrincipalID field carries that identity, so we delegate to
// it rather than relying on a Role string token.
func canDestroyConversation(c store.ChatConversation, pid store.PrincipalID) bool {
	return c.CreatedByPrincipalID == pid
}

// otherDMMember returns the other principal in a DM conversation. The
// caller passes its own principal id to disambiguate. Returns 0 if not
// a 2-member conversation or the principal is not a member.
func otherDMMember(memberships []store.ChatMembership, self store.PrincipalID) store.PrincipalID {
	if len(memberships) != 2 {
		return 0
	}
	for _, m := range memberships {
		if m.PrincipalID != self {
			return m.PrincipalID
		}
	}
	return 0
}

// principalDisplayName returns a human-readable label for a principal,
// falling back to the canonical email then the wire id.
func principalDisplayName(p store.Principal) string {
	if name := strings.TrimSpace(p.DisplayName); name != "" {
		return name
	}
	if email := strings.TrimSpace(p.CanonicalEmail); email != "" {
		return email
	}
	return jmapIDFromPrincipal(p.ID)
}

// resolvePrincipalEmail returns the canonical email / display name for
// a principal id for the purpose of computing a DM's auto-name.
// Returns "" when the principal does not exist; the caller falls back
// to an empty name (REQ-CHAT-02 leaves DM Name as optional).
func resolvePrincipalEmail(ctx context.Context, meta store.Metadata, pid store.PrincipalID) string {
	p, err := meta.GetPrincipalByID(ctx, pid)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			// Logging would require a logger reference; the caller
			// surfaces fatal errors through serverFail. A missing
			// display name is non-fatal.
		}
		return ""
	}
	return principalDisplayName(p)
}

// dedupePrincipals returns the input slice with duplicates removed,
// preserving order.
func dedupePrincipals(xs []store.PrincipalID) []store.PrincipalID {
	seen := map[store.PrincipalID]struct{}{}
	out := make([]store.PrincipalID, 0, len(xs))
	for _, x := range xs {
		if x == 0 {
			continue
		}
		if _, dup := seen[x]; dup {
			continue
		}
		seen[x] = struct{}{}
		out = append(out, x)
	}
	return out
}

// parseRFC3339 parses ts as an RFC 3339 timestamp.
func parseRFC3339(ts string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return time.Time{}, fmt.Errorf("must be an RFC 3339 timestamp: %w", err)
	}
	return t, nil
}

// principalConversationIDs returns the set of conversation ids the
// principal is a current member of. Used by every list / query path
// to enforce REQ-CHAT-101.
func principalConversationIDs(ctx context.Context, meta store.Metadata, pid store.PrincipalID) (map[store.ConversationID]struct{}, error) {
	mine, err := meta.ListChatMembershipsByPrincipal(ctx, pid)
	if err != nil {
		return nil, err
	}
	out := make(map[store.ConversationID]struct{}, len(mine))
	for _, m := range mine {
		out[m.ConversationID] = struct{}{}
	}
	return out, nil
}
