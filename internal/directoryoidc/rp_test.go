package directoryoidc_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
)

// oidcStub is a minimal OIDC provider that speaks just enough of the
// spec to satisfy go-oidc's NewProvider + Verifier flow. It signs RS256
// ID tokens with a test key.
type oidcStub struct {
	t        *testing.T
	srv      *httptest.Server
	issuer   string
	key      *rsa.PrivateKey
	kid      string
	clientID string

	// subject returned for the next token exchange.
	subject string
	// nonce echoed back verbatim. The caller sets this before
	// driving the flow so Verify succeeds.
	nonce string
}

func newOIDCStub(t *testing.T, clientID string) *oidcStub {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}
	s := &oidcStub{t: t, key: key, kid: "test-key-1", clientID: clientID}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", s.handleDiscovery)
	mux.HandleFunc("/jwks", s.handleJWKS)
	mux.HandleFunc("/authorize", s.handleAuthorize)
	mux.HandleFunc("/token", s.handleToken)
	s.srv = httptest.NewServer(mux)
	s.issuer = s.srv.URL
	t.Cleanup(s.srv.Close)
	return s
}

func (s *oidcStub) handleDiscovery(w http.ResponseWriter, r *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"issuer":                                s.issuer,
		"authorization_endpoint":                s.issuer + "/authorize",
		"token_endpoint":                        s.issuer + "/token",
		"jwks_uri":                              s.issuer + "/jwks",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
	})
}

func (s *oidcStub) handleJWKS(w http.ResponseWriter, r *http.Request) {
	n := s.key.PublicKey.N
	e := big.NewInt(int64(s.key.PublicKey.E))
	_ = json.NewEncoder(w).Encode(map[string]any{
		"keys": []map[string]any{{
			"kty": "RSA",
			"alg": "RS256",
			"use": "sig",
			"kid": s.kid,
			"n":   base64.RawURLEncoding.EncodeToString(n.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(e.Bytes()),
		}},
	})
}

// handleAuthorize redirects straight back with a canned code. A real
// provider would show a login page; we skip that.
func (s *oidcStub) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	redirect := r.URL.Query().Get("redirect_uri")
	state := r.URL.Query().Get("state")
	s.nonce = r.URL.Query().Get("nonce")
	u, err := url.Parse(redirect)
	if err != nil {
		http.Error(w, "bad redirect", 400)
		return
	}
	q := u.Query()
	q.Set("code", "test-code")
	q.Set("state", state)
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (s *oidcStub) handleToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", 400)
		return
	}
	if r.Form.Get("code") != "test-code" {
		http.Error(w, "bad code", 400)
		return
	}
	idTok, err := s.signIDToken()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": idTok,
		"token_type":   "Bearer",
		"id_token":     idTok,
		"expires_in":   3600,
	})
}

