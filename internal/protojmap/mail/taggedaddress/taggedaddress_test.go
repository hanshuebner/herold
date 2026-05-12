package taggedaddress

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

func newStore(t *testing.T) store.Store {
	t.Helper()
	s, err := storesqlite.Open(context.Background(),
		filepath.Join(t.TempDir(), "store.db"), nil,
		clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func newHandlers(t *testing.T) (*handlerSet, store.Store, store.Principal, string) {
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
	// A verified, persisted identity that filter rows reference.
	identityID := "test-ident-1"
	row := store.JMAPIdentity{
		ID:           identityID,
		PrincipalID:  p.ID,
		Email:        "alice@example.test",
		MayDelete:    true,
		VerifiedAtUs: time.Now().UnixMicro(),
	}
	if err := st.Meta().InsertJMAPIdentity(ctx, row); err != nil {
		t.Fatalf("insert identity: %v", err)
	}
	h := &handlerSet{
		store:  st,
		clk:    clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		caps: func() (int, int) {
			return store.MaxTaggedAddressFiltersPerPrincipal,
				store.MaxTaggedAddressDismissalsPerPrincipal
		},
	}
	return h, st, p, identityID
}

func accountID(p store.Principal) string {
	return string(protojmap.AccountIDForPrincipal(p.ID))
}

// -- TaggedAddressFilter/get ---------------------------------------

func TestGet_EmptyList(t *testing.T) {
	h, _, p, _ := newHandlers(t)
	args, _ := json.Marshal(map[string]any{"accountId": accountID(p)})
	r, merr := getHandler{h: h}.executeAs(p, args)
	if merr != nil {
		t.Fatalf("get: %v", merr)
	}
	resp := r.(getResponse)
	if len(resp.List) != 0 {
		t.Fatalf("empty list expected, got %+v", resp.List)
	}
	if resp.State == "" {
		t.Fatalf("state must be non-empty")
	}
	if resp.AccountID != accountID(p) {
		t.Fatalf("accountId echo mismatch: %q", resp.AccountID)
	}
}

func TestGet_RejectsForeignAccount(t *testing.T) {
	h, _, p, _ := newHandlers(t)
	args, _ := json.Marshal(map[string]any{"accountId": "not-mine"})
	_, merr := getHandler{h: h}.executeAs(p, args)
	if merr == nil || merr.Type != "accountNotFound" {
		t.Fatalf("expected accountNotFound, got %+v", merr)
	}
}

func TestGet_RequiresAccountID(t *testing.T) {
	h, _, p, _ := newHandlers(t)
	args, _ := json.Marshal(map[string]any{})
	_, merr := getHandler{h: h}.executeAs(p, args)
	if merr == nil || merr.Type != "invalidArguments" {
		t.Fatalf("expected invalidArguments for missing accountId, got %+v", merr)
	}
}

// -- TaggedAddressFilter/set ---------------------------------------

func createFilter(t *testing.T, h *handlerSet, p store.Principal, identityID, suffix, action, label string) jmapTaggedAddressFilter {
	t.Helper()
	args, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"create": map[string]any{
			"c1": map[string]any{
				"baseIdentityId": identityID,
				"suffix":         suffix,
				"action":         action,
				"labelName":      label,
			},
		},
	})
	r, merr := setHandler{h: h}.executeAs(p, args)
	if merr != nil {
		t.Fatalf("set create: %v", merr)
	}
	resp := r.(setResponse)
	if len(resp.NotCreated) != 0 {
		t.Fatalf("unexpected notCreated: %+v", resp.NotCreated)
	}
	rec, ok := resp.Created["c1"]
	if !ok {
		t.Fatalf("create did not produce a row: %+v", resp)
	}
	return rec
}

