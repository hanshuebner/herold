package directory

// oauth2client.go is the OAuth2 client registry for the native-client
// authorization-code + PKCE grant (issue #199, REQ-AND-AUTH-01/02).
//
// Clients are a DB-backed registry (store.OAuthClient / migration
// 0092), not a compiled-in Go map: an operator registers a client
// through the admin REST surface (RegisterOAuthClient, wired up by
// protoadmin's POST /api/v1/oauth2/clients) and it is usable at
// GET/POST /oauth2/authorize and POST /oauth2/token on the next
// request -- no herold rebuild or restart required.
//
// Every registered client is "public" per RFC 8252 (incapable of
// holding a secret; PKCE is mandatory rather than optional) unless the
// operator explicitly registers it as confidential, in which case the
// token endpoint additionally requires a matching client_secret
// (ClientSecretPrefix-prefixed, generated server-side and returned
// exactly once at registration time, hashed at rest the same way as a
// device token).

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/hanshuebner/herold/internal/auth"
	"github.com/hanshuebner/herold/internal/store"
)

// ClientSecretPrefix distinguishes an OAuth2 client-secret plaintext
// from every other bearer-shaped secret this package mints, so a
// client secret can never be accidentally accepted where an access /
// refresh / device token or code is expected (none of those verification
// paths look up the oauth_clients table).
const ClientSecretPrefix = "hcs_"

// Sentinel errors for the OAuth2 client registry.
var (
	// ErrOAuthClientExists is returned by RegisterOAuthClient when
	// ClientID is already registered.
	ErrOAuthClientExists = errors.New("directory: oauth2 client_id already registered")

	// ErrClientAuthenticationFailed is returned by
	// ExchangeAuthorizationCode / RefreshOAuthToken when a confidential
	// client's presented client_secret does not match the registered
	// hash (RFC 6749 §5.2 "invalid_client").
	ErrClientAuthenticationFailed = errors.New("directory: oauth2 client authentication failed")

	// ErrOAuthClientInvalid is returned by RegisterOAuthClient /
	// UpdateOAuthClient when the request itself is malformed (missing
	// redirect_uri, or an attempt to register auth.ScopeAdmin) rather
	// than a store-level conflict or not-found condition. Wrapped so
	// the HTTP layer can map it to 400 instead of 500.
	ErrOAuthClientInvalid = errors.New("directory: invalid oauth2 client registration")
)

// OAuthClient is the directory-level view of a registered OAuth2
// client: everything ValidateRedirectURI and the grant's issuance
// paths need, minus the secret hash (kept unexported so it can never
// leak through a DTO built carelessly from this struct — callers that
// need to verify a presented secret go through verifyClientSecret).
type OAuthClient struct {
	// ID is the client_id value the client presents.
	ID string
	// Name is an operator-facing label.
	Name string
	// RedirectURIs is the exact set of URIs this client may register
	// at /oauth2/authorize. Every entry MUST be one of:
	//   - an exact string (custom-scheme redirect, e.g.
	//     "net.netzhansa.herold:/oauth2redirect") -- matched byte-for-byte.
	//   - a loopback template "http://127.0.0.1/<path>" or
	//     "http://[::1]/<path>" -- matched on scheme+host+path, ignoring
	//     port (RFC 8252 §7.3: the OS assigns an ephemeral port to a
	//     loopback listener, so the AS cannot pin it in advance).
	RedirectURIs []string
	// Scopes constrains the scope set granted to tokens issued to this
	// client. Empty means "the default end-user scope set"
	// (auth.AllEndUserScopes). Never includes auth.ScopeAdmin: this
	// grant never issues an admin-scoped token, regardless of what a
	// client's registered scopes say (RegisterOAuthClient rejects an
	// attempt to register ScopeAdmin).
	Scopes []auth.Scope
	// Public is true for a client incapable of holding a secret (the
	// only kind PKCE alone is designed to secure). False marks a
	// confidential client whose token-endpoint requests must also
	// present a matching client_secret.
	Public bool

	secretHash string
}

// OAuthClientRegistration is the input to RegisterOAuthClient.
type OAuthClientRegistration struct {
	ClientID     string
	Name         string
	RedirectURIs []string
	// Scopes constrains issued tokens' scope set; empty means the
	// default end-user set. auth.ScopeAdmin is rejected.
	Scopes []auth.Scope
	// Confidential registers the client as non-public: the server
	// generates a client secret (returned once in the result) whose
	// hash is persisted, and the token endpoint requires it on every
	// request for this client_id.
	Confidential bool
}

