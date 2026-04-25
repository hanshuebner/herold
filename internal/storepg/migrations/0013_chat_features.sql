-- 0013_chat_features.sql — Phase 2 Wave 2.9.6 Track C: REQ-CHAT-20
-- (per-account / per-conversation edit window), REQ-CHAT-32 (Spaces
-- opt-out of read receipts; DMs always on), REQ-CHAT-92 (retention
-- sweeper). Mirrors storesqlite 0013. Postgres idioms applied where
-- helpful (BOOLEAN); column shapes stay isomorphic with SQLite so the
-- backup/migration tooling moves rows row-for-row across backends.
-- Forward-only.
--
-- See storesqlite/migrations/0013_chat_features.sql for the design
-- notes (NULL semantics for retention_seconds / edit_window_seconds,
-- REQ-CHAT-31/32 split for read_receipts_enabled).

ALTER TABLE chat_conversations
  ADD COLUMN read_receipts_enabled BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE chat_conversations
  ADD COLUMN retention_seconds BIGINT;
ALTER TABLE chat_conversations
  ADD COLUMN edit_window_seconds BIGINT;

CREATE TABLE chat_account_settings (
  principal_id                BIGINT  NOT NULL PRIMARY KEY
                              REFERENCES principals(id) ON DELETE CASCADE,
  default_retention_seconds   BIGINT  NOT NULL DEFAULT 0,
  default_edit_window_seconds BIGINT  NOT NULL DEFAULT 900,
  created_at_us               BIGINT  NOT NULL,
  updated_at_us               BIGINT  NOT NULL
);