func TestSet_CreateGetRoundtrip(t *testing.T) {
	h, _, p, identityID := newHandlers(t)
	created := createFilter(t, h, p, identityID, "amazon",
		store.TaggedAddressActionLabel, "Shopping")
	if created.ID == "" {
		t.Fatalf("create returned empty id")
	}
	if created.Suffix != "amazon" {
		t.Fatalf("created.suffix = %q", created.Suffix)
	}
	// Now /get should surface it.
	args, _ := json.Marshal(map[string]any{"accountId": accountID(p)})
	r, _ := getHandler{h: h}.executeAs(p, args)
	resp := r.(getResponse)
	if len(resp.List) != 1 || resp.List[0].ID != created.ID {
		t.Fatalf("get after create returned %+v", resp.List)
	}
}

func TestSet_NormalisesSuffixToLowerCase(t *testing.T) {
	h, _, p, identityID := newHandlers(t)
	rec := createFilter(t, h, p, identityID, "NewsLetters",
		store.TaggedAddressActionLabelArchive, "News")
	if rec.Suffix != "newsletters" {
		t.Fatalf("suffix not lowered: %q", rec.Suffix)
	}
}

func TestSet_RejectsInvalidAction(t *testing.T) {
	h, _, p, identityID := newHandlers(t)
	args, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"create": map[string]any{
			"c1": map[string]any{
				"baseIdentityId": identityID,
				"suffix":         "x",
				"action":         "delete_forever",
				"labelName":      "X",
			},
		},
	})
	r, merr := setHandler{h: h}.executeAs(p, args)
	if merr != nil {
		t.Fatalf("set: %v", merr)
	}
	resp := r.(setResponse)
	se, ok := resp.NotCreated["c1"]
	if !ok {
		t.Fatalf("expected notCreated entry, got %+v", resp)
	}
	if se.Type != "invalidProperties" {
		t.Fatalf("type = %q, want invalidProperties", se.Type)
	}
}

func TestSet_RejectsOversizedSuffix(t *testing.T) {
	h, _, p, identityID := newHandlers(t)
	tooLong := strings.Repeat("a", 65)
	args, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"create": map[string]any{
			"c1": map[string]any{
				"baseIdentityId": identityID,
				"suffix":         tooLong,
				"action":         store.TaggedAddressActionLabel,
				"labelName":      "X",
			},
		},
	})
	r, _ := setHandler{h: h}.executeAs(p, args)
	resp := r.(setResponse)
	se, ok := resp.NotCreated["c1"]
	if !ok || se.Type != "invalidProperties" {
		t.Fatalf("expected invalidProperties on oversized suffix, got %+v", resp.NotCreated)
	}
}

func TestSet_RejectsEmptySuffix(t *testing.T) {
	h, _, p, identityID := newHandlers(t)
	args, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"create": map[string]any{
			"c1": map[string]any{
				"baseIdentityId": identityID,
				"suffix":         "",
				"action":         store.TaggedAddressActionLabel,
				"labelName":      "X",
			},
		},
	})
	r, _ := setHandler{h: h}.executeAs(p, args)
	resp := r.(setResponse)
	if se, ok := resp.NotCreated["c1"]; !ok || se.Type != "invalidProperties" {
		t.Fatalf("expected invalidProperties on empty suffix, got %+v", resp.NotCreated)
	}
}

func TestSet_RejectsForeignIdentity(t *testing.T) {
	h, st, p, _ := newHandlers(t)
	// Create another principal + identity.
	other, err := st.Meta().InsertPrincipal(context.Background(), store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "bob@example.test",
		DisplayName:    "Bob",
	})
	if err != nil {
		t.Fatalf("insert principal: %v", err)
	}
	otherIdent := "other-ident"
	if err := st.Meta().InsertJMAPIdentity(context.Background(), store.JMAPIdentity{
		ID:           otherIdent,
		PrincipalID:  other.ID,
		Email:        "bob@example.test",
		MayDelete:    true,
		VerifiedAtUs: time.Now().UnixMicro(),
	}); err != nil {
		t.Fatalf("insert other identity: %v", err)
	}
	args, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"create": map[string]any{
			"c1": map[string]any{
				"baseIdentityId": otherIdent,
				"suffix":         "foo",
				"action":         store.TaggedAddressActionLabel,
				"labelName":      "X",
			},
		},
	})
	r, _ := setHandler{h: h}.executeAs(p, args)
	resp := r.(setResponse)
	if se, ok := resp.NotCreated["c1"]; !ok || se.Type != "invalidProperties" {
		t.Fatalf("expected invalidProperties on foreign identity, got %+v", resp.NotCreated)
	}
}