// scopesToStrings/stringsToScopes convert between the wire/storage
// string-slice representation and the validated auth.Scope slice.
func scopesToStrings(scopes []auth.Scope) []string {
	out := make([]string, len(scopes))
	for i, sc := range scopes {
		out[i] = string(sc)
	}
	return out
}

func stringsToScopes(strs []string) ([]auth.Scope, error) {
	out := make([]auth.Scope, 0, len(strs))
	for _, s := range strs {
		sc, err := auth.ParseScope(s)
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, nil
}

func toDirectoryOAuthClient(c store.OAuthClient) (OAuthClient, error) {
	scopes, err := stringsToScopes(c.Scopes)
	if err != nil {
		return OAuthClient{}, fmt.Errorf("directory: oauth2 client %q: %w", c.ClientID, err)
	}
	return OAuthClient{
		ID:           c.ClientID,
		Name:         c.Name,
		RedirectURIs: append([]string(nil), c.RedirectURIs...),
		Scopes:       scopes,
		Public:       c.Public,
		secretHash:   c.ClientSecretHash,
	}, nil
}

// grantedScopes returns the scope set a fresh token issued to client
// should carry: the client's registered Scopes, or auth.AllEndUserScopes
// when the client did not restrict its scopes.
func (c OAuthClient) grantedScopes() []auth.Scope {
	if len(c.Scopes) == 0 {
		return auth.AllEndUserScopes
	}
	return c.Scopes
}

// RegisterOAuthClient adds a new entry to the DB-backed OAuth2 client
// registry (issue #199's "DB-backed OAuth2 client registry" work item).
// Returns ErrOAuthClientExists if reg.ClientID is already registered.
// When reg.Confidential is true, the returned plaintext client secret
// is populated and MUST be shown to the operator exactly once — only
// its hash is persisted.
func (d *Directory) RegisterOAuthClient(ctx context.Context, reg OAuthClientRegistration) (OAuthClient, string, error) {
	if err := ctx.Err(); err != nil {
		return OAuthClient{}, "", err
	}
	if reg.ClientID == "" {
		return OAuthClient{}, "", fmt.Errorf("%w: client_id is required", ErrOAuthClientInvalid)
	}
	if len(reg.RedirectURIs) == 0 {
		return OAuthClient{}, "", fmt.Errorf("%w: at least one redirect_uri is required", ErrOAuthClientInvalid)
	}
	for _, sc := range reg.Scopes {
		if sc == auth.ScopeAdmin {
			return OAuthClient{}, "", fmt.Errorf("%w: scopes MUST NOT include %q", ErrOAuthClientInvalid, auth.ScopeAdmin)
		}
	}

	var plaintext, hash string
	if reg.Confidential {
		var err error
		plaintext, hash, err = generateOpaqueSecret(d.rand, ClientSecretPrefix)
		if err != nil {
			return OAuthClient{}, "", fmt.Errorf("directory: generate oauth2 client secret: %w", err)
		}
	}

	row := store.OAuthClient{
		ClientID:         reg.ClientID,
		Name:             reg.Name,
		RedirectURIs:     append([]string(nil), reg.RedirectURIs...),
		Scopes:           scopesToStrings(reg.Scopes),
		Public:           !reg.Confidential,
		ClientSecretHash: hash,
	}
	inserted, err := d.meta.InsertOAuthClient(ctx, row)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			return OAuthClient{}, "", ErrOAuthClientExists
		}
		return OAuthClient{}, "", fmt.Errorf("directory: register oauth2 client: %w", err)
	}

	client, err := toDirectoryOAuthClient(inserted)
	if err != nil {
		return OAuthClient{}, "", err
	}
	return client, plaintext, nil
}

// LookupOAuthClient returns the registered client for clientID, or
// ErrUnknownOAuthClient when the client is not registered.
func (d *Directory) LookupOAuthClient(ctx context.Context, clientID string) (OAuthClient, error) {
	if err := ctx.Err(); err != nil {
		return OAuthClient{}, err
	}
	row, err := d.meta.GetOAuthClient(ctx, clientID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return OAuthClient{}, ErrUnknownOAuthClient
		}
		return OAuthClient{}, fmt.Errorf("directory: lookup oauth2 client: %w", err)
	}
	return toDirectoryOAuthClient(row)
}

