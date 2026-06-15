-- 0059_messages_body_meta.sql — precomputed message body metadata.
--
-- Adds three columns to the messages table so the JMAP inbox list view
-- can serve Email/get for preview + hasAttachment from a pure metadata
-- query instead of reading and MIME-parsing each message blob.
--
-- Column semantics:
--
--   preview          — The first 256 characters of the plain-text body
--                      (RFC 8621 Email.preview). Empty string when
--                      body_meta_computed is 0 (not yet computed).
--
--   has_attachment   — 1 when the message has at least one non-inline
--                      attachment part; 0 otherwise. Authoritative only
--                      when body_meta_computed is 1.
--
--   body_meta_computed — 0 = preview & has_attachment not yet computed;
--                        1 = computed and authoritative. Existing rows
--                        default to 0; the background sweep worker calls
--                        SetMessageBodyMeta to fill them in descending
--                        id order (newest first = most-relevant first).
--
-- The partial index idx_messages_body_meta_pending over rows where
-- body_meta_computed = 0 makes the sweep worker's
-- ListMessagesNeedingBodyMeta query (ORDER BY id DESC, WHERE id < ?,
-- body_meta_computed = 0) efficient without touching already-computed
-- rows.
--
-- Forward-only. Mirrors storepg 0059.

ALTER TABLE messages ADD COLUMN preview TEXT NOT NULL DEFAULT '';
ALTER TABLE messages ADD COLUMN has_attachment INTEGER NOT NULL DEFAULT 0;
ALTER TABLE messages ADD COLUMN body_meta_computed INTEGER NOT NULL DEFAULT 0;

CREATE INDEX idx_messages_body_meta_pending
    ON messages(id)
 WHERE body_meta_computed = 0;
