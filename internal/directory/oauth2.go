package directory

// oauth2.go implements the OAuth2 authorization-code + PKCE grant for
// native clients (issue #199, REQ-AND-AUTH-01/02): the full flow beyond
// the direct password-for-token exchange devicetoken.go already
// provides. This file is the browser-mediated counterpart: a system
// browser / Custom Tab renders herold's login page (password + TOTP,
// reusing authenticateWithOptionalTOTP), the browser is redirected back
// to the client's registered redirect_uri with a single-use
// authorization code, and the client exchanges that code (plus its PKCE
// code_verifier) for a short-lived access token and a rotating refresh
// token.
//
// Token shapes:
//   - Access token: an "hk_..." Bearer token, bit-for-bit the same
//     mechanism as device tokens and operator API keys (protojmap's and
//     protoadmin's Bearer-verification code paths need zero changes),
//     but with a populated ExpiresAt (AccessTokenTTL, default 1 hour --
//     REQ-AND-AUTH-02/REQ-AUTH-71). GenerateDeviceToken is reused
//     verbatim for the plaintext+hash generation.
//   - Refresh token: a distinct "hr_..." opaque secret, hashed at rest
//     the same way, persisted as a store.OAuthRefreshToken row carrying
//     a FamilyID (the rotation-chain identity). Every refresh rotates:
//     the presented token is marked RotatedAt and a new row (same
//     FamilyID) is minted. Presenting an already-rotated (or revoked)
//     token is reuse detection -- RevokeOAuthRefreshTokenFamily tombstones
//     every row in the family and deletes the live access-token API key,
//     per REQ-AND-AUTH-02's rotation-with-reuse-detection requirement.
//
// Security properties and where they are enforced:
//   - PKCE mandatory, S256 only: ExchangeAuthorizationCode rejects a
//     missing/mismatched code_verifier (verifyPKCE) unconditionally --
//     there is no code path that skips this check.
//   - Single-use authorization codes, short TTL: store.ConsumeOAuthAuthCode
//     is an atomic compare-and-set (used_at_us IS NULL); a replay returns
//     store.ErrConflict, which this file turns into revoking the code's
//     FamilyID (RFC 6749 §4.1.2) before reporting the error.
//   - Refresh rotation + reuse detection: store.MarkOAuthRefreshTokenRotated
//     is the same atomic compare-and-set pattern; alreadyRotated=true
//     revokes the family.
//   - Constant-time comparisons: PKCE challenge/verifier compare via
//     crypto/subtle; token lookups go through the store's hash-column
//     equality (the token itself is a 256-bit random secret, not a
//     low-entropy value an attacker can usefully time-attack one bit at
//     a time via a hash-prefix scan -- the existing API-key model's
//     accepted trust boundary, matching devicetoken.go's `subtle`-free
//     GetAPIKeyByHash reliance elsewhere in the codebase). PKCE
//     verification specifically uses subtle.ConstantTimeCompare because
//     the check happens on every exchange and the "correct" value is
//     attacker-influenced input width, unlike a DB hash lookup.
//   - Redirect-URI exact-match: ValidateRedirectURI (oauth2client.go) at
//     issuance time, and a byte-exact match of the exchange request's
//     redirect_uri against the value recorded on the code row at
//     exchange time (RFC 6749 §4.1.3) -- a code minted for one redirect
//     target can never be redeemed against another.
//   - Tokens hashed at rest: HashDeviceToken (SHA-256) for both access
//     and refresh token plaintexts; only the hash is persisted.
//   - State/CSRF on the browser leg: oauth2_authreq.go's signed request
//     token + double-submit cookie, checked by the protoadmin HTTP layer
//     before authenticateWithOptionalTOTP ever runs.

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/auth"
	"github.com/hanshuebner/herold/internal/store"
)

const (
	// AuthorizationCodeTTL bounds how long an issued code is redeemable.
	// RFC 6749 recommends a maximum of 10 minutes; this deployment uses
	// a much tighter window because the redirect back to the client is
	// immediate (no user-facing delay to accommodate).
	AuthorizationCodeTTL = 60 * time.Second

	// AccessTokenTTL is the OAuth2 access token lifetime (REQ-AND-AUTH-02,
	// REQ-AUTH-71 default: 1 hour).
	AccessTokenTTL = time.Hour

	// RefreshTokenTTL is the OAuth2 refresh token absolute lifetime
	// (REQ-AND-AUTH-02, REQ-AUTH-71 default: 30 days), independent of
	// rotation -- a refresh chain stops working after this long even if
	// used continuously.
	RefreshTokenTTL = 30 * 24 * time.Hour

	// RefreshTokenPrefix distinguishes a refresh-token plaintext from an
	// access-token / device-token / API-key plaintext ("hk_...") so a
	// caller can never accidentally present one where the other is
	// expected, and so a refresh token never accidentally authenticates
	// through the "hk_" Bearer-verification path (it is not an api_keys
	// row at all).
	RefreshTokenPrefix = "hr_"
)

