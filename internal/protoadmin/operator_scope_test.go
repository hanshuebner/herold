package protoadmin_test

import (
	"context"
	"testing"

	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/store"
)

// TestResolveOperatorScope_SuperAdmin verifies that a principal holding both
// PrincipalFlagAdmin and PrincipalFlagSuperAdmin receives an unrestricted scope.
func TestResolveOperatorScope_SuperAdmin(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	p, err := h.h.Store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "super@example.test",
		Flags:          store.PrincipalFlagAdmin | store.PrincipalFlagSuperAdmin,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}

	scope := protoadmin.ResolveOperatorScope(ctx, h.h.Store.Meta(), p)
	if !scope.SuperAdmin {
		t.Errorf("SuperAdmin = false; want true")
	}
	if scope.Domains != nil {
		t.Errorf("Domains = %v; want nil for super-admin", scope.Domains)
	}
}

// TestResolveOperatorScope_DomainOperator verifies that a domain-scoped
// operator receives a scope containing exactly its managed domains.
func TestResolveOperatorScope_DomainOperator(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	p, err := h.h.Store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "operator@example.test",
		Flags:          store.PrincipalFlagAdmin, // no super-admin
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}

	grantDomainOperator(t, h, p.ID, "x.example")
	grantDomainOperator(t, h, p.ID, "y.example")

	scope := protoadmin.ResolveOperatorScope(ctx, h.h.Store.Meta(), p)
	if scope.SuperAdmin {
		t.Errorf("SuperAdmin = true; want false for domain operator")
	}
	if len(scope.Domains) != 2 {
		t.Fatalf("len(Domains) = %d; want 2", len(scope.Domains))
	}
	if scope.Domains[0] != "x.example" || scope.Domains[1] != "y.example" {
		t.Errorf("Domains = %v; want [x.example y.example]", scope.Domains)
	}
}

// TestResolveOperatorScope_DomainOperator_NoDomains verifies that a domain-
// scoped operator with no assigned domains receives a non-nil empty slice,
// enforcing the fail-closed contract of REQ-ADM-307.
//
// REGRESSION: before the fix, ListManagedDomains returned nil for a principal
// with no rows, and ResolveOperatorScope propagated that nil directly as
// Domains. SystemEventFilter treats nil Domains as unrestricted (super-admin
// equivalent), so a no-domains admin saw all events. The fix coerces nil to
// []string{} on all non-super-admin paths.
func TestResolveOperatorScope_DomainOperator_NoDomains(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	p, err := h.h.Store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "nodomain@example.test",
		Flags:          store.PrincipalFlagAdmin,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}

	scope := protoadmin.ResolveOperatorScope(ctx, h.h.Store.Meta(), p)
	if scope.SuperAdmin {
		t.Errorf("SuperAdmin = true; want false")
	}
	// Domains MUST be non-nil so that store filter nil/empty distinction
	// is honored (nil=unrestricted, non-nil-empty=fail-closed).
	if scope.Domains == nil {
		t.Errorf("Domains is nil; want non-nil empty slice (fail-closed contract)")
	}
	if len(scope.Domains) != 0 {
		t.Errorf("Domains = %v; want empty", scope.Domains)
	}
}

// TestResolveOperatorScope_NotAdmin verifies that a plain (non-admin) principal
// receives a non-nil empty Domains slice (fail-closed).
func TestResolveOperatorScope_NotAdmin(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	p, err := h.h.Store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "notadmin@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}

	scope := protoadmin.ResolveOperatorScope(ctx, h.h.Store.Meta(), p)
	if scope.SuperAdmin {
		t.Errorf("SuperAdmin = true; want false for non-admin")
	}
	// Domains must be non-nil even for a non-admin caller (fail-closed).
	if scope.Domains == nil {
		t.Errorf("Domains is nil for non-admin; want non-nil empty slice")
	}
	if len(scope.Domains) != 0 {
		t.Errorf("Domains = %v; want empty for non-admin", scope.Domains)
	}
}
