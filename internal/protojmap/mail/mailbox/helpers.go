package mailbox

import (
	"context"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// requirePrincipal pulls the authenticated principal id out of ctx.
// Returns a MethodError if the request reached the handler without
// authentication — which should not happen because the dispatcher's
// requireAuth middleware enforces it, but we re-verify so a future
// dispatcher rewrite cannot silently leak privileges.
func requirePrincipal(ctx context.Context) (store.PrincipalID, *protojmap.MethodError) {
	p, ok := protojmap.PrincipalFromContext(ctx)
	if !ok || p.ID == 0 {
		return 0, protojmap.NewMethodError("forbidden", "no authenticated principal")
	}
	return p.ID, nil
}

// resolveAccount maps a JMAP accountId to the owning principal,
// returning the owner pid alongside the caller's pid so handlers can
// route queries against the foreign account while still enforcing
// per-mailbox ACL on behalf of the caller. Implements REQ-PROTO-33.
//
// Empty accountId yields "invalidArguments"; an inaccessible or
// malformed accountId yields "accountNotFound". The owner==caller fast
// path is handled inside protojmap.ResolveAccount.
func resolveAccount(
	ctx context.Context,
	meta store.Metadata,
	callerPID store.PrincipalID,
	reqAccountID jmapID,
) (store.PrincipalID, *protojmap.MethodError) {
	return protojmap.ResolveAccount(ctx, meta, callerPID, reqAccountID)
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
