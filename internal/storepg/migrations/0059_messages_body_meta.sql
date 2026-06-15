-- 0059_messages_body_meta.sql — precomputed message body metadata.
-- Mirrors storesqlite/migrations/0059_messages_body_meta.sql.
-- Postgres idioms: BOOLEAN for boolean columns, TEXT for preview.
--
-- Column semantics identical to the SQLite mirror; see that file for
-- the full rationale.
--
-- Forward-only.

ALTER TABLE messages ADD COLUMN preview TEXT NOT NULL DEFAULT '';
ALTER TABLE messages ADD COLUMN has_attachment BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE messages ADD COLUMN body_meta_computed BOOLEAN NOT NULL DEFAULT FALSE;

CREATE INDEX IF NOT EXISTS idx_messages_body_meta_pending
    ON messages(id)
 WHERE NOT body_meta_computed;