func TestSet_RejectsUnverifiedIdentity(t *testing.T) {
	h, st, p, _ := newHandlers(t)
	// Create an additional unverified identity (mayDelete=true, verifiedAt=0).
	unv := "unverified-ident"
	if err := st.Meta().InsertJMAPIdentity(context.Background(), store.JMAPIdentity{
		ID:          unv,
		PrincipalID: p.ID,
		Email:       "alice2@example.test",
		MayDelete:   true,
	}); err != nil {
		t.Fatalf("insert unverified identity: %v", err)
	}
	args, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"create": map[string]any{
			"c1": map[string]any{
				"baseIdentityId": unv,
				"suffix":         "foo",
				"action":         store.TaggedAddressActionLabel,
				"labelName":      "X",
			},
		},
	})
	r, _ := setHandler{h: h}.executeAs(p, args)
	resp := r.(setResponse)
	se, ok := resp.NotCreated["c1"]
	if !ok || se.Type != "forbidden" {
		t.Fatalf("expected forbidden on unverified identity, got %+v", resp.NotCreated)
	}
	if !strings.Contains(se.Description, "identity_not_verified") {
		t.Fatalf("description should mention identity_not_verified: %q", se.Description)
	}
}

// TestSet_DefaultIdentityMaterialisesRow exercises Gap 1: a /set
// create whose baseIdentityId is the wire-form literal "default" must
// succeed by materialising the synthesised default identity. Without
// the fix, GetJMAPIdentity returns ErrNotFound and the create is
// rejected with invalidProperties even though the principal owns the
// identity by construction (REQ-TAG-72 + REQ-IDENT-*).
//
// Assertions:
//   - The create succeeds (Created non-empty, no NotCreated entry).
//   - The returned baseIdentityId is a numeric materialised id, NOT
//     the literal "default".
//   - A jmap_identities row now exists for that id.
//   - The tagged_address_filters row references the materialised id.
func TestSet_DefaultIdentityMaterialisesRow(t *testing.T) {
	// Build a fresh principal with NO jmap_identities row inserted —
	// the default identity is purely synthesised at this point. We
	// cannot use newHandlers() because it pre-inserts a verified
	// identity row.
	st := newStore(t)
	ctx := context.Background()
	if err := st.Meta().InsertDomain(ctx, store.Domain{Name: "example.test", IsLocal: true}); err != nil {
		t.Fatalf("insert domain: %v", err)
	}
	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "carol@example.test",
		DisplayName:    "Carol",
	})
	if err != nil {
		t.Fatalf("insert principal: %v", err)
	}
	h := &handlerSet{
		store:  st,
		clk:    clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		caps: func() (int, int) {
			return store.MaxTaggedAddressFiltersPerPrincipal,
				store.MaxTaggedAddressDismissalsPerPrincipal
		},
	}

	args, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"create": map[string]any{
			"c1": map[string]any{
				"baseIdentityId": "default",
				"suffix":         "amazon",
				"action":         store.TaggedAddressActionLabel,
				"labelName":      "Shopping",
			},
		},
	})
	r, merr := setHandler{h: h}.executeAs(p, args)
	if merr != nil {
		t.Fatalf("set: %v", merr)
	}
	resp := r.(setResponse)
	if len(resp.NotCreated) != 0 {
		t.Fatalf("unexpected notCreated: %+v", resp.NotCreated)
	}
	rec, ok := resp.Created["c1"]
	if !ok {
		t.Fatalf("c1 not created: %+v", resp)
	}
	if rec.BaseIdentityID == "default" {
		t.Fatalf("baseIdentityId must NOT be the literal \"default\" after materialisation, got %q", rec.BaseIdentityID)
	}
	// The materialised id is a decimal numeric string.
	if _, err := strconv.ParseUint(rec.BaseIdentityID, 10, 64); err != nil {
		t.Fatalf("materialised baseIdentityId is not numeric: %q (%v)", rec.BaseIdentityID, err)
	}
	// The jmap_identities row now exists.
	row, err := st.Meta().GetJMAPIdentity(ctx, rec.BaseIdentityID)
	if err != nil {
		t.Fatalf("GetJMAPIdentity(%q) after materialisation: %v", rec.BaseIdentityID, err)
	}
	if row.PrincipalID != p.ID {
		t.Fatalf("materialised row owned by pid %d; want %d", row.PrincipalID, p.ID)
	}
	if row.MayDelete {
		t.Fatalf("materialised default identity must have may_delete=false")
	}
	// The filter row references the materialised id.
	filters, err := st.Meta().ListTaggedAddressFiltersForPrincipal(ctx, p.ID)
	if err != nil {
		t.Fatalf("ListTaggedAddressFiltersForPrincipal: %v", err)
	}
	if len(filters) != 1 {
		t.Fatalf("filters = %d; want 1", len(filters))
	}
	if filters[0].BaseIdentityID != rec.BaseIdentityID {
		t.Fatalf("filter row base_identity_id = %q; want materialised %q", filters[0].BaseIdentityID, rec.BaseIdentityID)
	}

	// A second /set against the same "default" wire-form must
	// idempotently re-use the same materialised id (no second row).
	args2, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"create": map[string]any{
			"c2": map[string]any{
				"baseIdentityId": "default",
				"suffix":         "newsletters",
				"action":         store.TaggedAddressActionLabel,
				"labelName":      "News",
			},
		},
	})
	r2, merr2 := setHandler{h: h}.executeAs(p, args2)
	if merr2 != nil {
		t.Fatalf("second set: %v", merr2)
	}
	resp2 := r2.(setResponse)
	rec2, ok := resp2.Created["c2"]
	if !ok {
		t.Fatalf("c2 not created: %+v", resp2)
	}
	if rec2.BaseIdentityID != rec.BaseIdentityID {
		t.Fatalf("second create returned different materialised id: %q vs %q", rec2.BaseIdentityID, rec.BaseIdentityID)
	}
}

