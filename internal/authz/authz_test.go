package authz

import (
	"context"
	"errors"
	"testing"

	"github.com/hanshuebner/herold/internal/store"
)

// fakeReader is a minimal grantReader for resolution tests: it returns
// canned grants, or a canned error to exercise the fail-closed path.
type fakeReader struct {
	grants map[store.PrincipalID][]store.Grant
	err    error
}

func (f *fakeReader) ListGrantsForPrincipal(_ context.Context, id store.PrincipalID) ([]store.Grant, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.grants[id], nil
}

func principal(id store.PrincipalID, flags store.PrincipalFlags) store.Principal {
	return store.Principal{ID: id, Kind: store.PrincipalKindUser, Flags: flags}
}

func domainRes(name string) Resource {
	return Resource{Kind: store.GrantResourceDomain, ID: name}
}

func TestResolve_FlagSuperadminTopsEveryKind(t *testing.T) {
	ctx := context.Background()
	// Empty reader: a flag superadmin must resolve without any store data.
	r := &fakeReader{}
	p := principal(1, store.PrincipalFlagAdmin|store.PrincipalFlagSuperAdmin)

	cases := []struct {
		res  Resource
		want store.GrantLevel
	}{
		{Resource{Kind: store.GrantResourceServer}, store.GrantLevelSuperadmin},
		{domainRes("x.example"), store.GrantLevelOwner},
		{Resource{Kind: store.GrantResourceList, ID: "l1"}, store.GrantLevelOwner},
		{Resource{Kind: store.GrantResourceMailbox, ID: "m1"}, store.GrantLevelAdmin},
	}
	for _, c := range cases {
		got, err := Resolve(ctx, r, p, c.res)
		if err != nil {
			t.Fatalf("Resolve(%v): %v", c.res, err)
		}
		if got != c.want {
			t.Errorf("Resolve(%v) = %q; want %q", c.res, got, c.want)
		}
	}
}

func TestResolve_FlagSuperadminIgnoresStoreError(t *testing.T) {
	ctx := context.Background()
	// Even with the store failing, a flag superadmin is never denied.
	r := &fakeReader{err: errors.New("store down")}
	p := principal(1, store.PrincipalFlagAdmin|store.PrincipalFlagSuperAdmin)

	got, err := Resolve(ctx, r, p, Resource{Kind: store.GrantResourceServer})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != store.GrantLevelSuperadmin {
		t.Errorf("Resolve = %q; want superadmin", got)
	}
}

