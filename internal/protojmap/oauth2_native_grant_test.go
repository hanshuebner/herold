package protojmap_test

// oauth2_native_grant_test.go verifies the end-to-end OAuth2
// authorization-code + PKCE grant for native clients (issue #199,
// REQ-AND-AUTH-01/02) against the JMAP surface: authorize -> code ->
// token -> the minted access token authenticates a JMAP call exactly
// like an operator-issued API key (zero changes to protojmap's
// Bearer-verification path) -> refresh -> rotated -> the rotated access
// token also authenticates -> replaying the old refresh token is
// rejected and immediately revokes the live access token too.
//
// The browser-hosted /oauth2/authorize HTML page and the /oauth2/token
// REST adapter live in internal/protoadmin (oauth2_native_test.go
// covers them end to end over HTTP); this test drives the underlying
// internal/directory API directly, the same way
// device_token_grant_test.go drives IssueDeviceToken directly, so the
// JMAP-side assertion (does a minted token authenticate a real JMAP
// request) does not require standing up protoadmin's HTTP surface too.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/directory"
)

func oauth2JMAPPKCE(t *testing.T) (verifier, challenge string) {
	t.Helper()
	var b [32]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		t.Fatalf("read random: %v", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(b[:])
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge
}

func TestSession_OAuth2NativeGrant_FullFlow(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	verifier, challenge := oauth2JMAPPKCE(t)
	redirectURI := "net.netzhansa.herold:/oauth2redirect"
	authReq := directory.AuthorizeRequest{
		ClientID:            "herold-android",
		RedirectURI:         redirectURI,
		State:               "s1",
		CodeChallenge:       challenge,
		CodeChallengeMethod: "S256",
		CSRFToken:           "csrf",
		ExpiresAt:           f.clk.Now().Add(directory.AuthorizeRequestTTL),
	}

	code, err := f.dir.IssueAuthorizationCode(ctx, "alice@example.com", "correct-horse-battery-staple-1", "", authReq)
	if err != nil {
		t.Fatalf("IssueAuthorizationCode: %v", err)
	}
	result, err := f.dir.ExchangeAuthorizationCode(ctx, "herold-android", code, redirectURI, verifier)
	if err != nil {
		t.Fatalf("ExchangeAuthorizationCode: %v", err)
	}

	// The access token authenticates a JMAP request exactly like an
	// operator-issued API key.
	res, body := f.doRequest("GET", "/.well-known/jmap", result.AccessToken, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.StatusCode, body)
	}
	var desc map[string]any
	if err := json.Unmarshal(body, &desc); err != nil {
		t.Fatalf("decode session descriptor: %v", err)
	}
	if _, ok := desc["accounts"]; !ok {
		t.Fatalf("session descriptor missing accounts: %v", desc)
	}

	// Refresh: rotates.
	refreshed, err := f.dir.RefreshOAuthToken(ctx, "herold-android", result.RefreshToken)
	if err != nil {
		t.Fatalf("RefreshOAuthToken: %v", err)
	}
	if refreshed.AccessToken == result.AccessToken || refreshed.RefreshToken == result.RefreshToken {
		t.Fatalf("refresh did not rotate: %+v", refreshed)
	}

	// The old access token is gone (best-effort immediate revocation on
	// rotation); the new one authenticates JMAP.
	oldRes, _ := f.doRequest("GET", "/.well-known/jmap", result.AccessToken, nil)
	if oldRes.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old access token after refresh: status = %d, want 401", oldRes.StatusCode)
	}
	newRes, newBody := f.doRequest("GET", "/.well-known/jmap", refreshed.AccessToken, nil)
	if newRes.StatusCode != http.StatusOK {
		t.Fatalf("rotated access token: status = %d, body = %s", newRes.StatusCode, newBody)
	}

	// Replaying the old (already-rotated) refresh token is rejected and
	// revokes the whole chain, including the just-verified new access
	// token.
	if _, err := f.dir.RefreshOAuthToken(ctx, "herold-android", result.RefreshToken); err == nil {
		t.Fatalf("replayed refresh token should be rejected")
	}
	revokedRes, _ := f.doRequest("GET", "/.well-known/jmap", refreshed.AccessToken, nil)
	if revokedRes.StatusCode != http.StatusUnauthorized {
		t.Fatalf("access token after reuse-detected revocation: status = %d, want 401", revokedRes.StatusCode)
	}
}

// TestSession_OAuth2AccessToken_ExpiresAfterTTL asserts the short-lived
// access token (REQ-AND-AUTH-02, default 1 hour) stops authenticating
// JMAP once its TTL elapses, unlike a device token / operator API key.
func TestSession_OAuth2AccessToken_ExpiresAfterTTL(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	verifier, challenge := oauth2JMAPPKCE(t)
	redirectURI := "net.netzhansa.herold:/oauth2redirect"
	authReq := directory.AuthorizeRequest{
		ClientID: "herold-android", RedirectURI: redirectURI,
		CodeChallenge: challenge, CodeChallengeMethod: "S256",
		CSRFToken: "csrf", ExpiresAt: f.clk.Now().Add(directory.AuthorizeRequestTTL),
	}
	code, err := f.dir.IssueAuthorizationCode(ctx, "alice@example.com", "correct-horse-battery-staple-1", "", authReq)
	if err != nil {
		t.Fatalf("IssueAuthorizationCode: %v", err)
	}
	result, err := f.dir.ExchangeAuthorizationCode(ctx, "herold-android", code, redirectURI, verifier)
	if err != nil {
		t.Fatalf("ExchangeAuthorizationCode: %v", err)
	}

	res, _ := f.doRequest("GET", "/.well-known/jmap", result.AccessToken, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("fresh access token: status = %d, want 200", res.StatusCode)
	}

	f.clk.Advance(directory.AccessTokenTTL + time.Minute)

	res2, _ := f.doRequest("GET", "/.well-known/jmap", result.AccessToken, nil)
	if res2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired access token: status = %d, want 401", res2.StatusCode)
	}
}
