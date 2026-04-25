-- 0013_chat_features.sql — Phase 2 Wave 2.9.6 Track C: REQ-CHAT-20
-- (per-account / per-conversation edit window), REQ-CHAT-32 (Spaces
-- opt-out of read receipts; DMs always on), REQ-CHAT-92 (retention
-- sweeper). Mirrors storepg 0013. Forward-only and idempotent against
-- the columns added below.
--
-- Three additive columns on chat_conversations:
--
--   read_receipts_enabled — REQ-CHAT-32. DMs ignore the flag (always
--     on per REQ-CHAT-31); Spaces consult it. Default 1 (on).
--   retention_seconds     — REQ-CHAT-92. NULL = use account default;
--     0 = never expire; positive = seconds since created_at_us after
--     which a message is hard-deleted by the retention sweeper.
--   edit_window_seconds   — REQ-CHAT-20. NULL = use account default;
--     0 = no time limit (sender can always edit); positive = seconds
--     after which the body becomes immutable.
--
-- New chat_account_settings table carries the per-principal defaults
-- (15-minute edit window, never-expire retention) so an operator can
-- raise the bar globally for an account without touching every Space.

ALTER TABLE chat_conversations
  ADD COLUMN read_receipts_enabled INTEGER NOT NULL DEFAULT 1;
ALTER TABLE chat_conversations
  ADD COLUMN retention_seconds INTEGER;
ALTER TABLE chat_conversations
  ADD COLUMN edit_window_seconds INTEGER;

CREATE TABLE chat_account_settings (
  principal_id                INTEGER NOT NULL PRIMARY KEY
                              REFERENCES principals(id) ON DELETE CASCADE,
  default_retention_seconds   INTEGER NOT NULL DEFAULT 0,    -- 0 = never expire
  default_edit_window_seconds INTEGER NOT NULL DEFAULT 900,  -- 15 min default per REQ-CHAT-20
  created_at_us               INTEGER NOT NULL,
  updated_at_us               INTEGER NOT NULL
) STRICT;
