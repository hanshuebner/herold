package directory_test

// oauth2_test.go exercises the OAuth2 authorization-code + PKCE grant
// for native clients (issue #199, REQ-AND-AUTH-01/02): the full flow
// (authorize -> code -> token -> refresh -> rotated -> replay rejected)
// plus each security property in isolation (PKCE mismatch, redirect_uri
// mismatch, unknown client, single-use code, refresh reuse detection).

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/store"
)

// newPKCE returns a fresh (verifier, S256 challenge) pair.
func newPKCE(t *testing.T) (verifier, challenge string) {
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

func baseAuthorizeRequest(dir *directory.Directory, now time.Time, redirectURI, challenge string) directory.AuthorizeRequest {
	return directory.AuthorizeRequest{
		ClientID:            "herold-android",
		RedirectURI:         redirectURI,
		State:               "xyz-state",
		CodeChallenge:       challenge,
		CodeChallengeMethod: "S256",
		CSRFToken:           "csrf-tok",
		ExpiresAt:           now.Add(directory.AuthorizeRequestTTL),
	}
}

// mustRegisterAndroidClient registers the "herold-android" public
// client used throughout this file's tests. The DB-backed registry
// (issue #199) starts every test instance empty -- there is no
// compiled-in client list -- so every test that exercises the grant
// against a known client_id registers one first.
func mustRegisterAndroidClient(t *testing.T, dir *directory.Directory) directory.OAuthClient {
	t.Helper()
	client, secret, err := dir.RegisterOAuthClient(context.Background(), directory.OAuthClientRegistration{
		ClientID: "herold-android",
		Name:     "herold Android client",
		RedirectURIs: []string{
			"net.netzhansa.herold:/oauth2redirect",
			"http://127.0.0.1/oauth2redirect",
			"http://[::1]/oauth2redirect",
		},
	})
	if err != nil {
		t.Fatalf("RegisterOAuthClient: %v", err)
	}
	if secret != "" {
		t.Fatalf("public client registration returned a secret")
	}
	return client
}

// TestOAuth2_FullFlow drives authorize -> code -> token -> use access
// token (verified via the store, the same lookup protojmap/protoadmin's
// Bearer path performs) -> refresh -> rotated -> replay-old-refresh
// rejected, per the issue's required end-to-end acceptance shape.
func TestOAuth2_FullFlow(t *testing.T) {
	ctx := context.Background()
	dir, fs, clk := newDir(t)
	mustRegisterAndroidClient(t, dir)
	pid, err := dir.CreatePrincipal(ctx, "oauth-flow@example.test", "correct-horse-staple")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	verifier, challenge := newPKCE(t)
	redirectURI := "net.netzhansa.herold:/oauth2redirect"
	req := baseAuthorizeRequest(dir, clk.Now(), redirectURI, challenge)

	code, err := dir.IssueAuthorizationCode(ctx, "oauth-flow@example.test", "correct-horse-staple", "", req)
	if err != nil {
		t.Fatalf("IssueAuthorizationCode: %v", err)
	}
	if code == "" {
		t.Fatalf("empty code")
	}

	result, err := dir.ExchangeAuthorizationCode(ctx, "herold-android", "", code, redirectURI, verifier)
	if err != nil {
		t.Fatalf("ExchangeAuthorizationCode: %v", err)
	}
	if result.AccessToken == "" || result.RefreshToken == "" {
		t.Fatalf("empty token in result: %+v", result)
	}
	if result.ExpiresIn != int(directory.AccessTokenTTL.Seconds()) {
		t.Fatalf("ExpiresIn = %d, want %d", result.ExpiresIn, int(directory.AccessTokenTTL.Seconds()))
	}

	// The access token verifies through the store lookup exactly as
	// protojmap/protoadmin's Bearer-auth path does, and is not expired
	// yet.
	atKey, err := fs.Meta().GetAPIKeyByHash(ctx, directory.HashDeviceToken(result.AccessToken))
	if err != nil {
		t.Fatalf("GetAPIKeyByHash(access token): %v", err)
	}
	if atKey.PrincipalID != pid {
		t.Fatalf("access token principal = %d, want %d", atKey.PrincipalID, pid)
	}
	if atKey.ExpiresAt.IsZero() || !atKey.ExpiresAt.After(clk.Now()) {
		t.Fatalf("access token ExpiresAt = %v, want a future time", atKey.ExpiresAt)
	}

	// Refresh: rotates to a new pair.
	refreshResult, err := dir.RefreshOAuthToken(ctx, "herold-android", "", result.RefreshToken)
	if err != nil {
		t.Fatalf("RefreshOAuthToken: %v", err)
	}
	if refreshResult.RefreshToken == result.RefreshToken {
		t.Fatalf("refresh did not rotate the token")
	}
	if refreshResult.AccessToken == result.AccessToken {
		t.Fatalf("refresh did not mint a new access token")
	}

	// The original access token's api_keys row is gone (best-effort
	// immediate revocation on rotation).
	if _, err := fs.Meta().GetAPIKeyByHash(ctx, directory.HashDeviceToken(result.AccessToken)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("original access token should be revoked after refresh, got err=%v", err)
	}

	// The new access token verifies.
	if _, err := fs.Meta().GetAPIKeyByHash(ctx, directory.HashDeviceToken(refreshResult.AccessToken)); err != nil {
		t.Fatalf("new access token should verify: %v", err)
	}

	// Replay of the OLD (already-rotated) refresh token is rejected and
	// revokes the whole family.
	if _, err := dir.RefreshOAuthToken(ctx, "herold-android", "", result.RefreshToken); !errors.Is(err, directory.ErrRefreshReuse) {
		t.Fatalf("replayed refresh token = %v, want ErrRefreshReuse", err)
	}

	// The family revocation also killed the second-generation access
	// token (issued by the legitimate refresh above).
	if _, err := fs.Meta().GetAPIKeyByHash(ctx, directory.HashDeviceToken(refreshResult.AccessToken)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second-generation access token should be revoked after reuse detection, got err=%v", err)
	}

	// And the (also legitimate) newest refresh token from that refresh
	// is now dead too -- the whole chain is revoked.
	if _, err := dir.RefreshOAuthToken(ctx, "herold-android", "", refreshResult.RefreshToken); !errors.Is(err, directory.ErrInvalidGrant) {
		t.Fatalf("using the newest refresh token after family revocation = %v, want ErrInvalidGrant", err)
	}
}

func TestOAuth2_Exchange_PKCEMismatch(t *testing.T) {
	ctx := context.Background()
	dir, _, clk := newDir(t)
	mustRegisterAndroidClient(t, dir)
	if _, err := dir.CreatePrincipal(ctx, "pkce@example.test", "correct-horse-staple"); err != nil {
		t.Fatalf("create: %v", err)
	}
	_, challenge := newPKCE(t)
	redirectURI := "net.netzhansa.herold:/oauth2redirect"
	req := baseAuthorizeRequest(dir, clk.Now(), redirectURI, challenge)
	code, err := dir.IssueAuthorizationCode(ctx, "pkce@example.test", "correct-horse-staple", "", req)
	if err != nil {
		t.Fatalf("IssueAuthorizationCode: %v", err)
	}
	wrongVerifier, _ := newPKCE(t)
	if _, err := dir.ExchangeAuthorizationCode(ctx, "herold-android", "", code, redirectURI, wrongVerifier); !errors.Is(err, directory.ErrPKCEMismatch) {
		t.Fatalf("exchange with wrong verifier = %v, want ErrPKCEMismatch", err)
	}
}

func TestOAuth2_Exchange_MissingVerifierRejected(t *testing.T) {
	ctx := context.Background()
	dir, _, clk := newDir(t)
	mustRegisterAndroidClient(t, dir)
	if _, err := dir.CreatePrincipal(ctx, "noverifier@example.test", "correct-horse-staple"); err != nil {
		t.Fatalf("create: %v", err)
	}
	_, challenge := newPKCE(t)
	redirectURI := "net.netzhansa.herold:/oauth2redirect"
	req := baseAuthorizeRequest(dir, clk.Now(), redirectURI, challenge)
	code, err := dir.IssueAuthorizationCode(ctx, "noverifier@example.test", "correct-horse-staple", "", req)
	if err != nil {
		t.Fatalf("IssueAuthorizationCode: %v", err)
	}
	// PKCE is mandatory: an empty code_verifier must never succeed,
	// regardless of the stored challenge.
	if _, err := dir.ExchangeAuthorizationCode(ctx, "herold-android", "", code, redirectURI, ""); !errors.Is(err, directory.ErrPKCEMismatch) {
		t.Fatalf("exchange with empty verifier = %v, want ErrPKCEMismatch", err)
	}
}

func TestOAuth2_Exchange_RedirectURIMismatch(t *testing.T) {
	ctx := context.Background()
	dir, _, clk := newDir(t)
	mustRegisterAndroidClient(t, dir)
	if _, err := dir.CreatePrincipal(ctx, "redir@example.test", "correct-horse-staple"); err != nil {
		t.Fatalf("create: %v", err)
	}
	verifier, challenge := newPKCE(t)
	req := baseAuthorizeRequest(dir, clk.Now(), "net.netzhansa.herold:/oauth2redirect", challenge)
	code, err := dir.IssueAuthorizationCode(ctx, "redir@example.test", "correct-horse-staple", "", req)
	if err != nil {
		t.Fatalf("IssueAuthorizationCode: %v", err)
	}
	if _, err := dir.ExchangeAuthorizationCode(ctx, "herold-android", "", code, "http://127.0.0.1/oauth2redirect", verifier); !errors.Is(err, directory.ErrInvalidGrant) {
		t.Fatalf("exchange with mismatched redirect_uri = %v, want ErrInvalidGrant", err)
	}
}

func TestOAuth2_Exchange_UnknownClient(t *testing.T) {
	ctx := context.Background()
	dir, _, _ := newDir(t)
	if _, err := dir.ExchangeAuthorizationCode(ctx, "not-a-real-client", "", "whatever", "https://example.test/cb", "verifier"); !errors.Is(err, directory.ErrUnknownOAuthClient) {
		t.Fatalf("exchange with unknown client = %v, want ErrUnknownOAuthClient", err)
	}
}

func TestOAuth2_Exchange_CodeSingleUse(t *testing.T) {
	ctx := context.Background()
	dir, _, clk := newDir(t)
	mustRegisterAndroidClient(t, dir)
	if _, err := dir.CreatePrincipal(ctx, "singleuse@example.test", "correct-horse-staple"); err != nil {
		t.Fatalf("create: %v", err)
	}
	verifier, challenge := newPKCE(t)
	redirectURI := "net.netzhansa.herold:/oauth2redirect"
	req := baseAuthorizeRequest(dir, clk.Now(), redirectURI, challenge)
	code, err := dir.IssueAuthorizationCode(ctx, "singleuse@example.test", "correct-horse-staple", "", req)
	if err != nil {
		t.Fatalf("IssueAuthorizationCode: %v", err)
	}
	if _, err := dir.ExchangeAuthorizationCode(ctx, "herold-android", "", code, redirectURI, verifier); err != nil {
		t.Fatalf("first exchange: %v", err)
	}
	// Replay of the same code, even with a correct verifier, must fail.
	if _, err := dir.ExchangeAuthorizationCode(ctx, "herold-android", "", code, redirectURI, verifier); !errors.Is(err, directory.ErrInvalidGrant) {
		t.Fatalf("replayed code = %v, want ErrInvalidGrant", err)
	}
}

func TestOAuth2_Exchange_ExpiredCode(t *testing.T) {
	ctx := context.Background()
	dir, _, clk := newDir(t)
	mustRegisterAndroidClient(t, dir)
	if _, err := dir.CreatePrincipal(ctx, "expired@example.test", "correct-horse-staple"); err != nil {
		t.Fatalf("create: %v", err)
	}
	verifier, challenge := newPKCE(t)
	redirectURI := "net.netzhansa.herold:/oauth2redirect"
	req := baseAuthorizeRequest(dir, clk.Now(), redirectURI, challenge)
	code, err := dir.IssueAuthorizationCode(ctx, "expired@example.test", "correct-horse-staple", "", req)
	if err != nil {
		t.Fatalf("IssueAuthorizationCode: %v", err)
	}
	clk.Advance(directory.AuthorizationCodeTTL + time.Second)
	if _, err := dir.ExchangeAuthorizationCode(ctx, "herold-android", "", code, redirectURI, verifier); !errors.Is(err, directory.ErrInvalidGrant) {
		t.Fatalf("expired code = %v, want ErrInvalidGrant", err)
	}
}

func TestOAuth2_IssueAuthorizationCode_TOTPRequired(t *testing.T) {
	ctx := context.Background()
	dir, _, clk := newDir(t)
	mustRegisterAndroidClient(t, dir)
	pid, err := dir.CreatePrincipal(ctx, "totp-oauth@example.test", "correct-horse-staple")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	secret, _, err := dir.EnrollTOTP(ctx, pid)
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	code0 := mustGenerate(t, secret, clk.Now())
	if err := dir.ConfirmTOTP(ctx, pid, code0); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	_, challenge := newPKCE(t)
	req := baseAuthorizeRequest(dir, clk.Now(), "net.netzhansa.herold:/oauth2redirect", challenge)
	if _, err := dir.IssueAuthorizationCode(ctx, "totp-oauth@example.test", "correct-horse-staple", "", req); !errors.Is(err, directory.ErrTOTPRequired) {
		t.Fatalf("issue without totp = %v, want ErrTOTPRequired", err)
	}

	clk.Advance(time.Second)
	code1 := mustGenerate(t, secret, clk.Now())
	if _, err := dir.IssueAuthorizationCode(ctx, "totp-oauth@example.test", "correct-horse-staple", code1, req); err != nil {
		t.Fatalf("issue with valid totp: %v", err)
	}
}

// TestOAuth2_AuthorizeRequest_EncodeDecodeRoundtrip exercises the signed
// pre-login request token in isolation: it round-trips, rejects a
// tampered payload, and rejects an expired one.
func TestOAuth2_AuthorizeRequest_EncodeDecodeRoundtrip(t *testing.T) {
	ctx := context.Background()
	dir, _, clk := newDir(t)
	_ = ctx
	req := baseAuthorizeRequest(dir, clk.Now(), "net.netzhansa.herold:/oauth2redirect", "chal")
	encoded := dir.EncodeAuthorizeRequest(req)
	got, err := dir.DecodeAuthorizeRequest(encoded, clk.Now())
	if err != nil {
		t.Fatalf("DecodeAuthorizeRequest: %v", err)
	}
	if got.ClientID != req.ClientID || got.RedirectURI != req.RedirectURI ||
		got.CodeChallenge != req.CodeChallenge || got.CSRFToken != req.CSRFToken ||
		got.State != req.State {
		t.Fatalf("decoded = %+v, want %+v", got, req)
	}

	// Tampered payload: signature no longer verifies.
	tampered := encoded[:len(encoded)-1] + "x"
	if _, err := dir.DecodeAuthorizeRequest(tampered, clk.Now()); !errors.Is(err, directory.ErrAuthorizeRequestInvalid) {
		t.Fatalf("tampered decode = %v, want ErrAuthorizeRequestInvalid", err)
	}

	// Expired: TTL elapsed.
	if _, err := dir.DecodeAuthorizeRequest(encoded, clk.Now().Add(directory.AuthorizeRequestTTL+time.Second)); !errors.Is(err, directory.ErrAuthorizeRequestExpired) {
		t.Fatalf("expired decode = %v, want ErrAuthorizeRequestExpired", err)
	}
}

func TestOAuth2_RedirectURI_LoopbackPortIgnored(t *testing.T) {
	dir, _, _ := newDir(t)
	client := mustRegisterAndroidClient(t, dir)
	if !directory.ValidateRedirectURI(client, "http://127.0.0.1:54321/oauth2redirect") {
		t.Fatalf("loopback redirect with arbitrary port should validate")
	}
	if directory.ValidateRedirectURI(client, "http://127.0.0.1:54321/wrong-path") {
		t.Fatalf("loopback redirect with wrong path should not validate")
	}
	if directory.ValidateRedirectURI(client, "https://evil.example/oauth2redirect") {
		t.Fatalf("non-registered scheme/host should not validate")
	}
}
