package directoryoidc_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/authz"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testfakes/fakeoidc"
)

// newAutoProvisionRP wires an RP against a fresh store with dir as its
// PrincipalProvisioner, mirroring internal/admin/server.go's production
// wiring (oidc.SetProvisioner(dir)).
func newAutoProvisionRP(t *testing.T) (rp *directoryoidc.RP, fs store.Store, dir *directory.Directory) {
	t.Helper()
	fs = newFakeStore(t)
	dir = directory.New(fs.Meta(), slog.New(slog.NewTextHandler(io.Discard, nil)), clock.NewReal(), nil)
	rp = directoryoidc.New(fs.Meta(), slog.New(slog.NewTextHandler(io.Discard, nil)), &http.Client{Timeout: 5 * time.Second}, clock.NewReal())
	rp.SetProvisioner(dir)
	return rp, fs, dir
}

// signInVia drives BeginSignIn -> follow /authorize -> CompleteSignIn
// against stub, returning CompleteSignIn's result.
func signInVia(ctx context.Context, t *testing.T, rp *directoryoidc.RP, providerID directoryoidc.ProviderID) (directoryoidc.PrincipalID, error) {
	t.Helper()
	authURL, state, err := rp.BeginSignIn(ctx, providerID)
	if err != nil {
		t.Fatalf("begin signin: %v", err)
	}
	code, gotState := fakeoidc.FollowAuthorize(t, authURL, "http://localhost")
	if gotState != state {
		t.Fatalf("state mismatch: %q vs %q", gotState, state)
	}
	return rp.CompleteSignIn(ctx, state, code)
}

func addTestDomain(t *testing.T, fs store.Store, name string) {
	t.Helper()
	if err := fs.Meta().InsertDomain(context.Background(), store.Domain{Name: name, IsLocal: true}); err != nil {
		t.Fatalf("insert domain %q: %v", name, err)
	}
}

// TestCompleteSignIn_AutoProvision_Disabled: REQ-AUTH-56's off-by-default
// case. An unknown sub at a provider with AutoProvision=false is
// refused with ErrNotFound (indistinguishable from "no such user") and
// no principal is created.
func TestCompleteSignIn_AutoProvision_Disabled(t *testing.T) {
	rp, fs, _ := newAutoProvisionRP(t)
	ctx := context.Background()
	addTestDomain(t, fs, "example.test")
	stub := fakeoidc.New(t, fakeoidc.Options{ClientID: "herold-client", ClientSecret: "secret"})
	stub.SetIdentity(fakeoidc.Identity{Subject: "unknown-sub", Email: "new@example.test", EmailVerified: true})
	providerID, err := rp.AddProvider(ctx, directoryoidc.ProviderConfig{
		Name:         "prov",
		IssuerURL:    stub.IssuerURL(),
		ClientID:     "herold-client",
		ClientSecret: "secret",
		RedirectURL:  "http://localhost/cb",
		// AutoProvision intentionally left false.
	})
	if err != nil {
		t.Fatalf("add provider: %v", err)
	}
	if _, err := signInVia(ctx, t, rp, providerID); !errors.Is(err, directoryoidc.ErrNotFound) {
		t.Fatalf("signin with AutoProvision=false = %v, want ErrNotFound", err)
	}
	if _, err := fs.Meta().GetPrincipalByEmail(ctx, "new@example.test"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a principal was created despite AutoProvision=false: %v", err)
	}
}

