-- 0083_oidc_claim_mapping.sql -- external IdP claim-to-grant mapping
-- (epic #188, REQ-AC-60..70). Mirrors storesqlite/migrations/0083_oidc_claim_mapping.sql.
-- Postgres idioms: BIGSERIAL id, BIGINT for ids/timestamps, native BOOLEAN.
--
-- See storesqlite/migrations/0083_oidc_claim_mapping.sql for the full
-- design commentary (authz_trusted, oidc_claim_allowlist,
-- oidc_claim_mapping_rules).
--
-- Forward-only.

ALTER TABLE oidc_providers ADD COLUMN authz_trusted BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE oidc_claim_allowlist (
  provider_name TEXT NOT NULL REFERENCES oidc_providers(name) ON DELETE CASCADE,
  claim         TEXT NOT NULL,
  PRIMARY KEY (provider_name, claim)
);

CREATE TABLE oidc_claim_mapping_rules (
  id              BIGSERIAL PRIMARY KEY,
  provider_name   TEXT   NOT NULL REFERENCES oidc_providers(name) ON DELETE CASCADE,
  claim           TEXT   NOT NULL,
  match_value     TEXT   NOT NULL,
  resource_kind   TEXT   NOT NULL
                         CHECK(resource_kind IN ('server','domain','list','mailbox')),
  resource_id     TEXT   NOT NULL DEFAULT '',
  level           TEXT   NOT NULL,
  created_by      BIGINT REFERENCES principals(id) ON DELETE SET NULL,
  created_at_us   BIGINT NOT NULL
);

CREATE INDEX idx_oidc_claim_mapping_rules_provider ON oidc_claim_mapping_rules(provider_name);
