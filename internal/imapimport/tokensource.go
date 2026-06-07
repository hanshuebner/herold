package imapimport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/hanshuebner/herold/internal/sysconfig"
)

// httpTokenSource is the production oauthTokenSource implementation.
// It performs a standard OAuth 2.0 refresh_token grant POST to the
// provider's token endpoint and extracts the access_token from the
// JSON response.
//
// The HTTP client is injectable for tests (pass nil for the default
// http.DefaultClient). The provider's client secret is resolved at
// construction time via sysconfig.ResolveSecret.
type httpTokenSource struct {
	prov       sysconfig.IMAPImportOAuthProvider
	httpClient *http.Client
}

// newHTTPTokenSource constructs a token source for the given OAuth
// provider configuration. httpClient may be nil (uses http.DefaultClient).
func newHTTPTokenSource(prov sysconfig.IMAPImportOAuthProvider, httpClient *http.Client) oauthTokenSource {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &httpTokenSource{prov: prov, httpClient: httpClient}
}

// Token exchanges refreshToken for an access token using the standard
// refresh_token grant. It NEVER logs the refresh token or the returned
// access token. (REQ-IMAP-IMP-71.)
func (s *httpTokenSource) Token(ctx context.Context, refreshToken string) (string, error) {
	if s.prov.TokenEndpoint == "" {
		return "", fmt.Errorf("imapimport: OAuth provider token_endpoint is not configured")
	}
	if s.prov.ClientID == "" {
		return "", fmt.Errorf("imapimport: OAuth provider client_id is not configured")
	}

	// Resolve the client secret reference (REQ-IMAP-IMP-03; STANDARDS §9).
	clientSecret, err := sysconfig.ResolveSecret(s.prov.ClientSecretRef)
	if err != nil {
		return "", fmt.Errorf("imapimport: failed to resolve OAuth client_secret_ref: %w", err)
	}

	// Build the refresh_token grant request.
	form := url.Values{
		"client_id":     {s.prov.ClientID},
		"client_secret": {clientSecret},
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	}
	if s.prov.Scope != "" {
		form.Set("scope", s.prov.Scope)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.prov.TokenEndpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("imapimport: building token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("imapimport: token endpoint request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return "", fmt.Errorf("imapimport: reading token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		// Include the status code but NOT the body (it may contain
		// sensitive token values). (REQ-IMAP-IMP-71.)
		return "", fmt.Errorf("imapimport: token endpoint returned HTTP %d", resp.StatusCode)
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("imapimport: parsing token response: %w", err)
	}
	if tokenResp.Error != "" {
		return "", fmt.Errorf("imapimport: token endpoint error: %s", tokenResp.Error)
	}
	if tokenResp.AccessToken == "" {
		return "", fmt.Errorf("imapimport: token endpoint returned empty access_token")
	}

	return tokenResp.AccessToken, nil
}
