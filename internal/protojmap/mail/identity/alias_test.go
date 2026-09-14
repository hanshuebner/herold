package identity

// Identity alias addresses (REQ-IDENT-01, re #387): an Identity can
// carry additional addr-specs that select it as the reply sender
// alongside its primary Email. These tests exercise the JMAP-facing
// wiring -- Identity/get rendering, Identity/set create/update/destroy,
// and the mapping of the store's validation/conflict errors onto
// SetError -- on top of the already-tested store-level semantics
// (internal/store/storetest, migration 0108).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
)

// newHandlersPostgres is newHandlers' Postgres counterpart, without the
// sub-accounts capability (subaccount_test.go's
// newHandlersWithSubAccountsPostgres carries that). Skips when
// HEROLD_PG_DSN is unset or the connection cannot be established.
func newHandlersPostgres(t *testing.T) (*handlerSet, store.Store, store.Principal) {
	t.Helper()
	dsn := os.Getenv("HEROLD_PG_DSN")
	if dsn == "" {
		t.Skip("HEROLD_PG_DSN not set; skipping Postgres leg")
	}
	st, err := storepg.Open(context.Background(), dsn, t.TempDir(), nil, nil)
	if err != nil {
		t.Skipf("storepg.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	canonicalEmail := uniqueTestEmail("alice")
	// A real-time clock: allocateIdentityID keys off the clock's
	// nanosecond and this fixture's database persists across every
	// Postgres test function in the run (see the identical comment on
	// newHandlersUsingStore in identity_test.go).
	return newHandlersUsingStore(t, st, canonicalEmail, clock.NewReal())
}

// advanceClockIfFake nudges h's identity Store clock forward by one
// nanosecond when it is backed by a clock.FakeClock (SQLite fixtures
// use a fixed fake clock so results are deterministic). Postgres
// fixtures use a real clock and this is a no-op there.
// allocateIdentityID keys a new identity's id off the clock's
// nanosecond, so a test that creates two identities against a fixed
// fake clock without this would collide on the same id.
func advanceClockIfFake(h *handlerSet) {
	if fc, ok := h.identity.clk.(*clock.FakeClock); ok {
		fc.Advance(time.Nanosecond)
	}
}

// uniqueTestEmailCounter guarantees uniqueTestEmail never collides even
// across two calls in the same nanosecond.
var uniqueTestEmailCounter atomic.Uint64

// uniqueTestEmail returns a nanosecond-plus-counter-suffixed local-part
// @example.test address so repeated Postgres test runs against a
// persistent database never collide on an already-registered address.
func uniqueTestEmail(local string) string {
	n := uniqueTestEmailCounter.Add(1)
	return fmt.Sprintf("%s-%d-%s@example.test", local, time.Now().UnixNano(), strconv.FormatUint(n, 36))
}

// createIdentityWithAliases is createIdentity plus an aliases list.
func createIdentityWithAliases(t *testing.T, h *handlerSet, p store.Principal, clientID, name, email string, aliases []string) (jmapIdentity, *setError) {
	t.Helper()
	create := map[string]any{"name": name, "email": email}
	if aliases != nil {
		create["aliases"] = aliases
	}
	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create":    map[string]any{clientID: create},
	})
	resp, mErr := setHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("Identity/set create %s: %v", clientID, mErr)
	}
	sr := resp.(setResponse)
	if created, ok := sr.Created[clientID]; ok {
		return created, nil
	}
	se, ok := sr.NotCreated[clientID]
	if !ok {
		t.Fatalf("create %s: neither created nor notCreated in response: %+v", clientID, sr)
	}
	return jmapIdentity{}, &se
}

func testAliasCreateGetRoundTrip(t *testing.T, h *handlerSet, p store.Principal) {
	created, serr := createIdentityWithAliases(t, h, p, "work", "Alice At Work",
		"alice.work@example.test", []string{"alias-a@example.test", "alias-b@example.test"})
	if serr != nil {
		t.Fatalf("create rejected: %+v", serr)
	}
	if len(created.Aliases) != 2 || created.Aliases[0] != "alias-a@example.test" || created.Aliases[1] != "alias-b@example.test" {
		t.Fatalf("Created.Aliases = %+v; want [alias-a@example.test alias-b@example.test]", created.Aliases)
	}

	getArgs, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"ids":       []string{created.ID},
	})
	resp, mErr := getHandler{h: h}.executeAs(p, getArgs)
	if mErr != nil {
		t.Fatalf("Identity/get: %v", mErr)
	}
	list := resp.(getResponse).List
	if len(list) != 1 {
		t.Fatalf("Identity/get list len = %d; want 1", len(list))
	}
	got := list[0].Aliases
	if len(got) != 2 || got[0] != "alias-a@example.test" || got[1] != "alias-b@example.test" {
		t.Fatalf("Identity/get Aliases = %+v; want [alias-a@example.test alias-b@example.test]", got)
	}
}

