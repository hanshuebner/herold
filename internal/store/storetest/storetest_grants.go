package storetest

import (
	"errors"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// testGrants_InsertListDelete exercises the full grant repository lifecycle
// (epic #182, REQ-AC-05): insert defaults, read back by subject and by
// resource, and delete by natural key.
func testGrants_InsertListDelete(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	actor := mustInsertPrincipal(t, s, "grant-actor@example.test")
	subject := mustInsertPrincipal(t, s, "grant-subject@example.test")

	actorID := actor.ID
	got, err := s.Meta().InsertGrant(ctx, store.Grant{
		SubjectID:    uint64(subject.ID),
		ResourceKind: store.GrantResourceDomain,
		ResourceID:   "alpha.example",
		Level:        store.GrantLevelOperator,
		GrantedBy:    &actorID,
	})
	if err != nil {
		t.Fatalf("InsertGrant: %v", err)
	}
	if got.ID == 0 {
		t.Errorf("InsertGrant returned zero ID")
	}
	// Defaults applied.
	if got.SubjectKind != store.GrantSubjectPrincipal {
		t.Errorf("SubjectKind = %q; want principal", got.SubjectKind)
	}
	if got.Provenance != store.GrantProvenanceLocal {
		t.Errorf("Provenance = %q; want local", got.Provenance)
	}
	if got.GrantedBy == nil || *got.GrantedBy != actor.ID {
		t.Errorf("GrantedBy = %v; want %d", got.GrantedBy, actor.ID)
	}
	if got.GrantedAt.IsZero() {
		t.Errorf("GrantedAt is zero; store must set it")
	}
	if got.LastAssertedAt != nil {
		t.Errorf("LastAssertedAt = %v; want nil for a local grant", got.LastAssertedAt)
	}

	// Read back by subject.
	forSubject, err := s.Meta().ListGrantsForPrincipal(ctx, subject.ID)
	if err != nil {
		t.Fatalf("ListGrantsForPrincipal: %v", err)
	}
	if len(forSubject) != 1 || forSubject[0].ResourceID != "alpha.example" {
		t.Fatalf("ListGrantsForPrincipal = %+v; want one alpha.example grant", forSubject)
	}

	// Read back by resource.
	onResource, err := s.Meta().ListGrantsOnResource(ctx, store.GrantResourceDomain, "alpha.example")
	if err != nil {
		t.Fatalf("ListGrantsOnResource: %v", err)
	}
	if len(onResource) != 1 || onResource[0].SubjectID != uint64(subject.ID) {
		t.Fatalf("ListGrantsOnResource = %+v; want one grant for subject %d", onResource, subject.ID)
	}

	// The granting actor holds no grant of its own.
	forActor, err := s.Meta().ListGrantsForPrincipal(ctx, actor.ID)
	if err != nil {
		t.Fatalf("ListGrantsForPrincipal actor: %v", err)
	}
	if len(forActor) != 0 {
		t.Errorf("actor grants = %+v; want none (granted_by is not a grant)", forActor)
	}

	// Delete by natural key.
	if err := s.Meta().DeleteGrant(ctx, store.Grant{
		SubjectID:    uint64(subject.ID),
		ResourceKind: store.GrantResourceDomain,
		ResourceID:   "alpha.example",
	}); err != nil {
		t.Fatalf("DeleteGrant: %v", err)
	}
	forSubject, err = s.Meta().ListGrantsForPrincipal(ctx, subject.ID)
	if err != nil {
		t.Fatalf("ListGrantsForPrincipal after delete: %v", err)
	}
	if len(forSubject) != 0 {
		t.Errorf("grants after delete = %+v; want none", forSubject)
	}
}

// testGrants_InsertIdempotent verifies that re-inserting an identical grant
// is a no-op that preserves the original row (id + GrantedAt), not a duplicate
// or a conflict error.
func testGrants_InsertIdempotent(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	subject := mustInsertPrincipal(t, s, "grant-idem@example.test")

	g := store.Grant{
		SubjectID:    uint64(subject.ID),
		ResourceKind: store.GrantResourceServer,
		Level:        store.GrantLevelSuperadmin,
	}
	first, err := s.Meta().InsertGrant(ctx, g)
	if err != nil {
		t.Fatalf("InsertGrant first: %v", err)
	}
	second, err := s.Meta().InsertGrant(ctx, g)
	if err != nil {
		t.Fatalf("InsertGrant duplicate: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("idempotent insert changed ID: %d -> %d", first.ID, second.ID)
	}
	if !second.GrantedAt.Equal(first.GrantedAt) {
		t.Errorf("idempotent insert changed GrantedAt: %v -> %v", first.GrantedAt, second.GrantedAt)
	}
	all, err := s.Meta().ListGrantsForPrincipal(ctx, subject.ID)
	if err != nil {
		t.Fatalf("ListGrantsForPrincipal: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("grant count after duplicate insert = %d; want 1", len(all))
	}
}

// testGrants_ProvenanceDistinct verifies that a local and an idp: grant for
// the same (subject, resource) coexist as independent rows (REQ-AC-61), so
// IdP reconciliation can never disturb a local grant.
func testGrants_ProvenanceDistinct(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	subject := mustInsertPrincipal(t, s, "grant-prov@example.test")

	asserted := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	if _, err := s.Meta().InsertGrant(ctx, store.Grant{
		SubjectID:    uint64(subject.ID),
		ResourceKind: store.GrantResourceDomain,
		ResourceID:   "beta.example",
		Level:        store.GrantLevelOperator,
		Provenance:   store.GrantProvenanceLocal,
	}); err != nil {
		t.Fatalf("InsertGrant local: %v", err)
	}
	if _, err := s.Meta().InsertGrant(ctx, store.Grant{
		SubjectID:      uint64(subject.ID),
		ResourceKind:   store.GrantResourceDomain,
		ResourceID:     "beta.example",
		Level:          store.GrantLevelOwner,
		Provenance:     "idp:acme",
		LastAssertedAt: &asserted,
	}); err != nil {
		t.Fatalf("InsertGrant idp: %v", err)
	}

	all, err := s.Meta().ListGrantsForPrincipal(ctx, subject.ID)
	if err != nil {
		t.Fatalf("ListGrantsForPrincipal: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("grant count = %d; want 2 (local + idp coexist)", len(all))
	}
	var sawIDP bool
	for _, g := range all {
		if g.Provenance == "idp:acme" {
			sawIDP = true
			if g.LastAssertedAt == nil || !g.LastAssertedAt.Equal(asserted) {
				t.Errorf("idp grant LastAssertedAt = %v; want %v", g.LastAssertedAt, asserted)
			}
		}
	}
	if !sawIDP {
		t.Errorf("idp:acme grant missing from %+v", all)
	}

	// Deleting the local grant leaves the idp grant intact.
	if err := s.Meta().DeleteGrant(ctx, store.Grant{
		SubjectID:    uint64(subject.ID),
		ResourceKind: store.GrantResourceDomain,
		ResourceID:   "beta.example",
		Provenance:   store.GrantProvenanceLocal,
	}); err != nil {
		t.Fatalf("DeleteGrant local: %v", err)
	}
	all, err = s.Meta().ListGrantsForPrincipal(ctx, subject.ID)
	if err != nil {
		t.Fatalf("ListGrantsForPrincipal after local delete: %v", err)
	}
	if len(all) != 1 || all[0].Provenance != "idp:acme" {
		t.Errorf("after deleting local grant, remaining = %+v; want only idp:acme", all)
	}
}

// testGrants_DeleteNotFound verifies DeleteGrant returns ErrNotFound for an
// absent natural key.
func testGrants_DeleteNotFound(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	subject := mustInsertPrincipal(t, s, "grant-del-nf@example.test")

	err := s.Meta().DeleteGrant(ctx, store.Grant{
		SubjectID:    uint64(subject.ID),
		ResourceKind: store.GrantResourceDomain,
		ResourceID:   "missing.example",
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("DeleteGrant absent: got %v; want ErrNotFound", err)
	}
}

// testGrants_ListOrderedByID verifies ListGrantsForPrincipal returns rows in
// ascending id order and scopes strictly to the given principal.
func testGrants_ListOrderedByID(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	subject := mustInsertPrincipal(t, s, "grant-order@example.test")
	other := mustInsertPrincipal(t, s, "grant-order-other@example.test")

	domains := []string{"d1.example", "d2.example", "d3.example"}
	for _, d := range domains {
		if _, err := s.Meta().InsertGrant(ctx, store.Grant{
			SubjectID:    uint64(subject.ID),
			ResourceKind: store.GrantResourceDomain,
			ResourceID:   d,
			Level:        store.GrantLevelOperator,
		}); err != nil {
			t.Fatalf("InsertGrant %s: %v", d, err)
		}
	}
	// A grant for a different principal must not leak into subject's list.
	if _, err := s.Meta().InsertGrant(ctx, store.Grant{
		SubjectID:    uint64(other.ID),
		ResourceKind: store.GrantResourceDomain,
		ResourceID:   "other.example",
		Level:        store.GrantLevelOperator,
	}); err != nil {
		t.Fatalf("InsertGrant other: %v", err)
	}

	got, err := s.Meta().ListGrantsForPrincipal(ctx, subject.ID)
	if err != nil {
		t.Fatalf("ListGrantsForPrincipal: %v", err)
	}
	if len(got) != len(domains) {
		t.Fatalf("grant count = %d; want %d", len(got), len(domains))
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].ID >= got[i].ID {
			t.Errorf("grants not ascending by id: %d then %d", got[i-1].ID, got[i].ID)
		}
	}
	for _, g := range got {
		if g.SubjectID != uint64(subject.ID) {
			t.Errorf("leaked grant for subject %d in list for %d", g.SubjectID, subject.ID)
		}
	}
}
