-- 0098_mailing_list_held_post_archived_at.sql -- Mirrors
-- internal/storesqlite/migrations/0098_mailing_list_held_post_archived_at.sql;
-- see that file for the full rationale.
--
-- Forward-only.

ALTER TABLE mailing_list_held_post ADD COLUMN archived_at_us BIGINT;
