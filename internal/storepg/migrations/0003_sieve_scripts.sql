-- 0003_sieve_scripts.sql — Phase 1 Sieve script persistence.
--
-- Kept isomorphic to storesqlite/migrations/0003_sieve_scripts.sql so
-- the migration tool (REQ-STORE-83) copies rows verbatim. Phase 1 is
-- one active script per principal; RFC 5804 ManageSieve (Phase 2)
-- arrives as an additive migration with a named-scripts table.
--
-- Empty text is represented by the row's absence; GetSieveScript
-- returns ("", nil) when no row exists for the principal.

CREATE TABLE sieve_scripts (
  principal_id   BIGINT  PRIMARY KEY REFERENCES principals(id) ON DELETE CASCADE,
  script         TEXT    NOT NULL,
  updated_at_us  BIGINT  NOT NULL
);