func (s *oidcStub) signIDToken() (string, error) {
	header := map[string]any{
		"alg": "RS256",
		"kid": s.kid,
		"typ": "JWT",
	}
	now := time.Now().Unix()
	payload := map[string]any{
		"iss":   s.issuer,
		"sub":   s.subject,
		"aud":   s.clientID,
		"iat":   now,
		"exp":   now + 3600,
		"nonce": s.nonce,
	}
	hb, _ := json.Marshal(header)
	pb, _ := json.Marshal(payload)
	enc := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
	signingInput := enc(hb) + "." + enc(pb)
	h := sha256.New()
	h.Write([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.key, 5, h.Sum(nil)) // 5 = crypto.SHA256
	if err != nil {
		return "", err
	}
	return signingInput + "." + enc(sig), nil
}

func newFakeStore(t *testing.T) *fakestore.Store {
	t.Helper()
	fs, err := fakestore.New(fakestore.Options{
		Clock:   clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		BlobDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("fakestore: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	return fs
}

func TestAddProviderDiscovery(t *testing.T) {
	stub := newOIDCStub(t, "herold-client")
	fs := newFakeStore(t)
	rp := directoryoidc.New(fs.Meta(), slog.New(slog.NewTextHandler(io.Discard, nil)), &http.Client{Timeout: 5 * time.Second}, clock.NewReal())
	id, err := rp.AddProvider(context.Background(), directoryoidc.ProviderConfig{
		Name:         "test",
		IssuerURL:    stub.issuer,
		ClientID:     "herold-client",
		ClientSecret: "secret",
		RedirectURL:  "http://localhost/cb",
		Scopes:       []string{"email", "profile"},
	})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if id != "test" {
		t.Fatalf("id: %s", id)
	}
	ps, err := rp.ListProviders(context.Background())
	if err != nil || len(ps) != 1 {
		t.Fatalf("list: %v len=%d", err, len(ps))
	}
}

func TestLinkAndSignInRoundTrip(t *testing.T) {
	stub := newOIDCStub(t, "herold-client")
	fs := newFakeStore(t)
	// Seed a local principal.
	p, err := fs.Meta().InsertPrincipal(context.Background(), store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@example.test",
	})
	if err != nil {
		t.Fatalf("insert principal: %v", err)
	}

	rp := directoryoidc.New(fs.Meta(), slog.New(slog.NewTextHandler(io.Discard, nil)), &http.Client{Timeout: 5 * time.Second}, clock.NewReal())
	if _, err := rp.AddProvider(context.Background(), directoryoidc.ProviderConfig{
		Name:         "test",
		IssuerURL:    stub.issuer,
		ClientID:     "herold-client",
		ClientSecret: "secret",
		RedirectURL:  "http://localhost/cb",
	}); err != nil {
		t.Fatalf("add provider: %v", err)
	}

	// BeginLink -> follow the auth URL -> stub issues code + state on
	// redirect.
	stub.subject = "ext-sub-1"
	authURL, state, err := rp.BeginLink(context.Background(), p.ID, "test")
	if err != nil {
		t.Fatalf("begin link: %v", err)
	}
	code, gotState := followAuth(t, authURL)
	if gotState != state {
		t.Fatalf("state mismatch: %q vs %q", gotState, state)
	}

	linkedPID, err := rp.CompleteLink(context.Background(), state, code)
	if err != nil {
		t.Fatalf("complete link: %v", err)
	}
	if linkedPID != p.ID {
		t.Fatalf("linked pid: %d want %d", linkedPID, p.ID)
	}

	// Now the sign-in flow should resolve the same sub back to p.ID.
	authURL, state, err = rp.BeginSignIn(context.Background(), "test")
	if err != nil {
		t.Fatalf("begin signin: %v", err)
	}
	code, gotState = followAuth(t, authURL)
	if gotState != state {
		t.Fatalf("state mismatch: %q vs %q", gotState, state)
	}
	gotPID, err := rp.CompleteSignIn(context.Background(), state, code)
	if err != nil {
		t.Fatalf("complete signin: %v", err)
	}
	if gotPID != p.ID {
		t.Fatalf("signin pid: %d want %d", gotPID, p.ID)
	}

	// Sign in with an unknown subject must fail with ErrNotFound.
	stub.subject = "ext-sub-unknown"
	authURL, state, err = rp.BeginSignIn(context.Background(), "test")
	if err != nil {
		t.Fatalf("begin signin 2: %v", err)
	}
	code, gotState = followAuth(t, authURL)
	if gotState != state {
		t.Fatalf("state mismatch: %q vs %q", gotState, state)
	}
	_, err = rp.CompleteSignIn(context.Background(), state, code)
	if !errors.Is(err, directoryoidc.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestInvalidState(t *testing.T) {
	fs := newFakeStore(t)
	rp := directoryoidc.New(fs.Meta(), slog.New(slog.NewTextHandler(io.Discard, nil)), &http.Client{Timeout: 5 * time.Second}, clock.NewReal())
	_, err := rp.CompleteLink(context.Background(), "bogus", "code")
	if !errors.Is(err, directoryoidc.ErrInvalidState) {
		t.Fatalf("want ErrInvalidState, got %v", err)
	}
}

func TestPendingTTL(t *testing.T) {
	stub := newOIDCStub(t, "herold-client")
	fs := newFakeStore(t)
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	rp := directoryoidc.New(fs.Meta(), slog.New(slog.NewTextHandler(io.Discard, nil)), &http.Client{Timeout: 5 * time.Second}, clk)
	if _, err := rp.AddProvider(context.Background(), directoryoidc.ProviderConfig{
		Name:        "test",
		IssuerURL:   stub.issuer,
		ClientID:    "herold-client",
		RedirectURL: "http://localhost/cb",
	}); err != nil {
		t.Fatalf("add: %v", err)
	}
	_, state, err := rp.BeginSignIn(context.Background(), "test")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	// Advance clock past the TTL.
	clk.Advance(6 * time.Minute)
	_, err = rp.CompleteSignIn(context.Background(), state, "code")
	if !errors.Is(err, directoryoidc.ErrInvalidState) {
		t.Fatalf("want ErrInvalidState after TTL expiry, got %v", err)
	}
}

// followAuth simulates a user-agent following the provider's auth URL
// and capturing the redirect's ?code / ?state.
func followAuth(t *testing.T, authURL string) (code, state string) {
	t.Helper()
	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(authURL)
	if err != nil {
		t.Fatalf("follow auth: %v", err)
	}
	defer resp.Body.Close()
	loc, err := resp.Location()
	if err != nil {
		// Read body for diagnostics.
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("no redirect (%d): %s\n%s", resp.StatusCode, err, string(b))
	}
	if !strings.HasPrefix(loc.String(), "http://localhost") {
		t.Fatalf("unexpected redirect target: %s", loc)
	}
	return loc.Query().Get("code"), loc.Query().Get("state")
}

// TestVerifyAccessToken_RejectsCrossProviderToken is the regression
// guard for Wave 4 finding 9 (audience confusion). It registers two
// providers with disjoint audiences and a link under provider B for a
// subject that also exists under provider A. A token issued by
// provider A whose `sub` happens to collide must NOT unlock the
// principal linked under provider B.
//
// Pre-fix VerifyAccessToken iterated every registered provider and
// accepted the first verifier that succeeded; the loop would have
// promoted A's token into a B-scoped link lookup. Post-fix the caller
// names the provider and the audience check ensures `aud` matches that
// provider's ClientID.
func TestVerifyAccessToken_RejectsCrossProviderToken(t *testing.T) {
	stubA := newOIDCStub(t, "client-A")
	stubB := newOIDCStub(t, "client-B")
	fs := newFakeStore(t)
	ctx := context.Background()
	pA, err := fs.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@example.test",
	})
	if err != nil {
		t.Fatalf("insert principal A: %v", err)
	}
	pB, err := fs.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "bob@example.test",
	})
	if err != nil {
		t.Fatalf("insert principal B: %v", err)
	}
	rp := directoryoidc.New(fs.Meta(), slog.New(slog.NewTextHandler(io.Discard, nil)), &http.Client{Timeout: 5 * time.Second}, clock.NewReal())
	if _, err := rp.AddProvider(ctx, directoryoidc.ProviderConfig{
		Name:        "provA",
		IssuerURL:   stubA.issuer,
		ClientID:    "client-A",
		RedirectURL: "http://localhost/cb",
	}); err != nil {
		t.Fatalf("add A: %v", err)
	}
	if _, err := rp.AddProvider(ctx, directoryoidc.ProviderConfig{
		Name:        "provB",
		IssuerURL:   stubB.issuer,
		ClientID:    "client-B",
		RedirectURL: "http://localhost/cb",
	}); err != nil {
		t.Fatalf("add B: %v", err)
	}
	// Link the colliding subject "shared-sub" to A=alice and B=bob.
	if err := fs.Meta().LinkOIDC(ctx, store.OIDCLink{
		PrincipalID:  pA.ID,
		ProviderName: "provA",
		Subject:      "shared-sub",
	}); err != nil {
		t.Fatalf("link A: %v", err)
	}
	if err := fs.Meta().LinkOIDC(ctx, store.OIDCLink{
		PrincipalID:  pB.ID,
		ProviderName: "provB",
		Subject:      "shared-sub",
	}); err != nil {
		t.Fatalf("link B: %v", err)
	}
	// Forge a token signed by provider A with the shared sub.
	stubA.subject = "shared-sub"
	stubA.nonce = "" // VerifyAccessToken does not enforce nonce on the access-token path
	tokenA, err := stubA.signIDToken()
	if err != nil {
		t.Fatalf("sign token A: %v", err)
	}
	// Naming provA must resolve to alice (A's link).
	pid, err := rp.VerifyAccessToken(ctx, "provA", tokenA)
	if err != nil {
		t.Fatalf("verify under provA: %v", err)
	}
	if pid != pA.ID {
		t.Fatalf("provA: pid=%d want %d", pid, pA.ID)
	}
	// Naming provB must reject (`aud` is "client-A", not provB's
	// "client-B"; pre-fix iterated and would have promoted to bob).
	if _, err := rp.VerifyAccessToken(ctx, "provB", tokenA); err == nil {
		t.Fatalf("verify under provB with provA-signed token must fail")
	} else if !errors.Is(err, directoryoidc.ErrNotFound) {
		t.Fatalf("verify under provB: want ErrNotFound, got %v", err)
	}
	// Empty provider hint must reject — the verifier cannot pick.
	if _, err := rp.VerifyAccessToken(ctx, "", tokenA); err == nil {
		t.Fatalf("verify with empty hint must fail")
	}
	// Unknown provider hint must reject.
	if _, err := rp.VerifyAccessToken(ctx, "ghost", tokenA); err == nil {
		t.Fatalf("verify with unknown hint must fail")
	}
}

// TestPeekPendingFlow_ClassifiesWithoutConsuming covers the finding-10
// fix: the OIDC callback peeks the pending row's flow kind before
// consuming the state via takePending. Both kinds round-trip; an
// unknown state returns FlowKindUnknown without mutating the map.
func TestPeekPendingFlow_ClassifiesWithoutConsuming(t *testing.T) {
	stub := newOIDCStub(t, "herold-client")
	fs := newFakeStore(t)
	ctx := context.Background()
	p, err := fs.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@example.test",
	})
	if err != nil {
		t.Fatalf("insert principal: %v", err)
	}
	rp := directoryoidc.New(fs.Meta(), slog.New(slog.NewTextHandler(io.Discard, nil)), &http.Client{Timeout: 5 * time.Second}, clock.NewReal())
	if _, err := rp.AddProvider(ctx, directoryoidc.ProviderConfig{
		Name:        "test",
		IssuerURL:   stub.issuer,
		ClientID:    "herold-client",
		RedirectURL: "http://localhost/cb",
	}); err != nil {
		t.Fatalf("add provider: %v", err)
	}
	// Link branch.
	stub.subject = "ext-sub-1"
	_, linkState, err := rp.BeginLink(ctx, p.ID, "test")
	if err != nil {
		t.Fatalf("begin link: %v", err)
	}
	if got := rp.PeekPendingFlow(linkState); got != directoryoidc.FlowKindLink {
		t.Fatalf("link peek: got %v want FlowKindLink", got)
	}
	// Peek must not consume — calling twice still classifies.
	if got := rp.PeekPendingFlow(linkState); got != directoryoidc.FlowKindLink {
		t.Fatalf("link peek (2nd): got %v want FlowKindLink", got)
	}
	// Sign-in branch.
	_, signinState, err := rp.BeginSignIn(ctx, "test")
	if err != nil {
		t.Fatalf("begin signin: %v", err)
	}
	if got := rp.PeekPendingFlow(signinState); got != directoryoidc.FlowKindSignIn {
		t.Fatalf("signin peek: got %v want FlowKindSignIn", got)
	}
	if got := rp.PeekPendingFlow("nonsense"); got != directoryoidc.FlowKindUnknown {
		t.Fatalf("unknown peek: got %v want FlowKindUnknown", got)
	}
}

// ensure fmt import is used; Go will strip otherwise.
var _ = fmt.Sprintf