func TestIdentity_Set_Create_WithAliases_SQLite(t *testing.T) {
	h, _, p := newHandlers(t)
	testAliasCreateGetRoundTrip(t, h, p)
}

func TestIdentity_Set_Create_WithAliases_Postgres(t *testing.T) {
	h, _, p := newHandlersPostgres(t)
	testAliasCreateGetRoundTrip(t, h, p)
}

func testAliasUpdateFullReplace(t *testing.T, h *handlerSet, p store.Principal) {
	created, serr := createIdentityWithAliases(t, h, p, "work", "Alice At Work",
		uniqueTestEmail("alice.work"), []string{uniqueTestEmail("alias-a"), uniqueTestEmail("alias-b")})
	if serr != nil {
		t.Fatalf("create rejected: %+v", serr)
	}
	replacement := uniqueTestEmail("alias-c")
	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"update": map[string]any{
			created.ID: map[string]any{"aliases": []string{replacement}},
		},
	})
	resp, mErr := setHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("Identity/set update aliases: %v", mErr)
	}
	sr := resp.(setResponse)
	updated, ok := sr.Updated[created.ID]
	if !ok {
		t.Fatalf("expected updated; got notUpdated: %+v", sr.NotUpdated)
	}
	if len(updated.Aliases) != 1 || updated.Aliases[0] != replacement {
		t.Fatalf("Updated.Aliases = %+v; want [%s]", updated.Aliases, replacement)
	}

	// Clearing to an empty list is a full replace too.
	clearArgs, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"update": map[string]any{
			created.ID: map[string]any{"aliases": []string{}},
		},
	})
	clearResp, mErr := setHandler{h: h}.executeAs(p, clearArgs)
	if mErr != nil {
		t.Fatalf("Identity/set clear aliases: %v", mErr)
	}
	cleared, ok := clearResp.(setResponse).Updated[created.ID]
	if !ok {
		t.Fatalf("expected updated after clear; got notUpdated: %+v", clearResp.(setResponse).NotUpdated)
	}
	if len(cleared.Aliases) != 0 {
		t.Fatalf("Cleared.Aliases = %+v; want empty", cleared.Aliases)
	}
}

func TestIdentity_Set_Update_Aliases_FullReplace_SQLite(t *testing.T) {
	h, _, p := newHandlers(t)
	testAliasUpdateFullReplace(t, h, p)
}

func TestIdentity_Set_Update_Aliases_FullReplace_Postgres(t *testing.T) {
	h, _, p := newHandlersPostgres(t)
	testAliasUpdateFullReplace(t, h, p)
}

func testAliasCreateDuplicateWithinList(t *testing.T, h *handlerSet, p store.Principal) {
	dup := uniqueTestEmail("dup-alias")
	_, serr := createIdentityWithAliases(t, h, p, "work", "Alice At Work",
		uniqueTestEmail("alice.work"), []string{dup, dup})
	if serr == nil {
		t.Fatal("expected notCreated for a duplicate alias within one create")
	}
	if serr.Type != "invalidProperties" || len(serr.Properties) != 1 || serr.Properties[0] != "aliases" {
		t.Fatalf("setError = %+v; want invalidProperties [aliases]", serr)
	}
}

func TestIdentity_Set_Create_DuplicateAliasWithinList_Rejected_SQLite(t *testing.T) {
	h, _, p := newHandlers(t)
	testAliasCreateDuplicateWithinList(t, h, p)
}

func TestIdentity_Set_Create_DuplicateAliasWithinList_Rejected_Postgres(t *testing.T) {
	h, _, p := newHandlersPostgres(t)
	testAliasCreateDuplicateWithinList(t, h, p)
}

func testAliasCreateEqualsOwnPrimary(t *testing.T, h *handlerSet, p store.Principal) {
	email := uniqueTestEmail("alice.self")
	_, serr := createIdentityWithAliases(t, h, p, "self", "Alice", email, []string{email})
	if serr == nil {
		t.Fatal("expected notCreated for an alias equal to the identity's own primary address")
	}
	if serr.Type != "invalidProperties" || len(serr.Properties) != 1 || serr.Properties[0] != "aliases" {
		t.Fatalf("setError = %+v; want invalidProperties [aliases]", serr)
	}
}

func TestIdentity_Set_Create_AliasEqualsOwnPrimary_Rejected_SQLite(t *testing.T) {
	h, _, p := newHandlers(t)
	testAliasCreateEqualsOwnPrimary(t, h, p)
}

func TestIdentity_Set_Create_AliasEqualsOwnPrimary_Rejected_Postgres(t *testing.T) {
	h, _, p := newHandlersPostgres(t)
	testAliasCreateEqualsOwnPrimary(t, h, p)
}

