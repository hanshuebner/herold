-- 0066_sessions_device_context.sql -- per-session device context columns
-- for the session-listing and revocation surface (REQ-AUTH-73, REQ-AUTH-77,
-- issue #78, issue #80).
--
-- user_agent   TEXT -- HTTP User-Agent header from the login request.
--                      Set once at session creation; not updated on touch.
--                      Allows the session-list endpoint (#80) to show the
--                      browser or mail client that created the session.
--
-- last_seen_ip TEXT -- Client IP of the most recent authenticated request.
--                      Updated on every session touch alongside last_seen_at_us.
--                      Surfaced by the session-list endpoint (#80).
--
-- Both columns default to '' so existing rows are valid post-migration.
--
-- Forward-only. Mirrors storepg 0066.

ALTER TABLE sessions ADD COLUMN user_agent   TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN last_seen_ip TEXT NOT NULL DEFAULT '';
