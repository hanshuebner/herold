package directory_test

// oauth2client_test.go covers the DB-backed OAuth2 client registry
// (issue #199's "DB-backed OAuth2 client registry" work item) at the
// internal/directory level: registration validation, the confidential
// client secret (hashed at rest, required at exchange time), the
// registered scope set constraining issued tokens, immediate refusal
// after deletion, and -- the split's explicit security callout -- that
// registering a client through this new registry does not weaken the
// TOTP gate a web-login session already enforces (issue #228).

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/auth"
	"github.com/hanshuebner/herold/internal/directory"
)

func TestOAuthClient_Register_Validations(t *testing.T) {
	ctx := context.Background()
	dir, _, _ := newDir(t)

	if _, _, err := dir.RegisterOAuthClient(ctx, directory.OAuthClientRegistration{
		RedirectURIs: []string{"https://example.test/cb"},
	}); !errors.Is(err, directory.ErrOAuthClientInvalid) {
		t.Fatalf("register without client_id = %v, want ErrOAuthClientInvalid", err)
	}

	if _, _, err := dir.RegisterOAuthClient(ctx, directory.OAuthClientRegistration{
		ClientID: "no-redirects",
	}); !errors.Is(err, directory.ErrOAuthClientInvalid) {
		t.Fatalf("register without redirect_uris = %v, want ErrOAuthClientInvalid", err)
	}

	if _, _, err := dir.RegisterOAuthClient(ctx, directory.OAuthClientRegistration{
		ClientID:     "admin-scope-attempt",
		RedirectURIs: []string{"https://example.test/cb"},
		Scopes:       []auth.Scope{auth.ScopeAdmin},
	}); !errors.Is(err, directory.ErrOAuthClientInvalid) {
		t.Fatalf("register with admin scope = %v, want ErrOAuthClientInvalid", err)
	}
}

func TestOAuthClient_Register_Conflict(t *testing.T) {
	ctx := context.Background()
	dir, _, _ := newDir(t)
	reg := directory.OAuthClientRegistration{
		ClientID: "dup-client", RedirectURIs: []string{"https://example.test/cb"},
	}
	if _, _, err := dir.RegisterOAuthClient(ctx, reg); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if _, _, err := dir.RegisterOAuthClient(ctx, reg); !errors.Is(err, directory.ErrOAuthClientExists) {
		t.Fatalf("duplicate register = %v, want ErrOAuthClientExists", err)
	}
}

// TestOAuthClient_ConfidentialSecret_HashedAtRest_AndRequired proves the
// three parts of the "client secrets stored hashed, never plaintext"
// security property: the plaintext is handed back exactly once at
// registration, the store row never holds that plaintext, and a token
// exchange for the client is refused without a matching secret --
// wrong, missing, and correct are all exercised.
func TestOAuthClient_ConfidentialSecret_HashedAtRest_AndRequired(t *testing.T) {
	ctx := context.Background()
	dir, fs, clk := newDir(t)
	pid, err := dir.CreatePrincipal(ctx, "conf-client@example.test", "correct-horse-staple")
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	_ = pid

	client, secret, err := dir.RegisterOAuthClient(ctx, directory.OAuthClientRegistration{
		ClientID:     "conf-client",
		RedirectURIs: []string{"https://example.test/cb"},
		Confidential: true,
	})
	if err != nil {
		t.Fatalf("RegisterOAuthClient: %v", err)
	}
	if client.Public {
		t.Fatalf("confidential registration returned Public=true")
	}
	if secret == "" {
		t.Fatalf("confidential registration must return a plaintext secret")
	}

	row, err := fs.Meta().GetOAuthClient(ctx, "conf-client")
	if err != nil {
		t.Fatalf("GetOAuthClient: %v", err)
	}
	if row.ClientSecretHash == "" || row.ClientSecretHash == secret {
		t.Fatalf("stored client_secret_hash = %q must be a hash, never the plaintext %q", row.ClientSecretHash, secret)
	}

	verifier, challenge := newPKCE(t)
	redirectURI := "https://example.test/cb"
	req := directory.AuthorizeRequest{
		ClientID: "conf-client", RedirectURI: redirectURI,
		CodeChallenge: challenge, CodeChallengeMethod: "S256",
		CSRFToken: "csrf", ExpiresAt: clk.Now().Add(directory.AuthorizeRequestTTL),
	}

	// Missing secret: refused.
	code1, err := dir.IssueAuthorizationCode(ctx, "conf-client@example.test", "correct-horse-staple", "", req)
	if err != nil {
		t.Fatalf("IssueAuthorizationCode: %v", err)
	}
	if _, err := dir.ExchangeAuthorizationCode(ctx, "conf-client", "", code1, redirectURI, verifier); !errors.Is(err, directory.ErrClientAuthenticationFailed) {
		t.Fatalf("exchange without client_secret = %v, want ErrClientAuthenticationFailed", err)
	}

	// Wrong secret: refused (and the code is still live -- a rejected
	// client-auth attempt must not itself burn the single-use code).
	if _, err := dir.ExchangeAuthorizationCode(ctx, "conf-client", "hcs_totally-wrong", code1, redirectURI, verifier); !errors.Is(err, directory.ErrClientAuthenticationFailed) {
		t.Fatalf("exchange with wrong client_secret = %v, want ErrClientAuthenticationFailed", err)
	}

	// Correct secret: succeeds.
	result, err := dir.ExchangeAuthorizationCode(ctx, "conf-client", secret, code1, redirectURI, verifier)
	if err != nil {
		t.Fatalf("exchange with correct client_secret: %v", err)
	}
	if result.AccessToken == "" {
		t.Fatalf("empty access token")
	}

	// Refresh also requires the secret.
	if _, err := dir.RefreshOAuthToken(ctx, "conf-client", "", result.RefreshToken); !errors.Is(err, directory.ErrClientAuthenticationFailed) {
		t.Fatalf("refresh without client_secret = %v, want ErrClientAuthenticationFailed", err)
	}
	if _, err := dir.RefreshOAuthToken(ctx, "conf-client", secret, result.RefreshToken); err != nil {
		t.Fatalf("refresh with correct client_secret: %v", err)
	}
}

