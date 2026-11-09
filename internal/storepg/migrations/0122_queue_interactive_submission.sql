-- 0122_queue_interactive_submission.sql -- adds
-- queue.interactive_submission. Mirrors
-- internal/storesqlite/migrations/0122_queue_interactive_submission.sql;
-- see that file for the full rationale (issue #565).
--
-- Forward-only.

ALTER TABLE queue ADD COLUMN interactive_submission BOOLEAN NOT NULL DEFAULT FALSE;
