package directoryoidc_test

// subaccount_auth_test.go proves REQ-SUBACCT-02's follow-up for the two
// directoryoidc auth-time seams the coordinator's verification flagged:
// RP.CompleteSignIn (the OIDC sign-in leg -- reached both directly, per
// REQ-AUTH-50+, and via the OAuth2 authorization-code grant's federated
// leg, issue #238) and RP.VerifyAccessToken (SASL OAUTHBEARER/XOAUTH2
// for IMAP / SMTP submission, internal/sasl/oauth.go). Both link a
// sub-principal to a provider directly at the store layer -- the shape
// an admin-created OIDC link (handleBeginOIDCLink) would produce if its
// own creation-time guard were ever bypassed -- and drive the real
// provider-token round trip, so the assertion proves the auth-time Kind
// check independent of the creation-time guard covered elsewhere.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/store"
)

// mustInsertSubPrincipal creates an individual-kind parent and a
// sub-principal owned by it, returning the sub-principal.
func mustInsertSubPrincipal(t *testing.T, fs store.Store, email string) store.Principal {
	t.Helper()
	ctx := context.Background()
	parent, err := fs.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "parent-of-" + email,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal(parent): %v", err)
	}
	sub, err := fs.Meta().InsertSubPrincipal(ctx, parent.ID, store.Principal{
		CanonicalEmail: email,
	})
	if err != nil {
		t.Fatalf("InsertSubPrincipal: %v", err)
	}
	return sub
}

// TestCompleteSignIn_RejectsSubPrincipal drives a full OIDC sign-in
// round trip (BeginSignIn -> provider redirect -> CompleteSignIn)
// against a subject linked to a sub-principal and asserts rejection.
func TestCompleteSignIn_RejectsSubPrincipal(t *testing.T) {
	stub := newOIDCStub(t, "herold-client")
	fs := newFakeStore(t)
	ctx := context.Background()
	sub := mustInsertSubPrincipal(t, fs, "subsignin@example.test")

	rp := directoryoidc.New(fs.Meta(), slog.New(slog.NewTextHandler(io.Discard, nil)), &http.Client{Timeout: 5 * time.Second}, clock.NewReal())
	if _, err := rp.AddProvider(ctx, directoryoidc.ProviderConfig{
		Name:        "test",
		IssuerURL:   stub.issuer,
		ClientID:    "herold-client",
		RedirectURL: "http://localhost/cb",
	}); err != nil {
		t.Fatalf("add provider: %v", err)
	}
	if err := fs.Meta().LinkOIDC(ctx, store.OIDCLink{
		PrincipalID:  sub.ID,
		ProviderName: "test",
		Subject:      "sub-principal-subject",
	}); err != nil {
		t.Fatalf("LinkOIDC(sub-principal): %v", err)
	}

	stub.subject = "sub-principal-subject"
	authURL, state, err := rp.BeginSignIn(ctx, "test")
	if err != nil {
		t.Fatalf("BeginSignIn: %v", err)
	}
	code, gotState := followAuth(t, authURL)
	if gotState != state {
		t.Fatalf("state mismatch: %q vs %q", gotState, state)
	}
	if _, err := rp.CompleteSignIn(ctx, state, code); !errors.Is(err, directoryoidc.ErrNotFound) {
		t.Fatalf("CompleteSignIn(sub-principal) = %v, want ErrNotFound", err)
	}
}

// TestVerifyAccessToken_RejectsSubPrincipal presents a validly-signed
// access token whose linked principal is a sub-account (the SASL
// OAUTHBEARER/XOAUTH2 path, internal/sasl/oauth.go) and asserts
// rejection.
func TestVerifyAccessToken_RejectsSubPrincipal(t *testing.T) {
	stub := newOIDCStub(t, "herold-client")
	fs := newFakeStore(t)
	ctx := context.Background()
	sub := mustInsertSubPrincipal(t, fs, "subtoken@example.test")

	rp := directoryoidc.New(fs.Meta(), slog.New(slog.NewTextHandler(io.Discard, nil)), &http.Client{Timeout: 5 * time.Second}, clock.NewReal())
	if _, err := rp.AddProvider(ctx, directoryoidc.ProviderConfig{
		Name:        "test",
		IssuerURL:   stub.issuer,
		ClientID:    "herold-client",
		RedirectURL: "http://localhost/cb",
	}); err != nil {
		t.Fatalf("add provider: %v", err)
	}
	if err := fs.Meta().LinkOIDC(ctx, store.OIDCLink{
		PrincipalID:  sub.ID,
		ProviderName: "test",
		Subject:      "sub-principal-token-subject",
	}); err != nil {
		t.Fatalf("LinkOIDC(sub-principal): %v", err)
	}

	stub.subject = "sub-principal-token-subject"
	stub.nonce = "" // VerifyAccessToken does not enforce nonce on the access-token path.
	token, err := stub.signIDToken()
	if err != nil {
		t.Fatalf("signIDToken: %v", err)
	}
	if _, err := rp.VerifyAccessToken(ctx, "test", token); !errors.Is(err, directoryoidc.ErrNotFound) {
		t.Fatalf("VerifyAccessToken(sub-principal) = %v, want ErrNotFound", err)
	}
}
