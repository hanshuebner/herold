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
	owner, err := meta.GetPrincipalByID(ctx, ownerPID)
	if err != nil {
		return 0, NewMethodError("accountNotFound",
			"account "+string(accountID)+" is not accessible to this principal")
	}
	// Sub-account (REQ-SUBACCT-01/03/04): the caller reaches their own
	// sub-account unconditionally, without needing an ACL grant. A
	// sub-principal is never itself an authenticated caller
	// (REQ-SUBACCT-02, enforced at Directory.Authenticate), so this
	// branch cannot be used to pivot from a sub-account to its parent
	// or to a sibling sub-account -- only a genuine parent principal
	// reaches here as callerPID.
	if owner.IsSubAccount() && owner.ParentPrincipalID == callerPID {
		return ownerPID, nil
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

// HasOwnerAccess reports whether pid should be treated as having full
// owner-equivalent rights on resources owned by ownerPID: either they
// are the same principal, or ownerPID is a sub-account (REQ-SUBACCT-01)
// whose parent is pid (REQ-SUBACCT-04: the parent reaches every one of
// its own sub-accounts with full rights, without an explicit ACL grant
// -- unlike an ACL-shared foreign account, which is filtered to the
// caller's granted rights by each handler's ACL-checking helper).
//
// The mail/* packages' per-mailbox rights and listing helpers call this
// wherever they previously fast-pathed on "pid == ownerPID": that
// substitution is sufficient because a sub-account's mailboxes and
// messages are ordinary rows keyed by the sub-account's own
// PrincipalID, so the existing "am I the owner" branch already returns
// the full unfiltered result -- it only needed to also fire when pid is
// the sub-account's parent.
func HasOwnerAccess(ctx context.Context, meta store.Metadata, pid, ownerPID store.PrincipalID) bool {
	if pid == ownerPID {
		return true
	}
	owner, err := meta.GetPrincipalByID(ctx, ownerPID)
	if err != nil {
		return false
	}
	return owner.IsSubAccount() && owner.ParentPrincipalID == pid
}
