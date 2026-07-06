package storetest

import (
	"errors"
	"testing"

	"github.com/hanshuebner/herold/internal/store"
)

// testManagedDomains_AssignRevoke verifies the full assign / list / revoke
// lifecycle for principal_managed_domains (REQ-ADM-307, re #145).
func testManagedDomains_AssignRevoke(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "op-assignrevoke@example.test")

	// Start empty.
	domains, err := s.Meta().ListManagedDomains(ctx, p.ID)
	if err != nil {
		t.Fatalf("ListManagedDomains empty: %v", err)
	}
	if len(domains) != 0 {
		t.Errorf("initial domains = %v; want empty", domains)
	}

	// Assign two domains.
	if err := s.Meta().AssignManagedDomain(ctx, p.ID, "alpha.example"); err != nil {
		t.Fatalf("AssignManagedDomain alpha: %v", err)
	}
	if err := s.Meta().AssignManagedDomain(ctx, p.ID, "beta.example"); err != nil {
		t.Fatalf("AssignManagedDomain beta: %v", err)
	}

	// List returns both in ascending order.
	domains, err = s.Meta().ListManagedDomains(ctx, p.ID)
	if err != nil {
		t.Fatalf("ListManagedDomains after assign: %v", err)
	}
	if len(domains) != 2 || domains[0] != "alpha.example" || domains[1] != "beta.example" {
		t.Errorf("domains = %v; want [alpha.example beta.example]", domains)
	}

	// Revoke one.
	if err := s.Meta().RevokeManagedDomain(ctx, p.ID, "alpha.example"); err != nil {
		t.Fatalf("RevokeManagedDomain: %v", err)
	}
	domains, err = s.Meta().ListManagedDomains(ctx, p.ID)
	if err != nil {
		t.Fatalf("ListManagedDomains after revoke: %v", err)
	}
	if len(domains) != 1 || domains[0] != "beta.example" {
		t.Errorf("domains after revoke = %v; want [beta.example]", domains)
	}

	// Revoke the second.
	if err := s.Meta().RevokeManagedDomain(ctx, p.ID, "beta.example"); err != nil {
		t.Fatalf("RevokeManagedDomain beta: %v", err)
	}
	domains, err = s.Meta().ListManagedDomains(ctx, p.ID)
	if err != nil {
		t.Fatalf("ListManagedDomains after all revoked: %v", err)
	}
	if len(domains) != 0 {
		t.Errorf("domains after all revoked = %v; want empty", domains)
	}
}

// testManagedDomains_AssignIdempotent verifies that assigning a domain that
// is already present is a no-op and does not return an error.
func testManagedDomains_AssignIdempotent(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "op-idem@example.test")

	if err := s.Meta().AssignManagedDomain(ctx, p.ID, "dup.example"); err != nil {
		t.Fatalf("AssignManagedDomain first: %v", err)
	}
	// Second call must not error.
	if err := s.Meta().AssignManagedDomain(ctx, p.ID, "dup.example"); err != nil {
		t.Errorf("AssignManagedDomain duplicate: got %v; want nil", err)
	}
	// Exactly one row must exist.
	domains, err := s.Meta().ListManagedDomains(ctx, p.ID)
	if err != nil {
		t.Fatalf("ListManagedDomains: %v", err)
	}
	if len(domains) != 1 || domains[0] != "dup.example" {
		t.Errorf("domains = %v; want [dup.example]", domains)
	}
}

// testManagedDomains_RevokeNotFound verifies that RevokeManagedDomain returns
// ErrNotFound when the row is absent.
func testManagedDomains_RevokeNotFound(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "op-revoke-nf@example.test")

	err := s.Meta().RevokeManagedDomain(ctx, p.ID, "nobody.example")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("RevokeManagedDomain absent: got %v; want ErrNotFound", err)
	}
}

