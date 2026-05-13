// Package protojmap: cross-account routing surface.
//
// ResolveAccount and the supporting helpers map a JMAP accountId (as
// seen by the caller) to the owning principal. The mail/* handlers
// route per-method calls through this helper so a principal with an
// ACL row on another owner's mailbox can address that owner's account
// through JMAP and see only the mailboxes the rights grant them.
//
// Per-mailbox ACL enforcement remains the per-method handler's job;
// ResolveAccount only decides "does this caller have ANY reach into
// this account". See docs/design/server/requirements/01-protocols.md
// REQ-PROTO-33 and docs/design/server/requirements/02-identity-and-auth.md
// REQ-AUTH-63.
package protojmap

import (
	"context"

	"github.com/hanshuebner/herold/internal/store"
)

// ResolveAccount maps a JMAP accountId (as seen by callerPID) to the
// owner PrincipalID for that account.
//
// The caller's own account fast-paths without a metadata read: the
// stringified principal id round-trips through AccountIDForPrincipal,
// and an exact match returns callerPID without checking ACL state.
//
// For a foreign accountId the caller must hold at least one
// ACL-accessible mailbox in the foreign owner's account (a direct ACL
// row or an "anyone" row granting the Lookup right). The returned
// PrincipalID is the foreign owner's PID — callers MUST still honour
// per-mailbox ACL when accessing individual mailboxes or messages in
// that account.
//
// Errors:
//   - "invalidArguments" on empty accountId.
//   - "accountNotFound" when accountId is malformed, the owner does
//     not exist, or the caller has no ACL-accessible mailbox in that
//     account.
func ResolveAccount(
	ctx context.Context,
	meta store.Metadata,
	callerPID store.PrincipalID,
	accountID Id,
) (store.PrincipalID, *MethodError) {
	if accountID == "" {
		return 0, NewMethodError("invalidArguments", "accountId is required")
	}
	ownerPID, ok := principalIDFromAccountID(accountID)
	if !ok {
		return 0, NewMethodError("accountNotFound",
			"account "+string(accountID)+" is not accessible to this principal")
	}
	if ownerPID == callerPID {
		return callerPID, nil
	}
	// Foreign account: guard against stale numeric ids by confirming
	// the owner principal still exists, then require at least one
	// ACL-accessible mailbox in that owner's account.
	if _, err := meta.GetPrincipalByID(ctx, ownerPID); err != nil {
		return 0, NewMethodError("accountNotFound",
			"account "+string(accountID)+" is not accessible to this principal")
	}
	accessible, err := meta.ListMailboxesAccessibleBy(ctx, callerPID)
	if err != nil {
		return 0, NewMethodError("serverFail",
			"list accessible mailboxes: "+err.Error())
	}
	for _, mb := range accessible {
		if mb.PrincipalID == ownerPID {
			return ownerPID, nil
		}
	}
	return 0, NewMethodError("accountNotFound",
		"account "+string(accountID)+" is not accessible to this principal")
}
