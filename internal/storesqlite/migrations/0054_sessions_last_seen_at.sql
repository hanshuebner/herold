-- 0054_sessions_last_seen_at.sql — track per-session last-seen time so
-- the admin listener can enforce an idle TTL (REQ-AUTH-72, issue #12
-- slice 3).
--
-- Slice 2 wired sysconfig.UIConfig.AdminIdleTTL through
-- authsession.SessionConfig.IdleTTL on the admin listener. With this
-- column the resolver can compare (now - last_seen_at_us) against the
-- configured idle TTL on every authenticated request and reject the
-- session when the gap exceeds the configured window. The same
-- resolver path bumps last_seen_at_us to "now" on every accepted
-- request, sliding the deadline forward.
--
-- The public listener leaves SessionConfig.IdleTTL = 0 so the column
-- is touched but never consulted as a rejection trigger — semantics
-- identical to today.
--
-- Default value: same as created_at_us so existing rows do not look
-- "expired" the instant the column appears (they fall under whatever
-- absolute deadline the cookie was minted with).
--
-- Forward-only. Mirrors storepg 0054.

ALTER TABLE sessions ADD COLUMN last_seen_at_us INTEGER NOT NULL DEFAULT 0;

UPDATE sessions SET last_seen_at_us = created_at_us WHERE last_seen_at_us = 0;