// testManagedDomains_CascadeOnDeletePrincipal verifies that deleting a
// principal cascades to its principal_managed_domains rows.
func testManagedDomains_CascadeOnDeletePrincipal(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "op-cascade@example.test")

	if err := s.Meta().AssignManagedDomain(ctx, p.ID, "cascade.example"); err != nil {
		t.Fatalf("AssignManagedDomain: %v", err)
	}
	if err := s.Meta().DeletePrincipal(ctx, p.ID); err != nil {
		t.Fatalf("DeletePrincipal: %v", err)
	}
	// After the principal is deleted, listing its domains should return nothing
	// (the FK CASCADE removed the rows). We cannot call ListManagedDomains on a
	// deleted principal in all backends without an error; the empty-slice
	// result or a store-specific non-fatal response are both acceptable.
	domains, err := s.Meta().ListManagedDomains(ctx, p.ID)
	if err == nil && len(domains) != 0 {
		t.Errorf("ListManagedDomains after cascade delete: got %v; want empty", domains)
	}
}

// testListDomainOperators verifies that ListDomainOperators returns only
// principals with PrincipalFlagAdmin but NOT PrincipalFlagSuperAdmin.
func testListDomainOperators(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)

	// Insert three principals: one super-admin, one domain operator, one plain user.
	superAdmin := mustInsertPrincipal(t, s, "sa-listop@example.test")
	operator := mustInsertPrincipal(t, s, "op-listop@example.test")
	user := mustInsertPrincipal(t, s, "user-listop@example.test")

	// Promote to super-admin.
	superAdmin.Flags = store.PrincipalFlagAdmin | store.PrincipalFlagSuperAdmin
	if err := s.Meta().UpdatePrincipal(ctx, superAdmin); err != nil {
		t.Fatalf("UpdatePrincipal super-admin: %v", err)
	}

	// Promote to operator (admin but not super-admin).
	operator.Flags = store.PrincipalFlagAdmin
	if err := s.Meta().UpdatePrincipal(ctx, operator); err != nil {
		t.Fatalf("UpdatePrincipal operator: %v", err)
	}

	// user stays a plain user.
	_ = user

	ops, err := s.Meta().ListDomainOperators(ctx)
	if err != nil {
		t.Fatalf("ListDomainOperators: %v", err)
	}

	// Must contain the operator but not the super-admin or the plain user.
	found := false
	for _, op := range ops {
		if op.ID == operator.ID {
			found = true
		}
		if op.ID == superAdmin.ID {
			t.Errorf("ListDomainOperators returned super-admin %d; want only operators", superAdmin.ID)
		}
		if op.ID == user.ID {
			t.Errorf("ListDomainOperators returned plain user %d; want only operators", user.ID)
		}
	}
	if !found {
		t.Errorf("ListDomainOperators did not return operator %d", operator.ID)
	}
}

// testMigrationAutoPromotion verifies that any PrincipalFlagAdmin principal
// written via UpdatePrincipal with PrincipalFlagSuperAdmin set is correctly
// returned by GetPrincipalByID (exercises the flag storage round-trip that the
// migration's UPDATE relies on).
func testMigrationAutoPromotion(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)

	p := mustInsertPrincipal(t, s, "autopromote@example.test")

	// Simulate the migration: set both admin and super-admin flags.
	p.Flags = store.PrincipalFlagAdmin | store.PrincipalFlagSuperAdmin
	if err := s.Meta().UpdatePrincipal(ctx, p); err != nil {
		t.Fatalf("UpdatePrincipal (auto-promote): %v", err)
	}

	got, err := s.Meta().GetPrincipalByID(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetPrincipalByID: %v", err)
	}
	if !got.Flags.Has(store.PrincipalFlagAdmin) {
		t.Errorf("PrincipalFlagAdmin not set after promotion; flags=%d", got.Flags)
	}
	if !got.Flags.Has(store.PrincipalFlagSuperAdmin) {
		t.Errorf("PrincipalFlagSuperAdmin not set after promotion; flags=%d", got.Flags)
	}
	// Verify ListDomainOperators does NOT include this principal (it is a
	// super-admin, not a domain operator).
	ops, err := s.Meta().ListDomainOperators(ctx)
	if err != nil {
		t.Fatalf("ListDomainOperators: %v", err)
	}
	for _, op := range ops {
		if op.ID == p.ID {
			t.Errorf("ListDomainOperators included super-admin %d; want only operators", p.ID)
		}
	}
}
