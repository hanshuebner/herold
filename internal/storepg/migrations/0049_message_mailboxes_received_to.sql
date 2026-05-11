-- 0049_message_mailboxes_received_to.sql — per-recipient envelope
-- annotation on the message-mailbox fan-out row (REQ-FLOW-33..35).
--
-- Mirrors storesqlite/migrations/0049_message_mailboxes_received_to.sql.
-- Empty string is the "pre-feature / unknown" sentinel that the render
-- path treats as "do not inject the X-Herold-Recipient header".
--
-- Forward-only.

ALTER TABLE message_mailboxes
  ADD COLUMN received_to TEXT NOT NULL DEFAULT '';
