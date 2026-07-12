-- 0082_oauth2_native_grant.sql -- OAuth2 authorization-code + PKCE grant
-- for native clients (issue #199, REQ-AND-AUTH-01/02). Mirrors storepg
-- 0082.
--
-- api_keys.expires_at_us: nullable expiry timestamp. Every existing row
-- (operator-issued keys, device tokens, migration-0060 one-shot keys)
-- leaves this NULL, preserving their "revocation is the sole lifecycle
-- control" model unchanged. Only the short-lived OAuth2 access token
-- minted by POST /oauth2/token populates it (default 1 hour). The
-- Bearer-verification path in protojmap/protoadmin rejects a key whose
-- expires_at_us is non-NULL and in the past, on top of the existing
-- hash lookup -- no new verification code path, one added check.
--
-- oauth_auth_codes: single-use authorization codes issued by
-- GET/POST /oauth2/authorize and redeemed exactly once at
-- POST /oauth2/token. Hashed at rest (SHA-256, same primitive as
-- api_keys.hash and device tokens -- opaque high-entropy secret, not a
-- human-chosen password). used_at_us is NULL until the single exchange;
-- a second exchange attempt is a replay (RFC 6749 4.1.2) that revokes
-- family_id via oauth_refresh_tokens. code_challenge/code_challenge_method
-- persist the PKCE parameters captured at /authorize so /token can verify
-- the caller's code_verifier without a second round-trip to the browser
-- leg. FK to principals(id) ON DELETE CASCADE: a deleted principal's
-- outstanding codes are unredeemable.
--
-- oauth_refresh_tokens: one row per node in a rotation chain. Every
-- refresh mints a new row sharing family_id with its predecessor and
-- sets the predecessor's rotated_at_us; presenting an already-rotated
-- (or already-revoked) token again is reuse detection that revokes every
-- row sharing family_id (REQ-AND-AUTH-02's rotation-with-reuse-detection
-- requirement). access_key_id references the api_keys row minted
-- alongside this refresh token (ON DELETE SET NULL so a principal
-- deletion cascade on api_keys doesn't orphan the FK); family revocation
-- deletes that api_keys row directly to invalidate the still-live access
-- token immediately rather than waiting out its short TTL.
--
-- Forward-only.

ALTER TABLE api_keys ADD COLUMN expires_at_us INTEGER;

CREATE TABLE oauth_auth_codes (
  id                    INTEGER PRIMARY KEY AUTOINCREMENT,
  hash                  TEXT    NOT NULL UNIQUE,
  client_id             TEXT    NOT NULL,
  principal_id          INTEGER NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  redirect_uri          TEXT    NOT NULL,
  code_challenge        TEXT    NOT NULL,
  code_challenge_method TEXT    NOT NULL,
  scope_json            TEXT    NOT NULL,
  family_id             TEXT    NOT NULL,
  created_at_us         INTEGER NOT NULL,
  expires_at_us         INTEGER NOT NULL,
  used_at_us            INTEGER
) STRICT;

CREATE TABLE oauth_refresh_tokens (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  hash          TEXT    NOT NULL UNIQUE,
  family_id     TEXT    NOT NULL,
  principal_id  INTEGER NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  client_id     TEXT    NOT NULL,
  scope_json    TEXT    NOT NULL,
  access_key_id INTEGER REFERENCES api_keys(id) ON DELETE SET NULL,
  created_at_us INTEGER NOT NULL,
  expires_at_us INTEGER NOT NULL,
  rotated_at_us INTEGER,
  revoked_at_us INTEGER
) STRICT;

CREATE INDEX idx_oauth_refresh_tokens_family ON oauth_refresh_tokens(family_id);