// Sentinel errors for the OAuth2 native-client grant (issue #199).
var (
	// ErrUnknownOAuthClient is returned when client_id does not match a
	// registered OAuthClient.
	ErrUnknownOAuthClient = errors.New("directory: unknown oauth2 client")

	// ErrInvalidRedirectURI is returned when redirect_uri is not one of
	// the client's registered URIs (exact-match / loopback rules).
	ErrInvalidRedirectURI = errors.New("directory: invalid oauth2 redirect_uri")

	// ErrUnsupportedCodeChallengeMethod is returned when
	// code_challenge_method is anything other than "S256". PKCE "plain"
	// is never accepted (REQ-AND-AUTH-02 mandates PKCE; S256-only is
	// this deployment's floor, not a negotiable default).
	ErrUnsupportedCodeChallengeMethod = errors.New("directory: oauth2 code_challenge_method must be S256")

	// ErrInvalidGrant is the generic RFC 6749 §5.2 "invalid_grant"
	// condition: the code/refresh token is unknown, expired, already
	// used, revoked, or does not belong to the presenting client.
	ErrInvalidGrant = errors.New("directory: oauth2 invalid_grant")

	// ErrPKCEMismatch is returned by ExchangeAuthorizationCode when
	// code_verifier does not satisfy the code's recorded code_challenge.
	ErrPKCEMismatch = errors.New("directory: oauth2 pkce verification failed")

	// ErrRefreshReuse is returned by RefreshOAuthToken when the
	// presented refresh token has already been rotated (or revoked) --
	// a replay. The caller has already had the whole rotation family
	// revoked by the time this is returned.
	ErrRefreshReuse = errors.New("directory: oauth2 refresh token reuse detected")
)

// OAuthTokenResult is the token pair returned by both
// ExchangeAuthorizationCode and RefreshOAuthToken.
type OAuthTokenResult struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int // seconds
	Scope        []auth.Scope
}

// verifyPKCE reports whether verifier satisfies challenge under method.
// Only "S256" is accepted; any other method (including RFC 7636's
// "plain") fails closed.
func verifyPKCE(challenge, method, verifier string) bool {
	if method != "S256" || verifier == "" || challenge == "" {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(computed), []byte(challenge)) == 1
}

// IssueAuthorizationCode authenticates (email, password[, totpCode]) --
// the browser-submitted login form -- and, on success, mints a
// single-use authorization code bound to req's client_id, redirect_uri,
// and PKCE code_challenge. It does not itself validate client_id /
// redirect_uri / code_challenge_method; the HTTP layer validates those
// at GET /oauth2/authorize time (before the login form is even
// rendered) via LookupOAuthClient / ValidateRedirectURI, and req is the
// signed, tamper-evident carrier of that already-validated state across
// the browser round trip (oauth2_authreq.go).
//
// Returns the same error classes as IssueDeviceToken/authenticateWithOptionalTOTP:
// ErrUnauthorized, ErrRateLimited, ErrTOTPRequired.
func (d *Directory) IssueAuthorizationCode(ctx context.Context, email, password, totpCode string, req AuthorizeRequest) (code string, err error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	pid, err := d.authenticateWithOptionalTOTP(ctx, email, password, totpCode)
	if err != nil {
		return "", err
	}

	scopeJSON, err := json.Marshal(auth.AllEndUserScopes)
	if err != nil {
		return "", fmt.Errorf("directory: encode oauth2 code scope: %w", err)
	}

	plaintextCode, hash, err := generateOpaqueSecret(d.rand, "")
	if err != nil {
		return "", fmt.Errorf("directory: generate oauth2 code: %w", err)
	}
	familyID, err := generateFamilyID(d.rand)
	if err != nil {
		return "", fmt.Errorf("directory: generate oauth2 family id: %w", err)
	}

	now := d.clk.Now().UTC()
	_, err = d.meta.InsertOAuthAuthCode(ctx, store.OAuthAuthCode{
		Hash:                hash,
		ClientID:            req.ClientID,
		PrincipalID:         pid,
		RedirectURI:         req.RedirectURI,
		CodeChallenge:       req.CodeChallenge,
		CodeChallengeMethod: req.CodeChallengeMethod,
		ScopeJSON:           string(scopeJSON),
		FamilyID:            familyID,
		ExpiresAt:           now.Add(AuthorizationCodeTTL),
	})
	if err != nil {
		return "", fmt.Errorf("directory: insert oauth2 code: %w", err)
	}

	d.audit(ctx, pid, "principal.oauth2.code_issue",
		slog.String("client_id", req.ClientID),
	)

	return plaintextCode, nil
}

