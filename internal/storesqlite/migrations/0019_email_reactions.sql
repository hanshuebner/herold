-- 0019_email_reactions.sql -- Phase 3 Wave 3.9:
-- REQ-PROTO-100..103, REQ-FLOW-100..108 (Email reactions).
-- Mirrors storepg 0019. Forward-only.
--
-- email_reactions: one row per (email, emoji, principal).
-- A reaction is either present (row exists) or absent (no row);
-- there is no nullable "value" column.
--
-- created_at_us is stored as microseconds-since-epoch (SQLite integer)
-- to match the existing timestamp encoding convention.

CREATE TABLE email_reactions (
  email_id      INTEGER NOT NULL,
  emoji         TEXT    NOT NULL,
  principal_id  INTEGER NOT NULL,
  created_at_us INTEGER NOT NULL,
  PRIMARY KEY (email_id, emoji, principal_id)
) STRICT;

CREATE INDEX idx_email_reactions_email_id
  ON email_reactions(email_id);