func TestSet_CapEnforced(t *testing.T) {
	h, _, p, identityID := newHandlers(t)
	// Lower the cap to 2 to keep the test cheap.
	h.caps = func() (int, int) { return 2, 500 }

	// Fill to the cap.
	for i := 0; i < 2; i++ {
		args, _ := json.Marshal(map[string]any{
			"accountId": accountID(p),
			"create": map[string]any{
				"c": map[string]any{
					"baseIdentityId": identityID,
					"suffix":         "s" + strconv.Itoa(i),
					"action":         store.TaggedAddressActionLabel,
					"labelName":      "L",
				},
			},
		})
		r, merr := setHandler{h: h}.executeAs(p, args)
		if merr != nil {
			t.Fatalf("set #%d: %v", i, merr)
		}
		resp := r.(setResponse)
		if len(resp.Created) != 1 {
			t.Fatalf("set #%d not created: %+v", i, resp)
		}
	}
	// Third create exceeds the cap.
	args, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"create": map[string]any{
			"c2": map[string]any{
				"baseIdentityId": identityID,
				"suffix":         "overcap",
				"action":         store.TaggedAddressActionLabel,
				"labelName":      "L",
			},
		},
	})
	r, _ := setHandler{h: h}.executeAs(p, args)
	resp := r.(setResponse)
	se, ok := resp.NotCreated["c2"]
	if !ok || se.Type != "tooManyFilters" {
		t.Fatalf("expected tooManyFilters, got %+v", resp.NotCreated)
	}
}

