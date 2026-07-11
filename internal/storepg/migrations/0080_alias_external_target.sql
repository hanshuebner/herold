-- 0080_alias_external_target.sql -- external-address alias targets
-- (issue #181).
--
-- An alias may now forward to an address outside this deployment
-- instead of an internal principal. target_principal becomes nullable
-- (it keeps its FK to principals(id) ON DELETE CASCADE for the
-- internal-target case) and a new nullable target_address column
-- carries the external addr-spec. Exactly one of the two is set,
-- enforced by a CHECK constraint mirrored in store.Metadata.InsertAlias.
--
-- Forward-only. Mirrors storesqlite 0080.

ALTER TABLE aliases ALTER COLUMN target_principal DROP NOT NULL;
ALTER TABLE aliases ADD COLUMN target_address TEXT;
ALTER TABLE aliases ADD CONSTRAINT aliases_target_xor CHECK (
  (target_principal IS NOT NULL AND target_address IS NULL) OR
  (target_principal IS NULL AND target_address IS NOT NULL)
);
