package e2e

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testfakes/fakeoidc"
	"github.com/hanshuebner/herold/test/e2e/fixtures"
)

// TestPhase1_OIDCLink_RoundTrip stands up an in-process fake OIDC
// provider (discovery + JWKS + /authorize redirect + /token issuing a
// signed RS256 ID token), registers it with directoryoidc.RP, drives
// BeginLink + CompleteLink, asserts the link is persisted, verifies
// the subsequent sign-in flow resolves to the same principal, then
// exercises Unlink and confirms sign-in now fails with ErrNotFound.
//
// This test exercises the full external-OIDC surface the Phase-1
// admin feature depends on; the admin REST plumbing that fronts it is
// already covered in internal/protoadmin/server_test.go.
func TestPhase1_OIDCLink_RoundTrip(t *testing.T) {
	fixtures.Run(t, func(t *testing.T, newStore fixtures.BackendFactory) {
		st := fixtures.Prepare(t, newStore)
		ctx := context.Background()

		// Seed the directory with one principal; OIDC links target it.
		p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
			Kind:           store.PrincipalKindUser,
			CanonicalEmail: "alice@example.test",
		})
		if err != nil {
			t.Fatalf("insert principal: %v", err)
		}

		stub := fakeoidc.New(t, fakeoidc.Options{ClientID: "herold-client", ClientSecret: "secret"})

		rp := directoryoidc.New(
			st.Meta(),
			slog.New(slog.NewTextHandler(io.Discard, nil)),
			&http.Client{Timeout: 5 * time.Second},
			fixtures.NewFakeClock(),
		)
		providerID, err := rp.AddProvider(ctx, directoryoidc.ProviderConfig{
			Name:         "stub",
			IssuerURL:    stub.IssuerURL(),
			ClientID:     "herold-client",
			ClientSecret: "secret",
			RedirectURL:  "http://localhost/cb",
		})
		if err != nil {
			t.Fatalf("add provider: %v", err)
		}

		// --- Link flow ----
		stub.SetIdentity(fakeoidc.Identity{Subject: "alice-external"})
		authURL, state, err := rp.BeginLink(ctx, p.ID, providerID)
		if err != nil {
			t.Fatalf("begin link: %v", err)
		}
		code, gotState := followAuth(t, authURL)
		if gotState != state {
			t.Fatalf("state mismatch after auth redirect: %q vs %q", gotState, state)
		}
		linkedPID, err := rp.CompleteLink(ctx, state, code)
		if err != nil {
			t.Fatalf("complete link: %v", err)
		}
		if linkedPID != p.ID {
			t.Fatalf("linked pid: got %d want %d", linkedPID, p.ID)
		}

		// --- Sign-in round-trip with the same sub ----
		authURL, state, err = rp.BeginSignIn(ctx, providerID)
		if err != nil {
			t.Fatalf("begin signin: %v", err)
		}
		code, gotState = followAuth(t, authURL)
		if gotState != state {
			t.Fatalf("state mismatch on signin: %q vs %q", gotState, state)
		}
		gotPID, err := rp.CompleteSignIn(ctx, state, code)
		if err != nil {
			t.Fatalf("complete signin: %v", err)
		}
		if gotPID != p.ID {
			t.Fatalf("signin pid: %d want %d", gotPID, p.ID)
		}

		// --- Unlink and verify sign-in now fails ----
		if err := rp.Unlink(ctx, p.ID, providerID); err != nil {
			t.Fatalf("unlink: %v", err)
		}
		authURL, state, err = rp.BeginSignIn(ctx, providerID)
		if err != nil {
			t.Fatalf("begin signin after unlink: %v", err)
		}
		code, gotState = followAuth(t, authURL)
		if gotState != state {
			t.Fatalf("state mismatch after unlink: %q vs %q", gotState, state)
		}
		_, err = rp.CompleteSignIn(ctx, state, code)
		if !errors.Is(err, directoryoidc.ErrNotFound) {
			t.Fatalf("expected ErrNotFound after unlink, got %v", err)
		}
	})
}

// followAuth simulates a user-agent hitting the provider's /authorize
// URL and returns the ?code / ?state values the provider appended to
// the redirect.
func followAuth(t *testing.T, authURL string) (code, state string) {
	t.Helper()
	return fakeoidc.FollowAuthorize(t, authURL, "http://localhost")
}
