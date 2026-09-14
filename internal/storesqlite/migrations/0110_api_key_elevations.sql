-- 0110_api_key_elevations.sql -- per-Bearer-credential step-up elevation
-- records for TOTP-gated self-service access (REQ-AUTH-74, REQ-AUTH-78,
-- issue #357).
--
-- A device token or an OAuth2 access token has no cookie session, so it
-- cannot use session_elevations (0067/0091). This table mirrors that
-- schema exactly but keys each row on the authenticating api_keys row id
-- instead of a session_id, so POST /api/v1/auth/step-up can elevate a
-- Bearer-authenticated caller for the same window a cookie session gets.
--
-- api_key_id            INTEGER REFERENCES api_keys(id) ON DELETE CASCADE
--                        The credential that authenticated the step-up.
--                        One elevation per credential at a time; a
--                        subsequent step-up overwrites via ON CONFLICT.
--
-- principal_id          INTEGER REFERENCES principals(id)
--                        Denormalised from the api_keys row for auditing.
--
-- elevated_at_us        INTEGER  -- unix-micros instant the step-up completed.
-- idle_deadline_us      INTEGER  -- slides forward on elevated activity.
-- absolute_deadline_us  INTEGER  -- fixed at grant time, never extended.
--
-- Forward-only. Mirrors storepg 0110.

CREATE TABLE api_key_elevations (
  api_key_id           INTEGER  NOT NULL PRIMARY KEY
                                REFERENCES api_keys(id) ON DELETE CASCADE,
  principal_id         INTEGER  NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  elevated_at_us       INTEGER  NOT NULL,
  idle_deadline_us     INTEGER  NOT NULL,
  absolute_deadline_us INTEGER  NOT NULL
);

CREATE INDEX api_key_elevations_principal_id      ON api_key_elevations(principal_id);
CREATE INDEX api_key_elevations_idle_deadline     ON api_key_elevations(idle_deadline_us);
CREATE INDEX api_key_elevations_absolute_deadline ON api_key_elevations(absolute_deadline_us);