func TestResolve_GrantSuperadminWithoutFlag(t *testing.T) {
	ctx := context.Background()
	r := &fakeReader{grants: map[store.PrincipalID][]store.Grant{
		2: {{ResourceKind: store.GrantResourceServer, Level: store.GrantLevelSuperadmin}},
	}}
	p := principal(2, 0) // no flags

	got, err := Resolve(ctx, r, p, domainRes("any.example"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != store.GrantLevelOwner {
		t.Errorf("Resolve(domain) for grant-superadmin = %q; want owner (top domain level)", got)
	}
}

func TestResolve_DomainOperatorGrant(t *testing.T) {
	ctx := context.Background()
	r := &fakeReader{grants: map[store.PrincipalID][]store.Grant{
		3: {{ResourceKind: store.GrantResourceDomain, ResourceID: "x.example", Level: store.GrantLevelOperator}},
	}}
	p := principal(3, store.PrincipalFlagAdmin)

	got, err := Resolve(ctx, r, p, domainRes("x.example"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != store.GrantLevelOperator {
		t.Errorf("Resolve = %q; want operator", got)
	}
	if !AtLeast(store.GrantResourceDomain, got, store.GrantLevelOperator) {
		t.Errorf("AtLeast operator = false; want true")
	}
	if AtLeast(store.GrantResourceDomain, got, store.GrantLevelOwner) {
		t.Errorf("AtLeast owner = true; operator must not imply owner")
	}
	// A different domain resolves to nothing.
	other, err := Resolve(ctx, r, p, domainRes("y.example"))
	if err != nil {
		t.Fatalf("Resolve other: %v", err)
	}
	if other != "" {
		t.Errorf("Resolve(y.example) = %q; want empty", other)
	}
}

func TestResolve_DomainOwnerImpliesOperator(t *testing.T) {
	ctx := context.Background()
	r := &fakeReader{grants: map[store.PrincipalID][]store.Grant{
		4: {{ResourceKind: store.GrantResourceDomain, ResourceID: "x.example", Level: store.GrantLevelOwner}},
	}}
	p := principal(4, store.PrincipalFlagAdmin)

	got, err := Resolve(ctx, r, p, domainRes("x.example"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != store.GrantLevelOwner {
		t.Errorf("Resolve = %q; want owner", got)
	}
	if !AtLeast(store.GrantResourceDomain, got, store.GrantLevelOperator) {
		t.Errorf("owner must imply operator")
	}
}

func TestResolve_NoAuthority(t *testing.T) {
	ctx := context.Background()
	r := &fakeReader{}
	p := principal(6, 0)

	got, err := Resolve(ctx, r, p, domainRes("x.example"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "" {
		t.Errorf("Resolve = %q; want empty (default-deny)", got)
	}
}

func TestResolve_FailClosedOnStoreError(t *testing.T) {
	ctx := context.Background()
	r := &fakeReader{err: errors.New("store down")}
	// A non-superadmin principal: resolution must surface the error so the
	// caller denies, never allow.
	p := principal(7, store.PrincipalFlagAdmin)

	_, err := Resolve(ctx, r, p, domainRes("x.example"))
	if err == nil {
		t.Errorf("Resolve returned nil error on store failure; want the error (fail-closed)")
	}
}

func TestAtLeast(t *testing.T) {
	dom := store.GrantResourceDomain
	if !AtLeast(dom, store.GrantLevelOwner, store.GrantLevelOperator) {
		t.Errorf("owner AtLeast operator = false; want true")
	}
	if AtLeast(dom, store.GrantLevelOperator, store.GrantLevelOwner) {
		t.Errorf("operator AtLeast owner = true; want false")
	}
	if AtLeast(dom, store.GrantLevelOperator, "") {
		t.Errorf("AtLeast of zero required = true; want false (default-deny)")
	}
	mbx := store.GrantResourceMailbox
	if !AtLeast(mbx, store.GrantLevelAdmin, store.GrantLevelRead) {
		t.Errorf("mailbox admin AtLeast read = false; want true")
	}
}

func TestOperatorDomains_UnionSortedDeduped(t *testing.T) {
	ctx := context.Background()
	r := &fakeReader{
		grants: map[store.PrincipalID][]store.Grant{
			8: {
				{ResourceKind: store.GrantResourceDomain, ResourceID: "b.example", Level: store.GrantLevelOperator},
				{ResourceKind: store.GrantResourceDomain, ResourceID: "a.example", Level: store.GrantLevelOwner},
				// A duplicate resource under a different provenance must not
				// produce a duplicate entry in the result.
				{ResourceKind: store.GrantResourceDomain, ResourceID: "a.example", Level: store.GrantLevelOperator, Provenance: "idp:acme"},
				// A non-domain grant must be ignored.
				{ResourceKind: store.GrantResourceMailbox, ResourceID: "m1", Level: store.GrantLevelRead},
			},
		},
	}
	p := principal(8, store.PrincipalFlagAdmin)

	got, err := OperatorDomains(ctx, r, p)
	if err != nil {
		t.Fatalf("OperatorDomains: %v", err)
	}
	want := []string{"a.example", "b.example"}
	if len(got) != len(want) {
		t.Fatalf("OperatorDomains = %v; want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("OperatorDomains = %v; want %v", got, want)
		}
	}
}

func TestOperatorDomains_FailClosed(t *testing.T) {
	ctx := context.Background()
	r := &fakeReader{err: errors.New("store down")}
	p := principal(9, store.PrincipalFlagAdmin)

	_, err := OperatorDomains(ctx, r, p)
	if err == nil {
		t.Errorf("OperatorDomains returned nil error on store failure; want the error")
	}
}
