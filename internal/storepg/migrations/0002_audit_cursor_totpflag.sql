-- 0002_audit_cursor_totpflag.sql — Wave 2 storage interfaces.
--
-- Mirrors storesqlite/migrations/0002_audit_cursor_totpflag.sql.
-- Timestamps land in BIGINT _us columns so the migration tool copies
-- row-by-row without lossy conversion. metadata_json is a TEXT column
-- holding a JSON document (same shape as SQLite) — it could be JSONB
-- here, but parity with SQLite keeps the admin read path
-- backend-agnostic.

CREATE TABLE cursors (
  key TEXT   PRIMARY KEY,
  seq BIGINT NOT NULL
);

CREATE TABLE audit_log (
  id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  at_us          BIGINT  NOT NULL,
  actor_kind     INTEGER NOT NULL,
  actor_id       TEXT    NOT NULL DEFAULT '',
  action         TEXT    NOT NULL,
  subject        TEXT    NOT NULL DEFAULT '',
  remote_addr    TEXT    NOT NULL DEFAULT '',
  outcome        INTEGER NOT NULL,
  message        TEXT    NOT NULL DEFAULT '',
  metadata_json  TEXT    NOT NULL DEFAULT '',
  principal_id   BIGINT  NOT NULL DEFAULT 0
);

CREATE INDEX idx_audit_log_at ON audit_log(at_us);
CREATE INDEX idx_audit_log_principal_at ON audit_log(principal_id, at_us);
CREATE INDEX idx_audit_log_action_at ON audit_log(action, at_us);
