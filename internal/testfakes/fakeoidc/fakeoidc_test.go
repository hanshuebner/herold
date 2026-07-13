package fakeoidc_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/hanshuebner/herold/internal/testfakes/fakeoidc"
)

// postFormJSON POSTs form-encoded data and decodes a JSON object
// response. A tiny helper so this fixture-only test file does not need a
// golang.org/x/oauth2 dependency.
func postFormJSON(u string, form url.Values) (map[string]any, error) {
	resp, err := http.PostForm(u, form)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// TestDiscoveryAndVerify drives the exact subset of OIDC go-oidc uses --
// discovery, JWKS, /authorize, /token -- and verifies the returned ID
// token, proving the fake is a faithful stand-in for
// internal/directoryoidc.RP (which uses the same library).
func TestDiscoveryAndVerify(t *testing.T) {
	ctx := context.Background()
	srv := fakeoidc.New(t, fakeoidc.Options{ClientID: "herold-client", ClientSecret: "s3cr3t"})
	srv.SetIdentity(fakeoidc.Identity{
		Subject:       "sub-1",
		Email:         "alice@example.test",
		EmailVerified: true,
		Name:          "Alice Example",
		Extra:         map[string]any{"groups": []string{"eng"}},
	})

	prov, err := oidc.NewProvider(ctx, srv.IssuerURL())
	if err != nil {
		t.Fatalf("oidc.NewProvider: %v", err)
	}

	authURL := srv.IssuerURL() + "/authorize?" + url.Values{
		"response_type": {"code"},
		"client_id":     {"herold-client"},
		"redirect_uri":  {"http://localhost/cb"},
		"state":         {"st-1"},
		"nonce":         {"nonce-1"},
	}.Encode()
	code, state := fakeoidc.FollowAuthorize(t, authURL, "http://localhost")
	if state != "st-1" {
		t.Fatalf("state = %q, want st-1", state)
	}
	if code == "" {
		t.Fatalf("empty code")
	}

	tokReq := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {"herold-client"},
		"client_secret": {"s3cr3t"},
	}
	tok, err := postFormJSON(srv.IssuerURL()+"/token", tokReq)
	if err != nil {
		t.Fatalf("token exchange: %v", err)
	}
	idTokRaw, _ := tok["id_token"].(string)
	if idTokRaw == "" {
		t.Fatalf("no id_token in response: %+v", tok)
	}

	verifier := prov.Verifier(&oidc.Config{ClientID: "herold-client"})
	idTok, err := verifier.Verify(ctx, idTokRaw)
	if err != nil {
		t.Fatalf("verify id_token: %v", err)
	}
	if idTok.Subject != "sub-1" {
		t.Fatalf("subject = %q, want sub-1", idTok.Subject)
	}
	if idTok.Nonce != "nonce-1" {
		t.Fatalf("nonce = %q, want nonce-1", idTok.Nonce)
	}
	var claims struct {
		Email         string   `json:"email"`
		EmailVerified bool     `json:"email_verified"`
		Name          string   `json:"name"`
		Groups        []string `json:"groups"`
	}
	if err := idTok.Claims(&claims); err != nil {
		t.Fatalf("decode claims: %v", err)
	}
	if claims.Email != "alice@example.test" || !claims.EmailVerified {
		t.Fatalf("claims = %+v", claims)
	}
	if claims.Name != "Alice Example" {
		t.Fatalf("name = %q", claims.Name)
	}
	if len(claims.Groups) != 1 || claims.Groups[0] != "eng" {
		t.Fatalf("groups = %v", claims.Groups)
	}
}

// TestAuthorizeQueryOverride verifies a per-request query parameter
// overrides the default identity's field without mutating the default
// for subsequent requests, so a browser paste-URL can pick an address
// without recompiling anything (dev-instance manual verification).
func TestAuthorizeQueryOverride(t *testing.T) {
	ctx := context.Background()
	srv := fakeoidc.New(t, fakeoidc.Options{})
	srv.SetIdentity(fakeoidc.Identity{Subject: "default-sub", Email: "default@example.test", EmailVerified: true})

	prov, err := oidc.NewProvider(ctx, srv.IssuerURL())
	if err != nil {
		t.Fatalf("oidc.NewProvider: %v", err)
	}

	authURL := srv.IssuerURL() + "/authorize?" + url.Values{
		"response_type": {"code"},
		"client_id":     {srv.ClientID()},
		"redirect_uri":  {"http://localhost/cb"},
		"state":         {"st-2"},
		"sub":           {"override-sub"},
		"email":         {"override@example.test"},
	}.Encode()
	code, _ := fakeoidc.FollowAuthorize(t, authURL, "")

	tok, err := postFormJSON(srv.IssuerURL()+"/token", url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {srv.ClientID()},
		"client_secret": {srv.ClientSecret()},
	})
	if err != nil {
		t.Fatalf("token exchange: %v", err)
	}
	idTokRaw, _ := tok["id_token"].(string)
	verifier := prov.Verifier(&oidc.Config{ClientID: srv.ClientID()})
	idTok, err := verifier.Verify(ctx, idTokRaw)
	if err != nil {
		t.Fatalf("verify id_token: %v", err)
	}
	if idTok.Subject != "override-sub" {
		t.Fatalf("subject = %q, want override-sub (default must not have won)", idTok.Subject)
	}

	// A second, unrelated request still gets the unmodified default.
	authURL2 := srv.IssuerURL() + "/authorize?" + url.Values{
		"response_type": {"code"},
		"client_id":     {srv.ClientID()},
		"redirect_uri":  {"http://localhost/cb"},
		"state":         {"st-3"},
	}.Encode()
	code2, _ := fakeoidc.FollowAuthorize(t, authURL2, "")
	tok2, err := postFormJSON(srv.IssuerURL()+"/token", url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code2},
		"client_id":     {srv.ClientID()},
		"client_secret": {srv.ClientSecret()},
	})
	if err != nil {
		t.Fatalf("token exchange 2: %v", err)
	}
	idTokRaw2, _ := tok2["id_token"].(string)
	idTok2, err := verifier.Verify(ctx, idTokRaw2)
	if err != nil {
		t.Fatalf("verify id_token 2: %v", err)
	}
	if idTok2.Subject != "default-sub" {
		t.Fatalf("subject 2 = %q, want default-sub (SetIdentity default must survive the override request)", idTok2.Subject)
	}
}
