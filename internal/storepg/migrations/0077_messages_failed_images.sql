-- 0077_messages_failed_images.sql -- retained failed-image state for
-- the server-side retry affordance (issue #162).
-- Mirrors storesqlite/migrations/0077_messages_failed_images.sql; see
-- that file for the full rationale.
--
-- Forward-only.

ALTER TABLE messages ADD COLUMN failed_image_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE messages ADD COLUMN failed_image_state TEXT NOT NULL DEFAULT '';
