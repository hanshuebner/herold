-- 0087_queue_header_overlay.sql -- adds queue.header_overlay. Mirrors
-- internal/storesqlite/migrations/0087_queue_header_overlay.sql; see that
-- file for the full rationale.
--
-- Forward-only.

ALTER TABLE queue ADD COLUMN header_overlay TEXT NOT NULL DEFAULT '';
