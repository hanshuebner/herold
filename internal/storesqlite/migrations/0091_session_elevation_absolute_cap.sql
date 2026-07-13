-- 0091_session_elevation_absolute_cap.sql -- splits the single
-- expires_at_us elevation deadline into a sliding idle_deadline_us and a
-- fixed absolute_deadline_us (REQ-AUTH-74, REQ-AUTH-ELEV-CONFIG, issue #225).
--
-- Before this migration, elevation expired on a fixed wall-clock deadline
-- set once at grant time: an actively-working operator was thrown back to
-- the TOTP prompt mid-task the moment that fixed TTL elapsed, because no
-- code path ever extended expires_at_us. This migration renames
-- expires_at_us to idle_deadline_us -- the deadline the elevation-gate
-- middleware now extends on every request that passes the
-- active-elevation check (ExtendElevation), clamped to never exceed
-- absolute_deadline_us -- and adds absolute_deadline_us, a second,
-- never-extended deadline that caps elevation lifetime regardless of
-- activity. An elevation is active while now is before BOTH deadlines.
--
-- Existing rows: idle_deadline_us keeps its prior value via the rename;
-- absolute_deadline_us is backfilled to the same value so an elevation
-- already in flight when this migration runs is not silently granted a
-- fresh absolute window -- it keeps expiring at its original deadline
-- until the next step-up or login creates a fresh row with both
-- deadlines set from the current idle/absolute config.
--
-- Forward-only. Mirrors storepg 0091.

ALTER TABLE session_elevations RENAME COLUMN expires_at_us TO idle_deadline_us;
ALTER TABLE session_elevations ADD COLUMN absolute_deadline_us INTEGER NOT NULL DEFAULT 0;
UPDATE session_elevations SET absolute_deadline_us = idle_deadline_us;

DROP INDEX session_elevations_expires_at;
CREATE INDEX session_elevations_idle_deadline     ON session_elevations(idle_deadline_us);
CREATE INDEX session_elevations_absolute_deadline ON session_elevations(absolute_deadline_us);
