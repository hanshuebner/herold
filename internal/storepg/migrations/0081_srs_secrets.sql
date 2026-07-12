-- 0081_srs_secrets.sql -- keyed-MAC secrets for SRS (Sender Rewriting
-- Scheme) return-path rewriting on forwarded mail (issue #204). Mirrors
-- storesqlite/migrations/0081_srs_secrets.sql. Postgres idioms: BIGINT
-- GENERATED ALWAYS AS IDENTITY id, BYTEA for the secret.
--
-- One row per secret. Rotation is additive: inserting a new secret makes it
-- the signing secret (highest id); older rows stay so an SRS address signed
-- before a rotation still verifies until it ages out on its own SRS0
-- timestamp. Secret is stored in plaintext, mirroring the dkim_keys
-- (migration 0004) precedent -- the store itself is the trust boundary.
--
-- Forward-only.

CREATE TABLE srs_secrets (
  id            BIGINT  GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  secret        BYTEA   NOT NULL,
  created_at_us BIGINT  NOT NULL
);