// ExchangeAuthorizationCode redeems code for a fresh access + refresh
// token pair. Returns ErrUnknownOAuthClient, ErrInvalidGrant (unknown /
// expired / wrong-client / wrong-redirect-uri / already-used code), or
// ErrPKCEMismatch.
//
// A replayed code (already used) additionally revokes the code's
// FamilyID before ErrInvalidGrant is returned, per RFC 6749 §4.1.2's
// "SHOULD revoke all tokens previously issued based on that
// authorization code" -- reached defensively even though normally no
// tokens exist yet for a not-yet-exchanged code, this also covers the
// race where two concurrent exchange attempts both reach the PKCE check
// before either consumes the code.
func (d *Directory) ExchangeAuthorizationCode(ctx context.Context, clientID, code, redirectURI, codeVerifier string) (OAuthTokenResult, error) {
	if err := ctx.Err(); err != nil {
		return OAuthTokenResult{}, err
	}
	client, ok := LookupOAuthClient(clientID)
	if !ok {
		return OAuthTokenResult{}, ErrUnknownOAuthClient
	}

	hash := HashDeviceToken(code)
	row, err := d.meta.GetOAuthAuthCodeByHash(ctx, hash)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return OAuthTokenResult{}, ErrInvalidGrant
		}
		return OAuthTokenResult{}, fmt.Errorf("directory: load oauth2 code: %w", err)
	}

	now := d.clk.Now().UTC()
	if row.ClientID != clientID || row.RedirectURI != redirectURI || !now.Before(row.ExpiresAt) {
		return OAuthTokenResult{}, ErrInvalidGrant
	}
	if !ValidateRedirectURI(client, redirectURI) {
		return OAuthTokenResult{}, ErrInvalidGrant
	}
	if !verifyPKCE(row.CodeChallenge, row.CodeChallengeMethod, codeVerifier) {
		return OAuthTokenResult{}, ErrPKCEMismatch
	}

	if err := d.meta.ConsumeOAuthAuthCode(ctx, row.ID, now); err != nil {
		if errors.Is(err, store.ErrConflict) {
			// Replay: the code was already exchanged (or a concurrent
			// exchange won the race). Revoke anything derived from it.
			_, _ = d.meta.RevokeOAuthRefreshTokenFamily(ctx, row.FamilyID, now)
			d.audit(ctx, row.PrincipalID, "principal.oauth2.code_replay",
				slog.String("client_id", clientID))
			return OAuthTokenResult{}, ErrInvalidGrant
		}
		return OAuthTokenResult{}, fmt.Errorf("directory: consume oauth2 code: %w", err)
	}

	result, err := d.mintOAuthTokenPair(ctx, row.PrincipalID, clientID, row.FamilyID, row.ScopeJSON, now)
	if err != nil {
		return OAuthTokenResult{}, err
	}
	d.audit(ctx, row.PrincipalID, "principal.oauth2.token_issue",
		slog.String("client_id", clientID), slog.String("grant", "authorization_code"))
	return result, nil
}

// RefreshOAuthToken exchanges a refresh token for a fresh access +
// refresh token pair, rotating the presented token. Returns
// ErrUnknownOAuthClient, ErrInvalidGrant (unknown / expired / revoked /
// wrong-client token), or ErrRefreshReuse (the presented token was
// already rotated -- its whole family has now been revoked).
func (d *Directory) RefreshOAuthToken(ctx context.Context, clientID, refreshToken string) (OAuthTokenResult, error) {
	if err := ctx.Err(); err != nil {
		return OAuthTokenResult{}, err
	}
	if _, ok := LookupOAuthClient(clientID); !ok {
		return OAuthTokenResult{}, ErrUnknownOAuthClient
	}
	if !strings.HasPrefix(refreshToken, RefreshTokenPrefix) {
		return OAuthTokenResult{}, ErrInvalidGrant
	}

	hash := HashDeviceToken(refreshToken)
	row, err := d.meta.GetOAuthRefreshTokenByHash(ctx, hash)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return OAuthTokenResult{}, ErrInvalidGrant
		}
		return OAuthTokenResult{}, fmt.Errorf("directory: load oauth2 refresh token: %w", err)
	}

	now := d.clk.Now().UTC()
	if row.ClientID != clientID || !row.RevokedAt.IsZero() || !now.Before(row.ExpiresAt) {
		return OAuthTokenResult{}, ErrInvalidGrant
	}

	alreadyRotated, err := d.meta.MarkOAuthRefreshTokenRotated(ctx, row.ID, now)
	if err != nil {
		return OAuthTokenResult{}, fmt.Errorf("directory: rotate oauth2 refresh token: %w", err)
	}
	if alreadyRotated {
		// Reuse: this token was already exchanged for its successor (or
		// lost a rotation race). Revoke the whole chain immediately.
		_, _ = d.meta.RevokeOAuthRefreshTokenFamily(ctx, row.FamilyID, now)
		d.audit(ctx, row.PrincipalID, "principal.oauth2.refresh_reuse",
			slog.String("client_id", clientID))
		return OAuthTokenResult{}, ErrRefreshReuse
	}

	result, err := d.mintOAuthTokenPair(ctx, row.PrincipalID, clientID, row.FamilyID, row.ScopeJSON, now)
	if err != nil {
		return OAuthTokenResult{}, err
	}

	// Best-effort: delete the access-token API key this refresh token
	// was paired with. It is short-lived (AccessTokenTTL) and would
	// expire on its own, but deleting it immediately on rotation keeps
	// exactly one live access token per chain generation.
	if row.AccessKeyID != 0 {
		_ = d.meta.DeleteAPIKey(ctx, row.AccessKeyID)
	}

	d.audit(ctx, row.PrincipalID, "principal.oauth2.token_issue",
		slog.String("client_id", clientID), slog.String("grant", "refresh_token"))
	return result, nil
}

