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

// requireAccount validates the JMAP accountId against the authenticated
// principal. v1 maps one principal -> one account; cross-principal
// access is rejected with "accountNotFound" per RFC 8620 §3.6.2.
//
// An empty accountId in the request is treated as "the requesting
// principal" — JMAP technically requires the field, but several
// real-world clients omit it for single-account servers and we accept
// that.
func requireAccount(reqAccountID jmapID, pid store.PrincipalID) *protojmap.MethodError {
	if reqAccountID == "" {
		return nil
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