// TestCompleteSignIn_AutoProvision_NotWired: the provider opted in but
// SetProvisioner was never called (an operator misconfiguration this
// project's own production wiring should never hit, but a defensive
// unit test all the same) -- fails closed with ErrNotFound, same as the
// disabled case, never a panic or a signed-in-with-no-principal state.
func TestCompleteSignIn_AutoProvision_NotWired(t *testing.T) {
	fs := newFakeStore(t)
	rp := directoryoidc.New(fs.Meta(), slog.New(slog.NewTextHandler(io.Discard, nil)), &http.Client{Timeout: 5 * time.Second}, clock.NewReal())
	// Deliberately no rp.SetProvisioner call.
	ctx := context.Background()
	if err := fs.Meta().InsertDomain(ctx, store.Domain{Name: "example.test", IsLocal: true}); err != nil {
		t.Fatalf("insert domain: %v", err)
	}
	stub := fakeoidc.New(t, fakeoidc.Options{ClientID: "herold-client", ClientSecret: "secret"})
	stub.SetIdentity(fakeoidc.Identity{Subject: "unknown-sub", Email: "new@example.test", EmailVerified: true})
	providerID, err := rp.AddProvider(ctx, directoryoidc.ProviderConfig{
		Name:                "prov",
		IssuerURL:           stub.IssuerURL(),
		ClientID:            "herold-client",
		ClientSecret:        "secret",
		RedirectURL:         "http://localhost/cb",
		AutoProvision:       true,
		AutoProvisionDomain: "example.test",
	})
	if err != nil {
		t.Fatalf("add provider: %v", err)
	}
	if _, err := signInVia(ctx, t, rp, providerID); !errors.Is(err, directoryoidc.ErrNotFound) {
		t.Fatalf("signin with no provisioner wired = %v, want ErrNotFound", err)
	}
}

// TestCompleteSignIn_AutoProvision_UnverifiedEmail: an email claim that
// is present but not asserted email_verified must never provision or
// match a principal (REQ #230's account-takeover guard -- trusting an
// unverified claim would let any subject at the IdP claim any address).
func TestCompleteSignIn_AutoProvision_UnverifiedEmail(t *testing.T) {
	rp, fs, _ := newAutoProvisionRP(t)
	ctx := context.Background()
	addTestDomain(t, fs, "example.test")
	stub := fakeoidc.New(t, fakeoidc.Options{ClientID: "herold-client", ClientSecret: "secret"})
	stub.SetIdentity(fakeoidc.Identity{Subject: "unverified-sub", Email: "unverified@example.test", EmailVerified: false})
	providerID, err := rp.AddProvider(ctx, directoryoidc.ProviderConfig{
		Name:                "prov",
		IssuerURL:           stub.IssuerURL(),
		ClientID:            "herold-client",
		ClientSecret:        "secret",
		RedirectURL:         "http://localhost/cb",
		AutoProvision:       true,
		AutoProvisionDomain: "example.test",
	})
	if err != nil {
		t.Fatalf("add provider: %v", err)
	}
	if _, err := signInVia(ctx, t, rp, providerID); !errors.Is(err, directoryoidc.ErrAutoProvisionRefused) {
		t.Fatalf("signin with unverified email = %v, want ErrAutoProvisionRefused", err)
	}
	if _, err := fs.Meta().GetPrincipalByEmail(ctx, "unverified@example.test"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a principal was created from an unverified email claim: %v", err)
	}

	// A missing email claim entirely is refused the same way.
	stub.SetIdentity(fakeoidc.Identity{Subject: "no-email-sub"})
	if _, err := signInVia(ctx, t, rp, providerID); !errors.Is(err, directoryoidc.ErrAutoProvisionRefused) {
		t.Fatalf("signin with no email claim = %v, want ErrAutoProvisionRefused", err)
	}
}

// TestCompleteSignIn_AutoProvision_NoDomainConfigured: AutoProvision=true
// with AutoProvisionDomain left empty is "opted in but not configured"
// and must fail closed rather than defaulting to some domain.
func TestCompleteSignIn_AutoProvision_NoDomainConfigured(t *testing.T) {
	rp, fs, _ := newAutoProvisionRP(t)
	ctx := context.Background()
	addTestDomain(t, fs, "example.test")
	stub := fakeoidc.New(t, fakeoidc.Options{ClientID: "herold-client", ClientSecret: "secret"})
	stub.SetIdentity(fakeoidc.Identity{Subject: "sub-nodomain", Email: "nodomain@example.test", EmailVerified: true})
	providerID, err := rp.AddProvider(ctx, directoryoidc.ProviderConfig{
		Name:          "prov",
		IssuerURL:     stub.IssuerURL(),
		ClientID:      "herold-client",
		ClientSecret:  "secret",
		RedirectURL:   "http://localhost/cb",
		AutoProvision: true,
		// AutoProvisionDomain intentionally left empty.
	})
	if err != nil {
		t.Fatalf("add provider: %v", err)
	}
	if _, err := signInVia(ctx, t, rp, providerID); !errors.Is(err, directoryoidc.ErrAutoProvisionRefused) {
		t.Fatalf("signin with no AutoProvisionDomain = %v, want ErrAutoProvisionRefused", err)
	}
}

