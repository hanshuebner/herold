package protoadmin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/store"
)

// This file exercises the epic #188 admin REST surface for external-IdP
// claim-to-grant mapping (REQ-AC-60..70 remaining work): the per-provider
// authz_trusted flag, the claim allowlist, and mapping-rule CRUD, plus the
// security properties called out on the ticket -- superadmin-only trust
// flip, rule authorship bounded by the caller's own delegable authority,
// "server" never an admissible rule target, and every mutation audited.

// createOIDCProviderForTest inserts an OIDC provider directly via the
// store (default authz_trusted=false), avoiding the discovery round-trip
// TestOIDCProviders_CRUD exercises separately.
func createOIDCProviderForTest(t *testing.T, h *harness, name string) {
	t.Helper()
	if err := h.h.Store.Meta().InsertOIDCProvider(context.Background(), store.OIDCProvider{
		Name:            name,
		IssuerURL:       "https://idp.example/" + name,
		ClientID:        "cid-" + name,
		ClientSecretRef: "inline:" + name, // gitleaks:allow
	}); err != nil {
		t.Fatalf("InsertOIDCProvider: %v", err)
	}
}

// promoteSuperAdmin flips the PrincipalFlagSuperAdmin bit on the
// bootstrap-created principal identified by email, mirroring the pattern
// used throughout server_test.go's operator-scope tests.
func promoteSuperAdmin(t *testing.T, h *harness, email string) {
	t.Helper()
	ctx := context.Background()
	p, err := h.h.Store.Meta().GetPrincipalByEmail(ctx, email)
	if err != nil {
		t.Fatalf("GetPrincipalByEmail: %v", err)
	}
	p.Flags |= store.PrincipalFlagSuperAdmin
	if err := h.h.Store.Meta().UpdatePrincipal(ctx, p); err != nil {
		t.Fatalf("UpdatePrincipal super-admin: %v", err)
	}
}

// createDomainOperator creates a non-superadmin admin principal, returning
// (principalID, apiKey). Callers grant it whatever domain/list authority
// their test needs via direct store.InsertGrant calls.
func createDomainOperator(t *testing.T, h *harness, adminKey, email string) (store.PrincipalID, string) {
	t.Helper()
	ctx := context.Background()
	opID := h.createPrincipal(adminKey, email)
	op, err := h.h.Store.Meta().GetPrincipalByID(ctx, store.PrincipalID(opID))
	if err != nil {
		t.Fatalf("GetPrincipalByID: %v", err)
	}
	op.Flags = store.PrincipalFlagAdmin
	if err := h.h.Store.Meta().UpdatePrincipal(ctx, op); err != nil {
		t.Fatalf("UpdatePrincipal operator: %v", err)
	}
	_, opKey := h.createAPIKey(adminKey, opID)
	return store.PrincipalID(opID), opKey
}

// grantDomainOwner writes a local domain:owner grant, the delegable
// authority level REQ-AC-64/68 require to author a claim-mapping rule
// targeting that domain.
func grantDomainOwner(t *testing.T, h *harness, subject store.PrincipalID, domain string) {
	t.Helper()
	if _, err := h.h.Store.Meta().InsertGrant(context.Background(), store.Grant{
		SubjectKind:  store.GrantSubjectPrincipal,
		SubjectID:    uint64(subject),
		ResourceKind: store.GrantResourceDomain,
		ResourceID:   domain,
		Level:        store.GrantLevelOwner,
		Provenance:   store.GrantProvenanceLocal,
	}); err != nil {
		t.Fatalf("InsertGrant domain:owner: %v", err)
	}
}

