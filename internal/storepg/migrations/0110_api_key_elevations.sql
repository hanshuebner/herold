-- 0110_api_key_elevations.sql -- per-Bearer-credential step-up elevation
-- records for TOTP-gated self-service access (REQ-AUTH-74, REQ-AUTH-78,
-- issue #357).
-- Mirrors storesqlite 0110.

CREATE TABLE api_key_elevations (
  api_key_id           BIGINT   NOT NULL PRIMARY KEY
                                REFERENCES api_keys(id) ON DELETE CASCADE,
  principal_id         BIGINT   NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  elevated_at_us       BIGINT   NOT NULL,
  idle_deadline_us     BIGINT   NOT NULL,
  absolute_deadline_us BIGINT   NOT NULL
);

CREATE INDEX api_key_elevations_principal_id      ON api_key_elevations(principal_id);
CREATE INDEX api_key_elevations_idle_deadline     ON api_key_elevations(idle_deadline_us);
CREATE INDEX api_key_elevations_absolute_deadline ON api_key_elevations(absolute_deadline_us);