func TestSet_UpdateChangesActionAndLabel(t *testing.T) {
	h, _, p, identityID := newHandlers(t)
	created := createFilter(t, h, p, identityID, "github",
		store.TaggedAddressActionLabel, "Code")
	args, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"update": map[string]any{
			created.ID: map[string]any{
				"action":    store.TaggedAddressActionLabelArchiveRead,
				"labelName": "Code/Notifications",
			},
		},
	})
	r, merr := setHandler{h: h}.executeAs(p, args)
	if merr != nil {
		t.Fatalf("update: %v", merr)
	}
	resp := r.(setResponse)
	rec, ok := resp.Updated[created.ID]
	if !ok {
		t.Fatalf("update did not produce a row: %+v", resp)
	}
	if rec.Action != store.TaggedAddressActionLabelArchiveRead {
		t.Fatalf("action = %q after update", rec.Action)
	}
	if rec.LabelName != "Code/Notifications" {
		t.Fatalf("label = %q after update", rec.LabelName)
	}
}

func TestSet_UpdateRejectsImmutableSuffix(t *testing.T) {
	h, _, p, identityID := newHandlers(t)
	created := createFilter(t, h, p, identityID, "github",
		store.TaggedAddressActionLabel, "Code")
	args, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"update": map[string]any{
			created.ID: map[string]any{
				"suffix": "different",
			},
		},
	})
	r, _ := setHandler{h: h}.executeAs(p, args)
	resp := r.(setResponse)
	se, ok := resp.NotUpdated[created.ID]
	if !ok || se.Type != "invalidProperties" {
		t.Fatalf("expected invalidProperties on immutable suffix, got %+v", resp.NotUpdated)
	}
}

func TestSet_UpdateRejectsInvalidAction(t *testing.T) {
	h, _, p, identityID := newHandlers(t)
	created := createFilter(t, h, p, identityID, "x",
		store.TaggedAddressActionLabel, "L")
	args, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"update": map[string]any{
			created.ID: map[string]any{
				"action": "bogus",
			},
		},
	})
	r, _ := setHandler{h: h}.executeAs(p, args)
	resp := r.(setResponse)
	if se, ok := resp.NotUpdated[created.ID]; !ok || se.Type != "invalidProperties" {
		t.Fatalf("expected invalidProperties on bogus action, got %+v", resp.NotUpdated)
	}
}

func TestSet_DestroyExisting(t *testing.T) {
	h, _, p, identityID := newHandlers(t)
	created := createFilter(t, h, p, identityID, "shop",
		store.TaggedAddressActionLabel, "Shop")
	args, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"destroy":   []string{created.ID},
	})
	r, merr := setHandler{h: h}.executeAs(p, args)
	if merr != nil {
		t.Fatalf("destroy: %v", merr)
	}
	resp := r.(setResponse)
	if len(resp.Destroyed) != 1 || resp.Destroyed[0] != created.ID {
		t.Fatalf("destroyed = %+v, want [%s]", resp.Destroyed, created.ID)
	}
	// /get is empty again.
	gargs, _ := json.Marshal(map[string]any{"accountId": accountID(p)})
	gr, _ := getHandler{h: h}.executeAs(p, gargs)
	if len(gr.(getResponse).List) != 0 {
		t.Fatalf("list non-empty after destroy: %+v", gr)
	}
}

func TestSet_DestroyUnknownIDReports(t *testing.T) {
	h, _, p, _ := newHandlers(t)
	args, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"destroy":   []string{"no-such-id"},
	})
	r, _ := setHandler{h: h}.executeAs(p, args)
	resp := r.(setResponse)
	if se, ok := resp.NotDestroyed["no-such-id"]; !ok || se.Type != "notFound" {
		t.Fatalf("expected notFound for unknown id, got %+v", resp.NotDestroyed)
	}
}

