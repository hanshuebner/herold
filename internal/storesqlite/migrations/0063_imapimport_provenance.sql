-- 0063_imapimport_provenance.sql — per-account provenance label
-- (issue #25, REQ-IMAP-IMP-100..105).
--
-- Every message ingested by an account is tagged with a per-account
-- provenance label: an extra herold mailbox membership added at ingest
-- alongside the folder-mapped mailbox(es). The label is a normal herold
-- Mailbox (no role) created when the account is first enabled; its id is
-- cached here so the membership-add at ingest and the dedup-safe purge at
-- removal both address it by id (so a rename of the account renames the one
-- label rather than orphaning it).
--
-- provenance_mailbox_id is nullable: NULL until the worker first enables the
-- account and creates the label. The supplementary index on
-- imapimport_message_state(herold_message_id) supports the purge-time
-- "is this message claimed by another import account?" lookup, which keys on
-- the message id across all accounts.
--
-- Forward-only. Mirrors storepg 0063.

ALTER TABLE imapimport_account
  ADD COLUMN provenance_mailbox_id INTEGER;

CREATE INDEX idx_imapimport_message_state_msg
  ON imapimport_message_state(herold_message_id);