// TestCompleteSignIn_AutoProvision_CollisionRefused is the account-
// takeover regression guard: a verified email claim whose derived
// address already belongs to an existing, unlinked local principal must
// be refused, not silently attached to that principal.
func TestCompleteSignIn_AutoProvision_CollisionRefused(t *testing.T) {
	rp, fs, _ := newAutoProvisionRP(t)
	ctx := context.Background()
	addTestDomain(t, fs, "example.test")
	existing, err := fs.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "victim@example.test",
		PasswordHash:   "$argon2id$fake",
	})
	if err != nil {
		t.Fatalf("insert existing principal: %v", err)
	}
	stub := fakeoidc.New(t, fakeoidc.Options{ClientID: "herold-client", ClientSecret: "secret"})
	stub.SetIdentity(fakeoidc.Identity{Subject: "attacker-sub", Email: "victim@example.test", EmailVerified: true})
	providerID, err := rp.AddProvider(ctx, directoryoidc.ProviderConfig{
		Name:                "prov",
		IssuerURL:           stub.IssuerURL(),
		ClientID:            "herold-client",
		ClientSecret:        "secret",
		RedirectURL:         "http://localhost/cb",
		AutoProvision:       true,
		AutoProvisionDomain: "example.test",
	})
	if err != nil {
		t.Fatalf("add provider: %v", err)
	}
	if _, err := signInVia(ctx, t, rp, providerID); !errors.Is(err, directoryoidc.ErrAutoProvisionRefused) {
		t.Fatalf("signin colliding with an existing principal = %v, want ErrAutoProvisionRefused", err)
	}
	// The existing principal must not have gained an OIDC link.
	if _, err := fs.Meta().LookupOIDCLink(ctx, "prov", "attacker-sub"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("attacker-sub got silently linked to the victim principal: %v", err)
	}
	links, err := fs.Meta().ListOIDCLinksByPrincipal(ctx, existing.ID)
	if err != nil {
		t.Fatalf("ListOIDCLinksByPrincipal: %v", err)
	}
	if len(links) != 0 {
		t.Fatalf("victim principal acquired links: %+v", links)
	}
}

// TestCompleteSignIn_AutoProvision_AliasCollisionRefused: the derived
// address resolving via an alias (rather than a principal's own
// canonical email) is the same account-takeover shape and is refused
// the same way.
func TestCompleteSignIn_AutoProvision_AliasCollisionRefused(t *testing.T) {
	rp, fs, _ := newAutoProvisionRP(t)
	ctx := context.Background()
	addTestDomain(t, fs, "example.test")
	target, err := fs.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "realmailbox@example.test",
		PasswordHash:   "$argon2id$fake",
	})
	if err != nil {
		t.Fatalf("insert target principal: %v", err)
	}
	if _, err := fs.Meta().InsertAlias(ctx, store.Alias{
		LocalPart:       "alias-victim",
		Domain:          "example.test",
		TargetPrincipal: target.ID,
	}); err != nil {
		t.Fatalf("insert alias: %v", err)
	}
	stub := fakeoidc.New(t, fakeoidc.Options{ClientID: "herold-client", ClientSecret: "secret"})
	stub.SetIdentity(fakeoidc.Identity{Subject: "attacker-sub-2", Email: "alias-victim@example.test", EmailVerified: true})
	providerID, err := rp.AddProvider(ctx, directoryoidc.ProviderConfig{
		Name:                "prov",
		IssuerURL:           stub.IssuerURL(),
		ClientID:            "herold-client",
		ClientSecret:        "secret",
		RedirectURL:         "http://localhost/cb",
		AutoProvision:       true,
		AutoProvisionDomain: "example.test",
	})
	if err != nil {
		t.Fatalf("add provider: %v", err)
	}
	if _, err := signInVia(ctx, t, rp, providerID); !errors.Is(err, directoryoidc.ErrAutoProvisionRefused) {
		t.Fatalf("signin colliding with an alias = %v, want ErrAutoProvisionRefused", err)
	}
}

