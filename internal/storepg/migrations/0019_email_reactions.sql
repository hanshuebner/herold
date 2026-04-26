-- 0019_email_reactions.sql -- Phase 3 Wave 3.9:
-- REQ-PROTO-100..103, REQ-FLOW-100..108 (Email reactions).
-- Mirrors storesqlite 0019. Forward-only.
--
-- email_reactions: one row per (email, emoji, principal).
-- A reaction is either present (row exists) or absent (no row);
-- there is no nullable "value" column.

CREATE TABLE email_reactions (
  email_id     BIGINT      NOT NULL,
  emoji        TEXT        NOT NULL,
  principal_id BIGINT      NOT NULL,
  created_at   TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (email_id, emoji, principal_id)
);

CREATE INDEX idx_email_reactions_email_id
  ON email_reactions(email_id);
