-- 0003_sieve_scripts.sql — Phase 1 Sieve script persistence.
--
-- Adds one active Sieve script per principal. RFC 5804 ManageSieve
-- arrives in Phase 2 and introduces multiple named scripts with a
-- SETACTIVE verb; Phase 1 keeps the schema deliberately narrow so the
-- migration to multi-script storage is additive (a new scripts table
-- plus a foreign key from this one).
--
-- The row is upserted by admin REST (CRUD) and ManageSieve (once it
-- lands); the SMTP delivery pipeline reads it on every delivery via
-- store.Metadata.GetSieveScript. Empty text is represented by the
-- row's absence, not by an empty string — callers check for
-- ErrNotFound equivalence by the (text, error) shape:
-- GetSieveScript returns ("", nil) when no row exists.

CREATE TABLE sieve_scripts (
  principal_id   INTEGER PRIMARY KEY REFERENCES principals(id) ON DELETE CASCADE,
  script         TEXT    NOT NULL,
  updated_at_us  INTEGER NOT NULL
) STRICT;
