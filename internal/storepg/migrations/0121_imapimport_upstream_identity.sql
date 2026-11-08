-- 0121_imapimport_upstream_identity.sql -- adds the upstream identity
-- columns to imapimport_message_state. Mirrors
-- internal/storesqlite/migrations/0121_imapimport_upstream_identity.sql; see
-- that file for the full rationale (issue #561).
--
-- Forward-only.

ALTER TABLE imapimport_message_state ADD COLUMN upstream_message_id TEXT NOT NULL DEFAULT '';
ALTER TABLE imapimport_message_state ADD COLUMN upstream_size BIGINT NOT NULL DEFAULT 0;
