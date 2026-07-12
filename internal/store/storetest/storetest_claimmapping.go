package storetest

import (
	"errors"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// This file exercises the epic #188 external-IdP claim-to-grant mapping
// store surface (REQ-AC-60..70, migration 0083): the provider authz_trusted
// flag, the per-provider claim allowlist, mapping rules, and the atomic
// idp: grant reconciliation + staleness sweep. Runs on SQLite and Postgres
// via storetest.Run.

func mustInsertClaimMappingProvider(t *testing.T, s store.Store, name string) {
	t.Helper()
	ctx := ctxT(t)
	if err := s.Meta().InsertOIDCProvider(ctx, store.OIDCProvider{
		Name:            name,
		IssuerURL:       "https://idp.example/" + name,
		ClientID:        "cid-" + name,
		ClientSecretRef: "inline:" + name,
	}); err != nil {
		t.Fatalf("InsertOIDCProvider %s: %v", name, err)
	}
}

// testOIDCProviderAuthzTrusted exercises the default-false authz_trusted
// flag and SetOIDCProviderAuthzTrusted (REQ-AC-66).
func testOIDCProviderAuthzTrusted(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	mustInsertClaimMappingProvider(t, s, "trust-flag")

	got, err := s.Meta().GetOIDCProvider(ctx, "trust-flag")
	if err != nil {
		t.Fatalf("GetOIDCProvider: %v", err)
	}
	if got.AuthzTrusted {
		t.Errorf("AuthzTrusted = true on insert; want false (default-deny)")
	}

	if err := s.Meta().SetOIDCProviderAuthzTrusted(ctx, "trust-flag", true); err != nil {
		t.Fatalf("SetOIDCProviderAuthzTrusted: %v", err)
	}
	got, err = s.Meta().GetOIDCProvider(ctx, "trust-flag")
	if err != nil {
		t.Fatalf("GetOIDCProvider after set: %v", err)
	}
	if !got.AuthzTrusted {
		t.Errorf("AuthzTrusted = false after SetOIDCProviderAuthzTrusted(true); want true")
	}

	if err := s.Meta().SetOIDCProviderAuthzTrusted(ctx, "no-such-provider", true); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SetOIDCProviderAuthzTrusted unknown provider = %v; want ErrNotFound", err)
	}

	// ListOIDCProviders must also surface the flag.
	all, err := s.Meta().ListOIDCProviders(ctx)
	if err != nil {
		t.Fatalf("ListOIDCProviders: %v", err)
	}
	var found bool
	for _, p := range all {
		if p.Name == "trust-flag" {
			found = true
			if !p.AuthzTrusted {
				t.Errorf("ListOIDCProviders: AuthzTrusted = false; want true")
			}
		}
	}
	if !found {
		t.Fatalf("trust-flag provider missing from ListOIDCProviders")
	}
}

// testClaimAllowlistCRUD exercises the per-provider authorization-claim
// allowlist (REQ-AC-67).
func testClaimAllowlistCRUD(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	mustInsertClaimMappingProvider(t, s, "allowlist-p")

	empty, err := s.Meta().ListClaimAllowlist(ctx, "allowlist-p")
	if err != nil {
		t.Fatalf("ListClaimAllowlist empty: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("ListClaimAllowlist on fresh provider = %v; want empty", empty)
	}

	if err := s.Meta().InsertClaimAllowlistEntry(ctx, "allowlist-p", "groups"); err != nil {
		t.Fatalf("InsertClaimAllowlistEntry groups: %v", err)
	}
	if err := s.Meta().InsertClaimAllowlistEntry(ctx, "allowlist-p", "roles"); err != nil {
		t.Fatalf("InsertClaimAllowlistEntry roles: %v", err)
	}
	// Idempotent re-insert is not an error.
	if err := s.Meta().InsertClaimAllowlistEntry(ctx, "allowlist-p", "groups"); err != nil {
		t.Fatalf("InsertClaimAllowlistEntry duplicate: %v", err)
	}

	got, err := s.Meta().ListClaimAllowlist(ctx, "allowlist-p")
	if err != nil {
		t.Fatalf("ListClaimAllowlist: %v", err)
	}
	want := []string{"groups", "roles"}
	if len(got) != len(want) {
		t.Fatalf("ListClaimAllowlist = %v; want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ListClaimAllowlist[%d] = %q; want %q (alphabetical)", i, got[i], want[i])
		}
	}

	if err := s.Meta().DeleteClaimAllowlistEntry(ctx, "allowlist-p", "roles"); err != nil {
		t.Fatalf("DeleteClaimAllowlistEntry: %v", err)
	}
	got, err = s.Meta().ListClaimAllowlist(ctx, "allowlist-p")
	if err != nil {
		t.Fatalf("ListClaimAllowlist after delete: %v", err)
	}
	if len(got) != 1 || got[0] != "groups" {
		t.Errorf("ListClaimAllowlist after delete = %v; want [groups]", got)
	}

	if err := s.Meta().DeleteClaimAllowlistEntry(ctx, "allowlist-p", "roles"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("DeleteClaimAllowlistEntry absent = %v; want ErrNotFound", err)
	}
}