// TestOAuthClient_ScopeRestriction_GrantsOnlyRegisteredScopes proves a
// bearer token grants exactly the scopes it was issued for, and no
// more: a client registered with a narrower scope set than the default
// end-user set never has that widened at issuance.
func TestOAuthClient_ScopeRestriction_GrantsOnlyRegisteredScopes(t *testing.T) {
	ctx := context.Background()
	dir, _, clk := newDir(t)
	if _, err := dir.CreatePrincipal(ctx, "scoped-client@example.test", "correct-horse-staple"); err != nil {
		t.Fatalf("create principal: %v", err)
	}
	if _, _, err := dir.RegisterOAuthClient(ctx, directory.OAuthClientRegistration{
		ClientID:     "scoped-client",
		RedirectURIs: []string{"https://example.test/cb"},
		Scopes:       []auth.Scope{auth.ScopeMailSend},
	}); err != nil {
		t.Fatalf("RegisterOAuthClient: %v", err)
	}

	verifier, challenge := newPKCE(t)
	redirectURI := "https://example.test/cb"
	req := directory.AuthorizeRequest{
		ClientID: "scoped-client", RedirectURI: redirectURI,
		CodeChallenge: challenge, CodeChallengeMethod: "S256",
		CSRFToken: "csrf", ExpiresAt: clk.Now().Add(directory.AuthorizeRequestTTL),
	}
	code, err := dir.IssueAuthorizationCode(ctx, "scoped-client@example.test", "correct-horse-staple", "", req)
	if err != nil {
		t.Fatalf("IssueAuthorizationCode: %v", err)
	}
	result, err := dir.ExchangeAuthorizationCode(ctx, "scoped-client", "", code, redirectURI, verifier)
	if err != nil {
		t.Fatalf("ExchangeAuthorizationCode: %v", err)
	}
	if len(result.Scope) != 1 || result.Scope[0] != auth.ScopeMailSend {
		t.Fatalf("issued scope = %v, want exactly [%q]", result.Scope, auth.ScopeMailSend)
	}
	for _, sc := range result.Scope {
		if sc == auth.ScopeAdmin {
			t.Fatalf("a native-client grant must never carry admin scope: %v", result.Scope)
		}
	}
}

