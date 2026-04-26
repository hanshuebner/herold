package coach

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
)

// fixture drives the JMAP handler set with a fakestore.
type fixture struct {
	t    *testing.T
	st   *fakestore.Store
	pid  store.PrincipalID
	pid2 store.PrincipalID
	clk  *clock.FakeClock
	gh   *getHandler
	qh   *queryHandler
	ch   *changesHandler
	sh   *setHandler
}

// t0 is the base test time.
var t0 = time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

func newFixture(t *testing.T) *fixture {
	t.Helper()
	clk := clock.NewFake(t0)
	st, err := fakestore.New(fakestore.Options{Clock: clk})
	if err != nil {
		t.Fatalf("fakestore.New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	p1, err := st.Meta().InsertPrincipal(context.Background(), store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "alice@example.com",
		DisplayName: "Alice", QuotaBytes: 1 << 30,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	p2, err := st.Meta().InsertPrincipal(context.Background(), store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "bob@example.com",
		DisplayName: "Bob", QuotaBytes: 1 << 30,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}

	h := &handlerSet{store: st, logger: nil, clk: clk}
	return &fixture{
		t:   t,
		st:  st,
		pid: p1.ID, pid2: p2.ID,
		clk: clk,
		gh:  &getHandler{h: h},
		qh:  &queryHandler{h: h},
		ch:  &changesHandler{h: h},
		sh:  &setHandler{h: h},
	}
}

func (f *fixture) ctx() context.Context {
	return contextWithTestPrincipal(context.Background(), store.Principal{
		ID: f.pid, CanonicalEmail: "alice@example.com",
	})
}

func (f *fixture) ctxFor(pid store.PrincipalID) context.Context {
	return contextWithTestPrincipal(context.Background(), store.Principal{
		ID: pid, CanonicalEmail: "bob@example.com",
	})
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return b
}

// -- Tests ---------------------------------------------------------------

func TestGetEmptyPrincipal(t *testing.T) {
	f := newFixture(t)
	resp, merr := f.gh.Execute(f.ctx(), mustJSON(t, map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
	}))
	if merr != nil {
		t.Fatalf("get: %v", merr)
	}
	gr := resp.(getResponse)
	if len(gr.List) != 0 {
		t.Errorf("expected empty list, got %d items", len(gr.List))
	}
}

func TestSetCreateAndGet(t *testing.T) {
	f := newFixture(t)
	ctx := f.ctx()
	acct := protojmap.AccountIDForPrincipal(f.pid)

	// Create a stat via /set.
	_, merr := f.sh.Execute(ctx, mustJSON(t, map[string]any{
		"accountId": acct,
		"create": map[string]any{
			"a": map[string]any{
				"action":   "archive",
				"keyboard": 3,
				"mouse":    5,
			},
		},
	}))
	if merr != nil {
		t.Fatalf("set create: %v", merr)
	}

	// Get it back.
	resp, merr := f.gh.Execute(ctx, mustJSON(t, map[string]any{
		"accountId": acct,
	}))
	if merr != nil {
		t.Fatalf("get: %v", merr)
	}
	gr := resp.(getResponse)
	if len(gr.List) != 1 {
		t.Fatalf("expected 1 stat, got %d", len(gr.List))
	}
	s := gr.List[0]
	if s.Action != "archive" {
		t.Errorf("action = %q, want archive", s.Action)
	}
	if s.KeyboardCount14d != 3 {
		t.Errorf("KeyboardCount14d = %d, want 3", s.KeyboardCount14d)
	}
	if s.MouseCount14d != 5 {
		t.Errorf("MouseCount14d = %d, want 5", s.MouseCount14d)
	}
	if s.KeyboardCount90d != 3 {
		t.Errorf("KeyboardCount90d = %d, want 3", s.KeyboardCount90d)
	}
}

func TestSetUpdateIncrements(t *testing.T) {
	f := newFixture(t)
	ctx := f.ctx()
	acct := protojmap.AccountIDForPrincipal(f.pid)

	// Create initial.
	f.sh.Execute(ctx, mustJSON(t, map[string]any{ //nolint:errcheck
		"accountId": acct,
		"create": map[string]any{
			"a": map[string]any{"action": "reply", "keyboard": 2},
		},
	}))

	// Update with more events.
	_, merr := f.sh.Execute(ctx, mustJSON(t, map[string]any{
		"accountId": acct,
		"update": map[string]any{
			"reply": map[string]any{"keyboard": 4, "mouse": 1},
		},
	}))
	if merr != nil {
		t.Fatalf("set update: %v", merr)
	}

	resp, _ := f.gh.Execute(ctx, mustJSON(t, map[string]any{
		"accountId": acct, "ids": []string{"reply"},
	}))
	gr := resp.(getResponse)
	if len(gr.List) != 1 {
		t.Fatalf("expected 1 stat, got %d", len(gr.List))
	}
	// Total keyboard = 2 + 4 = 6, mouse = 1.
	s := gr.List[0]
	if s.KeyboardCount14d != 6 {
		t.Errorf("KeyboardCount14d = %d, want 6", s.KeyboardCount14d)
	}
	if s.MouseCount14d != 1 {
		t.Errorf("MouseCount14d = %d, want 1", s.MouseCount14d)
	}
}

func TestSetDestroy(t *testing.T) {
	f := newFixture(t)
	ctx := f.ctx()
	acct := protojmap.AccountIDForPrincipal(f.pid)

	f.sh.Execute(ctx, mustJSON(t, map[string]any{ //nolint:errcheck
		"accountId": acct,
		"create":    map[string]any{"a": map[string]any{"action": "mark-read", "keyboard": 1}},
	}))

	_, merr := f.sh.Execute(ctx, mustJSON(t, map[string]any{
		"accountId": acct,
		"destroy":   []string{"mark-read"},
	}))
	if merr != nil {
		t.Fatalf("set destroy: %v", merr)
	}

	// Should no longer appear in /get.
	resp, _ := f.gh.Execute(ctx, mustJSON(t, map[string]any{
		"accountId": acct,
	}))
	gr := resp.(getResponse)
	if len(gr.List) != 0 {
		t.Errorf("expected empty list after destroy, got %d", len(gr.List))
	}
}

func TestAuthzCrossAccount(t *testing.T) {
	f := newFixture(t)
	acct1 := protojmap.AccountIDForPrincipal(f.pid)
	acct2 := protojmap.AccountIDForPrincipal(f.pid2)

	// Alice creates a stat.
	f.sh.Execute(f.ctx(), mustJSON(t, map[string]any{ //nolint:errcheck
		"accountId": acct1,
		"create":    map[string]any{"a": map[string]any{"action": "archive", "keyboard": 10}},
	}))

	// Bob tries to get Alice's account — should fail with accountNotFound.
	_, merr := f.gh.Execute(f.ctxFor(f.pid2), mustJSON(t, map[string]any{
		"accountId": acct1,
	}))
	if merr == nil {
		t.Fatal("expected error when accessing another principal's account, got nil")
	}
	if merr.Type != "accountNotFound" {
		t.Errorf("error type = %q, want accountNotFound", merr.Type)
	}

	// Bob's own account should be empty.
	resp, merr := f.gh.Execute(f.ctxFor(f.pid2), mustJSON(t, map[string]any{
		"accountId": acct2,
	}))
	if merr != nil {
		t.Fatalf("get bob: %v", merr)
	}
	gr := resp.(getResponse)
	if len(gr.List) != 0 {
		t.Errorf("bob should have no stats, got %d", len(gr.List))
	}
}

func TestWindowBoundary14d(t *testing.T) {
	f := newFixture(t)
	ctx := f.ctx()
	acct := protojmap.AccountIDForPrincipal(f.pid)

	// Record an event 20 days ago (outside 14d, inside 90d).
	past20d := t0.Add(-20 * 24 * time.Hour)
	f.sh.Execute(ctx, mustJSON(t, map[string]any{ //nolint:errcheck
		"accountId": acct,
		"create": map[string]any{
			"a": map[string]any{
				"action":         "compose",
				"keyboard":       5,
				"lastKeyboardAt": past20d.Format(time.RFC3339),
			},
		},
	}))

	resp, _ := f.gh.Execute(ctx, mustJSON(t, map[string]any{"accountId": acct}))
	gr := resp.(getResponse)
	if len(gr.List) != 1 {
		t.Fatalf("expected 1 stat, got %d", len(gr.List))
	}
	s := gr.List[0]
	// 20 days ago: outside 14d window, inside 90d window.
	if s.KeyboardCount14d != 0 {
		t.Errorf("KeyboardCount14d = %d, want 0 (event is 20d ago)", s.KeyboardCount14d)
	}
	if s.KeyboardCount90d != 5 {
		t.Errorf("KeyboardCount90d = %d, want 5", s.KeyboardCount90d)
	}
}

func TestDismissFields(t *testing.T) {
	f := newFixture(t)
	ctx := f.ctx()
	acct := protojmap.AccountIDForPrincipal(f.pid)
	dismissUntil := t0.Add(24 * time.Hour).Format(time.RFC3339)

	// Create with dismiss fields.
	f.sh.Execute(ctx, mustJSON(t, map[string]any{ //nolint:errcheck
		"accountId": acct,
		"create": map[string]any{
			"a": map[string]any{
				"action":       "archive",
				"dismissCount": 2,
				"dismissUntil": dismissUntil,
			},
		},
	}))

	resp, _ := f.gh.Execute(ctx, mustJSON(t, map[string]any{"accountId": acct}))
	gr := resp.(getResponse)
	if len(gr.List) != 1 {
		t.Fatalf("expected 1 stat, got %d", len(gr.List))
	}
	s := gr.List[0]
	if s.DismissCount != 2 {
		t.Errorf("DismissCount = %d, want 2", s.DismissCount)
	}
	if s.DismissUntil == nil {
		t.Error("DismissUntil is nil, want non-nil")
	}
}

func TestQuery(t *testing.T) {
	f := newFixture(t)
	ctx := f.ctx()
	acct := protojmap.AccountIDForPrincipal(f.pid)

	for _, action := range []string{"archive", "reply", "nav_inbox"} {
		f.sh.Execute(ctx, mustJSON(t, map[string]any{ //nolint:errcheck
			"accountId": acct,
			"create": map[string]any{
				"a": map[string]any{"action": action, "keyboard": 1},
			},
		}))
	}

	resp, merr := f.qh.Execute(ctx, mustJSON(t, map[string]any{"accountId": acct}))
	if merr != nil {
		t.Fatalf("query: %v", merr)
	}
	qr := resp.(queryResponse)
	if qr.Total != 3 {
		t.Errorf("Total = %d, want 3", qr.Total)
	}
}

func TestStateAdvances(t *testing.T) {
	f := newFixture(t)
	ctx := f.ctx()
	acct := protojmap.AccountIDForPrincipal(f.pid)

	state0Resp, _ := f.gh.Execute(ctx, mustJSON(t, map[string]any{"accountId": acct}))
	state0 := state0Resp.(getResponse).State

	f.sh.Execute(ctx, mustJSON(t, map[string]any{ //nolint:errcheck
		"accountId": acct,
		"create":    map[string]any{"a": map[string]any{"action": "archive", "keyboard": 1}},
	}))

	state1Resp, _ := f.gh.Execute(ctx, mustJSON(t, map[string]any{"accountId": acct}))
	state1 := state1Resp.(getResponse).State

	if state0 == state1 {
		t.Errorf("state did not advance after set: %q", state0)
	}
}

func TestGCRemovesOldEvents(t *testing.T) {
	f := newFixture(t)
	ctx := f.ctx()
	acct := protojmap.AccountIDForPrincipal(f.pid)

	// Record event 100 days ago.
	past100d := t0.Add(-100 * 24 * time.Hour)
	f.sh.Execute(ctx, mustJSON(t, map[string]any{ //nolint:errcheck
		"accountId": acct,
		"create": map[string]any{
			"a": map[string]any{
				"action":         "archive",
				"keyboard":       3,
				"lastKeyboardAt": past100d.Format(time.RFC3339),
			},
		},
	}))

	// GC with 90-day cutoff.
	cutoff := t0.Add(-90 * 24 * time.Hour)
	n, err := f.st.Meta().GCCoachEvents(ctx, cutoff)
	if err != nil {
		t.Fatalf("GCCoachEvents: %v", err)
	}
	if n == 0 {
		t.Error("expected GC to delete rows, got 0")
	}

	// After GC the stat should show 0 counts (90d window empty).
	resp, _ := f.gh.Execute(ctx, mustJSON(t, map[string]any{"accountId": acct}))
	gr := resp.(getResponse)
	// The stat row may still be listed (dismiss row exists or window edge),
	// but counters should be 0.
	for _, s := range gr.List {
		if s.KeyboardCount90d != 0 || s.MouseCount90d != 0 {
			t.Errorf("expected 0 counts after GC, got kb90=%d ms90=%d", s.KeyboardCount90d, s.MouseCount90d)
		}
	}
}