// mintOAuthTokenPair mints the api_keys-backed access token and the
// oauth_refresh_tokens-backed refresh token sharing familyID, both
// scoped by scopeJSON.
func (d *Directory) mintOAuthTokenPair(ctx context.Context, pid PrincipalID, clientID, familyID, scopeJSON string, now time.Time) (OAuthTokenResult, error) {
	atPlain, atHash, err := GenerateDeviceToken(d.rand)
	if err != nil {
		return OAuthTokenResult{}, fmt.Errorf("directory: generate oauth2 access token: %w", err)
	}
	insertedKey, err := d.meta.InsertAPIKey(ctx, store.APIKey{
		PrincipalID: pid,
		Hash:        atHash,
		Name:        deviceTokenNamePrefix + "oauth2:" + clientID,
		ScopeJSON:   scopeJSON,
		ExpiresAt:   now.Add(AccessTokenTTL),
	})
	if err != nil {
		return OAuthTokenResult{}, fmt.Errorf("directory: insert oauth2 access token: %w", err)
	}

	rtPlain, rtHash, err := generateOpaqueSecret(d.rand, RefreshTokenPrefix)
	if err != nil {
		return OAuthTokenResult{}, fmt.Errorf("directory: generate oauth2 refresh token: %w", err)
	}
	if _, err := d.meta.InsertOAuthRefreshToken(ctx, store.OAuthRefreshToken{
		Hash:        rtHash,
		FamilyID:    familyID,
		PrincipalID: pid,
		ClientID:    clientID,
		ScopeJSON:   scopeJSON,
		AccessKeyID: insertedKey.ID,
		ExpiresAt:   now.Add(RefreshTokenTTL),
	}); err != nil {
		return OAuthTokenResult{}, fmt.Errorf("directory: insert oauth2 refresh token: %w", err)
	}

	var scopes []auth.Scope
	_ = json.Unmarshal([]byte(scopeJSON), &scopes)

	return OAuthTokenResult{
		AccessToken:  atPlain,
		RefreshToken: rtPlain,
		ExpiresIn:    int(AccessTokenTTL.Seconds()),
		Scope:        scopes,
	}, nil
}

// generateOpaqueSecret returns a fresh 256-bit random plaintext (with
// prefix prepended) and its SHA-256 hex hash, using the same shape as
// GenerateDeviceToken but with a caller-chosen prefix. Used for
// authorization codes (prefix "", so a code never collides in shape with
// an "hk_"-namespaced Bearer token) and refresh tokens (RefreshTokenPrefix).
func generateOpaqueSecret(rnd io.Reader, prefix string) (plaintext, hash string, err error) {
	var b [32]byte
	if _, err := io.ReadFull(rnd, b[:]); err != nil {
		return "", "", fmt.Errorf("directory: read random: %w", err)
	}
	plaintext = prefix + base64.RawURLEncoding.EncodeToString(b[:])
	sum := sha256.Sum256([]byte(plaintext))
	return plaintext, hex.EncodeToString(sum[:]), nil
}

// generateFamilyID returns a fresh random rotation-chain identifier.
// It is an opaque label (never presented to a client or compared as a
// secret against attacker input), so unlike generateOpaqueSecret it has
// no paired hash.
func generateFamilyID(rnd io.Reader) (string, error) {
	var b [16]byte
	if _, err := io.ReadFull(rnd, b[:]); err != nil {
		return "", fmt.Errorf("directory: read random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
