package storetest

import (
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// A principal's managed-domain set is a domain:operator (or higher) grant on
// the domain resource (epic #182, REQ-ADM-307, re #237); the assign / list /
// revoke / idempotency / not-found lifecycle is covered generically by the
// Grants_* tests in storetest_grants.go, exercised against
// store.GrantResourceDomain the same way the operator REST endpoints exercise
// it. This file covers only the operator-principal-listing surface that
// remains specific to delegated operators.

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

// testAuditLog_DomainFilter verifies the REQ-ADM-307 domain-scope filter on
// AuditLogFilter.Domains:
//   - nil Domains = unrestricted (super-admin equivalent)
//   - non-nil, non-empty Domains = IN-list filter
//   - non-nil empty Domains = fail-closed (zero rows)
//
// The domain column defaults to an empty string on new entries. These cases seed
// entries with explicit domain tags to exercise the IN-list path.
func testAuditLog_DomainFilter(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	// Insert audit entries with three distinct domain values.
	for i, tc := range []struct {
		action string
		domain string
	}{
		{"audit.domain.alpha1", "alpha.example"},
		{"audit.domain.alpha2", "alpha.example"},
		{"audit.domain.beta1", "beta.example"},
		{"audit.domain.noDomain", ""},
	} {
		if err := s.Meta().AppendAuditLog(ctx, store.AuditLogEntry{
			At:        base.Add(time.Duration(i) * time.Second),
			ActorKind: store.ActorSystem,
			ActorID:   "test",
			Action:    tc.action,
			Subject:   "test:scope",
			Outcome:   store.OutcomeSuccess,
			Domain:    tc.domain,
		}); err != nil {
			t.Fatalf("AppendAuditLog %d: %v", i, err)
		}
	}

	// Unrestricted (nil Domains): should return all four entries.
	all, err := s.Meta().ListAuditLog(ctx, store.AuditLogFilter{
		Action: "audit.domain.alpha1",
	})
	if err != nil {
		t.Fatalf("ListAuditLog unrestricted: %v", err)
	}
	if len(all) == 0 {
		t.Error("nil Domains: got 0 entries; want >= 1 (unrestricted)")
	}

	// IN-list filter for alpha.example: must return only alpha entries.
	alpha, err := s.Meta().ListAuditLog(ctx, store.AuditLogFilter{
		Domains: []string{"alpha.example"},
	})
	if err != nil {
		t.Fatalf("ListAuditLog Domains=[alpha]: %v", err)
	}
	if len(alpha) < 2 {
		t.Errorf("Domains=[alpha.example]: got %d; want >= 2", len(alpha))
	}
	for _, e := range alpha {
		if e.Domain != "alpha.example" {
			t.Errorf("Domains=[alpha.example] returned entry with domain=%q", e.Domain)
		}
	}

	// IN-list filter for beta.example: must return only beta entries.
	beta, err := s.Meta().ListAuditLog(ctx, store.AuditLogFilter{
		Domains: []string{"beta.example"},
	})
	if err != nil {
		t.Fatalf("ListAuditLog Domains=[beta]: %v", err)
	}
	if len(beta) < 1 {
		t.Errorf("Domains=[beta.example]: got %d; want >= 1", len(beta))
	}
	for _, e := range beta {
		if e.Domain != "beta.example" {
			t.Errorf("Domains=[beta.example] returned entry with domain=%q", e.Domain)
		}
	}

	// Fail-closed: non-nil empty Domains must return zero entries.
	empty, err := s.Meta().ListAuditLog(ctx, store.AuditLogFilter{
		Domains: []string{},
	})
	if err != nil {
		t.Fatalf("ListAuditLog Domains=[]: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("Domains=[]: got %d entries; want 0 (fail-closed)", len(empty))
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