// testClaimMappingRuleCRUD exercises rule insert/list/delete (REQ-AC-60).
func testClaimMappingRuleCRUD(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	mustInsertClaimMappingProvider(t, s, "rules-p")
	author := mustInsertPrincipal(t, s, "rule-author@example.test")

	rule, err := s.Meta().InsertClaimMappingRule(ctx, store.ClaimMappingRule{
		ProviderName: "rules-p",
		Claim:        "groups",
		MatchValue:   "list-x-admins",
		ResourceKind: store.GrantResourceList,
		ResourceID:   "x",
		Level:        store.GrantLevelOwner,
		CreatedBy:    author.ID,
	})
	if err != nil {
		t.Fatalf("InsertClaimMappingRule: %v", err)
	}
	if rule.ID == 0 {
		t.Errorf("InsertClaimMappingRule returned zero ID")
	}
	if rule.CreatedAt.IsZero() {
		t.Errorf("InsertClaimMappingRule: CreatedAt is zero")
	}

	rules, err := s.Meta().ListClaimMappingRules(ctx, "rules-p")
	if err != nil {
		t.Fatalf("ListClaimMappingRules: %v", err)
	}
	if len(rules) != 1 || rules[0].ID != rule.ID {
		t.Fatalf("ListClaimMappingRules = %+v; want one rule with id %d", rules, rule.ID)
	}
	if rules[0].CreatedBy != author.ID {
		t.Errorf("CreatedBy = %d; want %d", rules[0].CreatedBy, author.ID)
	}

	// A rule for a different provider must not leak in.
	mustInsertClaimMappingProvider(t, s, "rules-p-other")
	if _, err := s.Meta().InsertClaimMappingRule(ctx, store.ClaimMappingRule{
		ProviderName: "rules-p-other", Claim: "groups", MatchValue: "x",
		ResourceKind: store.GrantResourceDomain, ResourceID: "y.example", Level: store.GrantLevelOperator,
		CreatedBy: author.ID,
	}); err != nil {
		t.Fatalf("InsertClaimMappingRule other provider: %v", err)
	}
	rules, err = s.Meta().ListClaimMappingRules(ctx, "rules-p")
	if err != nil {
		t.Fatalf("ListClaimMappingRules after other-provider insert: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("ListClaimMappingRules leaked a rule from another provider: %+v", rules)
	}

	if err := s.Meta().DeleteClaimMappingRule(ctx, rule.ID); err != nil {
		t.Fatalf("DeleteClaimMappingRule: %v", err)
	}
	rules, err = s.Meta().ListClaimMappingRules(ctx, "rules-p")
	if err != nil {
		t.Fatalf("ListClaimMappingRules after delete: %v", err)
	}
	if len(rules) != 0 {
		t.Errorf("ListClaimMappingRules after delete = %+v; want none", rules)
	}
	if err := s.Meta().DeleteClaimMappingRule(ctx, rule.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("DeleteClaimMappingRule absent = %v; want ErrNotFound", err)
	}
}

// testReconcileIdPGrants_FullLifecycle drives ReconcileIdPGrants through
// insert, level-change update, refresh-only survivor, and delete-on-absence
// in successive calls, and asserts local grants and a different provider's
// idp: grants are never touched (REQ-AC-61/62).
func testReconcileIdPGrants_FullLifecycle(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	subject := mustInsertPrincipal(t, s, "reconcile-subject@example.test")

	// A local grant and another provider's idp: grant on overlapping
	// resources -- reconciliation for "acme" must never touch either.
	if _, err := s.Meta().InsertGrant(ctx, store.Grant{
		SubjectID: uint64(subject.ID), ResourceKind: store.GrantResourceDomain,
		ResourceID: "shared.example", Level: store.GrantLevelOwner,
		Provenance: store.GrantProvenanceLocal,
	}); err != nil {
		t.Fatalf("InsertGrant local: %v", err)
	}
	otherAsserted := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := s.Meta().InsertGrant(ctx, store.Grant{
		SubjectID: uint64(subject.ID), ResourceKind: store.GrantResourceDomain,
		ResourceID: "other-provider.example", Level: store.GrantLevelOperator,
		Provenance: "idp:okta", LastAssertedAt: &otherAsserted,
	}); err != nil {
		t.Fatalf("InsertGrant other-provider idp: %v", err)
	}

	t0 := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	added, removed, err := s.Meta().ReconcileIdPGrants(ctx, subject.ID, "acme", []store.GrantDesired{
		{ResourceKind: store.GrantResourceDomain, ResourceID: "x.example", Level: store.GrantLevelOperator},
		{ResourceKind: store.GrantResourceList, ResourceID: "l1", Level: store.GrantLevelModerator},
	}, t0)
	if err != nil {
		t.Fatalf("ReconcileIdPGrants first pass: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("first pass removed = %+v; want none", removed)
	}
	if len(added) != 2 {
		t.Fatalf("first pass added = %+v; want 2", added)
	}
	assertIdPGrantLevel(t, s, subject.ID, "acme", store.GrantResourceDomain, "x.example", store.GrantLevelOperator, t0)
	assertIdPGrantLevel(t, s, subject.ID, "acme", store.GrantResourceList, "l1", store.GrantLevelModerator, t0)

	// Idempotent re-reconciliation with the same desired set: no adds, no
	// removes, but last_asserted_at refreshes.
	t1 := t0.Add(time.Hour)
	added, removed, err = s.Meta().ReconcileIdPGrants(ctx, subject.ID, "acme", []store.GrantDesired{
		{ResourceKind: store.GrantResourceDomain, ResourceID: "x.example", Level: store.GrantLevelOperator},
		{ResourceKind: store.GrantResourceList, ResourceID: "l1", Level: store.GrantLevelModerator},
	}, t1)
	if err != nil {
		t.Fatalf("ReconcileIdPGrants idempotent pass: %v", err)
	}
	if len(added) != 0 || len(removed) != 0 {
		t.Fatalf("idempotent pass added=%+v removed=%+v; want none", added, removed)
	}
	assertIdPGrantLevel(t, s, subject.ID, "acme", store.GrantResourceDomain, "x.example", store.GrantLevelOperator, t1)

	// Third pass: x.example's level changes (operator -> owner), l1 drops
	// out of the entitled set, and a brand-new mailbox grant appears.
	t2 := t1.Add(time.Hour)
	added, removed, err = s.Meta().ReconcileIdPGrants(ctx, subject.ID, "acme", []store.GrantDesired{
		{ResourceKind: store.GrantResourceDomain, ResourceID: "x.example", Level: store.GrantLevelOwner},
		{ResourceKind: store.GrantResourceMailbox, ResourceID: "m1", Level: store.GrantLevelRead},
	}, t2)
	if err != nil {
		t.Fatalf("ReconcileIdPGrants third pass: %v", err)
	}
	if len(removed) != 2 {
		t.Fatalf("third pass removed = %+v; want 2 (old-level x.example row + l1)", removed)
	}
	if len(added) != 2 {
		t.Fatalf("third pass added = %+v; want 2 (new-level x.example row + m1)", added)
	}
	assertIdPGrantLevel(t, s, subject.ID, "acme", store.GrantResourceDomain, "x.example", store.GrantLevelOwner, t2)
	assertIdPGrantLevel(t, s, subject.ID, "acme", store.GrantResourceMailbox, "m1", store.GrantLevelRead, t2)
	assertNoIdPGrant(t, s, subject.ID, "acme", store.GrantResourceList, "l1")

	// The local grant and the other provider's idp: grant survived
	// untouched throughout.
	all, err := s.Meta().ListGrantsForPrincipal(ctx, subject.ID)
	if err != nil {
		t.Fatalf("ListGrantsForPrincipal: %v", err)
	}
	var sawLocal, sawOther bool
	for _, g := range all {
		if g.Provenance == store.GrantProvenanceLocal && g.ResourceID == "shared.example" {
			sawLocal = true
		}
		if g.Provenance == "idp:okta" && g.ResourceID == "other-provider.example" {
			sawOther = true
			if g.LastAssertedAt == nil || !g.LastAssertedAt.Equal(otherAsserted) {
				t.Errorf("okta grant LastAssertedAt = %v; want untouched %v", g.LastAssertedAt, otherAsserted)
			}
		}
	}
	if !sawLocal {
		t.Errorf("local grant on shared.example disappeared during acme reconciliation")
	}
	if !sawOther {
		t.Errorf("idp:okta grant disappeared during acme reconciliation")
	}

	// Final pass: empty desired set deletes every remaining acme idp: grant
	// (Unlink / rule-deletion behaviour, REQ-AC-63).
	added, removed, err = s.Meta().ReconcileIdPGrants(ctx, subject.ID, "acme", nil, t2.Add(time.Hour))
	if err != nil {
		t.Fatalf("ReconcileIdPGrants final pass: %v", err)
	}
	if len(added) != 0 || len(removed) != 2 {
		t.Fatalf("final pass added=%+v removed=%+v; want 0 added, 2 removed", added, removed)
	}
	assertNoIdPGrant(t, s, subject.ID, "acme", store.GrantResourceDomain, "x.example")
	assertNoIdPGrant(t, s, subject.ID, "acme", store.GrantResourceMailbox, "m1")
	// Still untouched.
	all, err = s.Meta().ListGrantsForPrincipal(ctx, subject.ID)
	if err != nil {
		t.Fatalf("ListGrantsForPrincipal final: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListGrantsForPrincipal final = %+v; want exactly the local + okta rows", all)
	}
}

func assertIdPGrantLevel(t *testing.T, s store.Store, subjectID store.PrincipalID, provider string, kind store.GrantResourceKind, resourceID string, level store.GrantLevel, wantAsserted time.Time) {
	t.Helper()
	all, err := s.Meta().ListGrantsForPrincipal(ctxT(t), subjectID)
	if err != nil {
		t.Fatalf("ListGrantsForPrincipal: %v", err)
	}
	prov := "idp:" + provider
	for _, g := range all {
		if g.Provenance == prov && g.ResourceKind == kind && g.ResourceID == resourceID {
			if g.Level != level {
				t.Errorf("grant %s:%s level = %q; want %q", kind, resourceID, g.Level, level)
			}
			if g.LastAssertedAt == nil || !g.LastAssertedAt.Equal(wantAsserted) {
				t.Errorf("grant %s:%s LastAssertedAt = %v; want %v", kind, resourceID, g.LastAssertedAt, wantAsserted)
			}
			return
		}
	}
	t.Fatalf("expected idp:%s grant %s:%s at level %q not found in %+v", provider, kind, resourceID, level, all)
}

func assertNoIdPGrant(t *testing.T, s store.Store, subjectID store.PrincipalID, provider string, kind store.GrantResourceKind, resourceID string) {
	t.Helper()
	all, err := s.Meta().ListGrantsForPrincipal(ctxT(t), subjectID)
	if err != nil {
		t.Fatalf("ListGrantsForPrincipal: %v", err)
	}
	prov := "idp:" + provider
	for _, g := range all {
		if g.Provenance == prov && g.ResourceKind == kind && g.ResourceID == resourceID {
			t.Fatalf("grant %s:%s still present: %+v", kind, resourceID, g)
		}
	}
}

// testSweepStaleIdPGrants exercises the periodic staleness sweep
// (REQ-AC-63b): a stale idp: grant is removed, a fresh one and a local
// grant are not.
func testSweepStaleIdPGrants(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	subject := mustInsertPrincipal(t, s, "sweep-subject@example.test")

	stale := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	fresh := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := s.Meta().InsertGrant(ctx, store.Grant{
		SubjectID: uint64(subject.ID), ResourceKind: store.GrantResourceDomain,
		ResourceID: "stale.example", Level: store.GrantLevelOperator,
		Provenance: "idp:acme", LastAssertedAt: &stale,
	}); err != nil {
		t.Fatalf("InsertGrant stale: %v", err)
	}
	if _, err := s.Meta().InsertGrant(ctx, store.Grant{
		SubjectID: uint64(subject.ID), ResourceKind: store.GrantResourceDomain,
		ResourceID: "fresh.example", Level: store.GrantLevelOperator,
		Provenance: "idp:acme", LastAssertedAt: &fresh,
	}); err != nil {
		t.Fatalf("InsertGrant fresh: %v", err)
	}
	if _, err := s.Meta().InsertGrant(ctx, store.Grant{
		SubjectID: uint64(subject.ID), ResourceKind: store.GrantResourceDomain,
		ResourceID: "local.example", Level: store.GrantLevelOperator,
		Provenance: store.GrantProvenanceLocal,
	}); err != nil {
		t.Fatalf("InsertGrant local: %v", err)
	}

	cutoff := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	removed, err := s.Meta().SweepStaleIdPGrants(ctx, cutoff)
	if err != nil {
		t.Fatalf("SweepStaleIdPGrants: %v", err)
	}
	if len(removed) != 1 || removed[0].ResourceID != "stale.example" {
		t.Fatalf("SweepStaleIdPGrants removed = %+v; want only stale.example", removed)
	}

	remaining, err := s.Meta().ListGrantsForPrincipal(ctx, subject.ID)
	if err != nil {
		t.Fatalf("ListGrantsForPrincipal: %v", err)
	}
	if len(remaining) != 2 {
		t.Fatalf("remaining grants = %+v; want fresh.example + local.example", remaining)
	}
}
