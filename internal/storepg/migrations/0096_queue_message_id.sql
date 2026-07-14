-- 0096_queue_message_id.sql -- adds queue.message_id. Mirrors
-- internal/storesqlite/migrations/0096_queue_message_id.sql; see that
-- file for the full rationale.
--
-- Forward-only.

ALTER TABLE queue ADD COLUMN message_id TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_queue_message_id ON queue(message_id) WHERE message_id <> '';
