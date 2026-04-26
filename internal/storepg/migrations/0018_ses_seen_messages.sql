-- 0018_ses_seen_messages.sql -- Phase 3 Wave 3.2:
-- REQ-HOOK-SES-02 (SNS MessageId deduplication, ≥24h retention).
-- Mirrors storesqlite 0018. Forward-only.
--
-- ses_seen_messages: one row per SNS MessageId that herold has
-- successfully processed.  The deduper checks this table on an
-- in-process LRU miss so a server restart does not lose dedupe
-- state across the 24 h minimum window.

CREATE TABLE ses_seen_messages (
  message_id  TEXT        NOT NULL PRIMARY KEY,
  seen_at     TIMESTAMPTZ NOT NULL
);

CREATE INDEX idx_ses_seen_messages_seen_at
  ON ses_seen_messages(seen_at);