// ListOAuthClients returns every registered client, in ascending
// ClientID order.
func (d *Directory) ListOAuthClients(ctx context.Context) ([]OAuthClient, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := d.meta.ListOAuthClients(ctx)
	if err != nil {
		return nil, fmt.Errorf("directory: list oauth2 clients: %w", err)
	}
	out := make([]OAuthClient, 0, len(rows))
	for _, row := range rows {
		c, err := toDirectoryOAuthClient(row)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// UpdateOAuthClient replaces the mutable fields (Name, RedirectURIs,
// Scopes) of an already-registered client. Public/confidential status
// and the client secret are immutable after registration — delete and
// re-register to rotate a secret. Returns ErrUnknownOAuthClient if
// clientID is not registered.
func (d *Directory) UpdateOAuthClient(ctx context.Context, clientID, name string, redirectURIs []string, scopes []auth.Scope) (OAuthClient, error) {
	if err := ctx.Err(); err != nil {
		return OAuthClient{}, err
	}
	if len(redirectURIs) == 0 {
		return OAuthClient{}, fmt.Errorf("%w: at least one redirect_uri is required", ErrOAuthClientInvalid)
	}
	for _, sc := range scopes {
		if sc == auth.ScopeAdmin {
			return OAuthClient{}, fmt.Errorf("%w: scopes MUST NOT include %q", ErrOAuthClientInvalid, auth.ScopeAdmin)
		}
	}
	existing, err := d.meta.GetOAuthClient(ctx, clientID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return OAuthClient{}, ErrUnknownOAuthClient
		}
		return OAuthClient{}, fmt.Errorf("directory: lookup oauth2 client: %w", err)
	}
	existing.Name = name
	existing.RedirectURIs = append([]string(nil), redirectURIs...)
	existing.Scopes = scopesToStrings(scopes)

	updated, err := d.meta.UpdateOAuthClient(ctx, existing)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return OAuthClient{}, ErrUnknownOAuthClient
		}
		return OAuthClient{}, fmt.Errorf("directory: update oauth2 client: %w", err)
	}
	return toDirectoryOAuthClient(updated)
}

// DeleteOAuthClient removes a registered client. Already-issued tokens
// for that client are unaffected — see store.DeleteOAuthClient — only
// new authorize/token requests for this client_id are refused going
// forward. Returns ErrUnknownOAuthClient if clientID is not registered.
func (d *Directory) DeleteOAuthClient(ctx context.Context, clientID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := d.meta.DeleteOAuthClient(ctx, clientID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrUnknownOAuthClient
		}
		return fmt.Errorf("directory: delete oauth2 client: %w", err)
	}
	return nil
}

// verifyClientSecret reports whether secret authenticates client. A
// public client never requires a secret (verifies trivially, matching
// this grant's PKCE-secured public-client model); a confidential
// client requires a constant-time match against its stored hash.
func verifyClientSecret(client OAuthClient, secret string) bool {
	if client.Public {
		return true
	}
	if secret == "" || client.secretHash == "" {
		return false
	}
	sum := sha256.Sum256([]byte(secret))
	computed := hex.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(computed), []byte(client.secretHash)) == 1
}

// ValidateRedirectURI reports whether redirectURI is an acceptable
// redirect target for client, per the exact-match / loopback rules
// documented on OAuthClient.RedirectURIs. Exact-match prevents an open
// redirect (REQ-AND-AUTH-02's security requirement); the loopback
// exception is the well-known, narrowly-scoped RFC 8252 §7.3 carve-out
// (scheme+host+path pinned, only the ephemeral port varies).
func ValidateRedirectURI(client OAuthClient, redirectURI string) bool {
	for _, registered := range client.RedirectURIs {
		if redirectURI == registered {
			return true
		}
		if host, path, ok := loopbackHostPath(registered); ok {
			if reqHost, reqPath, reqOK := loopbackHostPath(redirectURI); reqOK && reqHost == host && reqPath == path {
				return true
			}
		}
	}
	return false
}

// loopbackHostPath parses uri as "http://<host>[:<port>]<path>" and
// returns (host, path, true) when host is a loopback literal (127.0.0.1
// or [::1]) and the scheme is http. Any port present is intentionally
// discarded by the caller's comparison, not by this function -- it is
// simply never part of the returned tuple.
func loopbackHostPath(uri string) (host, path string, ok bool) {
	const httpPrefix = "http://"
	if len(uri) <= len(httpPrefix) || uri[:len(httpPrefix)] != httpPrefix {
		return "", "", false
	}
	rest := uri[len(httpPrefix):]
	for _, prefix := range []string{"127.0.0.1", "[::1]"} {
		if len(rest) < len(prefix) || rest[:len(prefix)] != prefix {
			continue
		}
		h := prefix
		tail := rest[len(prefix):]
		// tail is one of: "" | "/path..." | ":port" | ":port/path..."
		if len(tail) > 0 && tail[0] == ':' {
			i := 0
			for i < len(tail) && tail[i] != '/' {
				i++
			}
			tail = tail[i:]
		}
		if tail == "" {
			tail = "/"
		}
		return h, tail, true
	}
	return "", "", false
}
