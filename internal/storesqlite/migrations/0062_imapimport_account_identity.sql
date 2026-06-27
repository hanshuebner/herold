-- 0062_imapimport_account_identity.sql — per-identity re-scope of IMAP import
-- (issue #25, decision 10; REQ-IMAP-IMP-01/02).
--
-- The shipped imapimport_account table (0058) is principal_id-scoped. Decision
-- 10 re-scopes IMAP import to be the optional inbound half of a per-Identity
-- external-transport identity: each import account is owned by a JMAP Identity
-- (0-or-1 per identity). principal_id stays denormalised for the worker-pool
-- fan-out (REQ-IMAP-IMP-02). Deleting the owning Identity cascades to its
-- import config; the keep-or-purge of the imported mail itself
-- (REQ-IMAP-IMP-102..104) is a separate, explicit operation.
--
-- identity_id is nullable so legacy principal-scoped rows survive the
-- migration; accounts created through the JMAP / admin surfaces carry it. The
-- partial unique index enforces 0-or-1 account per identity (multiple NULLs are
-- permitted — legacy rows do not collide).
--
-- Forward-only. Mirrors storepg 0062.

ALTER TABLE imapimport_account
  ADD COLUMN identity_id TEXT REFERENCES jmap_identities(id) ON DELETE CASCADE;

CREATE UNIQUE INDEX idx_imapimport_account_identity
  ON imapimport_account(identity_id)
  WHERE identity_id IS NOT NULL;
