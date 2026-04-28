-- 0022_messages_env_message_id_index.sql -- storage: thread lookup at ingest
-- Adds an index on messages.env_message_id so that the thread-resolution
-- query executed inside InsertMessage (looking up prior messages by
-- Message-ID to find a matching thread_id) does not require a full-table
-- scan. Without this index the lookup degrades O(N) per delivery.
-- Mirrors storepg 0022. Forward-only (REQ-OPS-130).

CREATE INDEX idx_messages_env_message_id ON messages(env_message_id);
