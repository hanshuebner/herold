-- 0026_managed_rules.sql — ManagedRule structured filter abstraction (Wave 3.15)
--
-- REQ-FLT-01..31: server-side managed-rule abstraction that compiles to Sieve
-- and coexists with the user's hand-written script.
--
-- Two-source composition: the effective script = compiled_managed_preamble +
-- user_script. We store user_script in sieve_scripts.user_script so the
-- user's hand-written Sieve survives re-compilations of the managed preamble
-- without being corrupted. GetSieveScript / SetSieveScript continue to manage
-- the effective (combined) script; the new GetUserSieveScript /
-- SetUserSieveScript manage only the user-written half.

CREATE TABLE managed_rules (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  principal_id   INTEGER NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  name           TEXT    NOT NULL DEFAULT '',
  enabled        INTEGER NOT NULL DEFAULT 1,
  sort_order     INTEGER NOT NULL DEFAULT 0,
  conditions_json TEXT   NOT NULL DEFAULT '[]',
  actions_json   TEXT   NOT NULL DEFAULT '[]',
  created_at_us  INTEGER NOT NULL DEFAULT 0,
  updated_at_us  INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE INDEX idx_managed_rules_principal_order
  ON managed_rules(principal_id, sort_order, id);

-- JMAP state counter for ManagedRule objects (JMAPStateKindManagedRule).
ALTER TABLE jmap_states
  ADD COLUMN managed_rule_state INTEGER NOT NULL DEFAULT 0;

-- User-written Sieve script column added to the existing sieve_scripts table.
-- script remains the effective (combined) script written to disk for the
-- interpreter; user_script is the user-edited half that survives recompilation.
ALTER TABLE sieve_scripts
  ADD COLUMN user_script TEXT NOT NULL DEFAULT '';
