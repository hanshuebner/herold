-- 0118_apps.sql -- registered apps and their per-principal enablement
-- (issue #522, REQ-APP-01, REQ-APP-02). Mirrors storepg 0118.
--
-- apps holds one row per external app the operator registered: the
-- approved manifest snapshot and its digest, a fetched manifest awaiting
-- approval, the sealed signing secret, the lifecycle state
-- (pending_approval | active | disabled) and the all_principals switch.
-- Manifests are stored as canonical JSON text; NULL means "none".
--
-- app_principals is the explicit set of principals an app is enabled
-- for when all_principals is off (REQ-APP-13). Both FKs cascade, so
-- removing an app or a principal removes the membership rows.

CREATE TABLE apps (
  id                      INTEGER PRIMARY KEY AUTOINCREMENT,
  app_key                 TEXT    NOT NULL UNIQUE,
  origin                  TEXT    NOT NULL,
  manifest_json           TEXT,
  manifest_digest         TEXT    NOT NULL DEFAULT '',
  pending_manifest_json   TEXT,
  pending_manifest_digest TEXT    NOT NULL DEFAULT '',
  secret_sealed           BLOB    NOT NULL,
  state                   TEXT    NOT NULL DEFAULT 'pending_approval',
  all_principals          INTEGER NOT NULL DEFAULT 0,
  created_at_us           INTEGER NOT NULL DEFAULT 0,
  updated_at_us           INTEGER NOT NULL DEFAULT 0,
  last_invoked_at_us      INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE TABLE app_principals (
  app_id       INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  principal_id INTEGER NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  PRIMARY KEY (app_id, principal_id)
) STRICT;

CREATE INDEX idx_app_principals_principal ON app_principals(principal_id);
