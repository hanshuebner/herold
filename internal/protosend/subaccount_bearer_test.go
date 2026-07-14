package protosend_test

// subaccount_bearer_test.go proves REQ-SUBACCT-02's follow-up: a
// sub-principal must not authenticate on protosend's Bearer-API-key
// path (the mail.send REST API, /api/v1/mail/*) -- exactly the scope
// the reported protoadmin exploit's key already carried.

import (
	"context"
	"net/http"
	"testing"

	"github.com/hanshuebner/herold/internal/protosend"
	"github.com/hanshuebner/herold/internal/store"
)

func TestSend_RejectsSubPrincipalBearer(t *testing.T) {
	h := newSendHarness(t)
	ctx := context.Background()

	// A fresh individual-kind parent: the harness's seeded "alice"
	// fixture predates PrincipalKind and was inserted with Kind left at
	// its zero value, which InsertSubPrincipal correctly refuses as a
	// parent (REQ-SUBACCT-01 requires an individual principal).
	parent, err := h.store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "subparent@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal(parent): %v", err)
	}
	sub, err := h.store.Meta().InsertSubPrincipal(ctx, parent.ID, store.Principal{
		CanonicalEmail: "sub@example.test",
	})
	if err != nil {
		t.Fatalf("InsertSubPrincipal: %v", err)
	}
	const plain = "hk_unit_test_sub_token_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if _, err := h.store.Meta().InsertAPIKey(ctx, store.APIKey{
		PrincipalID: sub.ID,
		Hash:        protosend.HashAPIKey(plain),
		Name:        "sub-send",
		ScopeJSON:   `["mail.send"]`,
	}); err != nil {
		t.Fatalf("InsertAPIKey(sub-principal): %v", err)
	}

	res, buf := h.doRequest("POST", "/api/v1/mail/send", plain, map[string]any{
		"source": "sub@example.test",
		"destination": map[string]any{
			"toAddresses": []string{"bob@dest.test"},
		},
		"message": map[string]any{
			"subject": "Hello",
			"body":    map[string]any{"text": "Hi there"},
		},
	})
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("sub-principal Bearer against /api/v1/mail/send: status=%d body=%s, want 401", res.StatusCode, buf)
	}
}
