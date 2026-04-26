-- 0016_apikey_scope.sql -- Phase 3 Wave 3.6:
-- REQ-AUTH-SCOPE-04 (API key scope, immutable post-create).
-- Mirrors storepg 0016. Forward-only.
--
-- Adds a single column:
--
--   * scope_json -- JSON-encoded list of auth.Scope values that the
--                   key grants. NULL is reserved for legacy rows
--                   created before this migration; the migration body
--                   below sets every existing row to '["admin"]' so
--                   that capability survives the upgrade. New rows
--                   carry an explicit scope set chosen at create time.
--
-- Operators are encouraged (via the boot-time warn-log) to rotate any
-- backfilled key for least-privilege; "admin" is the broadest scope
-- and was the implicit Phase 1 / Phase 2 capability, but a transactional
-- sender that only needs ["mail.send"] should be rotated to that scope.

ALTER TABLE api_keys ADD COLUMN scope_json TEXT NOT NULL DEFAULT '';

UPDATE api_keys SET scope_json = '["admin"]' WHERE scope_json = '';
