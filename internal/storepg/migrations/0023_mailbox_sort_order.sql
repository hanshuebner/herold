-- 0023_mailbox_sort_order.sql — add sort_order to mailboxes
--
-- RFC 8621 §2.1 defines Mailbox.sortOrder as a uint32 that clients use
-- to reorder the mailbox list independently of the name. Herold did not
-- previously persist this field; all mailboxes read back sortOrder 0.
-- This migration adds the column with a default of 0 so existing rows
-- are unaffected.

ALTER TABLE mailboxes ADD COLUMN sort_order BIGINT NOT NULL DEFAULT 0;
