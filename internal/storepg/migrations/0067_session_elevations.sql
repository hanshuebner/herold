-- 0067_session_elevations.sql -- per-session step-up elevation records
-- for TOTP-gated admin access (REQ-AUTH-74, issue #79).
-- Mirrors storesqlite 0067.

CREATE TABLE session_elevations (
  session_id      TEXT     NOT NULL PRIMARY KEY
                           REFERENCES sessions(session_id) ON DELETE CASCADE,
  principal_id    BIGINT   NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  elevated_at_us  BIGINT   NOT NULL,
  expires_at_us   BIGINT   NOT NULL
);

CREATE INDEX session_elevations_principal_id ON session_elevations(principal_id);
CREATE INDEX session_elevations_expires_at   ON session_elevations(expires_at_us);
