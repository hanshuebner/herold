package authz

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
)

// fakeIdPStore is a minimal idpStore for ReconcileIdP unit tests. It layers
// on fakeReader for grant/managed-domain reads and adds the OIDC provider,
// claim-allowlist, mapping-rule, reconcile, and audit surfaces.
type fakeIdPStore struct {
	fakeReader

	principals  map[store.PrincipalID]store.Principal
	provider    store.OIDCProvider
	providerOK  bool
	providerErr error

	allowlist    []string
	allowlistErr error

	rules    []store.ClaimMappingRule
	rulesErr error

	reconcileErr error
	// reconcileCalled records the desired set passed to ReconcileIdPGrants,
	// for assertions independent of the added/removed return.
	reconcileCalled bool
	lastDesired     []store.GrantDesired

	audits []store.AuditLogEntry
}

func (f *fakeIdPStore) GetPrincipalByID(_ context.Context, id store.PrincipalID) (store.Principal, error) {
	p, ok := f.principals[id]
	if !ok {
		return store.Principal{}, store.ErrNotFound
	}
	return p, nil
}

func (f *fakeIdPStore) GetOIDCProvider(_ context.Context, _ string) (store.OIDCProvider, error) {
	if f.providerErr != nil {
		return store.OIDCProvider{}, f.providerErr
	}
	if !f.providerOK {
		return store.OIDCProvider{}, store.ErrNotFound
	}
	return f.provider, nil
}

func (f *fakeIdPStore) ListClaimAllowlist(_ context.Context, _ string) ([]string, error) {
	if f.allowlistErr != nil {
		return nil, f.allowlistErr
	}
	return f.allowlist, nil
}

func (f *fakeIdPStore) ListClaimMappingRules(_ context.Context, _ string) ([]store.ClaimMappingRule, error) {
	if f.rulesErr != nil {
		return nil, f.rulesErr
	}
	return f.rules, nil
}

func (f *fakeIdPStore) ReconcileIdPGrants(_ context.Context, subjectID store.PrincipalID, provider string, desired []store.GrantDesired, asOf time.Time) ([]store.Grant, []store.Grant, error) {
	f.reconcileCalled = true
	f.lastDesired = desired
	if f.reconcileErr != nil {
		return nil, nil, f.reconcileErr
	}
	var added []store.Grant
	for _, d := range desired {
		added = append(added, store.Grant{
			SubjectKind: store.GrantSubjectPrincipal, SubjectID: uint64(subjectID),
			ResourceKind: d.ResourceKind, ResourceID: d.ResourceID, Level: d.Level,
			Provenance: "idp:" + provider, GrantedAt: asOf, LastAssertedAt: &asOf,
		})
	}
	return added, nil, nil
}

func (f *fakeIdPStore) AppendAuditLog(_ context.Context, entry store.AuditLogEntry) error {
	f.audits = append(f.audits, entry)
	return nil
}

func newTestFakeIdPStore() *fakeIdPStore {
	return &fakeIdPStore{
		fakeReader: fakeReader{
			grants:  map[store.PrincipalID][]store.Grant{},
			managed: map[store.PrincipalID][]string{},
		},
		principals: map[store.PrincipalID]store.Principal{},
	}
}

func TestReconcileIdP_UntrustedProviderIsNoOp(t *testing.T) {
	ctx := context.Background()
	fs := newTestFakeIdPStore()
	fs.providerOK = true
	fs.provider = store.OIDCProvider{Name: "acme", AuthzTrusted: false}

	added, removed, err := ReconcileIdP(ctx, fs, clock.NewFake(time.Now()), store.Principal{ID: 1}, "acme", map[string]any{"groups": []any{"anything"}}, false)
	if err != nil {
		t.Fatalf("ReconcileIdP: %v", err)
	}
	if len(added) != 0 || len(removed) != 0 {
		t.Fatalf("added=%v removed=%v; want none (untrusted provider)", added, removed)
	}
	if fs.reconcileCalled {
		t.Errorf("ReconcileIdPGrants was called for an untrusted provider")
	}
}

