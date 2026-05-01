-- 0039_sessions.sql — server-side session rows for per-session state.
-- (REQ-OPS-208, REQ-CLOG-06).
--
-- The existing session surface is a stateless HMAC-signed cookie. This table
-- adds a lightweight DB-backed row keyed on the same session ID that the
-- cookie encodes. The row holds state that varies per session and cannot live
-- in the signed-but-not-encrypted cookie:
--
--   clientlog_telemetry_enabled  — the effective resolved telemetry flag
--                                  (always non-NULL; computed from the
--                                  principal override + system default at
--                                  session creation / refresh). Allows the
--                                  clientlog ingest handler to check the flag
--                                  via TelemetryGate.IsEnabled(sessionID)
--                                  without a principal lookup on the hot path.
--
--   clientlog_livetail_until     — optional expiry for the admin live-tail
--                                  mode (REQ-OPS-211 / REQ-ADM-232). NULL
--                                  when live-tail is inactive.
--
-- session_id is the stable identifier carried inside the signed cookie
-- (the CSRF token doubles as the session identifier; see authsession.Session).
--
-- Forward-only. Mirrors storesqlite 0039.

CREATE TABLE sessions (
  session_id                    TEXT        NOT NULL PRIMARY KEY,
  principal_id                  BIGINT      NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  created_at_us                 BIGINT      NOT NULL,
  expires_at_us                 BIGINT      NOT NULL,
  clientlog_telemetry_enabled   BOOLEAN     NOT NULL,
  clientlog_livetail_until_us   BIGINT
);

CREATE INDEX sessions_principal_id ON sessions(principal_id);
CREATE INDEX sessions_expires_at   ON sessions(expires_at_us);
