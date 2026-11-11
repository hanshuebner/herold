package directoryoidc_test

// rp_idtoken_test.go: sign-in is refused for every malformed ID token a
// hostile or misconfigured provider can mint (wrong nonce, audience,
// issuer, signing algorithm), and the PKCE verifier is bound to the flow
// that created it. Each refusal is paired with a well-formed control
// login on the same fixture so a refusal cannot come from broken setup.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testfakes/fakeoidc"
)

const idTokenRedirect = "https://mail.example.test/api/v1/oidc/callback"

type idTokenEnv struct {
	rp  *directoryoidc.RP
	idp *fakeoidc.Server
	pid store.PrincipalID
}

func newIDTokenEnv(t *testing.T) *idTokenEnv {
	t.Helper()
	ctx := context.Background()
	st := newFakeStore(t)
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	idp := fakeoidc.New(t, fakeoidc.Options{ClientID: "herold", ClientSecret: "s3cret", RequirePKCE: true})
	rp := directoryoidc.New(st.Meta(), nil, &http.Client{Timeout: 5 * time.Second}, clk)
	if _, err := rp.AddProvider(ctx, directoryoidc.ProviderConfig{
		Name: "idp", IssuerURL: idp.IssuerURL(), ClientID: "herold", ClientSecret: "s3cret",
	}); err != nil {
		t.Fatalf("AddProvider: %v", err)
	}
	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{Kind: store.PrincipalKindUser, CanonicalEmail: "a@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Meta().LinkOIDC(ctx, store.OIDCLink{PrincipalID: p.ID, ProviderName: "idp", Subject: "sub-a"}); err != nil {
		t.Fatal(err)
	}
	idp.SetIdentity(fakeoidc.Identity{Subject: "sub-a"})
	return &idTokenEnv{rp: rp, idp: idp, pid: p.ID}
}

// signIn runs one complete sign-in and returns CompleteSignIn's result.
func (e *idTokenEnv) signIn(t *testing.T) (directoryoidc.PrincipalID, error) {
	t.Helper()
	ctx := context.Background()
	authURL, state, err := e.rp.BeginSignInAt(ctx, "idp", idTokenRedirect)
	if err != nil {
		t.Fatalf("BeginSignInAt: %v", err)
	}
	code, _ := fakeoidc.FollowAuthorize(t, authURL, idTokenRedirect)
	return e.rp.CompleteSignIn(ctx, state, code)
}

func TestCompleteSignIn_RefusesMalformedIDTokens(t *testing.T) {
	cases := []struct {
		name    string
		fault   fakeoidc.Fault
		wantErr string
		invalid bool // want ErrInvalidState
	}{
		{name: "nonce mismatch", fault: fakeoidc.Fault{WrongNonce: true}, invalid: true},
		{name: "wrong audience", fault: fakeoidc.Fault{WrongAudience: true}, wantErr: "audience"},
		{name: "wrong issuer", fault: fakeoidc.Fault{WrongIssuer: true}, wantErr: "issued by a different provider"},
		{name: "alg none", fault: fakeoidc.Fault{Alg: "none"}, wantErr: "unexpected signature algorithm"},
		{name: "alg HS256 confusion", fault: fakeoidc.Fault{Alg: "HS256"}, wantErr: "unexpected signature algorithm"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newIDTokenEnv(t)
			if pid, err := e.signIn(t); err != nil || pid != e.pid {
				t.Fatalf("control sign-in = %d, %v; want %d", pid, err, e.pid)
			}
			e.idp.SetFault(tc.fault)
			pid, err := e.signIn(t)
			if err == nil {
				t.Fatalf("sign-in with a %s ID token succeeded as principal %d", tc.name, pid)
			}
			if pid != 0 {
				t.Fatalf("refused sign-in still returned principal %d", pid)
			}
			if tc.invalid && !errors.Is(err, directoryoidc.ErrInvalidState) {
				t.Fatalf("err = %v, want ErrInvalidState", err)
			}
			if tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

// TestCompleteSignIn_PKCEVerifierBoundToFlow: a code minted for flow A,
// presented with flow B's state, fails at the token endpoint because B's
// verifier does not match A's challenge.
func TestCompleteSignIn_PKCEVerifierBoundToFlow(t *testing.T) {
	e := newIDTokenEnv(t)
	ctx := context.Background()
	authA, _, err := e.rp.BeginSignInAt(ctx, "idp", idTokenRedirect)
	if err != nil {
		t.Fatal(err)
	}
	_, stateB, err := e.rp.BeginSignInAt(ctx, "idp", idTokenRedirect)
	if err != nil {
		t.Fatal(err)
	}
	codeA, _ := fakeoidc.FollowAuthorize(t, authA, idTokenRedirect)
	pid, err := e.rp.CompleteSignIn(ctx, stateB, codeA)
	if err == nil || pid != 0 {
		t.Fatalf("cross-flow code accepted: pid=%d err=%v", pid, err)
	}
	if !strings.Contains(err.Error(), "code_verifier") {
		t.Fatalf("err = %v, want the provider's code_verifier rejection", err)
	}
}
