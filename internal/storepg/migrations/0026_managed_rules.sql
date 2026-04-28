-- 0026_managed_rules.sql — ManagedRule structured filter abstraction (Wave 3.15)
--
-- REQ-FLT-01..31: server-side managed-rule abstraction that compiles to Sieve
-- and coexists with the user's hand-written script.

CREATE TABLE managed_rules (
  id             BIGSERIAL PRIMARY KEY,
  principal_id   BIGINT    NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  name           TEXT      NOT NULL DEFAULT '',
  enabled        BOOLEAN   NOT NULL DEFAULT TRUE,
  sort_order     INTEGER   NOT NULL DEFAULT 0,
  conditions_json TEXT     NOT NULL DEFAULT '[]',
  actions_json   TEXT      NOT NULL DEFAULT '[]',
  created_at_us  BIGINT    NOT NULL DEFAULT 0,
  updated_at_us  BIGINT    NOT NULL DEFAULT 0
);

CREATE INDEX idx_managed_rules_principal_order
  ON managed_rules(principal_id, sort_order, id);

-- JMAP state counter for ManagedRule objects (JMAPStateKindManagedRule).
ALTER TABLE jmap_states
  ADD COLUMN managed_rule_state BIGINT NOT NULL DEFAULT 0;

-- User-written Sieve script column: stores the hand-written half separately
-- so it survives recompilation of the managed-rules preamble.
ALTER TABLE sieve_scripts
  ADD COLUMN user_script TEXT NOT NULL DEFAULT '';
