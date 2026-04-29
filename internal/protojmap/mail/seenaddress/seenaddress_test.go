package seenaddress

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

func newTestStore(t *testing.T) *fakestore.Store {
	t.Helper()
	s, err := fakestore.New(fakestore.Options{
		Clock:   clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		BlobDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("fakestore.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func newHandlerSet(t *testing.T) (*handlerSet, *fakestore.Store, store.Principal) {
	t.Helper()
	st := newTestStore(t)
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
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	h := &handlerSet{store: st, log: nil, clk: clk}
	return h, st, p
}

// accountID returns the JMAP account ID for principal p.
func accountID(p store.Principal) string {
	return protojmap.AccountIDForPrincipal(p.ID)
}

// -- SeenAddress/get ---------------------------------------------------------

func TestSeenAddress_Get_IDsNull_ReturnsAll(t *testing.T) {
	h, st, p := newHandlerSet(t)
	ctx := context.Background()

	// Insert two entries.
	if _, _, err := st.Meta().UpsertSeenAddress(ctx, p.ID, "bob@example.test", "Bob", 1, 0); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, _, err := st.Meta().UpsertSeenAddress(ctx, p.ID, "carol@example.test", "Carol", 0, 1); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	args, _ := json.Marshal(map[string]any{"accountId": accountID(p)})
	resp, merr := getHandler{h: h}.executeAs(p, args)
	if merr != nil {
		t.Fatalf("SeenAddress/get: %v", merr)
	}
	js, _ := json.Marshal(resp)
	if !strings.Contains(string(js), `"bob@example.test"`) {
		t.Errorf("expected bob in response: %s", js)
	}
	if !strings.Contains(string(js), `"carol@example.test"`) {
		t.Errorf("expected carol in response: %s", js)
	}
}

func TestSeenAddress_Get_SpecificIDs_Found(t *testing.T) {
	h, st, p := newHandlerSet(t)
	ctx := context.Background()

	sa, _, err := st.Meta().UpsertSeenAddress(ctx, p.ID, "dave@example.test", "Dave", 1, 0)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	id := seenAddrID(sa.ID)

	args, _ := json.Marshal(map[string]any{"accountId": accountID(p), "ids": []string{id}})
	resp, merr := getHandler{h: h}.executeAs(p, args)
	if merr != nil {
		t.Fatalf("SeenAddress/get: %v", merr)
	}
	js, _ := json.Marshal(resp)
	if !strings.Contains(string(js), `"dave@example.test"`) {
		t.Errorf("expected dave in response: %s", js)
	}
	if strings.Contains(string(js), `"notFound":["`+id) {
		t.Errorf("unexpected notFound: %s", js)
	}
}

func TestSeenAddress_Get_SpecificIDs_NotFound(t *testing.T) {
	h, _, p := newHandlerSet(t)

	args, _ := json.Marshal(map[string]any{"accountId": accountID(p), "ids": []string{"9999"}})
	resp, merr := getHandler{h: h}.executeAs(p, args)
	if merr != nil {
		t.Fatalf("SeenAddress/get: %v", merr)
	}
	js, _ := json.Marshal(resp)
	if !strings.Contains(string(js), `"9999"`) {
		t.Errorf("expected 9999 in notFound: %s", js)
	}
}

// -- SeenAddress/changes -----------------------------------------------------

func TestSeenAddress_Changes_NoOpWhenSameState(t *testing.T) {
	h, st, p := newHandlerSet(t)
	ctx := context.Background()

	states, err := st.Meta().GetJMAPStates(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetJMAPStates: %v", err)
	}
	sinceState := strings.TrimSpace(strings.TrimLeft(
		func() string { v, _ := currentState(ctx, st.Meta(), p.ID); return v }(),
		"0"))
	if sinceState == "" {
		sinceState = "0"
	}
	_ = states

	args, _ := json.Marshal(map[string]any{
		"accountId":  accountID(p),
		"sinceState": sinceState,
	})
	resp, merr := changesHandler{h: h}.executeAs(p, args)
	if merr != nil {
		t.Fatalf("SeenAddress/changes: %v", merr)
	}
	js, _ := json.Marshal(resp)
	if !strings.Contains(string(js), `"created":[]`) {
		t.Errorf("expected empty created: %s", js)
	}
	if !strings.Contains(string(js), `"updated":[]`) {
		t.Errorf("expected empty updated: %s", js)
	}
	if !strings.Contains(string(js), `"destroyed":[]`) {
		t.Errorf("expected empty destroyed: %s", js)
	}
}

func TestSeenAddress_Changes_DetectsCreate(t *testing.T) {
	h, st, p := newHandlerSet(t)
	ctx := context.Background()

	state0, err := currentState(ctx, st.Meta(), p.ID)
	if err != nil {
		t.Fatalf("currentState: %v", err)
	}

	if _, _, err := st.Meta().UpsertSeenAddress(ctx, p.ID, "eve@example.test", "Eve", 1, 0); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	args, _ := json.Marshal(map[string]any{
		"accountId":  accountID(p),
		"sinceState": state0,
	})
	resp, merr := changesHandler{h: h}.executeAs(p, args)
	if merr != nil {
		t.Fatalf("SeenAddress/changes: %v", merr)
	}
	js, _ := json.Marshal(resp)
	if strings.Contains(string(js), `"created":[]`) {
		t.Errorf("expected non-empty created: %s", js)
	}
}

// -- SeenAddress/set ---------------------------------------------------------

func TestSeenAddress_Set_DestroySucceeds(t *testing.T) {
	h, st, p := newHandlerSet(t)
	ctx := context.Background()

	sa, _, err := st.Meta().UpsertSeenAddress(ctx, p.ID, "frank@example.test", "Frank", 1, 0)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	id := seenAddrID(sa.ID)

	args, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"destroy":   []string{id},
	})
	resp, merr := setHandler{h: h}.executeAs(p, args)
	if merr != nil {
		t.Fatalf("SeenAddress/set destroy: %v", merr)
	}
	js, _ := json.Marshal(resp)
	if !strings.Contains(string(js), id) {
		t.Errorf("expected %s in destroyed: %s", id, js)
	}
	if strings.Contains(string(js), `"notDestroyed":{"`+id) {
		t.Errorf("unexpected notDestroyed: %s", js)
	}

	// Verify it's gone from the store.
	rows, err := st.Meta().ListSeenAddressesByPrincipal(ctx, p.ID, 0)
	if err != nil {
		t.Fatalf("ListSeenAddressesByPrincipal: %v", err)
	}
	for _, r := range rows {
		if r.ID == sa.ID {
			t.Errorf("row %d still present after destroy", sa.ID)
		}
	}
}

func TestSeenAddress_Set_DestroyNotFound(t *testing.T) {
	h, _, p := newHandlerSet(t)

	args, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"destroy":   []string{"99999"},
	})
	resp, merr := setHandler{h: h}.executeAs(p, args)
	if merr != nil {
		t.Fatalf("SeenAddress/set: %v", merr)
	}
	js, _ := json.Marshal(resp)
	if !strings.Contains(string(js), `"notDestroyed"`) {
		t.Errorf("expected notDestroyed in response: %s", js)
	}
}

