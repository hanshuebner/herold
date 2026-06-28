-- 0066_sessions_device_context.sql -- per-session device context columns
-- for the session-listing and revocation surface (REQ-AUTH-73, REQ-AUTH-77,
-- issue #78, issue #80). Mirrors storesqlite 0066.

ALTER TABLE sessions ADD COLUMN user_agent   TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN last_seen_ip TEXT NOT NULL DEFAULT '';
