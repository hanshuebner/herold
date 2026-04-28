package contacts

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

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
// An absent accountId is rejected with "invalidArguments" per RFC 8620
// §5.1: every method that operates on an account MUST carry the field.
func requireAccount(reqAccountID jmapID, pid store.PrincipalID) *protojmap.MethodError {
	if reqAccountID == "" {
		return protojmap.NewMethodError("invalidArguments", "accountId is required")
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

// mintUID generates a server-side UID for a Contact that arrives
// without one. RFC 9553 prescribes a UUID; the contacts binding draft
// allows any opaque RFC 4122-shaped value. We emit a "urn:uuid:" prefix
// + 32 random hex bytes formatted as 8-4-4-4-12, version 4 / variant
// RFC 4122. Self-contained so we can mint UIDs without an external
// dependency.
func mintUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("contacts: mint uid: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant RFC 4122
	hexs := hex.EncodeToString(b[:])
	return "urn:uuid:" + hexs[0:8] + "-" + hexs[8:12] + "-" + hexs[12:16] + "-" + hexs[16:20] + "-" + hexs[20:32], nil
}