func TestSeenAddress_Set_CreateForbidden(t *testing.T) {
	h, _, p := newHandlerSet(t)

	args, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"create": map[string]any{
			"c1": map[string]any{"email": "x@example.test"},
		},
	})
	resp, merr := setHandler{h: h}.executeAs(p, args)
	if merr != nil {
		t.Fatalf("SeenAddress/set: %v", merr)
	}
	js, _ := json.Marshal(resp)
	if !strings.Contains(string(js), `"forbidden"`) {
		t.Errorf("expected forbidden error for create: %s", js)
	}
	if !strings.Contains(string(js), `"notCreated"`) {
		t.Errorf("expected notCreated in response: %s", js)
	}
}

func TestSeenAddress_Set_UpdateForbidden(t *testing.T) {
	h, st, p := newHandlerSet(t)
	ctx := context.Background()

	sa, _, err := st.Meta().UpsertSeenAddress(ctx, p.ID, "grace@example.test", "Grace", 1, 0)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	id := seenAddrID(sa.ID)

	args, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"update": map[string]any{
			id: map[string]any{"displayName": "Changed"},
		},
	})
	resp, merr := setHandler{h: h}.executeAs(p, args)
	if merr != nil {
		t.Fatalf("SeenAddress/set: %v", merr)
	}
	js, _ := json.Marshal(resp)
	if !strings.Contains(string(js), `"forbidden"`) {
		t.Errorf("expected forbidden error for update: %s", js)
	}
	if !strings.Contains(string(js), `"notUpdated"`) {
		t.Errorf("expected notUpdated in response: %s", js)
	}
}

func TestSeenAddress_Set_StateMismatch(t *testing.T) {
	h, _, p := newHandlerSet(t)

	badState := "9999"
	args, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"ifInState": badState,
		"destroy":   []string{},
	})
	_, merr := setHandler{h: h}.executeAs(p, args)
	if merr == nil {
		t.Fatal("expected stateMismatch error, got nil")
	}
	if merr.Type != "stateMismatch" {
		t.Errorf("expected stateMismatch, got %q", merr.Type)
	}
}
