package e2e

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/authz"
	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testfakes/fakeoidc"
	"github.com/hanshuebner/herold/test/e2e/fixtures"
)

// TestPhase188_OIDCClaimToGrantMapping drives the full epic #188 flow
// against an in-process fake external IdP (internal/testfakes/fakeoidc,
// the same fake TestPhase1_OIDCLink_RoundTrip uses, here carrying a
// "groups" claim):
//
//  1. An operator (a server:superadmin, so it can author a rule targeting
//     any resource) configures a claim-mapping rule on a provider flagged
//     authz_trusted, mapping the "groups" claim value "list-x-admins" to
//     list:x owner.
//  2. Alice signs in carrying that group claim. CompleteSignIn reconciles
//     her idp:<provider> grants, and authz.Resolve now reports her at
//     list:x owner.
//  3. Alice signs in again with the group claim gone. Reconciliation
//     revokes the stale idp: grant -- authz.Resolve reports no access to
//     list:x -- while a local grant assigned to her directly (never
//     idp:-provenance) survives untouched, proving reconciliation only
//     ever touches the provider's own idp: rows (REQ-AC-61/62).
func TestPhase188_OIDCClaimToGrantMapping(t *testing.T) {
	fixtures.Run(t, func(t *testing.T, newStore fixtures.BackendFactory) {
		st := fixtures.Prepare(t, newStore)
		ctx := context.Background()
		clk := fixtures.NewFakeClock()

		alice, err := st.Meta().InsertPrincipal(ctx, store.Principal{
			Kind:           store.PrincipalKindUser,
			CanonicalEmail: "alice-claimmap@example.test",
		})
		if err != nil {
			t.Fatalf("insert alice: %v", err)
		}
		// The rule author: a server:superadmin, so REQ-AC-68's "author
		// still holds delegable authority" check passes for any resource
		// kind without needing a separate domain/list grant setup.
		operator, err := st.Meta().InsertPrincipal(ctx, store.Principal{
			Kind:           store.PrincipalKindUser,
			CanonicalEmail: "operator-claimmap@example.test",
			Flags:          store.PrincipalFlagAdmin | store.PrincipalFlagSuperAdmin,
		})
		if err != nil {
			t.Fatalf("insert operator: %v", err)
		}

		// A manually-assigned local grant on an unrelated resource. It
		// must survive every reconciliation pass untouched (REQ-AC-61).
		if _, err := st.Meta().InsertGrant(ctx, store.Grant{
			SubjectID:    uint64(alice.ID),
			ResourceKind: store.GrantResourceDomain,
			ResourceID:   "manual.example",
			Level:        store.GrantLevelOwner,
			Provenance:   store.GrantProvenanceLocal,
		}); err != nil {
			t.Fatalf("insert manual grant: %v", err)
		}

		stub := fakeoidc.New(t, fakeoidc.Options{ClientID: "herold-client", ClientSecret: "secret"})
		rp := directoryoidc.New(
			st.Meta(),
			slog.New(slog.NewTextHandler(io.Discard, nil)),
			&http.Client{Timeout: 5 * time.Second},
			clk,
		)
		providerID, err := rp.AddProvider(ctx, directoryoidc.ProviderConfig{
			Name:         "acme",
			IssuerURL:    stub.IssuerURL(),
			ClientID:     "herold-client",
			ClientSecret: "secret",
			RedirectURL:  "http://localhost/cb",
		})
		if err != nil {
			t.Fatalf("add provider: %v", err)
		}

		// Link alice's external identity so sign-in can resolve her.
		stub.SetIdentity(fakeoidc.Identity{Subject: "alice-external-claimmap"})
		authURL, state, err := rp.BeginLink(ctx, alice.ID, providerID)
		if err != nil {
			t.Fatalf("begin link: %v", err)
		}
		code, gotState := followAuth(t, authURL)
		if gotState != state {
			t.Fatalf("state mismatch: %q vs %q", gotState, state)
		}
		if _, err := rp.CompleteLink(ctx, state, code); err != nil {
			t.Fatalf("complete link: %v", err)
		}

		// The mapping is inert until the provider is flagged authz_trusted
		// by a superadmin (REQ-AC-66) -- verify that gate before flipping
		// it, so a regression that skips the check would be caught here.
		if err := st.Meta().InsertClaimAllowlistEntry(ctx, "acme", "groups"); err != nil {
			t.Fatalf("insert allowlist entry: %v", err)
		}
		rule, err := st.Meta().InsertClaimMappingRule(ctx, store.ClaimMappingRule{
			ProviderName: "acme",
			Claim:        "groups",
			MatchValue:   "list-x-admins",
			ResourceKind: store.GrantResourceList,
			ResourceID:   "x",
			Level:        store.GrantLevelOwner,
			CreatedBy:    operator.ID,
		})
		if err != nil {
			t.Fatalf("insert claim mapping rule: %v", err)
		}

		signIn := func(groups []string) {
			t.Helper()
			stub.SetIdentity(fakeoidc.Identity{
				Subject: "alice-external-claimmap",
				Extra:   map[string]any{"groups": groups},
			})
			authURL, state, err := rp.BeginSignIn(ctx, providerID)
			if err != nil {
				t.Fatalf("begin signin: %v", err)
			}
			code, gotState := followAuth(t, authURL)
			if gotState != state {
				t.Fatalf("state mismatch on signin: %q vs %q", gotState, state)
			}
			pid, err := rp.CompleteSignIn(ctx, state, code)
			if err != nil {
				t.Fatalf("complete signin: %v", err)
			}
			if pid != alice.ID {
				t.Fatalf("signin resolved to %d; want alice %d", pid, alice.ID)
			}
		}

		resolve := func(res authz.Resource) store.GrantLevel {
			t.Helper()
			p, err := st.Meta().GetPrincipalByID(ctx, alice.ID)
			if err != nil {
				t.Fatalf("GetPrincipalByID: %v", err)
			}
			lvl, err := authz.Resolve(ctx, st.Meta(), p, res)
			if err != nil {
				t.Fatalf("authz.Resolve: %v", err)
			}
			return lvl
		}

		listX := authz.Resource{Kind: store.GrantResourceList, ID: "x"}
		manualDomain := authz.Resource{Kind: store.GrantResourceDomain, ID: "manual.example"}

		// --- Sign-in #1, provider not yet authz_trusted: mapping is inert ---
		signIn([]string{"list-x-admins"})
		if got := resolve(listX); got != "" {
			t.Fatalf("list:x resolved to %q before authz_trusted; want no access (REQ-AC-66)", got)
		}

		// --- Flip authz_trusted; re-run the same login ---
		if err := st.Meta().SetOIDCProviderAuthzTrusted(ctx, "acme", true); err != nil {
			t.Fatalf("SetOIDCProviderAuthzTrusted: %v", err)
		}
		signIn([]string{"list-x-admins", "some-other-group"})
		if got := resolve(listX); got != store.GrantLevelOwner {
			t.Fatalf("list:x resolved to %q after matching login; want owner", got)
		}
		if got := resolve(manualDomain); got != store.GrantLevelOwner {
			t.Fatalf("manual.example resolved to %q; want owner (local grant must be visible throughout)", got)
		}

		// --- Sign-in #2: the group claim is gone. The idp: grant is
		// revoked; the manual grant is untouched. ---
		signIn([]string{"some-other-group"})
		if got := resolve(listX); got != "" {
			t.Fatalf("list:x resolved to %q after losing the claim; want no access (REQ-AC-62/63a)", got)
		}
		if got := resolve(manualDomain); got != store.GrantLevelOwner {
			t.Fatalf("manual.example resolved to %q after reconciliation; want owner unchanged (REQ-AC-61)", got)
		}

		// --- Unlink removes any surviving idp: grants immediately
		// (REQ-AC-63). Re-grant list:x first to prove Unlink -- not just
		// the absence of a matching claim -- is what clears it. ---
		signIn([]string{"list-x-admins"})
		if got := resolve(listX); got != store.GrantLevelOwner {
			t.Fatalf("list:x resolved to %q before unlink; want owner", got)
		}
		if err := rp.Unlink(ctx, alice.ID, providerID); err != nil {
			t.Fatalf("unlink: %v", err)
		}
		if got := resolve(listX); got != "" {
			t.Fatalf("list:x resolved to %q after unlink; want no access (REQ-AC-63)", got)
		}
		if got := resolve(manualDomain); got != store.GrantLevelOwner {
			t.Fatalf("manual.example resolved to %q after unlink; want owner unchanged", got)
		}

		// The rule itself is still on record (deleting it is a separate,
		// admin-driven operation not exercised by this login-driven flow).
		rules, err := st.Meta().ListClaimMappingRules(ctx, "acme")
		if err != nil {
			t.Fatalf("ListClaimMappingRules: %v", err)
		}
		if len(rules) != 1 || rules[0].ID != rule.ID {
			t.Fatalf("ListClaimMappingRules = %+v; want the one rule created above", rules)
		}
	})
}
