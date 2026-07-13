package e2e

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testfakes/fakeoidc"
	"github.com/hanshuebner/herold/test/e2e/fixtures"
)

// newAutoProvisionRP wires a directoryoidc.RP against st with a real
// internal/directory.Directory as its PrincipalProvisioner -- the exact
// shape internal/admin/server.go wires in production
// (oidc.SetProvisioner(dir)) -- so this test exercises the real
// provisioning path end to end, not a test-only stand-in.
func newAutoProvisionRP(st store.Store) (*directoryoidc.RP, *directory.Directory) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir := directory.New(st.Meta(), logger, fixtures.NewFakeClock(), nil)
	rp := directoryoidc.New(st.Meta(), logger, &http.Client{Timeout: 5 * time.Second}, fixtures.NewFakeClock())
	rp.SetProvisioner(dir)
	return rp, dir
}

// TestPhase230_OIDCAutoProvision_FullFlow drives REQ-AUTH-56's whole
// first-login auto-provisioning flow against the in-tree fake OIDC
// provider (internal/testfakes/fakeoidc) on both storage backends:
//
//  1. An operator enables auto-provisioning for a provider with a target
//     local domain (the per-provider opt-in, off by default).
//  2. An unknown-but-valid subject with a verified email claim signs in.
//     CompleteSignIn provisions a new principal at
//     localpart(email)@domain, links it, and signs the user in; the
//     principal is external-only (no password), carries no admin flags,
//     and has the default mailbox set a normal CreatePrincipal call
//     gets -- usable for mail immediately.
//  3. A second sign-in for the same subject resolves the same principal
//     rather than creating another.
func TestPhase230_OIDCAutoProvision_FullFlow(t *testing.T) {
	fixtures.Run(t, func(t *testing.T, newStore fixtures.BackendFactory) {
		st := fixtures.Prepare(t, newStore)
		ctx := context.Background()

		if err := st.Meta().InsertDomain(ctx, store.Domain{Name: "example.test", IsLocal: true}); err != nil {
			t.Fatalf("insert domain: %v", err)
		}
		rp, _ := newAutoProvisionRP(st)

		stub := fakeoidc.New(t, fakeoidc.Options{ClientID: "herold-client", ClientSecret: "secret"})
		stub.SetIdentity(fakeoidc.Identity{
			Subject:       "first-login-sub",
			Email:         "Grace.Hopper@Example.Test",
			EmailVerified: true,
			Name:          "Grace Hopper",
		})
		providerID, err := rp.AddProvider(ctx, directoryoidc.ProviderConfig{
			Name:                "autoprov",
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
			t.Fatalf("first sign-in (auto-provisioning): %v", err)
		}

		p, err := st.Meta().GetPrincipalByID(ctx, pid1)
		if err != nil {
			t.Fatalf("GetPrincipalByID: %v", err)
		}
		if p.CanonicalEmail != "grace.hopper@example.test" {
			t.Fatalf("CanonicalEmail = %q, want grace.hopper@example.test", p.CanonicalEmail)
		}
		if p.PasswordHash != "" {
			t.Fatalf("auto-provisioned principal has a password set (must be external-only, REQ-AUTH-54)")
		}
		if p.Flags != 0 {
			t.Fatalf("auto-provisioned principal Flags = %d, want 0 -- no admin/superadmin on a first-login provisioning (REQ-AC-69's spirit extends to directory flags too)", p.Flags)
		}
		if len(p.TOTPSecret) != 0 {
			t.Fatalf("auto-provisioned principal has a TOTP secret enrolled")
		}

		// Usable for mail immediately: the standard mailbox set exists.
		inbox, err := st.Meta().GetMailboxByName(ctx, pid1, "INBOX")
		if err != nil {
			t.Fatalf("GetMailboxByName(INBOX): %v", err)
		}
		if inbox.Attributes&store.MailboxAttrInbox == 0 {
			t.Fatalf("INBOX mailbox missing the Inbox special-use attribute: %+v", inbox)
		}

		link, err := st.Meta().LookupOIDCLink(ctx, "autoprov", "first-login-sub")
		if err != nil {
			t.Fatalf("LookupOIDCLink: %v", err)
		}
		if link.PrincipalID != pid1 {
			t.Fatalf("link principal = %d, want %d", link.PrincipalID, pid1)
		}

		// --- Second sign-in resolves the same principal ----
		pid2, err := signInVia(ctx, t, rp, providerID)
		if err != nil {
			t.Fatalf("second sign-in: %v", err)
		}
		if pid2 != pid1 {
			t.Fatalf("second sign-in resolved to %d, want the same principal %d", pid2, pid1)
		}
	})
}

// TestPhase230_OIDCAutoProvision_Disabled_Refused: REQ-AUTH-56's
// off-by-default case, at the acceptance layer and on both backends. An
// unknown subject at a provider with no AutoProvision opt-in is refused
// and no principal is created.
func TestPhase230_OIDCAutoProvision_Disabled_Refused(t *testing.T) {
	fixtures.Run(t, func(t *testing.T, newStore fixtures.BackendFactory) {
		st := fixtures.Prepare(t, newStore)
		ctx := context.Background()
		if err := st.Meta().InsertDomain(ctx, store.Domain{Name: "example.test", IsLocal: true}); err != nil {
			t.Fatalf("insert domain: %v", err)
		}
		rp, _ := newAutoProvisionRP(st)
		stub := fakeoidc.New(t, fakeoidc.Options{ClientID: "herold-client", ClientSecret: "secret"})
		stub.SetIdentity(fakeoidc.Identity{Subject: "unknown-sub", Email: "someone@example.test", EmailVerified: true})
		providerID, err := rp.AddProvider(ctx, directoryoidc.ProviderConfig{
			Name:         "noauto",
			IssuerURL:    stub.IssuerURL(),
			ClientID:     "herold-client",
			ClientSecret: "secret",
			RedirectURL:  "http://localhost/cb",
			// AutoProvision intentionally left false (the default).
		})
		if err != nil {
			t.Fatalf("add provider: %v", err)
		}
		if _, err := signInVia(ctx, t, rp, providerID); !errors.Is(err, directoryoidc.ErrNotFound) {
			t.Fatalf("sign-in with auto-provisioning off = %v, want ErrNotFound", err)
		}
		if _, err := st.Meta().GetPrincipalByEmail(ctx, "someone@example.test"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("a principal was created despite auto-provisioning being off: %v", err)
		}
	})
}

// TestPhase230_OIDCAutoProvision_UnverifiedEmail_Refused: an email claim
// present but not asserted email_verified must never provision or match
// a principal, at the acceptance layer and on both backends.
func TestPhase230_OIDCAutoProvision_UnverifiedEmail_Refused(t *testing.T) {
	fixtures.Run(t, func(t *testing.T, newStore fixtures.BackendFactory) {
		st := fixtures.Prepare(t, newStore)
		ctx := context.Background()
		if err := st.Meta().InsertDomain(ctx, store.Domain{Name: "example.test", IsLocal: true}); err != nil {
			t.Fatalf("insert domain: %v", err)
		}
		rp, _ := newAutoProvisionRP(st)
		stub := fakeoidc.New(t, fakeoidc.Options{ClientID: "herold-client", ClientSecret: "secret"})
		stub.SetIdentity(fakeoidc.Identity{Subject: "unverified-sub", Email: "unverified@example.test", EmailVerified: false})
		providerID, err := rp.AddProvider(ctx, directoryoidc.ProviderConfig{
			Name:                "autoprov",
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
			t.Fatalf("sign-in with unverified email = %v, want ErrAutoProvisionRefused", err)
		}
		if _, err := st.Meta().GetPrincipalByEmail(ctx, "unverified@example.test"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("a principal was created from an unverified email claim: %v", err)
		}
	})
}

// TestPhase230_OIDCAutoProvision_Collision_Refused: the account-
// takeover regression guard at the acceptance layer, on both backends.
// A verified email claim whose derived address already belongs to an
// existing, unlinked local principal is refused rather than silently
// linked onto that principal.
func TestPhase230_OIDCAutoProvision_Collision_Refused(t *testing.T) {
	fixtures.Run(t, func(t *testing.T, newStore fixtures.BackendFactory) {
		st := fixtures.Prepare(t, newStore)
		ctx := context.Background()
		if err := st.Meta().InsertDomain(ctx, store.Domain{Name: "example.test", IsLocal: true}); err != nil {
			t.Fatalf("insert domain: %v", err)
		}
		victim, err := st.Meta().InsertPrincipal(ctx, store.Principal{
			Kind:           store.PrincipalKindUser,
			CanonicalEmail: "victim@example.test",
			PasswordHash:   "$argon2id$fake",
		})
		if err != nil {
			t.Fatalf("insert victim principal: %v", err)
		}
		rp, _ := newAutoProvisionRP(st)
		stub := fakeoidc.New(t, fakeoidc.Options{ClientID: "herold-client", ClientSecret: "secret"})
		stub.SetIdentity(fakeoidc.Identity{Subject: "attacker-sub", Email: "victim@example.test", EmailVerified: true})
		providerID, err := rp.AddProvider(ctx, directoryoidc.ProviderConfig{
			Name:                "autoprov",
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
			t.Fatalf("sign-in colliding with an existing principal = %v, want ErrAutoProvisionRefused", err)
		}
		if _, err := st.Meta().LookupOIDCLink(ctx, "autoprov", "attacker-sub"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("attacker-sub got silently linked to the victim principal: %v", err)
		}
		links, err := st.Meta().ListOIDCLinksByPrincipal(ctx, victim.ID)
		if err != nil {
			t.Fatalf("ListOIDCLinksByPrincipal: %v", err)
		}
		if len(links) != 0 {
			t.Fatalf("victim principal acquired links: %+v", links)
		}
	})
}

// signInVia drives BeginSignIn -> follow /authorize -> CompleteSignIn
// against the fake provider, returning CompleteSignIn's result.
func signInVia(ctx context.Context, t *testing.T, rp *directoryoidc.RP, providerID directoryoidc.ProviderID) (directoryoidc.PrincipalID, error) {
	t.Helper()
	authURL, state, err := rp.BeginSignIn(ctx, providerID)
	if err != nil {
		t.Fatalf("begin signin: %v", err)
	}
	code, gotState := followAuth(t, authURL)
	if gotState != state {
		t.Fatalf("state mismatch: %q vs %q", gotState, state)
	}
	return rp.CompleteSignIn(ctx, state, code)
}