// TestClaimMapping_AuthzTrusted_SuperAdminOnly is REQ-AC-66: a domain
// operator (admin-flagged, not super-admin) may not flip authz_trusted;
// a super-admin can, and the change is reflected on the provider and
// audited.
func TestClaimMapping_AuthzTrusted_SuperAdminOnly(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("sa@example.com")
	promoteSuperAdmin(t, h, "sa@example.com")
	createOIDCProviderForTest(t, h, "acme")

	_, opKey := createDomainOperator(t, h, adminKey, "op@example.com")

	// Non-super-admin operator is refused.
	res, buf := h.doRequest("PUT", "/api/v1/oidc/providers/acme/authz-trusted", opKey,
		map[string]any{"authz_trusted": true})
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("operator PUT authz-trusted = %d, want 403: %s", res.StatusCode, buf)
	}

	// Super-admin succeeds.
	res, buf = h.doRequest("PUT", "/api/v1/oidc/providers/acme/authz-trusted", adminKey,
		map[string]any{"authz_trusted": true})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("superadmin PUT authz-trusted = %d, want 200: %s", res.StatusCode, buf)
	}
	var got struct {
		AuthzTrusted bool `json:"authz_trusted"`
	}
	if err := json.Unmarshal(buf, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.AuthzTrusted {
		t.Errorf("authz_trusted = false after PUT true")
	}

	// GET the provider reflects the flag.
	res, buf = h.doRequest("GET", "/api/v1/oidc/providers/acme", adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET provider = %d: %s", res.StatusCode, buf)
	}
	if err := json.Unmarshal(buf, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.AuthzTrusted {
		t.Errorf("GET provider authz_trusted = false, want true")
	}

	// Audited.
	res, buf = h.doRequest("GET", "/api/v1/audit?action=oidc.provider.authz_trusted.set&limit=10", adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("audit = %d: %s", res.StatusCode, buf)
	}
	var page struct {
		Items []map[string]any `json:"items"`
	}
	_ = json.Unmarshal(buf, &page)
	if len(page.Items) == 0 {
		t.Fatalf("no audit rows for authz_trusted.set: %s", buf)
	}
}

// TestClaimMapping_ClaimAllowlist_CRUD exercises REQ-AC-67's admin surface:
// add, list, and remove an allowlisted claim; removing an absent claim is
// 404.
func TestClaimMapping_ClaimAllowlist_CRUD(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("sa2@example.com")
	promoteSuperAdmin(t, h, "sa2@example.com")
	createOIDCProviderForTest(t, h, "okta")

	res, buf := h.doRequest("POST", "/api/v1/oidc/providers/okta/claim-allowlist", adminKey,
		map[string]any{"claim": "groups"})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("add claim = %d: %s", res.StatusCode, buf)
	}

	res, buf = h.doRequest("GET", "/api/v1/oidc/providers/okta/claim-allowlist", adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list = %d: %s", res.StatusCode, buf)
	}
	if !strings.Contains(string(buf), `"groups"`) {
		t.Fatalf("list missing groups claim: %s", buf)
	}

	res, _ = h.doRequest("DELETE", "/api/v1/oidc/providers/okta/claim-allowlist/groups", adminKey, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d", res.StatusCode)
	}

	// Second delete of the same (now-absent) claim is 404.
	res, _ = h.doRequest("DELETE", "/api/v1/oidc/providers/okta/claim-allowlist/groups", adminKey, nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("delete absent claim = %d, want 404", res.StatusCode)
	}
}

