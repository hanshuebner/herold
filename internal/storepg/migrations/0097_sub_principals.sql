-- 0097_sub_principals.sql -- sub-account substrate (issue #227,
-- REQ-SUBACCT-01..11). Mirrors
-- internal/storesqlite/migrations/0097_sub_principals.sql; see that
-- file for the full rationale.
--
-- Forward-only.

ALTER TABLE principals
  ADD COLUMN parent_principal_id BIGINT REFERENCES principals(id) ON DELETE CASCADE;

CREATE INDEX idx_principals_parent
  ON principals(parent_principal_id)
  WHERE parent_principal_id IS NOT NULL;
