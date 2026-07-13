-- 0091_session_elevation_absolute_cap.sql -- splits the single
-- expires_at_us elevation deadline into a sliding idle_deadline_us and a
-- fixed absolute_deadline_us (REQ-AUTH-74, REQ-AUTH-ELEV-CONFIG, issue #225).
-- Mirrors storesqlite 0091.

ALTER TABLE session_elevations RENAME COLUMN expires_at_us TO idle_deadline_us;
ALTER TABLE session_elevations ADD COLUMN absolute_deadline_us BIGINT NOT NULL DEFAULT 0;
UPDATE session_elevations SET absolute_deadline_us = idle_deadline_us;

DROP INDEX session_elevations_expires_at;
CREATE INDEX session_elevations_idle_deadline     ON session_elevations(idle_deadline_us);
CREATE INDEX session_elevations_absolute_deadline ON session_elevations(absolute_deadline_us);
