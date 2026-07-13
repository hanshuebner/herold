-- 0092_oauth_clients.sql -- DB-backed OAuth2 client registry for the
-- native-client authorization-code + PKCE grant (issue #199,
-- REQ-AND-AUTH-01/02). Mirrors storesqlite 0092. Postgres idioms:
-- BOOLEAN for public, BIGINT for created_at_us.
--
-- See storesqlite/migrations/0092_oauth_clients.sql for the full design
-- commentary.
--
-- Forward-only.

CREATE TABLE oauth_clients (
  client_id          TEXT    PRIMARY KEY,
  name               TEXT    NOT NULL,
  redirect_uris_json TEXT    NOT NULL,
  scopes_json        TEXT    NOT NULL,
  public             BOOLEAN NOT NULL,
  client_secret_hash TEXT,
  created_at_us      BIGINT NOT NULL
);
