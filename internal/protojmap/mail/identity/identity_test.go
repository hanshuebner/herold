package identity

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
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

// runWithPrincipal invokes f under a context whose Value lookup
// returns p when asked through the handlerSet's helper. Each handler
// calls h.principal(ctx) (defined in helpers.go) to obtain the
// authenticated principal; the test seam intercepts that lookup
// without depending on protojmap's private context key.
func runWithPrincipal(p store.Principal, f func(ctx context.Context) (any, error)) (any, error) {
	return f(contextWithTestPrincipal(context.Background(), p))
}

func TestIdentity_Get_DefaultIdentityIsSynthesized(t *testing.T) {
	h, _, p := newHandlers(t)
	args, _ := json.Marshal(map[string]any{})
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
	args, _ := json.Marshal(map[string]any{"sinceState": state})
	resp, mErr := changesHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("Identity/changes: %v", mErr)
	}
	js, _ := json.Marshal(resp)
	if !strings.Contains(string(js), `"updated":[]`) {
		t.Fatalf("expected empty updated: %s", js)
	}
}
