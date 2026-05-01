-- 0038_clientlog_telemetry.sql — per-user client-log telemetry opt-out
-- (REQ-OPS-208, REQ-CLOG-06).
--
-- Adds a nullable boolean column to principals so each user can opt in or
-- out of behavioural telemetry (kind=log and kind=vital client events).
-- NULL means "use the system default from [clientlog.defaults].telemetry_enabled".
-- Explicit TRUE / FALSE override the default in either direction.
--
-- Forward-only.

ALTER TABLE principals
  ADD COLUMN clientlog_telemetry_enabled INTEGER;
