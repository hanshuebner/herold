package spam

// own_addresses_test.go is the #386 unit test of the request builder's
// own-address resolver: a principal carrying a canonical email, an
// alias, a verified Identity with an alias address (re #387), an
// unverified Identity, and two IMAP-import accounts (one resolved via
// its owning Identity, one legacy row with no IdentityID) must resolve
// to exactly the addresses the principal actually controls.

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite/sqlitetest"
)

func TestResolveOwnAddresses(t *testing.T) {
	ctx := context.Background()
	st := sqlitetest.Open(t, clock.NewReal())

	owner, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "Owner@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal owner: %v", err)
	}
	other, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "other@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal other: %v", err)
	}

	// A live alias routing to owner.
	if _, err := st.Meta().InsertAlias(ctx, store.Alias{
		LocalPart:       "alias1",
		Domain:          "example.test",
		TargetPrincipal: owner.ID,
	}); err != nil {
		t.Fatalf("InsertAlias alias1: %v", err)
	}
	// An expired alias routing to owner: must not appear (ListAliases
	// includes expired rows for the admin surface; ResolveOwnAddresses
	// filters them, mirroring the store's own routing behaviour).
	past := time.Now().Add(-time.Hour)
	if _, err := st.Meta().InsertAlias(ctx, store.Alias{
		LocalPart:       "expired",
		Domain:          "example.test",
		TargetPrincipal: owner.ID,
		ExpiresAt:       &past,
	}); err != nil {
		t.Fatalf("InsertAlias expired: %v", err)
	}
	// An alias routing to a different principal: must not appear.
	if _, err := st.Meta().InsertAlias(ctx, store.Alias{
		LocalPart:       "notmine",
		Domain:          "example.test",
		TargetPrincipal: other.ID,
	}); err != nil {
		t.Fatalf("InsertAlias notmine: %v", err)
	}

	// A verified Identity with an alias address (re #387): both the
	// primary and the alias must appear.
	if err := st.Meta().InsertJMAPIdentity(ctx, store.JMAPIdentity{
		ID:          "ident-verified",
		PrincipalID: owner.ID,
		Email:       "verified@ext.test",
		Aliases:     []string{"ivalias@ext.test"},
		MayDelete:   true,
	}); err != nil {
		t.Fatalf("InsertJMAPIdentity ident-verified: %v", err)
	}
	if err := st.Meta().MarkIdentityVerified(ctx, "ident-verified"); err != nil {
		t.Fatalf("MarkIdentityVerified: %v", err)
	}

	// An unverified Identity: neither its primary nor its alias belong
	// in the set -- verification is the confirmation the principal
	// actually controls the address.
	if err := st.Meta().InsertJMAPIdentity(ctx, store.JMAPIdentity{
		ID:          "ident-unverified",
		PrincipalID: owner.ID,
		Email:       "unverified@ext.test",
		Aliases:     []string{"notincluded@ext.test"},
		MayDelete:   true,
	}); err != nil {
		t.Fatalf("InsertJMAPIdentity ident-unverified: %v", err)
	}

	// A third Identity backing an IMAP-import account (unverified: the
	// import account address is included unconditionally -- configuring
	// working upstream credentials already demonstrates the principal
	// receives mail there).
	if err := st.Meta().InsertJMAPIdentity(ctx, store.JMAPIdentity{
		ID:          "ident-import",
		PrincipalID: owner.ID,
		Email:       "Imported@ext.test",
		MayDelete:   true,
	}); err != nil {
		t.Fatalf("InsertJMAPIdentity ident-import: %v", err)
	}
	if _, err := st.Meta().CreateIMAPImportAccount(ctx, store.IMAPImportAccountCreate{
		IdentityID:   "ident-import",
		PrincipalID:  owner.ID,
		AccountName:  "Imported Account",
		Host:         "imap.ext.test",
		Port:         993,
		TLSMode:      store.IMAPImportTLSModeImplicit,
		Username:     "imported",
		AuthMethod:   store.IMAPImportAuthMethodPassword,
		CredentialCT: []byte("v1:test"),
		State:        store.IMAPImportAccountStateEnabled,
	}); err != nil {
		t.Fatalf("CreateIMAPImportAccount ident-import: %v", err)
	}

	// A legacy IMAP-import account with no owning Identity: Username is
	// the best-effort fallback address.
	if _, err := st.Meta().CreateIMAPImportAccount(ctx, store.IMAPImportAccountCreate{
		PrincipalID:  owner.ID,
		AccountName:  "Legacy Account",
		Host:         "imap.legacy.test",
		Port:         993,
		TLSMode:      store.IMAPImportTLSModeImplicit,
		Username:     "legacyuser@legacy.test",
		AuthMethod:   store.IMAPImportAuthMethodPassword,
		CredentialCT: []byte("v1:test"),
		State:        store.IMAPImportAccountStateEnabled,
	}); err != nil {
		t.Fatalf("CreateIMAPImportAccount legacy: %v", err)
	}

	got, err := ResolveOwnAddresses(ctx, st.Meta(), owner.ID, nil)
	if err != nil {
		t.Fatalf("ResolveOwnAddresses: %v", err)
	}
	want := []string{
		"alias1@example.test",
		"imported@ext.test",
		"ivalias@ext.test",
		"legacyuser@legacy.test",
		"owner@example.test",
		"verified@ext.test",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ResolveOwnAddresses = %#v, want %#v", got, want)
	}

	// Zero principal resolves to nothing without touching the store.
	if got, err := ResolveOwnAddresses(ctx, st.Meta(), 0, nil); err != nil || got != nil {
		t.Fatalf("ResolveOwnAddresses(0) = %#v, %v, want nil, nil", got, err)
	}
}

