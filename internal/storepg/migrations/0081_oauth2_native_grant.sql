-- 0081_oauth2_native_grant.sql -- OAuth2 authorization-code + PKCE grant
-- for native clients (issue #199, REQ-AND-AUTH-01/02). Mirrors
-- storesqlite 0081. Postgres idioms: BIGSERIAL id, BIGINT for ids/timestamps.
--
-- See storesqlite/migrations/0081_oauth2_native_grant.sql for the full
-- design commentary (api_keys.expires_at_us, oauth_auth_codes,
-- oauth_refresh_tokens).
--
-- Forward-only.

ALTER TABLE api_keys ADD COLUMN expires_at_us BIGINT;

CREATE TABLE oauth_auth_codes (
  id                    BIGSERIAL PRIMARY KEY,
  hash                  TEXT   NOT NULL UNIQUE,
  client_id             TEXT   NOT NULL,
  principal_id          BIGINT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  redirect_uri          TEXT   NOT NULL,
  code_challenge        TEXT   NOT NULL,
  code_challenge_method TEXT   NOT NULL,
  scope_json            TEXT   NOT NULL,
  family_id             TEXT   NOT NULL,
  created_at_us         BIGINT NOT NULL,
  expires_at_us         BIGINT NOT NULL,
  used_at_us            BIGINT
);

CREATE TABLE oauth_refresh_tokens (
  id            BIGSERIAL PRIMARY KEY,
  hash          TEXT   NOT NULL UNIQUE,
  family_id     TEXT   NOT NULL,
  principal_id  BIGINT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  client_id     TEXT   NOT NULL,
  scope_json    TEXT   NOT NULL,
  access_key_id BIGINT REFERENCES api_keys(id) ON DELETE SET NULL,
  created_at_us BIGINT NOT NULL,
  expires_at_us BIGINT NOT NULL,
  rotated_at_us BIGINT,
  revoked_at_us BIGINT
);

CREATE INDEX idx_oauth_refresh_tokens_family ON oauth_refresh_tokens(family_id);
