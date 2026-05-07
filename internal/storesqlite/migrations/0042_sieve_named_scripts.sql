-- 0042_sieve_named_scripts.sql — Phase 2 ManageSieve named scripts.
--
-- RFC 5804 ManageSieve operates on named scripts (LISTSCRIPTS,
-- PUTSCRIPT, GETSCRIPT, DELETESCRIPT, RENAMESCRIPT, SETACTIVE), not
-- the single-script-per-principal model the legacy sieve_scripts
-- table encodes. Adding a name column to that table and changing the
-- primary key would force every existing reader through a wider row
-- shape and complicate the SQLite migration.
--
-- Instead this migration adds a separate sieve_named_scripts table
-- that holds the multi-script set ManageSieve operates on. The
-- runtime delivery path continues to read the legacy sieve_scripts
-- table; SETACTIVE syncs the chosen named script's text into the
-- legacy slot so the runtime never sees a partially-migrated state.
-- Existing rows in sieve_scripts are seeded into sieve_named_scripts
-- as a "default" script marked active so an operator who upgrades
-- mid-deployment retains their existing script and can list / rename
-- it via ManageSieve immediately.

CREATE TABLE sieve_named_scripts (
  principal_id   INTEGER NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  name           TEXT    NOT NULL,
  script         TEXT    NOT NULL,
  is_active      INTEGER NOT NULL DEFAULT 0,
  updated_at_us  INTEGER NOT NULL,
  PRIMARY KEY (principal_id, name)
) STRICT;

-- Exactly one active script per principal. The partial unique index
-- enforces the invariant at the schema level so a buggy SETACTIVE
-- cannot leave two rows flagged active simultaneously.
CREATE UNIQUE INDEX sieve_named_active_per_principal
  ON sieve_named_scripts(principal_id) WHERE is_active = 1;

-- Backfill: seed a "default" named script for every legacy row. The
-- script's text is copied verbatim and the row is marked active so
-- the runtime path's GetSieveScript continues to return the same
-- text.
INSERT INTO sieve_named_scripts (principal_id, name, script, is_active, updated_at_us)
SELECT principal_id, 'default', script, 1, updated_at_us FROM sieve_scripts;
