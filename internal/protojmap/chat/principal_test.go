package chat_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// -- helpers ----------------------------------------------------------

func insertPrincipal(t *testing.T, f *fixture, email, displayName string) store.Principal {
	t.Helper()
	ctx := context.Background()
	p, err := f.srv.Store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: email,
		DisplayName:    displayName,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal(%s): %v", email, err)
	}
	return p
}

func principalGet(t *testing.T, f *fixture, ids []string) (string, map[string]any) {
	t.Helper()
	name, raw := f.invoke(t, "Principal/get", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"ids":       ids,
	})
	var resp map[string]any
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal Principal/get: %v: %s", err, raw)
	}
	return name, resp
}

func principalQuery(t *testing.T, f *fixture, filter map[string]any, limit *int) (string, map[string]any) {
	t.Helper()
	args := map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"filter":    filter,
	}
	if limit != nil {
		args["limit"] = *limit
	}
	name, raw := f.invoke(t, "Principal/query", args)
	var resp map[string]any
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal Principal/query: %v: %s", err, raw)
	}
	return name, resp
}

// -- Principal/get tests ----------------------------------------------

func TestPrincipalGet_FoundAndNotFound(t *testing.T) {
	f := setupFixture(t)

	diana := insertPrincipal(t, f, "diana@example.test", "Diana Prince")

	name, resp := principalGet(t, f, []string{pidStr(diana.ID), "999999"})
	if name != "Principal/get" {
		t.Fatalf("method name = %q, want Principal/get", name)
	}

	list, _ := resp["list"].([]any)
	if len(list) != 1 {
		t.Fatalf("list len = %d, want 1", len(list))
	}
	rec := list[0].(map[string]any)
	if got := rec["id"]; got != pidStr(diana.ID) {
		t.Errorf("id = %v, want %s", got, pidStr(diana.ID))
	}
	if got := rec["email"]; got != "diana@example.test" {
		t.Errorf("email = %v, want diana@example.test", got)
	}
	if got := rec["displayName"]; got != "Diana Prince" {
		t.Errorf("displayName = %v, want Diana Prince", got)
	}
	// Sensitive fields must not appear.
	for _, banned := range []string{"passwordHash", "totpSecret", "flags", "createdAt", "updatedAt"} {
		if _, present := rec[banned]; present {
			t.Errorf("banned field %q present in response", banned)
		}
	}

	notFound, _ := resp["notFound"].([]any)
	if len(notFound) != 1 {
		t.Fatalf("notFound len = %d, want 1", len(notFound))
	}
	if notFound[0] != "999999" {
		t.Errorf("notFound[0] = %v, want 999999", notFound[0])
	}
}

func TestPrincipalGet_EmptyIDsReturnsEmptyList(t *testing.T) {
	f := setupFixture(t)
	name, resp := principalGet(t, f, []string{})
	if name != "Principal/get" {
		t.Fatalf("method = %q", name)
	}
	list, _ := resp["list"].([]any)
	if len(list) != 0 {
		t.Errorf("list len = %d, want 0", len(list))
	}
}

func TestPrincipalGet_PropertiesMask(t *testing.T) {
	f := setupFixture(t)
	diana := insertPrincipal(t, f, "diana2@example.test", "Diana Two")

	name, raw := f.invoke(t, "Principal/get", map[string]any{
		"accountId":  string(protojmap.AccountIDForPrincipal(f.pid)),
		"ids":        []string{pidStr(diana.ID)},
		"properties": []string{"email"},
	})
	if name != "Principal/get" {
		t.Fatalf("method = %q", name)
	}
	var resp map[string]any
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	list, _ := resp["list"].([]any)
	if len(list) != 1 {
		t.Fatalf("list len = %d, want 1", len(list))
	}
	rec := list[0].(map[string]any)
	if got := rec["email"]; got != "diana2@example.test" {
		t.Errorf("email = %v", got)
	}
	// id and displayName are masked out.
	if id, ok := rec["id"]; ok && id != "" {
		t.Errorf("id present after properties mask: %v", id)
	}
	if dn, ok := rec["displayName"]; ok && dn != "" {
		t.Errorf("displayName present after properties mask: %v", dn)
	}
}