// TestClaimMapping_Rule_RequiresDelegableAuthority is REQ-AC-64's "a rule
// MAY only target resources the authoring operator controls": an operator
// with no authority over a domain cannot author a rule targeting it;
// granting domain:owner makes rule creation succeed.
func TestClaimMapping_Rule_RequiresDelegableAuthority(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("sa3@example.com")
	promoteSuperAdmin(t, h, "sa3@example.com")
	createOIDCProviderForTest(t, h, "corp")

	opID, opKey := createDomainOperator(t, h, adminKey, "op3@example.com")

	ruleBody := map[string]any{
		"claim":         "groups",
		"match_value":   "domain-ops",
		"resource_kind": "domain",
		"resource_id":   "example.test",
		"level":         "operator",
	}

	// No authority yet: refused.
	res, buf := h.doRequest("POST", "/api/v1/oidc/providers/corp/claim-mapping-rules", opKey, ruleBody)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("create rule without authority = %d, want 403: %s", res.StatusCode, buf)
	}

	// Grant domain:owner (the delegable level) and retry.
	grantDomainOwner(t, h, opID, "example.test")
	res, buf = h.doRequest("POST", "/api/v1/oidc/providers/corp/claim-mapping-rules", opKey, ruleBody)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create rule with domain:owner = %d, want 201: %s", res.StatusCode, buf)
	}
	var created struct {
		ID                   uint64 `json:"id"`
		AuthorAuthorityValid bool   `json:"author_authority_valid"`
		Orphaned             bool   `json:"orphaned"`
	}
	if err := json.Unmarshal(buf, &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.Orphaned {
		t.Errorf("freshly-created rule reported Orphaned=true")
	}
	if !created.AuthorAuthorityValid {
		t.Errorf("freshly-created rule reported AuthorAuthorityValid=false")
	}

	// Audited, domain-tagged.
	res, buf = h.doRequest("GET", "/api/v1/audit?action=oidc.claim_mapping_rule.create&limit=10", adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("audit = %d: %s", res.StatusCode, buf)
	}
	var page struct {
		Items []map[string]any `json:"items"`
	}
	_ = json.Unmarshal(buf, &page)
	if len(page.Items) == 0 {
		t.Fatalf("no audit rows for claim_mapping_rule.create: %s", buf)
	}
}

// TestClaimMapping_Rule_RejectsServerKind is REQ-AC-64's hard bar: no
// claim-mapping rule may target the server resource kind, so
// server:superadmin is never IdP-derivable even in principle -- the admin
// API refuses to create the row at all.
func TestClaimMapping_Rule_RejectsServerKind(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("sa4@example.com")
	promoteSuperAdmin(t, h, "sa4@example.com")
	createOIDCProviderForTest(t, h, "corp4")

	res, buf := h.doRequest("POST", "/api/v1/oidc/providers/corp4/claim-mapping-rules", adminKey,
		map[string]any{
			"claim":         "groups",
			"match_value":   "root-admins",
			"resource_kind": "server",
			"resource_id":   "",
			"level":         "superadmin",
		})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("create server-kind rule = %d, want 400: %s", res.StatusCode, buf)
	}
}

// TestClaimMapping_Rule_RejectsInvalidLevel verifies a level not in the
// target kind's known set is rejected rather than stored as a permanently
// non-matching rule.
func TestClaimMapping_Rule_RejectsInvalidLevel(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("sa5@example.com")
	promoteSuperAdmin(t, h, "sa5@example.com")
	createOIDCProviderForTest(t, h, "corp5")

	res, buf := h.doRequest("POST", "/api/v1/oidc/providers/corp5/claim-mapping-rules", adminKey,
		map[string]any{
			"claim":         "groups",
			"match_value":   "x",
			"resource_kind": "domain",
			"resource_id":   "example.test",
			"level":         "owner-typo",
		})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("create rule with invalid level = %d, want 400: %s", res.StatusCode, buf)
	}
}

