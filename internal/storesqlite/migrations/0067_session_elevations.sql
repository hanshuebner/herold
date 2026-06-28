-- 0067_session_elevations.sql -- per-session step-up elevation records
-- for TOTP-gated admin access (REQ-AUTH-74, issue #79).
--
-- A successful POST /api/v1/auth/step-up creates one row here bound to the
-- current session.  Admin-scoped endpoint middleware queries this table to
-- decide whether the caller is currently elevated.  The row expires after
-- the configured elevation_ttl (default 15 minutes); the eviction sweep and
-- on-expiry enforcement both use expires_at_us.  Deleting the parent session
-- (logout, revocation) cascades here automatically so stale elevation rows
-- never outlive their session.
--
-- session_id    TEXT REFERENCES sessions(session_id) ON DELETE CASCADE
--               The owning session.  One elevation per session at a time;
--               subsequent step-ups overwrite via ON CONFLICT.
--
-- principal_id  INTEGER REFERENCES principals(id)
--               Denormalised from the session row for auditing purposes and
--               to let the middleware verify the principal still has the admin
--               flag without a principals join on the hot path.
--
-- elevated_at_us  INTEGER  -- unix-micros instant the step-up completed.
-- expires_at_us   INTEGER  -- unix-micros instant the elevation expires.
--
-- Forward-only.  Mirrors storepg 0067.

CREATE TABLE session_elevations (
  session_id      TEXT     NOT NULL PRIMARY KEY
                           REFERENCES sessions(session_id) ON DELETE CASCADE,
  principal_id    INTEGER  NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  elevated_at_us  INTEGER  NOT NULL,
  expires_at_us   INTEGER  NOT NULL
);

CREATE INDEX session_elevations_principal_id ON session_elevations(principal_id);
CREATE INDEX session_elevations_expires_at   ON session_elevations(expires_at_us);