// TestResolveOwnAddresses_Cache verifies the optional cache parameter
// memoizes the result per principal (re #386): a caller looping over
// many messages for the same principal (spam reclassify / apply-
// verdicts) reads the store once for the whole batch.
func TestResolveOwnAddresses_Cache(t *testing.T) {
	ctx := context.Background()
	st := sqlitetest.Open(t, clock.NewReal())

	owner, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "cache-owner@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}

	cache := make(map[store.PrincipalID][]string)
	first, err := ResolveOwnAddresses(ctx, st.Meta(), owner.ID, cache)
	if err != nil {
		t.Fatalf("ResolveOwnAddresses (first): %v", err)
	}
	if want := []string{"cache-owner@example.test"}; !reflect.DeepEqual(first, want) {
		t.Fatalf("first = %#v, want %#v", first, want)
	}

	// Add an alias after the first resolve; a cached second call must
	// still return the pre-alias set.
	if _, err := st.Meta().InsertAlias(ctx, store.Alias{
		LocalPart:       "late",
		Domain:          "example.test",
		TargetPrincipal: owner.ID,
	}); err != nil {
		t.Fatalf("InsertAlias late: %v", err)
	}

	second, err := ResolveOwnAddresses(ctx, st.Meta(), owner.ID, cache)
	if err != nil {
		t.Fatalf("ResolveOwnAddresses (cached): %v", err)
	}
	if !reflect.DeepEqual(second, first) {
		t.Fatalf("cached resolve = %#v, want unchanged %#v", second, first)
	}

	// A fresh (nil) cache observes the alias added above.
	fresh, err := ResolveOwnAddresses(ctx, st.Meta(), owner.ID, nil)
	if err != nil {
		t.Fatalf("ResolveOwnAddresses (fresh): %v", err)
	}
	want := []string{"cache-owner@example.test", "late@example.test"}
	if !reflect.DeepEqual(fresh, want) {
		t.Fatalf("fresh resolve = %#v, want %#v", fresh, want)
	}
}
