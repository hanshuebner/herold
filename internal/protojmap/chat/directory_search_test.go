package chat_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/protojmap/chat"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/sysconfig"
	"github.com/hanshuebner/herold/internal/testharness"
)

// directoryFixture extends the chat fixture with directory-search support.
// It wires RegisterDirectorySearch with a swappable mode function so tests
// can exercise "all", "domain", and "off" without restarting the server.
type directoryFixture struct {
	*fixture
	// modeFn is the closure passed to RegisterDirectorySearch. Tests can
	// point mode at their own variable to change the mode at call time.
	mode sysconfig.DirectoryAutocompleteMode
}

// setupDirectoryFixture starts a testharness server, registers both the
// chat capability and the directory-autocomplete capability, and returns a
// directoryFixture whose mode field can be mutated per-test.
func setupDirectoryFixture(t *testing.T) *directoryFixture {
	t.Helper()
	srv, _ := testharness.Start(t, testharness.Options{
		Listeners: []testharness.ListenerSpec{{Name: "jmap", Protocol: "jmap"}},
	})

	ctx := context.Background()
	alice, err := srv.Store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@mail.example",
		DisplayName:    "Alice",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal alice: %v", err)
	}
	bob, err := srv.Store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "bob@mail.example",
		DisplayName:    "Bob",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal bob: %v", err)
	}
	carol, err := srv.Store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "carol@mail.example",
		DisplayName:    "Carol",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal carol: %v", err)
	}

	plaintext := "hk_dir_alice_" + fmt.Sprintf("%d", alice.ID)
	hash := protoadmin.HashAPIKey(plaintext)
	if _, err := srv.Store.Meta().InsertAPIKey(ctx, store.APIKey{
		PrincipalID: alice.ID,
		Hash:        hash,
		Name:        "dir-test",
	}); err != nil {
		t.Fatalf("InsertAPIKey: %v", err)
	}

	df := &directoryFixture{
		fixture: &fixture{
			srv:      srv,
			pid:      alice.ID,
			otherPID: bob.ID,
			thirdPID: carol.ID,
		},
		mode: sysconfig.DirectoryAutocompleteModeAll,
	}

	dir := directory.New(srv.Store.Meta(), srv.Logger, srv.Clock, nil)
	jmapServ := protojmap.NewServer(srv.Store, dir, nil, srv.Logger, srv.Clock, protojmap.Options{})
	chat.Register(jmapServ.Registry(), srv.Store, srv.Logger, srv.Clock)
	chat.RegisterDirectorySearch(jmapServ.Registry(), srv.Store, func() sysconfig.DirectoryAutocompleteMode {
		return df.mode
	})

	if err := srv.AttachJMAP("jmap", jmapServ, protojmap.ListenerModePlain); err != nil {
		t.Fatalf("AttachJMAP: %v", err)
	}
	client, base := srv.DialJMAPByName(ctx, "jmap")
	df.fixture.client = client
	df.fixture.baseURL = base
	df.fixture.apiKey = plaintext
	df.fixture.jmapServ = jmapServ
	return df
}

// dirSearch posts a Directory/search call and returns (responseName, parsed map).
func dirSearch(t *testing.T, f *directoryFixture, textPrefix string, limit *int) (string, map[string]any) {
	t.Helper()
	args := map[string]any{
		"accountId":  string(protojmap.AccountIDForPrincipal(f.pid)),
		"textPrefix": textPrefix,
	}
	if limit != nil {
		args["limit"] = *limit
	}
	name, raw := f.invoke(t, "Directory/search", args, protojmap.CapabilityCore, protojmap.CapabilityDirectoryAutocomplete)
	var resp map[string]any
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal Directory/search: %v: %s", err, raw)
	}
	return name, resp
}

// -- Happy path: mode = "all" -----------------------------------------

func TestDirectorySearch_ModeAll_MatchesAcrossDomains(t *testing.T) {
	f := setupDirectoryFixture(t)
	f.mode = sysconfig.DirectoryAutocompleteModeAll

	ctx := context.Background()
	// Insert a principal in a different domain.
	other, err := f.srv.Store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@other.example",
		DisplayName:    "Alice Other",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}

	name, resp := dirSearch(t, f, "alice", nil)
	if name != "Directory/search" {
		t.Fatalf("method = %q, want Directory/search", name)
	}

	items, _ := resp["items"].([]any)
	// Both alice@mail.example and alice@other.example should appear.
	if len(items) < 2 {
		t.Fatalf("items len = %d, want >= 2 (cross-domain match expected)", len(items))
	}

	// Verify each item has exactly id, email, displayName and no extra fields.
	for i, raw := range items {
		rec, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("items[%d]: not an object", i)
		}
		for _, required := range []string{"id", "email", "displayName"} {
			if _, ok := rec[required]; !ok {
				t.Errorf("items[%d] missing required field %q", i, required)
			}
		}
		// Privacy: only three fields may appear.
		for key := range rec {
			switch key {
			case "id", "email", "displayName":
				// expected
			default:
				t.Errorf("items[%d] contains unexpected field %q", i, key)
			}
		}
	}
	_ = other
}

