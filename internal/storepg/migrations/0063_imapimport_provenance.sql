-- 0063_imapimport_provenance.sql — per-account provenance label
-- (issue #25, REQ-IMAP-IMP-100..105).
-- Mirrors storesqlite/migrations/0063_imapimport_provenance.sql.
--
-- Adds the cached provenance-label mailbox id to imapimport_account (NULL until
-- the worker first enables the account and creates the label) and an index on
-- imapimport_message_state(herold_message_id) for the purge-time cross-account
-- claim lookup.
--
-- Forward-only.

ALTER TABLE imapimport_account
  ADD COLUMN provenance_mailbox_id BIGINT;

CREATE INDEX IF NOT EXISTS idx_imapimport_message_state_msg
  ON imapimport_message_state(herold_message_id);
