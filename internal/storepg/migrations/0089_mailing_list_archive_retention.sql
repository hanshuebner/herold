-- 0089_mailing_list_archive_retention.sql -- adds the S4 archive
-- retention bound columns (epic #187, REQ-MLIST-74). Mirrors
-- internal/storesqlite/migrations/0089_mailing_list_archive_retention.sql;
-- see that file for the full rationale.
--
-- Forward-only.

ALTER TABLE mailing_list ADD COLUMN archive_retention_days BIGINT NOT NULL DEFAULT 0;
ALTER TABLE mailing_list ADD COLUMN archive_retention_max_messages BIGINT NOT NULL DEFAULT 0;
