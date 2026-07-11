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
-- SQLite cannot ALTER a column's NOT NULL / FK constraint in place, so
-- the table is rebuilt (mirrors migration 0024's messages rebuild).
-- Forward-only. Mirrors storepg 0080.

CREATE TABLE aliases_new (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  local_part        TEXT    NOT NULL,
  domain            TEXT    NOT NULL,
  target_principal  INTEGER REFERENCES principals(id) ON DELETE CASCADE,
  target_address    TEXT,
  expires_at_us     INTEGER,
  created_at_us     INTEGER NOT NULL,
  UNIQUE(local_part, domain),
  CHECK (
    (target_principal IS NOT NULL AND target_address IS NULL) OR
    (target_principal IS NULL AND target_address IS NOT NULL)
  )
) STRICT;

INSERT INTO aliases_new (id, local_part, domain, target_principal, target_address, expires_at_us, created_at_us)
SELECT id, local_part, domain, target_principal, NULL, expires_at_us, created_at_us
  FROM aliases;

DROP TABLE aliases;
ALTER TABLE aliases_new RENAME TO aliases;

CREATE INDEX idx_aliases_target ON aliases(target_principal);