// testAliasCreateEqualsAnotherIdentityPrimary proves the cross-identity
// uniqueness invariant: no identity's alias may claim another
// identity's primary address.
func testAliasCreateEqualsAnotherIdentityPrimary(t *testing.T, h *handlerSet, p store.Principal) {
	first, serr := createIdentityWithAliases(t, h, p, "first", "Alice First",
		uniqueTestEmail("alice.first"), nil)
	if serr != nil {
		t.Fatalf("create first rejected: %+v", serr)
	}
	advanceClockIfFake(h)
	_, serr = createIdentityWithAliases(t, h, p, "second", "Alice Second",
		uniqueTestEmail("alice.second"), []string{first.Email})
	if serr == nil {
		t.Fatal("expected notCreated for an alias claiming another identity's primary address")
	}
	if serr.Type != "invalidProperties" || len(serr.Properties) != 1 || serr.Properties[0] != "aliases" {
		t.Fatalf("setError = %+v; want invalidProperties [aliases]", serr)
	}
}

func TestIdentity_Set_Create_AliasEqualsAnotherIdentityPrimary_Rejected_SQLite(t *testing.T) {
	h, _, p := newHandlers(t)
	testAliasCreateEqualsAnotherIdentityPrimary(t, h, p)
}

func TestIdentity_Set_Create_AliasEqualsAnotherIdentityPrimary_Rejected_Postgres(t *testing.T) {
	h, _, p := newHandlersPostgres(t)
	testAliasCreateEqualsAnotherIdentityPrimary(t, h, p)
}

func testAliasCreateMalformed(t *testing.T, h *handlerSet, p store.Principal) {
	_, serr := createIdentityWithAliases(t, h, p, "work", "Alice At Work",
		uniqueTestEmail("alice.work"), []string{"not-an-email-address"})
	if serr == nil {
		t.Fatal("expected notCreated for a malformed alias")
	}
	if serr.Type != "invalidProperties" || len(serr.Properties) != 1 || serr.Properties[0] != "aliases" {
		t.Fatalf("setError = %+v; want invalidProperties [aliases]", serr)
	}
}

func TestIdentity_Set_Create_MalformedAlias_Rejected_SQLite(t *testing.T) {
	h, _, p := newHandlers(t)
	testAliasCreateMalformed(t, h, p)
}

func TestIdentity_Set_Create_MalformedAlias_Rejected_Postgres(t *testing.T) {
	h, _, p := newHandlersPostgres(t)
	testAliasCreateMalformed(t, h, p)
}

// testAliasUpdateDefaultRejected proves the synthesised default
// identity -- which has no backing jmap_identities row -- refuses an
// "aliases" update outright rather than silently dropping it.
func testAliasUpdateDefaultRejected(t *testing.T, h *handlerSet, p store.Principal) {
	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"update": map[string]any{
			"default": map[string]any{"aliases": []string{uniqueTestEmail("alias")}},
		},
	})
	resp, mErr := setHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("Identity/set: %v", mErr)
	}
	se, ok := resp.(setResponse).NotUpdated["default"]
	if !ok {
		t.Fatalf("expected notUpdated[default]; got updated: %+v", resp.(setResponse).Updated)
	}
	if se.Type != "invalidProperties" || len(se.Properties) != 1 || se.Properties[0] != "aliases" {
		t.Fatalf("setError = %+v; want invalidProperties [aliases]", se)
	}
}

func TestIdentity_Set_Update_Default_RejectsAliases_SQLite(t *testing.T) {
	h, _, p := newHandlers(t)
	testAliasUpdateDefaultRejected(t, h, p)
}

func TestIdentity_Set_Update_Default_RejectsAliases_Postgres(t *testing.T) {
	h, _, p := newHandlersPostgres(t)
	testAliasUpdateDefaultRejected(t, h, p)
}

// testAliasDestroyFreesAlias proves the migration 0108 cascade: once an
// identity carrying an alias is destroyed, the same address is free to
// be claimed by a brand new identity (the alias row was removed with
// its owner rather than lingering as an orphan unique-index entry).
func testAliasDestroyFreesAlias(t *testing.T, h *handlerSet, p store.Principal) {
	alias := uniqueTestEmail("alias-reclaim")
	created, serr := createIdentityWithAliases(t, h, p, "work", "Alice At Work",
		uniqueTestEmail("alice.work"), []string{alias})
	if serr != nil {
		t.Fatalf("create rejected: %+v", serr)
	}

	destroyArgs, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"destroy":   []string{created.ID},
	})
	destroyResp, mErr := setHandler{h: h}.executeAs(p, destroyArgs)
	if mErr != nil {
		t.Fatalf("Identity/set destroy: %v", mErr)
	}
	sr := destroyResp.(setResponse)
	if len(sr.Destroyed) != 1 || sr.Destroyed[0] != created.ID {
		t.Fatalf("Destroyed = %+v; want [%s]", sr.Destroyed, created.ID)
	}

	// The alias address, previously claimed by the destroyed identity,
	// is now free to become a fresh identity's primary address.
	reused, serr := createIdentityWithAliases(t, h, p, "reused", "Alice Reused", alias, nil)
	if serr != nil {
		t.Fatalf("re-create with the freed alias address rejected: %+v", serr)
	}
	if reused.Email != alias {
		t.Fatalf("reused.Email = %q; want %q", reused.Email, alias)
	}
}

