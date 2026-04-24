-- 0002_audit_cursor_totpflag.sql — Wave 2 storage interfaces.
--
-- Adds:
--   * cursors: a generic (key, seq) table carrying the FTS worker's
--     durable position and future per-consumer cursors (DKIM report,
--     webhook relay, etc.). One row per key; idempotent upsert.
--   * audit_log: append-only audit trail (REQ-AUTH-62). Timestamps are
--     BIGINT Unix micros to match the rest of the schema. The Metadata
--     column is stored as a JSON string (TEXT) because SQLite JSON1
--     is always enabled in modernc.org/sqlite and a structured column
--     reads cleaner in admin diagnostics than CSV. Postgres keeps the
--     same TEXT column so the migration tool is single-path.
--
-- No schema change is needed for PrincipalFlagTOTPEnabled — the flag
-- lives in the existing principals.flags bitfield. The backfill that
-- lifts the legacy 1-byte prefix on totp_secret into the flag runs at
-- Open() time (see Store.backfillTOTPFlags); applying it in SQL would
-- require parsing the binary column in pure SQL, which is ugly.

CREATE TABLE cursors (
  key TEXT    PRIMARY KEY,
  seq INTEGER NOT NULL
) STRICT;

CREATE TABLE audit_log (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  at_us          INTEGER NOT NULL,
  actor_kind     INTEGER NOT NULL,
  actor_id       TEXT    NOT NULL DEFAULT '',
  action         TEXT    NOT NULL,
  subject        TEXT    NOT NULL DEFAULT '',
  remote_addr    TEXT    NOT NULL DEFAULT '',
  outcome        INTEGER NOT NULL,
  message        TEXT    NOT NULL DEFAULT '',
  metadata_json  TEXT    NOT NULL DEFAULT '',
  principal_id   INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE INDEX idx_audit_log_at ON audit_log(at_us);
CREATE INDEX idx_audit_log_principal_at ON audit_log(principal_id, at_us);
CREATE INDEX idx_audit_log_action_at ON audit_log(action, at_us);
