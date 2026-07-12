-- 0084_mailbox_acl_grants.sql -- retire mailbox_acl onto the grant
-- substrate (epic #210; extends #182/#186, REQ-AC-50..53).
--
-- Every mailbox_acl row becomes a `mailbox`-kind grant whose Level column
-- carries the FULL RFC 4314 letter-set the row conveyed -- not the coarse
-- read/write/admin tier -- so the migration is an exact, order-independent
-- round-trip: for every (principal, mailbox), the rights resolved after
-- migration equal the rights mailbox_acl resolved before. Bit values mirror
-- internal/store/types_phase2.go's ACLRights constants and the letter
-- order internal/aclcodec.LetterTable defines (l=1 r=2 s=4 w=8 i=16 p=32
-- k=64 x=128 t=256 e=512 a=1024).
--
-- The RFC 4314 "anyone" pseudo-identifier (mailbox_acl.principal_id IS
-- NULL) needed a subject the grants table did not yet admit: subject_kind
-- gains a third value, 'anyone' (subject_id unused, stored as 0), alongside
-- 'principal' and the already-reserved 'group'. SQLite cannot ALTER a
-- CHECK constraint in place, so the table is rebuilt (mirrors migration
-- 0080's aliases rebuild); every existing row is 'principal'-subject and
-- copies over unchanged.
--
-- Migrated rows carry provenance 'acl-migration' (store.GrantProvenanceACLMigration),
-- distinct from 'local' and 'idp:<provider>' so they never collide with,
-- or mask, a manually-assigned local grant or a #188 IdP-reconciled one
-- (unique key includes provenance). granted_by is copied from the legacy
-- row's granted_by (NOT NULL there); the rebuilt grants table's
-- ON DELETE SET NULL is a strictly safer default than mailbox_acl's
-- ON DELETE CASCADE (a deleted granting admin no longer silently revokes a
-- live share).
--
-- Forward-only.

CREATE TABLE grants_new (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  subject_kind        TEXT    NOT NULL DEFAULT 'principal'
                              CHECK(subject_kind IN ('principal','group','anyone')),
  subject_id          INTEGER NOT NULL,
  resource_kind       TEXT    NOT NULL
                              CHECK(resource_kind IN ('server','domain','list','mailbox')),
  resource_id         TEXT    NOT NULL DEFAULT '',
  level               TEXT    NOT NULL,
  provenance          TEXT    NOT NULL DEFAULT 'local',
  granted_by          INTEGER REFERENCES principals(id) ON DELETE SET NULL,
  granted_at_us       INTEGER NOT NULL,
  last_asserted_at_us INTEGER,
  UNIQUE(subject_kind, subject_id, resource_kind, resource_id, provenance)
) STRICT;

INSERT INTO grants_new (id, subject_kind, subject_id, resource_kind, resource_id,
                         level, provenance, granted_by, granted_at_us, last_asserted_at_us)
SELECT id, subject_kind, subject_id, resource_kind, resource_id,
       level, provenance, granted_by, granted_at_us, last_asserted_at_us
  FROM grants;

DROP TABLE grants;
ALTER TABLE grants_new RENAME TO grants;

CREATE INDEX idx_grants_resource ON grants(resource_kind, resource_id);
CREATE INDEX idx_grants_subject  ON grants(subject_kind, subject_id);

-- Migrate every mailbox_acl row into an equivalent mailbox grant.
INSERT INTO grants (subject_kind, subject_id, resource_kind, resource_id,
                    level, provenance, granted_by, granted_at_us)
SELECT
  CASE WHEN principal_id IS NULL THEN 'anyone' ELSE 'principal' END,
  COALESCE(principal_id, 0),
  'mailbox',
  CAST(mailbox_id AS TEXT),
  (CASE WHEN rights_mask & 1    != 0 THEN 'l' ELSE '' END) ||
  (CASE WHEN rights_mask & 2    != 0 THEN 'r' ELSE '' END) ||
  (CASE WHEN rights_mask & 4    != 0 THEN 's' ELSE '' END) ||
  (CASE WHEN rights_mask & 8    != 0 THEN 'w' ELSE '' END) ||
  (CASE WHEN rights_mask & 16   != 0 THEN 'i' ELSE '' END) ||
  (CASE WHEN rights_mask & 32   != 0 THEN 'p' ELSE '' END) ||
  (CASE WHEN rights_mask & 64   != 0 THEN 'k' ELSE '' END) ||
  (CASE WHEN rights_mask & 128  != 0 THEN 'x' ELSE '' END) ||
  (CASE WHEN rights_mask & 256  != 0 THEN 't' ELSE '' END) ||
  (CASE WHEN rights_mask & 512  != 0 THEN 'e' ELSE '' END) ||
  (CASE WHEN rights_mask & 1024 != 0 THEN 'a' ELSE '' END),
  'acl-migration',
  granted_by,
  created_at_us
FROM mailbox_acl
WHERE rights_mask != 0;

DROP TABLE mailbox_acl;
