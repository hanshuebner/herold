-- 0107_mailbox_disposition_priority.sql -- category disposition and
-- priority on mailboxes (issue #333). Mirrors
-- storesqlite/migrations/0107_mailbox_disposition_priority.sql.
--
-- Forward-only.

ALTER TABLE mailboxes ADD COLUMN disposition TEXT NOT NULL DEFAULT 'none';
ALTER TABLE mailboxes ADD COLUMN priority BIGINT;
