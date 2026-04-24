package e2e

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/test/e2e/fixtures"
)

// TestPhase1_OIDCLink_RoundTrip stands up an in-process OIDC provider
// stub (discovery + JWKS + /authorize redirect + /token issuing a
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

		stub := newOIDCStub(t, "herold-client")

		rp := directoryoidc.New(
			st.Meta(),
			slog.New(slog.NewTextHandler(io.Discard, nil)),
			&http.Client{Timeout: 5 * time.Second},
			fixtures.NewFakeClock(),
		)
		providerID, err := rp.AddProvider(ctx, directoryoidc.ProviderConfig{
			Name:         "stub",
			IssuerURL:    stub.issuer,
			ClientID:     "herold-client",
			ClientSecret: "secret",
			RedirectURL:  "http://localhost/cb",
		})
		if err != nil {
			t.Fatalf("add provider: %v", err)
		}

		// --- Link flow ----
		stub.subject = "alice-external"
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

// --- OIDC provider stub --------------------------------------------------

// oidcStub is the in-process OIDC provider used by the e2e OIDC test.
// It speaks exactly the subset of OpenID Connect that go-oidc's
// NewProvider + Verifier requires: OIDC discovery at
// /.well-known/openid-configuration, JWKS at /jwks, a /authorize
// endpoint that echoes ?code/?state back to the redirect URI, and a
// /token endpoint that returns a signed RS256 ID token.
type oidcStub struct {
	t        *testing.T
	srv      *httptest.Server
	issuer   string
	key      *rsa.PrivateKey
	kid      string
	clientID string

	subject string // sub claim emitted by the next /token response.
	nonce   string // nonce captured from /authorize and echoed back.
}

func newOIDCStub(t *testing.T, clientID string) *oidcStub {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}
	s := &oidcStub{t: t, key: key, kid: "phase1-oidc-stub", clientID: clientID}
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

func (s *oidcStub) handleDiscovery(w http.ResponseWriter, _ *http.Request) {
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

func (s *oidcStub) handleJWKS(w http.ResponseWriter, _ *http.Request) {
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

func (s *oidcStub) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	redirect := r.URL.Query().Get("redirect_uri")
	state := r.URL.Query().Get("state")
	s.nonce = r.URL.Query().Get("nonce")
	u, err := url.Parse(redirect)
	if err != nil {
		http.Error(w, "bad redirect", http.StatusBadRequest)
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
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if r.Form.Get("code") != "test-code" {
		http.Error(w, "bad code", http.StatusBadRequest)
		return
	}
	idTok, err := s.signIDToken()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.key, 5 /* crypto.SHA256 */, h.Sum(nil))
	if err != nil {
		return "", err
	}
	return signingInput + "." + enc(sig), nil
}

// followAuth simulates a user-agent hitting the provider's /authorize
// URL and returns the ?code / ?state values the provider appended to
// the redirect.
func followAuth(t *testing.T, authURL string) (code, state string) {
	t.Helper()
	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
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
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("no redirect (status=%d): %v\n%s", resp.StatusCode, err, string(b))
	}
	if !strings.HasPrefix(loc.String(), "http://localhost") {
		t.Fatalf("unexpected redirect target: %s", loc)
	}
	return loc.Query().Get("code"), loc.Query().Get("state")
}