func TestReconcileIdP_MatchedRuleGrantsAccess(t *testing.T) {
	ctx := context.Background()
	fs := newTestFakeIdPStore()
	fs.providerOK = true
	fs.provider = store.OIDCProvider{Name: "acme", AuthzTrusted: true}
	fs.allowlist = []string{"groups"}
	author := store.Principal{ID: 42, Flags: store.PrincipalFlagAdmin | store.PrincipalFlagSuperAdmin}
	fs.principals[42] = author
	fs.rules = []store.ClaimMappingRule{{
		ID: 1, ProviderName: "acme", Claim: "groups", MatchValue: "list-x-admins",
		ResourceKind: store.GrantResourceList, ResourceID: "x", Level: store.GrantLevelOwner,
		CreatedBy: 42,
	}}

	added, removed, err := ReconcileIdP(ctx, fs, clock.NewFake(time.Now()), store.Principal{ID: 7}, "acme",
		map[string]any{"groups": []any{"list-x-admins", "other"}}, false)
	if err != nil {
		t.Fatalf("ReconcileIdP: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("removed = %v; want none", removed)
	}
	if len(added) != 1 || added[0].ResourceKind != store.GrantResourceList || added[0].Level != store.GrantLevelOwner {
		t.Fatalf("added = %+v; want one list:x owner grant", added)
	}
	if len(fs.audits) == 0 {
		t.Errorf("expected an audit entry for the added grant")
	}
}

func TestReconcileIdP_ClaimNotOnAllowlist(t *testing.T) {
	ctx := context.Background()
	fs := newTestFakeIdPStore()
	fs.providerOK = true
	fs.provider = store.OIDCProvider{Name: "acme", AuthzTrusted: true}
	fs.allowlist = []string{"roles"} // "groups" is NOT allowlisted
	fs.principals[42] = store.Principal{ID: 42, Flags: store.PrincipalFlagAdmin | store.PrincipalFlagSuperAdmin}
	fs.rules = []store.ClaimMappingRule{{
		ProviderName: "acme", Claim: "groups", MatchValue: "list-x-admins",
		ResourceKind: store.GrantResourceList, ResourceID: "x", Level: store.GrantLevelOwner,
		CreatedBy: 42,
	}}

	added, _, err := ReconcileIdP(ctx, fs, clock.NewFake(time.Now()), store.Principal{ID: 7}, "acme",
		map[string]any{"groups": []any{"list-x-admins"}}, false)
	if err != nil {
		t.Fatalf("ReconcileIdP: %v", err)
	}
	if len(added) != 0 {
		t.Fatalf("added = %v; want none (claim not allowlisted, REQ-AC-67)", added)
	}
}

func TestReconcileIdP_ServerKindRuleNeverMatches(t *testing.T) {
	ctx := context.Background()
	fs := newTestFakeIdPStore()
	fs.providerOK = true
	fs.provider = store.OIDCProvider{Name: "acme", AuthzTrusted: true}
	fs.allowlist = []string{"groups"}
	fs.principals[42] = store.Principal{ID: 42, Flags: store.PrincipalFlagAdmin | store.PrincipalFlagSuperAdmin}
	fs.rules = []store.ClaimMappingRule{{
		ProviderName: "acme", Claim: "groups", MatchValue: "root",
		ResourceKind: store.GrantResourceServer, ResourceID: "", Level: store.GrantLevelSuperadmin,
		CreatedBy: 42,
	}}

	added, _, err := ReconcileIdP(ctx, fs, clock.NewFake(time.Now()), store.Principal{ID: 7}, "acme",
		map[string]any{"groups": []any{"root"}}, false)
	if err != nil {
		t.Fatalf("ReconcileIdP: %v", err)
	}
	if len(added) != 0 {
		t.Fatalf("added = %v; want none -- server:superadmin must never be IdP-derivable (REQ-AC-64)", added)
	}
}

func TestReconcileIdP_AuthorLostAuthorityMakesRuleInert(t *testing.T) {
	ctx := context.Background()
	fs := newTestFakeIdPStore()
	fs.providerOK = true
	fs.provider = store.OIDCProvider{Name: "acme", AuthzTrusted: true}
	fs.allowlist = []string{"groups"}
	// Author holds no grant at all now (e.g. their domain:owner grant was
	// revoked after the rule was written) and carries no admin flag.
	fs.principals[42] = store.Principal{ID: 42}
	fs.rules = []store.ClaimMappingRule{{
		ProviderName: "acme", Claim: "groups", MatchValue: "list-x-admins",
		ResourceKind: store.GrantResourceList, ResourceID: "x", Level: store.GrantLevelOwner,
		CreatedBy: 42,
	}}

	added, _, err := ReconcileIdP(ctx, fs, clock.NewFake(time.Now()), store.Principal{ID: 7}, "acme",
		map[string]any{"groups": []any{"list-x-admins"}}, false)
	if err != nil {
		t.Fatalf("ReconcileIdP: %v", err)
	}
	if len(added) != 0 {
		t.Fatalf("added = %v; want none (author no longer holds delegable authority, REQ-AC-68)", added)
	}
}

func TestReconcileIdP_NewPrincipalOnlyGetsMailboxRead(t *testing.T) {
	ctx := context.Background()
	fs := newTestFakeIdPStore()
	fs.providerOK = true
	fs.provider = store.OIDCProvider{Name: "acme", AuthzTrusted: true}
	fs.allowlist = []string{"groups"}
	fs.principals[42] = store.Principal{ID: 42, Flags: store.PrincipalFlagAdmin | store.PrincipalFlagSuperAdmin}
	fs.rules = []store.ClaimMappingRule{
		{
			ProviderName: "acme", Claim: "groups", MatchValue: "list-x-admins",
			ResourceKind: store.GrantResourceList, ResourceID: "x", Level: store.GrantLevelOwner,
			CreatedBy: 42,
		},
		{
			ProviderName: "acme", Claim: "groups", MatchValue: "list-x-admins",
			ResourceKind: store.GrantResourceMailbox, ResourceID: "shared-archive", Level: store.GrantLevelRead,
			CreatedBy: 42,
		},
	}

	added, _, err := ReconcileIdP(ctx, fs, clock.NewFake(time.Now()), store.Principal{ID: 7}, "acme",
		map[string]any{"groups": []any{"list-x-admins"}}, true /* isNewPrincipal */)
	if err != nil {
		t.Fatalf("ReconcileIdP: %v", err)
	}
	if len(added) != 1 || added[0].ResourceKind != store.GrantResourceMailbox || added[0].Level != store.GrantLevelRead {
		t.Fatalf("added = %+v; want only the mailbox:read grant (REQ-AC-69)", added)
	}
}

func TestReconcileIdP_LoadFailureLeavesGrantsUntouched(t *testing.T) {
	ctx := context.Background()
	fs := newTestFakeIdPStore()
	fs.providerOK = true
	fs.provider = store.OIDCProvider{Name: "acme", AuthzTrusted: true}
	fs.allowlistErr = errors.New("store down")

	_, _, err := ReconcileIdP(ctx, fs, clock.NewFake(time.Now()), store.Principal{ID: 7}, "acme",
		map[string]any{"groups": []any{"list-x-admins"}}, false)
	if err == nil {
		t.Fatalf("ReconcileIdP: want error on allowlist load failure (REQ-AC-70)")
	}
	if fs.reconcileCalled {
		t.Errorf("ReconcileIdPGrants was called despite the load failure -- grants must be left untouched")
	}
	if len(fs.audits) != 1 || fs.audits[0].Action != "idp.reconcile.failed" {
		t.Errorf("audits = %+v; want one idp.reconcile.failed entry", fs.audits)
	}
}

func TestReconcileIdP_HighestLevelWinsWhenTwoRulesTargetSameResource(t *testing.T) {
	ctx := context.Background()
	fs := newTestFakeIdPStore()
	fs.providerOK = true
	fs.provider = store.OIDCProvider{Name: "acme", AuthzTrusted: true}
	fs.allowlist = []string{"groups"}
	fs.principals[42] = store.Principal{ID: 42, Flags: store.PrincipalFlagAdmin | store.PrincipalFlagSuperAdmin}
	fs.rules = []store.ClaimMappingRule{
		{
			ProviderName: "acme", Claim: "groups", MatchValue: "domain-viewers",
			ResourceKind: store.GrantResourceDomain, ResourceID: "x.example", Level: store.GrantLevelOperator,
			CreatedBy: 42,
		},
		{
			ProviderName: "acme", Claim: "groups", MatchValue: "domain-owners",
			ResourceKind: store.GrantResourceDomain, ResourceID: "x.example", Level: store.GrantLevelOwner,
			CreatedBy: 42,
		},
	}

	added, _, err := ReconcileIdP(ctx, fs, clock.NewFake(time.Now()), store.Principal{ID: 7}, "acme",
		map[string]any{"groups": []any{"domain-viewers", "domain-owners"}}, false)
	if err != nil {
		t.Fatalf("ReconcileIdP: %v", err)
	}
	if len(added) != 1 || added[0].Level != store.GrantLevelOwner {
		t.Fatalf("added = %+v; want a single domain:x.example owner grant (highest level wins)", added)
	}
}
