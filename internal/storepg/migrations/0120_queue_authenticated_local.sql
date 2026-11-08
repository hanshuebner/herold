-- 0120_queue_authenticated_local.sql -- adds queue.authenticated_local.
-- Mirrors internal/storesqlite/migrations/0120_queue_authenticated_local.sql;
-- see that file for the full rationale (issue #559).
--
-- Forward-only.

ALTER TABLE queue ADD COLUMN authenticated_local BOOLEAN NOT NULL DEFAULT FALSE;