func TestSet_DuplicateSuffixReportsConflict(t *testing.T) {
	h, _, p, identityID := newHandlers(t)
	_ = createFilter(t, h, p, identityID, "dup",
		store.TaggedAddressActionLabel, "L")
	// Try to create another with the same suffix.
	args, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"create": map[string]any{
			"c2": map[string]any{
				"baseIdentityId": identityID,
				"suffix":         "dup",
				"action":         store.TaggedAddressActionLabel,
				"labelName":      "L",
			},
		},
	})
	r, _ := setHandler{h: h}.executeAs(p, args)
	resp := r.(setResponse)
	if se, ok := resp.NotCreated["c2"]; !ok || se.Type != "conflict" {
		t.Fatalf("expected conflict, got %+v", resp.NotCreated)
	}
}

// -- State token monotonicity ---------------------------------------

func TestSet_StateAdvancesOnEachMutation(t *testing.T) {
	h, _, p, identityID := newHandlers(t)
	// Initial state.
	args, _ := json.Marshal(map[string]any{"accountId": accountID(p)})
	r, _ := getHandler{h: h}.executeAs(p, args)
	s0 := r.(getResponse).State

	created := createFilter(t, h, p, identityID, "a",
		store.TaggedAddressActionLabel, "A")
	r, _ = getHandler{h: h}.executeAs(p, args)
	s1 := r.(getResponse).State
	if s1 == s0 {
		t.Fatalf("state did not advance after create: %q -> %q", s0, s1)
	}
	// Update.
	uargs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"update": map[string]any{
			created.ID: map[string]any{"labelName": "AA"},
		},
	})
	_, merr := setHandler{h: h}.executeAs(p, uargs)
	if merr != nil {
		t.Fatalf("update: %v", merr)
	}
	r, _ = getHandler{h: h}.executeAs(p, args)
	s2 := r.(getResponse).State
	if s2 == s1 {
		t.Fatalf("state did not advance after update: %q -> %q", s1, s2)
	}
	// Destroy.
	dargs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"destroy":   []string{created.ID},
	})
	_, merrD := setHandler{h: h}.executeAs(p, dargs)
	if merrD != nil {
		t.Fatalf("destroy: %v", merrD)
	}
	r, _ = getHandler{h: h}.executeAs(p, args)
	s3 := r.(getResponse).State
	if s3 == s2 {
		t.Fatalf("state did not advance after destroy: %q -> %q", s2, s3)
	}
	// Monotonic ordering (numeric).
	if !strictlyAscending(t, []string{s0, s1, s2, s3}) {
		t.Fatalf("state not strictly ascending: %q,%q,%q,%q", s0, s1, s2, s3)
	}
}

func strictlyAscending(t *testing.T, vals []string) bool {
	t.Helper()
	prev := int64(-1)
	for _, v := range vals {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			t.Fatalf("non-numeric state %q: %v", v, err)
		}
		if n <= prev {
			return false
		}
		prev = n
	}
	return true
}

func TestSet_NonMutatingCallDoesNotAdvanceState(t *testing.T) {
	h, _, p, _ := newHandlers(t)
	// Initial state.
	args, _ := json.Marshal(map[string]any{"accountId": accountID(p)})
	r, _ := getHandler{h: h}.executeAs(p, args)
	s0 := r.(getResponse).State

	// /set with a bad create — produces a notCreated but no mutation.
	sargs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"create": map[string]any{
			"c1": map[string]any{
				"baseIdentityId": "bad-ident",
				"suffix":         "x",
				"action":         store.TaggedAddressActionLabel,
				"labelName":      "X",
			},
		},
	})
	r2, _ := setHandler{h: h}.executeAs(p, sargs)
	resp := r2.(setResponse)
	if resp.NewState != s0 {
		t.Fatalf("state advanced on failed create: %q -> %q", s0, resp.NewState)
	}
}

// -- TaggedAddressFilter/changes -----------------------------------

