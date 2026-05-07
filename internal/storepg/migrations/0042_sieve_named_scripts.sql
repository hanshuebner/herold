-- 0042_sieve_named_scripts.sql — Phase 2 ManageSieve named scripts.
--
-- See internal/storesqlite/migrations/0042_sieve_named_scripts.sql
-- for the rationale; the Postgres variant uses BIGINT for the
-- principal_id and microseconds-since-epoch for updated_at_us to
-- mirror the legacy sieve_scripts table that 0003 created.

CREATE TABLE sieve_named_scripts (
  principal_id   BIGINT  NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  name           TEXT    NOT NULL,
  script         TEXT    NOT NULL,
  is_active      BOOLEAN NOT NULL DEFAULT FALSE,
  updated_at_us  BIGINT  NOT NULL,
  PRIMARY KEY (principal_id, name)
);

CREATE UNIQUE INDEX sieve_named_active_per_principal
  ON sieve_named_scripts(principal_id) WHERE is_active;

-- Backfill: seed a "default" named script for every legacy row. The
-- legacy sieve_scripts table stores updated_at_us as microseconds
-- since the Unix epoch; copy verbatim.
INSERT INTO sieve_named_scripts (principal_id, name, script, is_active, updated_at_us)
SELECT principal_id, 'default', script, TRUE, updated_at_us FROM sieve_scripts;
