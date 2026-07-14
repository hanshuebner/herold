package protoadmin_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/hanshuebner/herold/internal/store"
)

// TestOperators_AssignAndRevoke exercises the full managed-domain lifecycle
// via REST: assign, list, revoke (REQ-ADM-307, re #145, re #237). Assign and
// revoke are asserted against the store's grants directly (not just the REST
// response) to demonstrate that the REST endpoint writes and deletes a
// domain:operator grant rather than the retired principal_managed_domains
// row.
func TestOperators_AssignAndRevoke(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// Bootstrap a super-admin and an operator principal.
	_, adminKey := h.bootstrap("superadmin@example.test")

	// Promote bootstrap admin to super-admin (bootstrap gives PrincipalFlagAdmin,
	// we additionally need PrincipalFlagSuperAdmin for these endpoints).
	p, err := h.h.Store.Meta().GetPrincipalByEmail(ctx, "superadmin@example.test")
	if err != nil {
		t.Fatalf("GetPrincipalByEmail: %v", err)
	}
	p.Flags |= store.PrincipalFlagSuperAdmin
	if err := h.h.Store.Meta().UpdatePrincipal(ctx, p); err != nil {
		t.Fatalf("UpdatePrincipal: %v", err)
	}

	// Create an operator principal (admin but not super-admin).
	opID := h.createPrincipal(adminKey, "operator@example.test")
	op, err := h.h.Store.Meta().GetPrincipalByID(ctx, store.PrincipalID(opID))
	if err != nil {
		t.Fatalf("GetPrincipalByID: %v", err)
	}
	op.Flags = store.PrincipalFlagAdmin
	if err := h.h.Store.Meta().UpdatePrincipal(ctx, op); err != nil {
		t.Fatalf("UpdatePrincipal operator: %v", err)
	}

	// Assign a domain.
	res, _ := h.doRequest("POST",
		fmt.Sprintf("/api/v1/principals/%d/managed-domains", opID),
		adminKey, map[string]string{"domain": "alpha.example"})
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("assign domain: status=%d; want 204", res.StatusCode)
	}

	// The assign endpoint must have created a domain:operator grant, not a
	// principal_managed_domains row (re #237).
	grants, err := h.h.Store.Meta().ListGrantsForPrincipal(ctx, store.PrincipalID(opID))
	if err != nil {
		t.Fatalf("ListGrantsForPrincipal after assign: %v", err)
	}
	if len(grants) != 1 || grants[0].ResourceKind != store.GrantResourceDomain ||
		grants[0].ResourceID != "alpha.example" || grants[0].Level != store.GrantLevelOperator ||
		grants[0].Provenance != store.GrantProvenanceLocal {
		t.Fatalf("grants after assign = %+v; want one local domain:operator grant on alpha.example", grants)
	}

	// List managed domains.
	var listBody map[string]any
	res, buf := h.doRequest("GET",
		fmt.Sprintf("/api/v1/principals/%d/managed-domains", opID),
		adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list domains: status=%d body=%s; want 200", res.StatusCode, buf)
	}
	if err := json.Unmarshal(buf, &listBody); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	domains, _ := listBody["domains"].([]any)
	if len(domains) != 1 || domains[0].(string) != "alpha.example" {
		t.Errorf("domains = %v; want [alpha.example]", domains)
	}

	// List operators.
	var opsBody map[string]any
	res, buf = h.doRequest("GET", "/api/v1/admin/operators", adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list operators: status=%d body=%s; want 200", res.StatusCode, buf)
	}
	if err := json.Unmarshal(buf, &opsBody); err != nil {
		t.Fatalf("unmarshal operators: %v", err)
	}
	items, _ := opsBody["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items count = %d; want 1", len(items))
	}
	item := items[0].(map[string]any)
	md, _ := item["managed_domains"].([]any)
	if len(md) != 1 || md[0].(string) != "alpha.example" {
		t.Errorf("managed_domains = %v; want [alpha.example]", md)
	}

	// Revoke the domain.
	res, _ = h.doRequest("DELETE",
		fmt.Sprintf("/api/v1/principals/%d/managed-domains/alpha.example", opID),
		adminKey, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke domain: status=%d; want 204", res.StatusCode)
	}

	// The grant must be gone.
	grants, err = h.h.Store.Meta().ListGrantsForPrincipal(ctx, store.PrincipalID(opID))
	if err != nil {
		t.Fatalf("ListGrantsForPrincipal after revoke: %v", err)
	}
	if len(grants) != 0 {
		t.Errorf("grants after revoke = %+v; want none", grants)
	}

	// List should now be empty.
	res, buf = h.doRequest("GET",
		fmt.Sprintf("/api/v1/principals/%d/managed-domains", opID),
		adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list after revoke: status=%d body=%s", res.StatusCode, buf)
	}
	if err := json.Unmarshal(buf, &listBody); err != nil {
		t.Fatalf("unmarshal after revoke: %v", err)
	}
	domains, _ = listBody["domains"].([]any)
	if len(domains) != 0 {
		t.Errorf("domains after revoke = %v; want empty", domains)
	}
}

// TestOperators_Forbids_DomainOperator verifies that a domain-scoped operator
// (not a super-admin) cannot access operator management endpoints.
func TestOperators_Forbids_DomainOperator(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	_, adminKey := h.bootstrap("sa2@example.test")
	p, err := h.h.Store.Meta().GetPrincipalByEmail(ctx, "sa2@example.test")
	if err != nil {
		t.Fatalf("GetPrincipalByEmail: %v", err)
	}
	p.Flags |= store.PrincipalFlagSuperAdmin
	if err := h.h.Store.Meta().UpdatePrincipal(ctx, p); err != nil {
		t.Fatalf("UpdatePrincipal: %v", err)
	}

	// Create a domain-scoped operator with its own API key.
	opID := h.createPrincipal(adminKey, "op2@example.test")
	op, _ := h.h.Store.Meta().GetPrincipalByID(ctx, store.PrincipalID(opID))
	op.Flags = store.PrincipalFlagAdmin
	_ = h.h.Store.Meta().UpdatePrincipal(ctx, op)
	_, opKey := h.createAPIKey(adminKey, opID)

	// The domain-scoped operator must not be able to list operators.
	res, _ := h.doRequest("GET", "/api/v1/admin/operators", opKey, nil)
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("list operators with operator key: status=%d; want 403", res.StatusCode)
	}

	// The domain-scoped operator must not be able to assign managed domains.
	res, _ = h.doRequest("POST",
		fmt.Sprintf("/api/v1/principals/%d/managed-domains", opID),
		opKey, map[string]string{"domain": "foo.example"})
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("assign domain with operator key: status=%d; want 403", res.StatusCode)
	}
}

// TestOperators_RevokeManagedDomain_NotFound verifies that revoking a domain
// that was never assigned returns 404.
func TestOperators_RevokeManagedDomain_NotFound(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	_, adminKey := h.bootstrap("sa3@example.test")
	p, _ := h.h.Store.Meta().GetPrincipalByEmail(ctx, "sa3@example.test")
	p.Flags |= store.PrincipalFlagSuperAdmin
	_ = h.h.Store.Meta().UpdatePrincipal(ctx, p)

	opID := h.createPrincipal(adminKey, "op3@example.test")

	res, _ := h.doRequest("DELETE",
		fmt.Sprintf("/api/v1/principals/%d/managed-domains/absent.example", opID),
		adminKey, nil)
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("revoke absent domain: status=%d; want 404", res.StatusCode)
	}
}
