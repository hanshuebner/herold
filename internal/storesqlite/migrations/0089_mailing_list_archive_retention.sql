-- 0089_mailing_list_archive_retention.sql -- adds the S4 archive
-- retention bound columns (epic #187, REQ-MLIST-74).
--
-- archive_retention_days / archive_retention_max_messages follow the same
-- "0 means unset" convention as mailing_list.max_message_size_bytes
-- (migration 0085): 0 is "no bound" (keep every archived post forever),
-- a positive value is the age (days) or count ceiling. archiveretention
-- (internal/archiveretention) sweeps mailing_list.archive_mailbox_id
-- mailboxes against these bounds; a list with no archive
-- (archive_mailbox_id NULL) is never visited regardless of these values.
--
-- Forward-only.

ALTER TABLE mailing_list ADD COLUMN archive_retention_days INTEGER NOT NULL DEFAULT 0;
ALTER TABLE mailing_list ADD COLUMN archive_retention_max_messages INTEGER NOT NULL DEFAULT 0;