// TestCompleteSignIn_AutoProvision_HappyPath_SecondSignInResolvesSame is
// the REQ-AUTH-56 acceptance path at the directoryoidc unit level: a
// first sign-in for an unknown, verified sub provisions and links a
// principal at localpart(email)@AutoProvisionDomain with no password and
// no elevated flags (REQ-AC-69 -- see the companion test below for the
// claim-mapping angle); a second sign-in for the same sub resolves that
// same principal rather than creating another.
func TestCompleteSignIn_AutoProvision_HappyPath_SecondSignInResolvesSame(t *testing.T) {
	rp, fs, _ := newAutoProvisionRP(t)
	ctx := context.Background()
	addTestDomain(t, fs, "example.test")
	stub := fakeoidc.New(t, fakeoidc.Options{ClientID: "herold-client", ClientSecret: "secret"})
	stub.SetIdentity(fakeoidc.Identity{
		Subject:       "new-user-sub",
		Email:         "New.User@Example.Test", // mixed case exercises canonicalization
		EmailVerified: true,
		Name:          "New User",
	})
	providerID, err := rp.AddProvider(ctx, directoryoidc.ProviderConfig{
		Name:                "prov",
		IssuerURL:           stub.IssuerURL(),
		ClientID:            "herold-client",
		ClientSecret:        "secret",
		RedirectURL:         "http://localhost/cb",
		AutoProvision:       true,
		AutoProvisionDomain: "example.test",
	})
	if err != nil {
		t.Fatalf("add provider: %v", err)
	}

	pid1, err := signInVia(ctx, t, rp, providerID)
	if err != nil {
		t.Fatalf("first signin: %v", err)
	}
	p, err := fs.Meta().GetPrincipalByID(ctx, pid1)
	if err != nil {
		t.Fatalf("GetPrincipalByID: %v", err)
	}
	if p.CanonicalEmail != "new.user@example.test" {
		t.Fatalf("CanonicalEmail = %q, want new.user@example.test", p.CanonicalEmail)
	}
	if p.PasswordHash != "" {
		t.Fatalf("auto-provisioned principal has a PasswordHash set (must be external-only, REQ-AUTH-54)")
	}
	if p.Flags != 0 {
		t.Fatalf("auto-provisioned principal Flags = %d, want 0 (no admin/superadmin)", p.Flags)
	}
	if len(p.TOTPSecret) != 0 {
		t.Fatalf("auto-provisioned principal has a TOTP secret enrolled")
	}
	if p.DisplayName != "New User" {
		t.Fatalf("DisplayName = %q, want %q (from the name claim)", p.DisplayName, "New User")
	}
	// Default mailboxes were provisioned -- the account is usable for
	// mail immediately, not just a bare directory row.
	ab, err := fs.Meta().DefaultAddressBook(ctx, pid1)
	if err != nil {
		t.Fatalf("DefaultAddressBook: %v", err)
	}
	if ab.Name != "Personal" {
		t.Fatalf("default address book = %+v", ab)
	}

	link, err := fs.Meta().LookupOIDCLink(ctx, "prov", "new-user-sub")
	if err != nil {
		t.Fatalf("LookupOIDCLink after provisioning: %v", err)
	}
	if link.PrincipalID != pid1 {
		t.Fatalf("link principal = %d, want %d", link.PrincipalID, pid1)
	}

	// Second sign-in for the same sub resolves the same principal --
	// it must not re-enter auto-provisioning or create a second one.
	pid2, err := signInVia(ctx, t, rp, providerID)
	if err != nil {
		t.Fatalf("second signin: %v", err)
	}
	if pid2 != pid1 {
		t.Fatalf("second signin resolved to %d, want the same principal %d", pid2, pid1)
	}
	all, err := fs.Meta().ListPrincipals(ctx, 0, 100)
	if err != nil {
		t.Fatalf("ListPrincipals: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("ListPrincipals after two sign-ins = %d principals, want 1", len(all))
	}
}

// TestCompleteSignIn_AutoProvision_REQAC69_WithholdsAdminGrant is the
// REQ-AC-69 regression guard this ticket supplies the live caller for
// (epic #188's ReconcileIdP already implements and unit-tests the bar;
// this proves CompleteSignIn's auto-provisioning path actually invokes
// it with isNewPrincipal=true). A claim-mapping rule that would confer
// domain:operator is withheld on the provisioning login and applied on
// the very next, non-provisioning login for the same principal.
func TestCompleteSignIn_AutoProvision_REQAC69_WithholdsAdminGrant(t *testing.T) {
	rp, fs, _ := newAutoProvisionRP(t)
	ctx := context.Background()
	addTestDomain(t, fs, "example.test")

	operator, err := fs.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "rule-author@example.test",
		Flags:          store.PrincipalFlagAdmin | store.PrincipalFlagSuperAdmin,
	})
	if err != nil {
		t.Fatalf("insert operator: %v", err)
	}

	stub := fakeoidc.New(t, fakeoidc.Options{ClientID: "herold-client", ClientSecret: "secret"})
	stub.SetIdentity(fakeoidc.Identity{
		Subject:       "provisioning-sub",
		Email:         "provisioning@example.test",
		EmailVerified: true,
		Extra:         map[string]any{"groups": []string{"ops-admins"}},
	})
	providerID, err := rp.AddProvider(ctx, directoryoidc.ProviderConfig{
		Name:                "acme",
		IssuerURL:           stub.IssuerURL(),
		ClientID:            "herold-client",
		ClientSecret:        "secret",
		RedirectURL:         "http://localhost/cb",
		AutoProvision:       true,
		AutoProvisionDomain: "example.test",
	})
	if err != nil {
		t.Fatalf("add provider: %v", err)
	}
	if err := fs.Meta().SetOIDCProviderAuthzTrusted(ctx, "acme", true); err != nil {
		t.Fatalf("SetOIDCProviderAuthzTrusted: %v", err)
	}
	if err := fs.Meta().InsertClaimAllowlistEntry(ctx, "acme", "groups"); err != nil {
		t.Fatalf("InsertClaimAllowlistEntry: %v", err)
	}
	if _, err := fs.Meta().InsertClaimMappingRule(ctx, store.ClaimMappingRule{
		ProviderName: "acme",
		Claim:        "groups",
		MatchValue:   "ops-admins",
		ResourceKind: store.GrantResourceDomain,
		ResourceID:   "example.test",
		Level:        store.GrantLevelOperator,
		CreatedBy:    operator.ID,
	}); err != nil {
		t.Fatalf("InsertClaimMappingRule: %v", err)
	}

	pid, err := signInVia(ctx, t, rp, providerID)
	if err != nil {
		t.Fatalf("provisioning signin: %v", err)
	}

	resolve := func() store.GrantLevel {
		t.Helper()
		p, err := fs.Meta().GetPrincipalByID(ctx, pid)
		if err != nil {
			t.Fatalf("GetPrincipalByID: %v", err)
		}
		lvl, err := authz.Resolve(ctx, fs.Meta(), p, authz.Resource{Kind: store.GrantResourceDomain, ID: "example.test"})
		if err != nil {
			t.Fatalf("authz.Resolve: %v", err)
		}
		return lvl
	}

	// REQ-AC-69: withheld on the provisioning login itself.
	if got := resolve(); got != "" {
		t.Fatalf("domain:operator resolved to %q on the provisioning login; want no access (REQ-AC-69)", got)
	}

	// The very next (non-provisioning) login for the same sub applies it.
	if _, err := signInVia(ctx, t, rp, providerID); err != nil {
		t.Fatalf("second signin: %v", err)
	}
	if got := resolve(); got != store.GrantLevelOperator {
		t.Fatalf("domain:operator resolved to %q after the second login; want operator", got)
	}
}
