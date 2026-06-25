-- 0060_apikey_oneshot.sql -- one-shot bootstrap API key flag (re #21, re #24).
--
-- Mirrors storesqlite 0060.  Postgres idioms applied (BOOLEAN).
--
-- Adds a one_shot column to api_keys so the bootstrap endpoint (and the
-- herold recover CLI command) can mint keys that are automatically deleted
-- on the first successful TOTP confirm call.  The bootstrap superadmin is
-- the sole exception to mandatory TOTP (REQ-AUTH-44): their initial API
-- key works exactly once to reach the TOTP enroll+confirm endpoints, after
-- which the key is consumed and normal enforcement applies.
--
-- Existing rows default to FALSE (not one-shot).  Forward-only.

ALTER TABLE api_keys ADD COLUMN one_shot BOOLEAN NOT NULL DEFAULT FALSE;