// TestOAuthClient_Delete_RefusesNewAuthorization proves a deleted
// client is refused immediately: a pre-signed AuthorizeRequest carrying
// the now-deleted client_id (the same shape the /oauth2/authorize POST
// leg decodes) can no longer mint a code once the client is removed
// from the registry, even though the signed token itself would still
// pass its own TTL/signature check.
func TestOAuthClient_Delete_RefusesNewAuthorization(t *testing.T) {
	ctx := context.Background()
	dir, _, clk := newDir(t)
	if _, err := dir.CreatePrincipal(ctx, "del-client@example.test", "correct-horse-staple"); err != nil {
		t.Fatalf("create principal: %v", err)
	}
	if _, _, err := dir.RegisterOAuthClient(ctx, directory.OAuthClientRegistration{
		ClientID: "del-client", RedirectURIs: []string{"https://example.test/cb"},
	}); err != nil {
		t.Fatalf("RegisterOAuthClient: %v", err)
	}

	_, challenge := newPKCE(t)
	req := directory.AuthorizeRequest{
		ClientID: "del-client", RedirectURI: "https://example.test/cb",
		CodeChallenge: challenge, CodeChallengeMethod: "S256",
		CSRFToken: "csrf", ExpiresAt: clk.Now().Add(directory.AuthorizeRequestTTL),
	}

	if err := dir.DeleteOAuthClient(ctx, "del-client"); err != nil {
		t.Fatalf("DeleteOAuthClient: %v", err)
	}

	if _, err := dir.IssueAuthorizationCode(ctx, "del-client@example.test", "correct-horse-staple", "", req); !errors.Is(err, directory.ErrUnknownOAuthClient) {
		t.Fatalf("IssueAuthorizationCode for deleted client = %v, want ErrUnknownOAuthClient", err)
	}
	if _, err := dir.LookupOAuthClient(ctx, "del-client"); !errors.Is(err, directory.ErrUnknownOAuthClient) {
		t.Fatalf("LookupOAuthClient(deleted) = %v, want ErrUnknownOAuthClient", err)
	}
	if err := dir.DeleteOAuthClient(ctx, "del-client"); !errors.Is(err, directory.ErrUnknownOAuthClient) {
		t.Fatalf("DeleteOAuthClient(already deleted) = %v, want ErrUnknownOAuthClient", err)
	}
}

// TestOAuthClient_TOTPGate_NotWeakenedByRegistry is the split's explicit
// callout: a DB-registered client must require the SAME authentication
// strength as web login (issue #228) -- registering an arbitrary new
// client through this registry must never become a weaker back door
// around the TOTP gate a TOTP-enrolled principal's web session already
// enforces. Exercised against a freshly registered, non-"herold-android"
// client so the property is proven for the general registry, not just
// the one client every other test in this package happens to use.
func TestOAuthClient_TOTPGate_NotWeakenedByRegistry(t *testing.T) {
	ctx := context.Background()
	dir, _, clk := newDir(t)
	pid, err := dir.CreatePrincipal(ctx, "totp-registry@example.test", "correct-horse-staple")
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	secret, _, err := dir.EnrollTOTP(ctx, pid)
	if err != nil {
		t.Fatalf("EnrollTOTP: %v", err)
	}
	code0 := mustGenerate(t, secret, clk.Now())
	if err := dir.ConfirmTOTP(ctx, pid, code0); err != nil {
		t.Fatalf("ConfirmTOTP: %v", err)
	}

	if _, _, err := dir.RegisterOAuthClient(ctx, directory.OAuthClientRegistration{
		ClientID: "arbitrary-new-client", RedirectURIs: []string{"https://example.test/cb"},
	}); err != nil {
		t.Fatalf("RegisterOAuthClient: %v", err)
	}

	_, challenge := newPKCE(t)
	req := directory.AuthorizeRequest{
		ClientID: "arbitrary-new-client", RedirectURI: "https://example.test/cb",
		CodeChallenge: challenge, CodeChallengeMethod: "S256",
		CSRFToken: "csrf", ExpiresAt: clk.Now().Add(directory.AuthorizeRequestTTL),
	}

	// Password alone, against a TOTP-enrolled principal, on a
	// freshly-registered client: must be refused exactly like
	// IssueDeviceToken and the web JSON login endpoint refuse it.
	if _, err := dir.IssueAuthorizationCode(ctx, "totp-registry@example.test", "correct-horse-staple", "", req); !errors.Is(err, directory.ErrTOTPRequired) {
		t.Fatalf("password-only issuance against a TOTP-enrolled principal = %v, want ErrTOTPRequired (the grant must not be a weaker path than web login)", err)
	}
	// A wrong code is refused too, not just an absent one.
	if _, err := dir.IssueAuthorizationCode(ctx, "totp-registry@example.test", "correct-horse-staple", "000000", req); err == nil {
		t.Fatalf("wrong totp code should not succeed")
	}

	clk.Advance(time.Second)
	code1 := mustGenerate(t, secret, clk.Now())
	if _, err := dir.IssueAuthorizationCode(ctx, "totp-registry@example.test", "correct-horse-staple", code1, req); err != nil {
		t.Fatalf("issuance with a valid totp code should succeed: %v", err)
	}
}