func TestPrincipalGet_InvalidAccountID(t *testing.T) {
	f := setupFixture(t)
	name, raw := f.invoke(t, "Principal/get", map[string]any{
		"accountId": "badaccount",
		"ids":       []string{},
	})
	if name != "error" {
		t.Fatalf("method = %q, want error", name)
	}
	var mErr map[string]any
	if err := json.Unmarshal(raw, &mErr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if typ := mErr["type"]; typ != "accountNotFound" {
		t.Errorf("type = %v, want accountNotFound", typ)
	}
}

// -- Principal/query tests --------------------------------------------

func TestPrincipalQuery_EmailExact_Found(t *testing.T) {
	f := setupFixture(t)
	eve := insertPrincipal(t, f, "eve@example.test", "Eve Adams")

	name, resp := principalQuery(t, f, map[string]any{"emailExact": "eve@example.test"}, nil)
	if name != "Principal/query" {
		t.Fatalf("method = %q", name)
	}
	ids, _ := resp["ids"].([]any)
	if len(ids) != 1 {
		t.Fatalf("ids len = %d, want 1: %v", len(ids), ids)
	}
	if ids[0] != pidStr(eve.ID) {
		t.Errorf("ids[0] = %v, want %s", ids[0], pidStr(eve.ID))
	}
	if total := resp["total"]; total != float64(1) {
		t.Errorf("total = %v, want 1", total)
	}
}

func TestPrincipalQuery_EmailExact_CaseInsensitive(t *testing.T) {
	f := setupFixture(t)
	eve := insertPrincipal(t, f, "eve2@example.test", "Eve Two")

	name, resp := principalQuery(t, f, map[string]any{"emailExact": "EVE2@EXAMPLE.TEST"}, nil)
	if name != "Principal/query" {
		t.Fatalf("method = %q", name)
	}
	ids, _ := resp["ids"].([]any)
	if len(ids) != 1 || ids[0] != pidStr(eve.ID) {
		t.Errorf("ids = %v, want [%s]", ids, pidStr(eve.ID))
	}
}

func TestPrincipalQuery_EmailExact_Miss(t *testing.T) {
	f := setupFixture(t)

	name, resp := principalQuery(t, f, map[string]any{"emailExact": "ghost@example.test"}, nil)
	if name != "Principal/query" {
		t.Fatalf("method = %q", name)
	}
	ids, _ := resp["ids"].([]any)
	if len(ids) != 0 {
		t.Errorf("ids = %v, want empty", ids)
	}
	if total := resp["total"]; total != float64(0) {
		t.Errorf("total = %v, want 0", total)
	}
}

func TestPrincipalQuery_TextPrefix_DisplayNameMatch(t *testing.T) {
	f := setupFixture(t)
	insertPrincipal(t, f, "frank@example.test", "Frank Castle")
	insertPrincipal(t, f, "greta@example.test", "Greta Garbo")

	name, resp := principalQuery(t, f, map[string]any{"textPrefix": "frank"}, nil)
	if name != "Principal/query" {
		t.Fatalf("method = %q", name)
	}
	ids, _ := resp["ids"].([]any)
	if len(ids) != 1 {
		t.Fatalf("ids len = %d, want 1 (only Frank): %v", len(ids), ids)
	}
}

func TestPrincipalQuery_TextPrefix_EmailLocalPartMatch(t *testing.T) {
	f := setupFixture(t)
	insertPrincipal(t, f, "harold@example.test", "Harold Smith")
	insertPrincipal(t, f, "iris@example.test", "Iris West")

	// "har" matches "harold" local-part prefix; "Harold" also in display name.
	name, resp := principalQuery(t, f, map[string]any{"textPrefix": "har"}, nil)
	if name != "Principal/query" {
		t.Fatalf("method = %q", name)
	}
	ids, _ := resp["ids"].([]any)
	if len(ids) != 1 {
		t.Fatalf("ids len = %d, want 1: %v", len(ids), ids)
	}
}

func TestPrincipalQuery_TextPrefix_SortOrder(t *testing.T) {
	f := setupFixture(t)
	// Use a prefix "xqzali" that is unlikely to match any fixture principal
	// but appears in names and email local-parts below.
	// xqzali-zara: display name contains "xqzali" -> prio 0
	// xqzali-alice: display name "Xqzali Alice" contains "xqzali" -> prio 0
	// xqzali-only: display name "Plain User" does NOT contain "xqzali";
	//              email local "xqzali-only" starts with "xqzali" -> prio 1
	zaraP := insertPrincipal(t, f, "xqzali-zara@example.test", "Zara Xqzali")
	aliceP := insertPrincipal(t, f, "xqzali-alice@example.test", "Xqzali Alice")
	onlyP := insertPrincipal(t, f, "xqzali-only@example.test", "Plain User")

	name, resp := principalQuery(t, f, map[string]any{"textPrefix": "xqzali"}, nil)
	if name != "Principal/query" {
		t.Fatalf("method = %q", name)
	}
	ids, _ := resp["ids"].([]any)
	if len(ids) != 3 {
		t.Fatalf("ids len = %d, want 3: %v", len(ids), ids)
	}
	// prio 0 alphabetical by lower displayName:
	//   "xqzali alice" < "zara xqzali"
	// prio 1: "plain user" (email-local-part match only)
	wantIDs := []string{pidStr(aliceP.ID), pidStr(zaraP.ID), pidStr(onlyP.ID)}
	for i, w := range wantIDs {
		if ids[i] != w {
			t.Errorf("pos %d: got %v, want %s", i, ids[i], w)
		}
	}
}

func TestPrincipalQuery_LimitClamp(t *testing.T) {
	f := setupFixture(t)
	for i := 0; i < 30; i++ {
		insertPrincipal(t, f, fmt.Sprintf("clampuser%02d@example.test", i), "Clampuser")
	}
	// Request limit=100 — should be clamped to server cap of 25.
	limit := 100
	name, resp := principalQuery(t, f, map[string]any{"textPrefix": "clampuser"}, &limit)
	if name != "Principal/query" {
		t.Fatalf("method = %q", name)
	}
	ids, _ := resp["ids"].([]any)
	if len(ids) > 25 {
		t.Errorf("ids len = %d, want <= 25 (server cap)", len(ids))
	}
}

func TestPrincipalQuery_LimitDefault(t *testing.T) {
	f := setupFixture(t)
	for i := 0; i < 15; i++ {
		insertPrincipal(t, f, fmt.Sprintf("defaultlimit%02d@example.test", i), "Defaultlimit")
	}
	name, resp := principalQuery(t, f, map[string]any{"textPrefix": "defaultlimit"}, nil)
	if name != "Principal/query" {
		t.Fatalf("method = %q", name)
	}
	ids, _ := resp["ids"].([]any)
	// Default is 10; we seeded 15 so we expect 10 back.
	if len(ids) != 10 {
		t.Errorf("ids len = %d, want 10 (default limit)", len(ids))
	}
}

func TestPrincipalQuery_LimitZeroInvalidArguments(t *testing.T) {
	f := setupFixture(t)
	limit := 0
	name, raw := f.invoke(t, "Principal/query", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"filter":    map[string]any{"textPrefix": "x"},
		"limit":     limit,
	})
	if name != "error" {
		t.Fatalf("method = %q, want error", name)
	}
	var mErr map[string]any
	if err := json.Unmarshal(raw, &mErr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if typ := mErr["type"]; typ != "invalidArguments" {
		t.Errorf("type = %v, want invalidArguments", typ)
	}
}

func TestPrincipalQuery_LimitNegativeInvalidArguments(t *testing.T) {
	f := setupFixture(t)
	name, raw := f.invoke(t, "Principal/query", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"filter":    map[string]any{"textPrefix": "x"},
		"limit":     -1,
	})
	if name != "error" {
		t.Fatalf("method = %q, want error", name)
	}
	var mErr map[string]any
	if err := json.Unmarshal(raw, &mErr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if typ := mErr["type"]; typ != "invalidArguments" {
		t.Errorf("type = %v, want invalidArguments", typ)
	}
}

func TestPrincipalQuery_ConflictingFiltersInvalidArguments(t *testing.T) {
	f := setupFixture(t)
	name, raw := f.invoke(t, "Principal/query", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"filter": map[string]any{
			"emailExact": "alice@example.test",
			"textPrefix": "ali",
		},
	})
	if name != "error" {
		t.Fatalf("method = %q, want error", name)
	}
	var mErr map[string]any
	if err := json.Unmarshal(raw, &mErr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if typ := mErr["type"]; typ != "invalidArguments" {
		t.Errorf("type = %v, want invalidArguments", typ)
	}
}

func TestPrincipalQuery_MissingFilterInvalidArguments(t *testing.T) {
	f := setupFixture(t)
	name, raw := f.invoke(t, "Principal/query", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
	})
	if name != "error" {
		t.Fatalf("method = %q, want error", name)
	}
	var mErr map[string]any
	if err := json.Unmarshal(raw, &mErr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if typ := mErr["type"]; typ != "invalidArguments" {
		t.Errorf("type = %v, want invalidArguments", typ)
	}
}

func TestPrincipalQuery_EmptyFilterInvalidArguments(t *testing.T) {
	f := setupFixture(t)
	name, raw := f.invoke(t, "Principal/query", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"filter":    map[string]any{},
	})
	if name != "error" {
		t.Fatalf("method = %q, want error", name)
	}
	var mErr map[string]any
	if err := json.Unmarshal(raw, &mErr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if typ := mErr["type"]; typ != "invalidArguments" {
		t.Errorf("type = %v, want invalidArguments", typ)
	}
}

func TestPrincipal_CapabilityGate_WithoutChatCapabilityUnknownMethod(t *testing.T) {
	f := setupFixture(t)
	// Invoke without the chat capability — the dispatcher should return
	// unknownMethod since Principal/get is not in the core method set.
	name, raw := f.invoke(t, "Principal/get", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"ids":       []string{},
	}, protojmap.CapabilityCore) // only Core, no Chat
	if name != "error" {
		t.Fatalf("method = %q, want error", name)
	}
	var mErr map[string]any
	if err := json.Unmarshal(raw, &mErr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if typ := mErr["type"]; typ != "unknownMethod" {
		t.Errorf("type = %v, want unknownMethod", typ)
	}
}