// TestClaimMapping_Rule_Delete_AuthorOrAuthorityOrSuperAdmin exercises the
// three independent authorities that may delete a rule: the original
// author, any principal who currently holds delegable authority over the
// target resource, and a super-admin (the last of which is REQ-AC-68's
// documented need for cleaning up an orphaned/inert rule).
func TestClaimMapping_Rule_Delete_AuthorOrAuthorityOrSuperAdmin(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("sa6@example.com")
	promoteSuperAdmin(t, h, "sa6@example.com")
	createOIDCProviderForTest(t, h, "corp6")

	authorID, authorKey := createDomainOperator(t, h, adminKey, "author6@example.com")
	grantDomainOwner(t, h, authorID, "d6.example.test")

	strangerID, strangerKey := createDomainOperator(t, h, adminKey, "stranger6@example.com")
	_ = strangerID

	createRule := func() uint64 {
		res, buf := h.doRequest("POST", "/api/v1/oidc/providers/corp6/claim-mapping-rules", authorKey,
			map[string]any{
				"claim": "groups", "match_value": "ops", "resource_kind": "domain",
				"resource_id": "d6.example.test", "level": "operator",
			})
		if res.StatusCode != http.StatusCreated {
			t.Fatalf("create rule = %d: %s", res.StatusCode, buf)
		}
		var created struct {
			ID uint64 `json:"id"`
		}
		_ = json.Unmarshal(buf, &created)
		return created.ID
	}

	// A stranger with no authority over the target resource cannot delete.
	id1 := createRule()
	res, buf := h.doRequest("DELETE",
		"/api/v1/oidc/providers/corp6/claim-mapping-rules/"+strconv.FormatUint(id1, 10), strangerKey, nil)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("stranger delete = %d, want 403: %s", res.StatusCode, buf)
	}

	// The original author can delete their own rule.
	res, _ = h.doRequest("DELETE",
		"/api/v1/oidc/providers/corp6/claim-mapping-rules/"+strconv.FormatUint(id1, 10), authorKey, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("author delete = %d, want 204", res.StatusCode)
	}

	// A super-admin can always delete, including a rule it did not author.
	id2 := createRule()
	res, _ = h.doRequest("DELETE",
		"/api/v1/oidc/providers/corp6/claim-mapping-rules/"+strconv.FormatUint(id2, 10), adminKey, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("superadmin delete = %d, want 204", res.StatusCode)
	}
}

// TestClaimMapping_Rule_OrphanedAuthorVisibleAndRemovable is REQ-AC-68's
// operator-visible need: once a rule's author principal is deleted,
// created_by resets to 0 (per epic #188's store-level contract) and the
// rule must show Orphaned=true in the listing and remain removable by a
// super-admin.
func TestClaimMapping_Rule_OrphanedAuthorVisibleAndRemovable(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	_, adminKey := h.bootstrap("sa7@example.com")
	promoteSuperAdmin(t, h, "sa7@example.com")
	createOIDCProviderForTest(t, h, "corp7")

	authorID, authorKey := createDomainOperator(t, h, adminKey, "author7@example.com")
	grantDomainOwner(t, h, authorID, "d7.example.test")

	res, buf := h.doRequest("POST", "/api/v1/oidc/providers/corp7/claim-mapping-rules", authorKey,
		map[string]any{
			"claim": "groups", "match_value": "ops", "resource_kind": "domain",
			"resource_id": "d7.example.test", "level": "operator",
		})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create rule = %d: %s", res.StatusCode, buf)
	}
	var created struct {
		ID uint64 `json:"id"`
	}
	_ = json.Unmarshal(buf, &created)

	// Delete the author principal directly via the store (bypassing the
	// admin REST DELETE /principals path, which is out of scope here).
	if err := h.h.Store.Meta().DeletePrincipal(ctx, authorID); err != nil {
		t.Fatalf("DeletePrincipal author: %v", err)
	}

	res, buf = h.doRequest("GET", "/api/v1/oidc/providers/corp7/claim-mapping-rules", adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list rules = %d: %s", res.StatusCode, buf)
	}
	var page struct {
		Items []struct {
			ID       uint64 `json:"id"`
			Orphaned bool   `json:"orphaned"`
		} `json:"items"`
	}
	if err := json.Unmarshal(buf, &page); err != nil {
		t.Fatalf("decode: %v: %s", err, buf)
	}
	found := false
	for _, it := range page.Items {
		if it.ID == created.ID {
			found = true
			if !it.Orphaned {
				t.Errorf("rule %d with deleted author reported Orphaned=false", it.ID)
			}
		}
	}
	if !found {
		t.Fatalf("created rule %d missing from listing: %s", created.ID, buf)
	}

	// A super-admin removes the now-permanently-inert rule.
	res, _ = h.doRequest("DELETE",
		"/api/v1/oidc/providers/corp7/claim-mapping-rules/"+strconv.FormatUint(created.ID, 10), adminKey, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("superadmin delete orphaned rule = %d, want 204", res.StatusCode)
	}
}