func TestChanges_EmptyOnSameState(t *testing.T) {
	h, _, p, _ := newHandlers(t)
	gargs, _ := json.Marshal(map[string]any{"accountId": accountID(p)})
	r, _ := getHandler{h: h}.executeAs(p, gargs)
	s0 := r.(getResponse).State

	cargs, _ := json.Marshal(map[string]any{
		"accountId":  accountID(p),
		"sinceState": s0,
	})
	cr, merr := changesHandler{h: h}.executeAs(p, cargs)
	if merr != nil {
		t.Fatalf("changes: %v", merr)
	}
	resp := cr.(changesResponse)
	if resp.OldState != s0 || resp.NewState != s0 {
		t.Fatalf("changes mismatch: old=%q new=%q want %q", resp.OldState, resp.NewState, s0)
	}
	if len(resp.Created) != 0 || len(resp.Updated) != 0 || len(resp.Destroyed) != 0 {
		t.Fatalf("changes nonempty: %+v", resp)
	}
}

func TestChanges_MentionsUpdatedIDs(t *testing.T) {
	h, _, p, identityID := newHandlers(t)
	gargs, _ := json.Marshal(map[string]any{"accountId": accountID(p)})
	r, _ := getHandler{h: h}.executeAs(p, gargs)
	s0 := r.(getResponse).State

	rec := createFilter(t, h, p, identityID, "abc",
		store.TaggedAddressActionLabel, "L")

	cargs, _ := json.Marshal(map[string]any{
		"accountId":  accountID(p),
		"sinceState": s0,
	})
	cr, _ := changesHandler{h: h}.executeAs(p, cargs)
	resp := cr.(changesResponse)
	if resp.NewState == s0 {
		t.Fatalf("changes.newState did not advance from %q", s0)
	}
	found := false
	for _, id := range resp.Updated {
		if id == rec.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("changes.updated did not include %q: %+v", rec.ID, resp)
	}
}

// -- Audit log entries ----------------------------------------------

func TestAudit_CreateUpdateDestroyEmitsRows(t *testing.T) {
	h, st, p, identityID := newHandlers(t)
	created := createFilter(t, h, p, identityID, "audit",
		store.TaggedAddressActionLabel, "L")
	uargs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"update": map[string]any{
			created.ID: map[string]any{"labelName": "LL"},
		},
	})
	_, merr := setHandler{h: h}.executeAs(p, uargs)
	if merr != nil {
		t.Fatalf("update: %v", merr)
	}
	dargs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"destroy":   []string{created.ID},
	})
	_, merr2 := setHandler{h: h}.executeAs(p, dargs)
	if merr2 != nil {
		t.Fatalf("destroy: %v", merr2)
	}

	// Read recent audit entries; assert each action appears.
	entries, err := st.Meta().ListAuditLog(context.Background(), store.AuditLogFilter{
		Limit: 100,
	})
	if err != nil {
		t.Fatalf("list audit log: %v", err)
	}
	want := map[string]bool{
		"tagged_address_filter.create":  false,
		"tagged_address_filter.update":  false,
		"tagged_address_filter.destroy": false,
	}
	for _, e := range entries {
		if _, ok := want[e.Action]; ok {
			want[e.Action] = true
			if e.Subject != created.ID {
				t.Fatalf("audit subject = %q, want %q", e.Subject, created.ID)
			}
			if e.ActorKind != store.ActorPrincipal {
				t.Fatalf("audit actor kind = %v", e.ActorKind)
			}
		}
	}
	for k, seen := range want {
		if !seen {
			t.Fatalf("audit action %q not emitted", k)
		}
	}
}

// -- ifInState concurrency guard ------------------------------------

func TestSet_IfInStateMismatchRejects(t *testing.T) {
	h, _, p, _ := newHandlers(t)
	bogus := "999999"
	args, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"ifInState": bogus,
	})
	_, merr := setHandler{h: h}.executeAs(p, args)
	if merr == nil || merr.Type != "stateMismatch" {
		t.Fatalf("expected stateMismatch, got %+v", merr)
	}
}
