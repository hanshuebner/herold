package identity

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
)

func newStore(t *testing.T) *fakestore.Store {
	t.Helper()
	s, err := fakestore.New(fakestore.Options{
		Clock:   clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		BlobDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("fakestore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// newHandlers builds the handler set directly (bypassing the
// CapabilityRegistry) so tests can drive Execute without injecting a
// principal through protojmap.PrincipalFromContext (the package-private
// context key is owned by the Core agent's protojmap package).
func newHandlers(t *testing.T) (*handlerSet, *fakestore.Store, store.Principal) {
	t.Helper()
	st := newStore(t)
	ctx := context.Background()
	if err := st.Meta().InsertDomain(ctx, store.Domain{Name: "example.test", IsLocal: true}); err != nil {
		t.Fatalf("insert domain: %v", err)
	}
	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@example.test",
		DisplayName:    "Alice",
	})
	if err != nil {
		t.Fatalf("insert principal: %v", err)
	}
	return &handlerSet{
		store:    st,
		identity: NewStoreWith(st, clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))),
		domains:  makeDomainsFn(st),
	}, st, p
}

func TestIdentity_Get_DefaultIdentityIsSynthesized(t *testing.T) {
	h, _, p := newHandlers(t)
	args, _ := json.Marshal(map[string]any{"accountId": protojmap.AccountIDForPrincipal(p.ID)})
	resp, mErr := getHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("Identity/get: %v", mErr)
	}
	js, _ := json.Marshal(resp)
	if !strings.Contains(string(js), `"id":"default"`) {
		t.Fatalf("expected default identity in response: %s", js)
	}
	if !strings.Contains(string(js), `"email":"alice@example.test"`) {
		t.Fatalf("expected synthesized email: %s", js)
	}
}

func TestIdentity_Set_RejectsForeignDomain(t *testing.T) {
	h, _, p := newHandlers(t)
	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"alt": map[string]any{
				"name":  "Alice Elsewhere",
				"email": "alice@otherdomain.test",
			},
		},
	})
	resp, mErr := setHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("Identity/set: %v", mErr)
	}
	js, _ := json.Marshal(resp)
	if !strings.Contains(string(js), `"forbiddenFrom"`) {
		t.Fatalf("expected forbiddenFrom in response: %s", js)
	}
}

func TestIdentity_Set_AcceptsLocalDomain(t *testing.T) {
	h, _, p := newHandlers(t)
	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"work": map[string]any{
				"name":          "Alice At Work",
				"email":         "alice@example.test",
				"textSignature": "Sent from work",
			},
		},
	})
	resp, mErr := setHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("Identity/set: %v", mErr)
	}
	js, _ := json.Marshal(resp)
	if !strings.Contains(string(js), `"created"`) {
		t.Fatalf("expected created: %s", js)
	}
	if strings.Contains(string(js), `"notCreated"`) {
		t.Fatalf("unexpected notCreated: %s", js)
	}
}

func TestIdentity_Changes_NoOpWhenSameState(t *testing.T) {
	h, st, p := newHandlers(t)
	stState, err := st.Meta().GetJMAPStates(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("states: %v", err)
	}
	state := stateString(stState.Identity)
	args, _ := json.Marshal(map[string]any{"accountId": protojmap.AccountIDForPrincipal(p.ID), "sinceState": state})
	resp, mErr := changesHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("Identity/changes: %v", mErr)
	}
	js, _ := json.Marshal(resp)
	if !strings.Contains(string(js), `"updated":[]`) {
		t.Fatalf("expected empty updated: %s", js)
	}
}

// -- REQ-PROTO-57 / REQ-STORE-35 Identity.signature extension ------

// TestIdentity_Get_IncludesSignature verifies that Identity/get reflects
// the signature extension property both for the principal-default
// (overlay) row and for a custom persisted identity.
func TestIdentity_Get_IncludesSignature(t *testing.T) {
	h, _, p := newHandlers(t)
	// Set the signature on the default identity via the overlay path.
	sig := "Cheers,\nAlice"
	updateArgs, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"update": map[string]any{
			"default": map[string]any{"signature": sig},
		},
	})
	_, mErr := setHandler{h: h}.executeAs(p, updateArgs)
	if mErr != nil {
		t.Fatalf("Identity/set update default: %v", mErr)
	}
	getArgs, _ := json.Marshal(map[string]any{"accountId": protojmap.AccountIDForPrincipal(p.ID)})
	resp, mErr := getHandler{h: h}.executeAs(p, getArgs)
	if mErr != nil {
		t.Fatalf("Identity/get: %v", mErr)
	}
	js, _ := json.Marshal(resp)
	if !strings.Contains(string(js), `"signature":"Cheers,\nAlice"`) {
		t.Fatalf("default signature missing in response: %s", js)
	}
}

// TestIdentity_Set_AcceptsSignature exercises the create + update
// signature paths plus the explicit-null clear.
func TestIdentity_Set_AcceptsSignature(t *testing.T) {
	h, _, p := newHandlers(t)
	createArgs, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"alt": map[string]any{
				"name":      "Alice Personal",
				"email":     "alice@example.test",
				"signature": "Best,\nA.",
			},
		},
	})
	resp, mErr := setHandler{h: h}.executeAs(p, createArgs)
	if mErr != nil {
		t.Fatalf("Identity/set create: %v", mErr)
	}
	js, _ := json.Marshal(resp)
	if !strings.Contains(string(js), `"signature":"Best,\nA."`) {
		t.Fatalf("created identity missing signature: %s", js)
	}
	// Find the created id so we can flip the signature.
	sresp := resp.(setResponse)
	created, ok := sresp.Created["alt"]
	if !ok {
		t.Fatalf("create response missing alt: %+v", sresp.Created)
	}
	updateArgs, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"update": map[string]any{
			created.ID: map[string]any{"signature": "Updated"},
		},
	})
	resp2, mErr := setHandler{h: h}.executeAs(p, updateArgs)
	if mErr != nil {
		t.Fatalf("Identity/set update: %v", mErr)
	}
	js2, _ := json.Marshal(resp2)
	if !strings.Contains(string(js2), `"signature":"Updated"`) {
		t.Fatalf("updated signature missing: %s", js2)
	}
	// Clear via explicit null.
	clearArgs, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"update": map[string]any{
			created.ID: map[string]any{"signature": nil},
		},
	})
	_, mErr3 := setHandler{h: h}.executeAs(p, clearArgs)
	if mErr3 != nil {
		t.Fatalf("Identity/set clear: %v", mErr3)
	}
	getArgs3, _ := json.Marshal(map[string]any{"accountId": protojmap.AccountIDForPrincipal(p.ID)})
	resp3, _ := getHandler{h: h}.executeAs(p, getArgs3)
	js3, _ := json.Marshal(resp3)
	if strings.Contains(string(js3), `"signature":"Updated"`) {
		t.Fatalf("signature did not clear: %s", js3)
	}
}
