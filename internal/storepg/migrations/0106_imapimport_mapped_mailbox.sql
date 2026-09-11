-- 0106_imapimport_mapped_mailbox.sql -- separates "the mailbox an upstream
-- folder maps to" from "the mailbox a message was actually placed in" on
-- imapimport_message_state (issue #319).
-- Mirrors storesqlite/migrations/0106_imapimport_mapped_mailbox.sql.
--
-- Forward-only.

ALTER TABLE imapimport_message_state ADD COLUMN mapped_mailbox_id BIGINT NOT NULL DEFAULT 0;
