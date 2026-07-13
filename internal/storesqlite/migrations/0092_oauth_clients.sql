-- 0092_oauth_clients.sql -- DB-backed OAuth2 client registry for the
-- native-client authorization-code + PKCE grant (issue #199,
-- REQ-AND-AUTH-01/02). Replaces the compiled-in map in
-- internal/directory/oauth2client.go: an operator registers a client
-- through the admin REST surface (POST /api/v1/oauth2/clients) without
-- a herold rebuild. Mirrors storepg 0092.
--
-- client_id is the operator-chosen value the client presents at
-- GET/POST /oauth2/authorize and POST /oauth2/token; it is the primary
-- key, not a surrogate id, matching the existing oauth_auth_codes /
-- oauth_refresh_tokens rows that already carry client_id as a free-text
-- column (no FK -- a deleted client does not retroactively invalidate
-- tokens already issued to it; see internal/store/store.go
-- DeleteOAuthClient).
--
-- redirect_uris_json / scopes_json: JSON-encoded string arrays,
-- consistent with the scope_json convention already used by
-- oauth_auth_codes and oauth_refresh_tokens in migration 0082. An empty
-- scopes_json array means "the default end-user scope set"
-- (auth.AllEndUserScopes) -- this grant never issues an admin-scoped
-- token regardless of what a client's registered scopes say.
--
-- public / client_secret_hash: registered clients are public (RFC 8252)
-- by default -- incapable of holding a secret, secured by PKCE alone,
-- matching the compiled-in registry's original design decision for the
-- Android client. An operator MAY register a confidential client
-- (public = 0) whose token-endpoint requests must also present a
-- matching client_secret; only its SHA-256 hash is ever persisted, the
-- plaintext is returned exactly once at creation.
--
-- Forward-only.

CREATE TABLE oauth_clients (
  client_id          TEXT    PRIMARY KEY,
  name               TEXT    NOT NULL,
  redirect_uris_json TEXT    NOT NULL,
  scopes_json        TEXT    NOT NULL,
  public             INTEGER NOT NULL,
  client_secret_hash TEXT,
  created_at_us      INTEGER NOT NULL
) STRICT;
