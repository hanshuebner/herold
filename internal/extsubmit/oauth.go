package extsubmit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/secrets"
	"github.com/hanshuebner/herold/internal/store"
)

// ErrAuthFailed is returned by Refresher.Refresh when the token endpoint
// responds with a 4xx status (invalid_grant, unauthorized_client, etc.).
// Callers should record IdentitySubmissionStateAuthFailed on the store row.
var ErrAuthFailed = errors.New("extsubmit: oauth token refresh: auth failed")

// OAuthClientCredentials carries the OAuth application-level credentials.
// In v1 these come from the operator-level provider configuration (analogous
// to REQ-AUTH-50); the IdentitySubmission row carries the per-user token
// material separately.
type OAuthClientCredentials struct {
	// ClientID is the OAuth client_id registered with the provider.
	ClientID string
	// ClientSecret is the OAuth client_secret.
	ClientSecret string
	// TokenEndpoint is the provider's token endpoint URL.
	TokenEndpoint string
}

// SubmissionUpsertStore is the minimal store interface the Refresher needs:
// a single method to persist the updated IdentitySubmission after a refresh.
// It is satisfied by store.Metadata (via any concrete store backend) and by
// trivial test doubles.
type SubmissionUpsertStore interface {
	UpsertIdentitySubmission(ctx context.Context, sub store.IdentitySubmission) error
}

// Refresher holds the dependency surface needed to refresh an OAuth2 access
// token for one IdentitySubmission and persist the result.
type Refresher struct {
	// Meta is the store used to upsert the updated IdentitySubmission after a
	// successful refresh. Accepts any store.Metadata value or a test double
	// that implements SubmissionUpsertStore.
	Meta SubmissionUpsertStore
	// DataKey is the 32-byte AEAD key used by secrets.Seal / secrets.Open.
	DataKey []byte
	// Now is a clock function injected for deterministic testing.
	// If nil, time.Now is used.
	Now func() time.Time
}

func (r *Refresher) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Refresh obtains a fresh access token for sub. If the current access token
// is not within the refresh window (or has already expired), Refresh contacts
// the token endpoint using sub.OAuthRefreshCT.
//
// On success it seals the new token, updates sub in the store, and returns
// the plaintext access token for immediate use.
//
// On a 4xx response from the token endpoint, Refresh returns ErrAuthFailed.
// Any other error (network, seal/open failure) is returned as-is.
//
// creds carries the application-level client credentials; the per-user
// refresh token is read from sub.OAuthRefreshCT.
func (r *Refresher) Refresh(ctx context.Context, sub store.IdentitySubmission, creds OAuthClientCredentials) (accessToken string, err error) {
	// Decrypt the refresh token.
	refreshPT, err := secrets.Open(r.DataKey, sub.OAuthRefreshCT)
	if err != nil {
		return "", fmt.Errorf("extsubmit: open refresh token: %w", err)
	}
	defer func() {
		// Zero the plaintext slice before returning; the secret lives in
		// memory only for the duration of this call (REQ-AUTH-EXT-SUBMIT-02).
		for i := range refreshPT {
			refreshPT[i] = 0
		}
	}()

	// Use golang.org/x/oauth2 to perform the refresh.
	cfg := &oauth2.Config{
		ClientID:     creds.ClientID,
		ClientSecret: creds.ClientSecret,
		Endpoint: oauth2.Endpoint{
			TokenURL: creds.TokenEndpoint,
		},
	}

	// Build a token with only the refresh_token set so the oauth2 library
	// treats it as expired and performs a refresh_token grant.
	oldToken := &oauth2.Token{
		RefreshToken: string(refreshPT),
		// Expiry in the distant past forces a refresh.
		Expiry: r.now().Add(-time.Hour),
	}
	src := cfg.TokenSource(ctx, oldToken)
	newToken, err := src.Token()
	if err != nil {
		observe.RecordOAuthRefreshOutcome("failure")
		// golang.org/x/oauth2 wraps the HTTP response as an *oauth2.RetrieveError
		// with StatusCode for HTTP errors.
		var rerr *oauth2.RetrieveError
		if errors.As(err, &rerr) {
			if rerr.Response != nil && rerr.Response.StatusCode >= 400 && rerr.Response.StatusCode < 500 {
				return "", fmt.Errorf("%w: %s", ErrAuthFailed, rerr.ErrorDescription)
			}
			// Textual "invalid_grant" without a status code also signals
			// auth failure.
			if strings.Contains(rerr.ErrorCode, "invalid_grant") ||
				strings.Contains(rerr.ErrorCode, "unauthorized_client") {
				return "", fmt.Errorf("%w: %s", ErrAuthFailed, rerr.ErrorCode)
			}
		}
		return "", fmt.Errorf("extsubmit: token refresh: %w", err)
	}
	observe.RecordOAuthRefreshOutcome("success")

	// Seal the new access token.
	newAccessCT, err := secrets.Seal(r.DataKey, []byte(newToken.AccessToken))
	if err != nil {
		return "", fmt.Errorf("extsubmit: seal new access token: %w", err)
	}

	// Update the store row with the new token material.
	updated := sub
	updated.OAuthAccessCT = newAccessCT
	updated.OAuthExpiresAt = newToken.Expiry
	// Refresh again 5 minutes before expiry.
	if !newToken.Expiry.IsZero() {
		updated.RefreshDue = newToken.Expiry.Add(-5 * time.Minute)
	}
	updated.State = store.IdentitySubmissionStateOK
	updated.StateAt = r.now()

	if err := r.Meta.UpsertIdentitySubmission(ctx, updated); err != nil {
		return "", fmt.Errorf("extsubmit: persist refreshed token: %w", err)
	}

	return newToken.AccessToken, nil
}

// openAccessToken decrypts and returns the plaintext access token from sub.
// The caller is responsible for zeroing the returned bytes when done.
func openAccessToken(dataKey []byte, sub store.IdentitySubmission) ([]byte, error) {
	pt, err := secrets.Open(dataKey, sub.OAuthAccessCT)
	if err != nil {
		return nil, fmt.Errorf("extsubmit: open access token: %w", err)
	}
	return pt, nil
}

// openPassword decrypts and returns the plaintext password from sub.
// The caller is responsible for zeroing the returned bytes when done.
func openPassword(dataKey []byte, sub store.IdentitySubmission) ([]byte, error) {
	pt, err := secrets.Open(dataKey, sub.PasswordCT)
	if err != nil {
		return nil, fmt.Errorf("extsubmit: open password: %w", err)
	}
	return pt, nil
}
