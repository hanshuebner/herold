-- 0074_imapimport_debug_log.sql — per-account debug-logging toggle
-- (REQ-ADM-304, re #138).
--
-- When debug_log is true the account's worker emits fine-grained operational
-- events (connection lifecycle, IDLE arm/wake, sync round counts, NOOP poll
-- ticks) into the system_events ring-buffer tagged with the account ID as
-- actor_id.  The flag is toggled at runtime via the admin PATCH endpoint
-- without a herold restart; the worker picks it up at the next account-state
-- read (within the stateRecheckInterval).
--
-- Forward-only.

ALTER TABLE imapimport_account ADD COLUMN debug_log BOOLEAN NOT NULL DEFAULT FALSE;
