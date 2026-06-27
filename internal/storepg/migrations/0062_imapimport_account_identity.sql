-- 0062_imapimport_account_identity.sql — per-identity re-scope of IMAP import
-- (issue #25, decision 10; REQ-IMAP-IMP-01/02).
-- Mirrors storesqlite/migrations/0062_imapimport_account_identity.sql.
--
-- Adds the owning JMAP Identity association to imapimport_account. principal_id
-- stays denormalised for the worker-pool fan-out. Deleting the owning Identity
-- cascades to its import config (REQ-IMAP-IMP-02). identity_id is nullable so
-- legacy principal-scoped rows survive; the partial unique index enforces
-- 0-or-1 account per identity (multiple NULLs are permitted).
--
-- Forward-only.

ALTER TABLE imapimport_account
  ADD COLUMN identity_id TEXT REFERENCES jmap_identities(id) ON DELETE CASCADE;

CREATE UNIQUE INDEX IF NOT EXISTS idx_imapimport_account_identity
  ON imapimport_account(identity_id)
  WHERE identity_id IS NOT NULL;
