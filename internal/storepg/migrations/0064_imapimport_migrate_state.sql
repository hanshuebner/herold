-- 0064_imapimport_migrate_state.sql — complete-migration cutover states
-- (issue #25, REQ-IMAP-IMP-90..96, Wave C).
--
-- The complete-migration cutover adds two lifecycle states to
-- imapimport_account.state: 'migrating' (one-shot complete backfill with
-- authority transferred to herold) and 'migrated' (terminal; herold
-- authoritative, upstream retired). The 0058 CHECK constraint only allowed
-- enabled/disabled/errored, so it is widened here.
--
-- Postgres: drop the auto-named single-column CHECK and re-add the widened
-- one. Mirrors storesqlite/migrations/0064_imapimport_migrate_state.sql,
-- which must rebuild the STRICT table because SQLite cannot ALTER a CHECK.
--
-- Forward-only.

ALTER TABLE imapimport_account
  DROP CONSTRAINT imapimport_account_state_check;

ALTER TABLE imapimport_account
  ADD CONSTRAINT imapimport_account_state_check
  CHECK (state IN ('enabled','disabled','errored','migrating','migrated'));