func TestIdentity_Set_Destroy_FreesAlias_SQLite(t *testing.T) {
	h, _, p := newHandlers(t)
	testAliasDestroyFreesAlias(t, h, p)
}

func TestIdentity_Set_Destroy_FreesAlias_Postgres(t *testing.T) {
	h, _, p := newHandlersPostgres(t)
	testAliasDestroyFreesAlias(t, h, p)
}

// testAliasSurvivesSeparation proves RebindJMAPIdentityPrincipal (the
// #227 sub-account promotion primitive) carries an identity's alias
// rows along with it: an identity's aliases are unchanged immediately
// after Identity/set{separated:true} and remain visible through
// Identity/get addressed at the resulting sub-account.
func testAliasSurvivesSeparation(t *testing.T, h *handlerSet, p store.Principal) {
	alias := uniqueTestEmail("alias-carry")
	created, serr := createIdentityWithAliases(t, h, p, "ext", "External",
		uniqueTestEmail("ext"), []string{alias})
	if serr != nil {
		t.Fatalf("create rejected: %+v", serr)
	}

	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"update":    map[string]any{created.ID: map[string]any{"separated": true}},
	})
	resp, mErr := setHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("Identity/set separated:true: %v", mErr)
	}
	sr := resp.(setResponse)
	updated, ok := sr.Updated[created.ID]
	if !ok {
		t.Fatalf("expected updated; got notUpdated: %+v", sr.NotUpdated)
	}
	if updated.SubAccountId == nil {
		t.Fatal("subAccountId is nil immediately after separated:true")
	}
	if len(updated.Aliases) != 1 || updated.Aliases[0] != alias {
		t.Fatalf("Updated.Aliases immediately after separation = %+v; want [%s]", updated.Aliases, alias)
	}
	subAccountID := *updated.SubAccountId

	// The alias also round-trips through Identity/get addressed at the
	// sub-account (the identity is now listed there, not under the
	// parent).
	getArgs, _ := json.Marshal(map[string]any{"accountId": subAccountID, "ids": []string{created.ID}})
	getResp, mErr := getHandler{h: h}.executeAs(p, getArgs)
	if mErr != nil {
		t.Fatalf("Identity/get(sub-account): %v", mErr)
	}
	list := getResp.(getResponse).List
	if len(list) != 1 {
		t.Fatalf("sub-account Identity/get list len = %d; want 1", len(list))
	}
	if len(list[0].Aliases) != 1 || list[0].Aliases[0] != alias {
		t.Fatalf("sub-account Identity/get Aliases = %+v; want [%s]", list[0].Aliases, alias)
	}
}

func TestIdentity_Set_Separation_CarriesAliases_SQLite(t *testing.T) {
	h, _, p := newHandlersWithSubAccounts(t)
	testAliasSurvivesSeparation(t, h, p)
}

func TestIdentity_Set_Separation_CarriesAliases_Postgres(t *testing.T) {
	h, _, p := newHandlersWithSubAccountsPostgres(t)
	testAliasSurvivesSeparation(t, h, p)
}

// TestIdentity_Get_DefaultIdentity_AliasesEmptyArray proves the
// synthesised default identity always renders aliases as an empty
// array (never null, never populated) -- it carries no aliases
// per the issue's contract.
func TestIdentity_Get_DefaultIdentity_AliasesEmptyArray(t *testing.T) {
	h, _, p := newHandlers(t)
	args, _ := json.Marshal(map[string]any{"accountId": protojmap.AccountIDForPrincipal(p.ID)})
	resp, mErr := getHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("Identity/get: %v", mErr)
	}
	list := resp.(getResponse).List
	for _, rec := range list {
		if rec.ID != "default" {
			continue
		}
		if rec.Aliases == nil || len(rec.Aliases) != 0 {
			t.Fatalf("default identity Aliases = %+v; want empty non-nil slice", rec.Aliases)
		}
		js, _ := json.Marshal(rec)
		if !strings.Contains(string(js), `"aliases":[]`) {
			t.Fatalf("default identity aliases did not marshal as []: %s", js)
		}
		return
	}
	t.Fatalf("default identity not found in list: %+v", list)
}
