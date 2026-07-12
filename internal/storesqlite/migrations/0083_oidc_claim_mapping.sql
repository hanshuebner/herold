-- 0083_oidc_claim_mapping.sql -- external IdP claim-to-grant mapping
-- (epic #188, REQ-AC-60..70; docs/design/server/architecture/15-access-control.md
-- "IdP claim reconciliation"). Layers on the #182 grant substrate
-- (migration 0079) and the #199 OAuth2 grant (migration 0082).
--
-- oidc_providers.authz_trusted: claim-to-grant mapping is inert for a
-- provider until a server:superadmin sets this true (REQ-AC-66).
-- Authorization trust is separate from authentication trust: a provider
-- usable for login is not thereby usable to confer grants.
--
-- oidc_claim_allowlist: per-provider allowlist of claim names ("groups",
-- "roles", ...) a mapping rule may consult (REQ-AC-67). A claim absent
-- from this list can never satisfy a rule, closing the "arbitrary
-- user-influenced claim" smuggling path. FK cascades with the provider.
--
-- oidc_claim_mapping_rules: one rule maps "claim has this value" to a
-- (resource, level) grant (REQ-AC-60). match_value is checked for
-- membership (array-valued claim) or equality (scalar claim) against the
-- named claim -- see internal/authz.ReconcileIdP for the exact match
-- semantics. created_by is the authoring operator; ON DELETE SET NULL
-- because a deleted author's rules become unauthorable (no one to
-- re-validate against) rather than referentially broken -- the
-- evaluator treats a NULL created_by as "no author, always inert"
-- (REQ-AC-68). FK to oidc_providers cascades so removing a provider
-- drops its rules with it.
--
-- Forward-only.

ALTER TABLE oidc_providers ADD COLUMN authz_trusted INTEGER NOT NULL DEFAULT 0;

CREATE TABLE oidc_claim_allowlist (
  provider_name TEXT NOT NULL REFERENCES oidc_providers(name) ON DELETE CASCADE,
  claim         TEXT NOT NULL,
  PRIMARY KEY (provider_name, claim)
) STRICT;

CREATE TABLE oidc_claim_mapping_rules (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  provider_name   TEXT    NOT NULL REFERENCES oidc_providers(name) ON DELETE CASCADE,
  claim           TEXT    NOT NULL,
  match_value     TEXT    NOT NULL,
  resource_kind   TEXT    NOT NULL
                          CHECK(resource_kind IN ('server','domain','list','mailbox')),
  resource_id     TEXT    NOT NULL DEFAULT '',
  level           TEXT    NOT NULL,
  created_by      INTEGER REFERENCES principals(id) ON DELETE SET NULL,
  created_at_us   INTEGER NOT NULL
) STRICT;

CREATE INDEX idx_oidc_claim_mapping_rules_provider ON oidc_claim_mapping_rules(provider_name);