// -- Happy path: mode = "domain" --------------------------------------

func TestDirectorySearch_ModeDomain_RestrictsToCallerDomain(t *testing.T) {
	f := setupDirectoryFixture(t)
	f.mode = sysconfig.DirectoryAutocompleteModeDomain

	ctx := context.Background()
	// Insert a principal in a different domain that should NOT appear.
	outOfDomain, err := f.srv.Store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@other.example",
		DisplayName:    "Alice Other",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	// alice@mail.example IS in the fixture already; Bob and Carol also match.
	name, resp := dirSearch(t, f, "alice", nil)
	if name != "Directory/search" {
		t.Fatalf("method = %q, want Directory/search", name)
	}

	items, _ := resp["items"].([]any)
	// Only alice@mail.example should appear (same domain as caller alice@mail.example).
	for i, raw := range items {
		rec := raw.(map[string]any)
		email, _ := rec["email"].(string)
		if email == outOfDomain.CanonicalEmail {
			t.Errorf("items[%d] contains cross-domain result %q but mode=domain", i, email)
		}
		if len(email) > 0 {
			// domain part must be mail.example
			at := len(email) - 1
			for j := len(email) - 1; j >= 0; j-- {
				if email[j] == '@' {
					at = j
					break
				}
			}
			domain := email[at+1:]
			if domain != "mail.example" {
				t.Errorf("items[%d] email %q has domain %q, want mail.example", i, email, domain)
			}
		}
	}
	_ = outOfDomain
}

// -- mode = "off" defence-in-depth ------------------------------------

