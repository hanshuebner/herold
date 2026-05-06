package protojmap

import (
	"context"

	"github.com/hanshuebner/herold/internal/store"
)

// ResolveAccount resolves a JMAP accountId as seen from callerPID to
// the owner principal id. If accountId matches the caller's own account,
// the caller's own PID is returned immediately.
//
// For a foreign accountId the caller must hold at least the Lookup right
// on at least one of the foreign owner's mailboxes (via a direct ACL row
// or an "anyone" row). If the call succeeds, the returned PID is the
// foreign owner's PID — callers must still honour per-mailbox ACL when
// accessing individual mailboxes or messages in that account.
//
// Errors:
//   - "invalidArguments" when accountId is empty.
//   - "accountNotFound" when accountId is syntactically invalid, the
//     owner principal does not exist, or the caller has no accessible
//     mailboxes in that account.
func ResolveAccount(ctx context.Context, meta store.Metadata, callerPID store.PrincipalID, accountID Id) (store.PrincipalID, *MethodError) {
	if accountID == "" {
		return 0, NewMethodError("invalidArguments", "accountId is required")
	}
	ownerPID, ok := principalIDFromAccountID(accountID)
	if !ok {
		return 0, NewMethodError("accountNotFound", "malformed accountId: "+accountID)
	}
	// Fast path: caller's own account.
	if ownerPID == callerPID {
		return callerPID, nil
	}
	// Foreign account: verify the owner exists (guard against stale numeric ids).
	if _, err := meta.GetPrincipalByID(ctx, ownerPID); err != nil {
		return 0, NewMethodError("accountNotFound", "account "+accountID+" not found")
	}
	// Caller must hold l-right on at least one mailbox in the foreign account.
	accessible, err := meta.ListMailboxesAccessibleBy(ctx, callerPID)
	if err != nil {
		return 0, NewMethodError("serverFail", "list accessible mailboxes: "+err.Error())
	}
	for _, mb := range accessible {
		if mb.PrincipalID == ownerPID {
			return ownerPID, nil
		}
	}
	return 0, NewMethodError("accountNotFound", "account "+accountID+" is not accessible to this principal")
}
