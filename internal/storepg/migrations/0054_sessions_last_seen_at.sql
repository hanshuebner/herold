-- 0054_sessions_last_seen_at.sql — track per-session last-seen time so
-- the admin listener can enforce an idle TTL (REQ-AUTH-72, issue #12
-- slice 3). Mirrors storesqlite 0054.

ALTER TABLE sessions ADD COLUMN last_seen_at_us BIGINT NOT NULL DEFAULT 0;

UPDATE sessions SET last_seen_at_us = created_at_us WHERE last_seen_at_us = 0;
