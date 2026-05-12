package taggedaddress

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// dispatch resolves the named JMAP method through a CapabilityRegistry
// the test has primed with Register. It returns the raw response object
// (or method error) so assertions can target the wire surface the JMAP
// HTTP layer would emit.
func dispatch(t *testing.T, reg *protojmap.CapabilityRegistry, p store.Principal, method string, args any) (any, *protojmap.MethodError) {
	t.Helper()
	h, ok := reg.Resolve(method)
	if !ok {
		t.Fatalf("registry has no handler for %s", method)
	}
	raw, _ := json.Marshal(args)
	ctx := contextWithTestPrincipal(context.Background(), p)
	return h.Execute(ctx, raw)
}

// newRegistry sets up a fresh CapabilityRegistry, store, and verified
// identity and returns the registry along with the seed values tests
// drive against.
func newRegistry(t *testing.T) (*protojmap.CapabilityRegistry, store.Store, store.Principal, string) {
	t.Helper()
	st, err := storesqlite.Open(context.Background(),
		filepath.Join(t.TempDir(), "store.db"), nil,
		clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
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
	identityID := "int-ident-1"
	if err := st.Meta().InsertJMAPIdentity(ctx, store.JMAPIdentity{
		ID:           identityID,
		PrincipalID:  p.ID,
		Email:        "alice@example.test",
		MayDelete:    true,
		VerifiedAtUs: time.Now().UnixMicro(),
	}); err != nil {
		t.Fatalf("insert identity: %v", err)
	}
	reg := protojmap.NewCapabilityRegistry()
	Register(reg, st, slog.New(slog.NewTextHandler(io.Discard, nil)),
		clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		func() (int, int) { return 100, 500 })
	return reg, st, p, identityID
}

// TestIntegration_FullRoundtrip exercises the get/set/changes triple
// through the CapabilityRegistry's Resolve+Execute path with mixed valid
// and invalid inputs in a single /set call, then re-reads via /get and
// /changes.
func TestIntegration_FullRoundtrip(t *testing.T) {
	reg, _, p, identityID := newRegistry(t)
	acct := accountID(p)

	// /get on empty.
	r0, merr := dispatch(t, reg, p, "TaggedAddressFilter/get", map[string]any{"accountId": acct})
	if merr != nil {
		t.Fatalf("get empty: %v", merr)
	}
	state0 := r0.(getResponse).State

	// /set with one valid create + three invalid creates.
	r1, merr := dispatch(t, reg, p, "TaggedAddressFilter/set", map[string]any{
		"accountId": acct,
		"create": map[string]any{
			"good": map[string]any{
				"baseIdentityId": identityID,
				"suffix":         "Github",
				"action":         store.TaggedAddressActionLabelArchive,
				"labelName":      "Code",
			},
			"badAction": map[string]any{
				"baseIdentityId": identityID,
				"suffix":         "x",
				"action":         "explode_inbox",
				"labelName":      "X",
			},
			"badSuffix": map[string]any{
				"baseIdentityId": identityID,
				"suffix":         strings.Repeat("z", 65),
				"action":         store.TaggedAddressActionLabel,
				"labelName":      "L",
			},
			"badLabel": map[string]any{
				"baseIdentityId": identityID,
				"suffix":         "ok",
				"action":         store.TaggedAddressActionLabel,
				"labelName":      "",
			},
		},
	})
	if merr != nil {
		t.Fatalf("set: %v", merr)
	}
	resp := r1.(setResponse)
	if _, ok := resp.Created["good"]; !ok {
		t.Fatalf("good create missing: %+v", resp)
	}
	for _, bad := range []string{"badAction", "badSuffix", "badLabel"} {
		if _, ok := resp.NotCreated[bad]; !ok {
			t.Fatalf("expected NotCreated[%s], got %+v", bad, resp.NotCreated)
		}
	}
	if resp.NewState == state0 {
		t.Fatalf("state did not advance after partial create: %q -> %q", state0, resp.NewState)
	}
	state1 := resp.NewState

	// Pull the created row's id.
	gotID := resp.Created["good"].ID

	// /changes from state0 -> state1 mentions the row.
	r2, merr := dispatch(t, reg, p, "TaggedAddressFilter/changes", map[string]any{
		"accountId":  acct,
		"sinceState": state0,
	})
	if merr != nil {
		t.Fatalf("changes: %v", merr)
	}
	chResp := r2.(changesResponse)
	if chResp.NewState != state1 {
		t.Fatalf("changes.newState = %q, want %q", chResp.NewState, state1)
	}
	found := false
	for _, id := range chResp.Updated {
		if id == gotID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("changes did not surface %q: %+v", gotID, chResp)
	}

	// Invalid /update: bogus action.
	r3, _ := dispatch(t, reg, p, "TaggedAddressFilter/set", map[string]any{
		"accountId": acct,
		"update": map[string]any{
			gotID: map[string]any{"action": "garbled"},
		},
	})
	resp3 := r3.(setResponse)
	if _, ok := resp3.NotUpdated[gotID]; !ok {
		t.Fatalf("expected NotUpdated for bogus update, got %+v", resp3)
	}
	if resp3.NewState != state1 {
		t.Fatalf("state advanced on failed update: %q -> %q", state1, resp3.NewState)
	}

	// Valid /destroy of the created row.
	r4, _ := dispatch(t, reg, p, "TaggedAddressFilter/set", map[string]any{
		"accountId": acct,
		"destroy":   []string{gotID},
	})
	resp4 := r4.(setResponse)
	if len(resp4.Destroyed) != 1 || resp4.Destroyed[0] != gotID {
		t.Fatalf("destroyed = %+v", resp4.Destroyed)
	}
	if resp4.NewState == state1 {
		t.Fatalf("state did not advance after destroy: %q", resp4.NewState)
	}

	// /get final empty list.
	r5, _ := dispatch(t, reg, p, "TaggedAddressFilter/get", map[string]any{"accountId": acct})
	if len(r5.(getResponse).List) != 0 {
		t.Fatalf("list non-empty after destroy: %+v", r5)
	}
}

// TestIntegration_CapAcrossCalls drives multiple /set calls to assert
// the cap rolls over correctly when the handler precounts.
func TestIntegration_CapAcrossCalls(t *testing.T) {
	_, st, p, identityID := newRegistry(t)
	acct := accountID(p)
	// Build a separate registry with cap=2 so the test exercises the
	// per-handler precount path rather than the store-level cap.
	reg := protojmap.NewCapabilityRegistry()
	Register(reg, st, slog.New(slog.NewTextHandler(io.Discard, nil)),
		clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		func() (int, int) { return 2, 500 })

	// First two creates succeed.
	for i, sfx := range []string{"a", "b"} {
		r, merr := dispatch(t, reg, p, "TaggedAddressFilter/set", map[string]any{
			"accountId": acct,
			"create": map[string]any{
				"c": map[string]any{
					"baseIdentityId": identityID,
					"suffix":         sfx,
					"action":         store.TaggedAddressActionLabel,
					"labelName":      "L",
				},
			},
		})
		if merr != nil {
			t.Fatalf("set %d: %v", i, merr)
		}
		if _, ok := r.(setResponse).Created["c"]; !ok {
			t.Fatalf("set %d not created: %+v", i, r)
		}
	}
	// Third trips the cap on a separate call.
	r, _ := dispatch(t, reg, p, "TaggedAddressFilter/set", map[string]any{
		"accountId": acct,
		"create": map[string]any{
			"c": map[string]any{
				"baseIdentityId": identityID,
				"suffix":         "c",
				"action":         store.TaggedAddressActionLabel,
				"labelName":      "L",
			},
		},
	})
	resp := r.(setResponse)
	if se, ok := resp.NotCreated["c"]; !ok || se.Type != "tooManyFilters" {
		t.Fatalf("expected tooManyFilters, got %+v", resp.NotCreated)
	}
}

// TestPushIntegration_IdentityStateAdvancesOnMutation asserts that a
// tagged-address mutation bumps the JMAP Identity state counter so the
// EventSource push loop's collectStateMap returns a higher Identity
// state after /set than before. This is the contract REQ-TAG-72
// promises: tag-filter mutations fire an Identity/changes push.
func TestPushIntegration_IdentityStateAdvancesOnMutation(t *testing.T) {
	reg, st, p, identityID := newRegistry(t)
	acct := accountID(p)

	pre, err := st.Meta().GetJMAPStates(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("get pre states: %v", err)
	}
	// Mutate via /set.
	r, merr := dispatch(t, reg, p, "TaggedAddressFilter/set", map[string]any{
		"accountId": acct,
		"create": map[string]any{
			"c": map[string]any{
				"baseIdentityId": identityID,
				"suffix":         "push-test",
				"action":         store.TaggedAddressActionLabel,
				"labelName":      "L",
			},
		},
	})
	if merr != nil {
		t.Fatalf("set: %v", merr)
	}
	resp := r.(setResponse)
	if len(resp.Created) != 1 {
		t.Fatalf("create did not succeed: %+v", resp)
	}
	post, err := st.Meta().GetJMAPStates(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("get post states: %v", err)
	}
	if post.Identity <= pre.Identity {
		t.Fatalf("Identity state did not advance: %d -> %d", pre.Identity, post.Identity)
	}
}
