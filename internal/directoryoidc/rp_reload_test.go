package directoryoidc_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testfakes/fakeoidc"
)

// TestProviderReloadedFromStore models a restart: a second RP over the
// same store has nothing in memory. A provider registered with a secret
// reference is loaded on first use and completes a PKCE-protected
// sign-in; one registered with an inline secret is reported unavailable.
func TestProviderReloadedFromStore(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore(t)
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	idp := fakeoidc.New(t, fakeoidc.Options{ClientID: "herold", ClientSecret: "s3cret", RequirePKCE: true})
	secretFile := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secretFile, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	first := directoryoidc.New(st.Meta(), nil, &http.Client{Timeout: 5 * time.Second}, clk)
	if _, err := first.AddProvider(ctx, directoryoidc.ProviderConfig{
		Name: "byref", IssuerURL: idp.IssuerURL(), ClientID: "herold", ClientSecretRef: "file:" + secretFile,
	}); err != nil {
		t.Fatalf("AddProvider byref: %v", err)
	}
	if _, err := first.AddProvider(ctx, directoryoidc.ProviderConfig{
		Name: "inline", IssuerURL: idp.IssuerURL(), ClientID: "herold", ClientSecret: "s3cret",
	}); err != nil {
		t.Fatalf("AddProvider inline: %v", err)
	}
	row, err := st.Meta().GetOIDCProvider(ctx, "byref")
	if err != nil || row.ClientSecretRef != "file:"+secretFile {
		t.Fatalf("stored ref = %q (%v), want the file reference", row.ClientSecretRef, err)
	}
	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{Kind: store.PrincipalKindUser, CanonicalEmail: "a@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Meta().LinkOIDC(ctx, store.OIDCLink{PrincipalID: p.ID, ProviderName: "byref", Subject: "sub-a"}); err != nil {
		t.Fatal(err)
	}

	restarted := directoryoidc.New(st.Meta(), nil, &http.Client{Timeout: 5 * time.Second}, clk)
	list, err := restarted.ListProviders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	avail := map[string]bool{}
	for _, pr := range list {
		avail[pr.Name] = pr.Available
	}
	if !avail["byref"] || avail["inline"] {
		t.Fatalf("Available = %v, want byref only", avail)
	}

	idp.SetIdentity(fakeoidc.Identity{Subject: "sub-a"})
	authURL, state, err := restarted.BeginSignInAt(ctx, "byref", "https://mail.example.test/api/v1/oidc/callback")
	if err != nil {
		t.Fatalf("BeginSignInAt after restart: %v", err)
	}
	u, _ := url.Parse(authURL)
	if u.Query().Get("code_challenge_method") != "S256" || u.Query().Get("code_challenge") == "" {
		t.Fatalf("authorization URL lacks a PKCE S256 challenge: %s", authURL)
	}
	code, gotState := fakeoidc.FollowAuthorize(t, authURL, "https://mail.example.test/api/v1/oidc/callback")
	if gotState != state {
		t.Fatalf("state %q, want %q", gotState, state)
	}
	pid, err := restarted.CompleteSignIn(ctx, state, code)
	if err != nil || pid != p.ID {
		t.Fatalf("CompleteSignIn = %d, %v; want %d", pid, err, p.ID)
	}

	if _, _, err := restarted.BeginSignInAt(ctx, "inline", "https://mail.example.test/cb"); !errors.Is(err, directoryoidc.ErrProviderUnavailable) {
		t.Fatalf("inline provider after restart: err = %v, want ErrProviderUnavailable", err)
	}
	if _, _, err := restarted.BeginSignInAt(ctx, "missing", "https://mail.example.test/cb"); !errors.Is(err, directoryoidc.ErrNotFound) {
		t.Fatalf("unknown provider: err = %v, want ErrNotFound", err)
	}
}

func TestAddProvider_RejectsBadSecretRef(t *testing.T) {
	ctx := context.Background()
	rp := directoryoidc.New(newFakeStore(t).Meta(), nil, nil, nil)
	for _, ref := range []string{"plain-secret", "file:" + filepath.Join(t.TempDir(), "absent")} {
		_, err := rp.AddProvider(ctx, directoryoidc.ProviderConfig{
			Name: "p", IssuerURL: "https://idp.invalid", ClientID: "c", ClientSecretRef: ref,
		})
		if !errors.Is(err, directoryoidc.ErrInvalidSecretRef) {
			t.Fatalf("ref %q: err = %v, want ErrInvalidSecretRef", ref, err)
		}
	}
}