func TestDirectorySearch_ModeOff_ReturnsUnknownMethod(t *testing.T) {
	f := setupDirectoryFixture(t)
	f.mode = sysconfig.DirectoryAutocompleteModeOff

	name, raw := f.invoke(t, "Directory/search", map[string]any{
		"accountId":  string(protojmap.AccountIDForPrincipal(f.pid)),
		"textPrefix": "alice",
	}, protojmap.CapabilityCore, protojmap.CapabilityDirectoryAutocomplete)
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

// -- Validation tests -------------------------------------------------

func TestDirectorySearch_EmptyTextPrefix_InvalidArguments(t *testing.T) {
	f := setupDirectoryFixture(t)
	f.mode = sysconfig.DirectoryAutocompleteModeAll

	name, raw := f.invoke(t, "Directory/search", map[string]any{
		"accountId":  string(protojmap.AccountIDForPrincipal(f.pid)),
		"textPrefix": "",
	}, protojmap.CapabilityCore, protojmap.CapabilityDirectoryAutocomplete)
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

func TestDirectorySearch_WhitespaceOnlyPrefix_InvalidArguments(t *testing.T) {
	f := setupDirectoryFixture(t)
	f.mode = sysconfig.DirectoryAutocompleteModeAll

	name, raw := f.invoke(t, "Directory/search", map[string]any{
		"accountId":  string(protojmap.AccountIDForPrincipal(f.pid)),
		"textPrefix": "   ",
	}, protojmap.CapabilityCore, protojmap.CapabilityDirectoryAutocomplete)
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

func TestDirectorySearch_LimitZero_InvalidArguments(t *testing.T) {
	f := setupDirectoryFixture(t)
	f.mode = sysconfig.DirectoryAutocompleteModeAll

	limit := 0
	name, raw := f.invoke(t, "Directory/search", map[string]any{
		"accountId":  string(protojmap.AccountIDForPrincipal(f.pid)),
		"textPrefix": "alice",
		"limit":      limit,
	}, protojmap.CapabilityCore, protojmap.CapabilityDirectoryAutocomplete)
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

func TestDirectorySearch_LimitNegative_InvalidArguments(t *testing.T) {
	f := setupDirectoryFixture(t)
	f.mode = sysconfig.DirectoryAutocompleteModeAll

	name, raw := f.invoke(t, "Directory/search", map[string]any{
		"accountId":  string(protojmap.AccountIDForPrincipal(f.pid)),
		"textPrefix": "alice",
		"limit":      -5,
	}, protojmap.CapabilityCore, protojmap.CapabilityDirectoryAutocomplete)
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

func TestDirectorySearch_LimitOver25_ClampedTo25(t *testing.T) {
	f := setupDirectoryFixture(t)
	f.mode = sysconfig.DirectoryAutocompleteModeAll

	ctx := context.Background()
	for i := 0; i < 30; i++ {
		if _, err := f.srv.Store.Meta().InsertPrincipal(ctx, store.Principal{
			Kind:           store.PrincipalKindUser,
			CanonicalEmail: fmt.Sprintf("clamp%02d@mail.example", i),
			DisplayName:    "Clampuser",
		}); err != nil {
			t.Fatalf("InsertPrincipal: %v", err)
		}
	}

	limit := 100
	name, resp := dirSearch(t, f, "clamp", &limit)
	if name != "Directory/search" {
		t.Fatalf("method = %q, want Directory/search", name)
	}
	items, _ := resp["items"].([]any)
	if len(items) > 25 {
		t.Errorf("items len = %d, want <= 25 (server cap)", len(items))
	}
}

// -- Limit respected --------------------------------------------------

func TestDirectorySearch_LimitRespected(t *testing.T) {
	f := setupDirectoryFixture(t)
	f.mode = sysconfig.DirectoryAutocompleteModeAll

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if _, err := f.srv.Store.Meta().InsertPrincipal(ctx, store.Principal{
			Kind:           store.PrincipalKindUser,
			CanonicalEmail: fmt.Sprintf("zqlimit%02d@mail.example", i),
			DisplayName:    fmt.Sprintf("Zqlimit %02d", i),
		}); err != nil {
			t.Fatalf("InsertPrincipal: %v", err)
		}
	}

	// 5 matching principals; request limit 3 -> expect exactly 3.
	limit := 3
	name, resp := dirSearch(t, f, "zqlimit", &limit)
	if name != "Directory/search" {
		t.Fatalf("method = %q, want Directory/search", name)
	}
	items, _ := resp["items"].([]any)
	if len(items) != 3 {
		t.Errorf("items len = %d, want 3 (limit applied)", len(items))
	}
}

// -- Privacy ----------------------------------------------------------

func TestDirectorySearch_Privacy_NoExtraFields(t *testing.T) {
	f := setupDirectoryFixture(t)
	f.mode = sysconfig.DirectoryAutocompleteModeAll

	name, resp := dirSearch(t, f, "alice", nil)
	if name != "Directory/search" {
		t.Fatalf("method = %q, want Directory/search", name)
	}
	items, _ := resp["items"].([]any)
	if len(items) == 0 {
		t.Fatal("expected at least one item for prefix 'alice'")
	}
	for i, raw := range items {
		rec, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("items[%d]: not an object", i)
		}
		for key := range rec {
			switch key {
			case "id", "email", "displayName":
				// only these three are permitted
			default:
				t.Errorf("items[%d] contains banned field %q", i, key)
			}
		}
	}
}

// -- Capability gate --------------------------------------------------

func TestDirectorySearch_WithoutCapability_UnknownMethod(t *testing.T) {
	f := setupDirectoryFixture(t)
	f.mode = sysconfig.DirectoryAutocompleteModeAll

	// Invoke with only the chat capability — Directory/search must not
	// be reachable via the chat cap alone.
	name, raw := f.invoke(t, "Directory/search", map[string]any{
		"accountId":  string(protojmap.AccountIDForPrincipal(f.pid)),
		"textPrefix": "alice",
	}, protojmap.CapabilityCore, protojmap.CapabilityJMAPChat)
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

// TestDirectorySearch_DefaultLimit verifies the server applies a default
// of 10 when limit is omitted, even when more results exist.
func TestDirectorySearch_DefaultLimit(t *testing.T) {
	f := setupDirectoryFixture(t)
	f.mode = sysconfig.DirectoryAutocompleteModeAll

	ctx := context.Background()
	for i := 0; i < 15; i++ {
		if _, err := f.srv.Store.Meta().InsertPrincipal(ctx, store.Principal{
			Kind:           store.PrincipalKindUser,
			CanonicalEmail: fmt.Sprintf("deflim%02d@mail.example", i),
			DisplayName:    "Deflim",
		}); err != nil {
			t.Fatalf("InsertPrincipal: %v", err)
		}
	}

	name, resp := dirSearch(t, f, "deflim", nil)
	if name != "Directory/search" {
		t.Fatalf("method = %q, want Directory/search", name)
	}
	items, _ := resp["items"].([]any)
	if len(items) != 10 {
		t.Errorf("items len = %d, want 10 (default limit)", len(items))
	}
}
