-- 0034_chat_dm_pairs.sql — server-side DM deduplication (re #47).
--
-- Each DM conversation corresponds to exactly one unordered pair of
-- principals. Without a server-side constraint two concurrent
-- Conversation/set calls from the same pair of users can both race past
-- a check-then-insert guard and produce duplicate rows.
--
-- This table holds the canonical mapping. pid_lo < pid_hi is the
-- normalised ordering. The PRIMARY KEY on (pid_lo, pid_hi) causes a
-- unique-constraint violation on any concurrent attempt to create a
-- second DM for the same pair, allowing the handler to detect the race
-- and return the winner instead of a new row.
--
-- Existing DM rows (from before this migration) are left without a
-- chat_dm_pairs entry; InsertDMConversation back-fills them lazily via
-- the FindDMBetween JOIN path before deciding to insert.
--
-- Forward-only.

CREATE TABLE chat_dm_pairs (
  pid_lo          BIGINT  NOT NULL,
  pid_hi          BIGINT  NOT NULL,
  conversation_id BIGINT  NOT NULL REFERENCES chat_conversations(id) ON DELETE CASCADE,
  PRIMARY KEY (pid_lo, pid_hi)
);
