package protojmap_test

// subaccount_bearer_test.go proves REQ-SUBACCT-02's follow-up: a
// sub-principal must not authenticate on protojmap's Bearer-API-key
// path. The package doc for requireAuth notes "an operator-issued admin
// API key authenticates JMAP too" -- so the protoadmin Bearer-mint
// exploit (a key whose PrincipalID names a sub-principal) grants a full
// JMAP session unless protojmap's own Bearer resolution seam
// (authenticateBearer) independently rejects it. This test mints the
// key directly at the store layer (the same shape the reported exploit
// used against protoadmin) and hits the JMAP session-discovery endpoint,
// which requireAuth gates.

import (
	"context"
	"net/http"
	"testing"

	"github.com/hanshuebner/herold/internal/store"
)

func TestSession_RejectsSubPrincipalBearer(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	sub, err := f.store.Meta().InsertSubPrincipal(ctx, f.pid, store.Principal{
		CanonicalEmail: "sub@example.com",
	})
	if err != nil {
		t.Fatalf("InsertSubPrincipal: %v", err)
	}
	plaintext, _, err := createAPIKeyNamed(ctx, f.store, sub.ID, "sub-key")
	if err != nil {
		t.Fatalf("createAPIKeyNamed(sub-principal): %v", err)
	}

	res, body := f.doRequest("GET", "/.well-known/jmap", plaintext, nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("sub-principal Bearer against JMAP session endpoint: status=%d body=%s, want 401", res.StatusCode, body)
	}
}
