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

// resolveAccount resolves the JMAP accountId against the authenticated
// principal and returns the target owner PID. When the accountId is the
// caller's own, the caller's PID is returned. When it is a foreign
// account, the caller must hold at least the Lookup right on at least
// one mailbox in that account (RFC 8620 §3.6 / cross-account access).
//
// Use this for cross-account capable methods: Mailbox/get, Mailbox/query,
// Mailbox/changes.
func resolveAccount(ctx context.Context, meta store.Metadata, reqAccountID jmapID, callerPID store.PrincipalID) (store.PrincipalID, *protojmap.MethodError) {
	return protojmap.ResolveAccount(ctx, meta, callerPID, reqAccountID)
}

// requireOwnAccount validates that the JMAP accountId matches the
// authenticated principal's own account. Used for personal-only JMAP
// object types (Mailbox/set) where cross-account write access is not
// permitted in v1.
func requireOwnAccount(reqAccountID jmapID, pid store.PrincipalID) *protojmap.MethodError {
	if reqAccountID == "" {
		return protojmap.NewMethodError("invalidArguments", "accountId is required")
	}
	if reqAccountID != protojmap.AccountIDForPrincipal(pid) {
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
