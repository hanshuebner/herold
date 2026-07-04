package protoadmin

// submissionGetResponse is the wire form returned by
// GET /api/v1/identities/{id}/submission (REQ-AUTH-EXT-SUBMIT-04).
//
// No credential material is ever included: passwords, tokens, and ciphertext
// blobs are omitted by design. The suite computes the masked preview
// (e.g. "Password configured", "Connected via OAuth") from the auth_method
// field alone (REQ-MAIL-SUBMIT-08).
type submissionGetResponse struct {
	// Configured is true when a submission config row exists for the identity.
	// When false the remaining fields are zero/empty.
	Configured bool `json:"configured"`
	// SubmitHost is the SMTP server hostname.
	SubmitHost string `json:"submit_host,omitempty"`
	// SubmitPort is the TCP port (typically 587 or 465).
	SubmitPort int `json:"submit_port,omitempty"`
	// SubmitSecurity is one of "implicit_tls", "starttls", or "none".
	SubmitSecurity string `json:"submit_security,omitempty"`
	// SubmitAuthMethod is one of "password" or "oauth2".
	SubmitAuthMethod string `json:"submit_auth_method,omitempty"`
	// State is the current health state per REQ-AUTH-EXT-SUBMIT-07.
	State string `json:"state,omitempty"`
	// AvailableOAuthProviders is the sorted list of OAuth provider ids that
	// are configured on this server (ClientSecret non-empty). The Suite
	// renders one OAuth sign-in button per id. The list is identity-independent
	// and present in every response (re #73).
	AvailableOAuthProviders []string `json:"available_oauth_providers"`
	// DomainAuthoritative is true when the identity's email domain is locally
	// authoritative on this server. The Suite uses this to recommend external
	// submission over the local server when false (re #74).
	DomainAuthoritative bool `json:"domain_authoritative"`
}

// submissionTestRequest is the optional wire form accepted by
// POST /api/v1/identities/{id}/submission/test (re #122).
//
// When the body is absent or empty, the handler defaults to the identity's
// own address as both MAIL FROM and RCPT TO (the original self-addressed
// probe behaviour). When To is non-empty it overrides the RCPT TO, allowing
// the caller to direct the test message to a different address — typically
// the user's own primary inbox.
type submissionTestRequest struct {
	// To is the RFC 5321 addr-spec to use as RCPT TO. Optional: when absent
	// or empty the handler falls back to the identity's own email address.
	To string `json:"to,omitempty"`
}

// submissionPutRequest is the wire form accepted by
// PUT /api/v1/identities/{id}/submission. The credential fields are
// consumed exactly once: the server seals and stores them, then discards
// the plaintext immediately.
type submissionPutRequest struct {
	// SubmitHost is the external SMTP server hostname.
	SubmitHost string `json:"submit_host"`
	// SubmitPort is the TCP port.
	SubmitPort int `json:"submit_port"`
	// SubmitSecurity is the TLS posture: "implicit_tls", "starttls", or "none".
	SubmitSecurity string `json:"submit_security"`
	// SubmitAuthMethod selects the SASL method: "password" or "oauth2".
	SubmitAuthMethod string `json:"submit_auth_method"`
	// Password is the plaintext password or app-specific password. Present only
	// when SubmitAuthMethod == "password". Consumed once; never stored.
	Password string `json:"password,omitempty"`
	// OAuthAccessToken is the OAuth 2.0 access token. Present only when
	// SubmitAuthMethod == "oauth2". Consumed once; never stored.
	OAuthAccessToken string `json:"oauth_access_token,omitempty"`
	// OAuthRefreshToken is the OAuth 2.0 refresh token. Optional even for
	// oauth2 (short-lived token-only flows). Consumed once; never stored.
	OAuthRefreshToken string `json:"oauth_refresh_token,omitempty"`
	// OAuthTokenEndpoint is the provider's token endpoint URL, used for future
	// refresh calls. Required when OAuthRefreshToken is set.
	OAuthTokenEndpoint string `json:"oauth_token_endpoint,omitempty"`
	// OAuthClientID is the per-user identifier in the XOAUTH2 bearer string
	// (typically the user's email at the provider). Required for oauth2.
	OAuthClientID string `json:"oauth_client_id,omitempty"`
	// AuthUser is the SMTP AUTH username used for the probe. For password auth
	// this is the account email at the external provider; for oauth2 it is the
	// same as OAuthClientID. If empty the probe uses OAuthClientID for oauth2
	// and SubmitHost as a fallback for password.
	AuthUser string `json:"auth_user,omitempty"`
}
