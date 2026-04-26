-- 0016_apikey_scope.sql -- Phase 3 Wave 3.6:
-- REQ-AUTH-SCOPE-04 (API key scope, immutable post-create).
-- Mirrors storesqlite 0016. Forward-only.
--
-- Adds a single column:
--
--   * scope_json -- JSON-encoded list of auth.Scope values that the
--                   key grants. NULL is reserved for legacy rows
--                   created before this migration; the migration body
--                   below sets every existing row to '["admin"]' so
--                   that capability survives the upgrade. New rows
--                   carry an explicit scope set chosen at create time.

ALTER TABLE api_keys ADD COLUMN scope_json TEXT NOT NULL DEFAULT '';

UPDATE api_keys SET scope_json = '["admin"]' WHERE scope_json = '';
