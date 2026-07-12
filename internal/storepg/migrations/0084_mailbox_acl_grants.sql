-- 0084_mailbox_acl_grants.sql -- retire mailbox_acl onto the grant
-- substrate (epic #210; extends #182/#186, REQ-AC-50..53). Mirrors
-- storesqlite/migrations/0084_mailbox_acl_grants.sql.
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
-- 'principal' and the already-reserved 'group'. Postgres can alter the
-- CHECK constraint in place: drop the auto-named single-column CHECK and
-- re-add the widened one (mirrors migration 0064's imapimport_account
-- state-CHECK widening).
--
-- Migrated rows carry provenance 'acl-migration' (store.GrantProvenanceACLMigration),
-- distinct from 'local' and 'idp:<provider>' so they never collide with,
-- or mask, a manually-assigned local grant or a #188 IdP-reconciled one
-- (unique key includes provenance). granted_by is copied from the legacy
-- row's granted_by (NOT NULL there); the grants table's ON DELETE SET NULL
-- is a strictly safer default than mailbox_acl's ON DELETE CASCADE (a
-- deleted granting admin no longer silently revokes a live share).
--
-- Forward-only.

ALTER TABLE grants
  DROP CONSTRAINT grants_subject_kind_check;

ALTER TABLE grants
  ADD CONSTRAINT grants_subject_kind_check
  CHECK (subject_kind IN ('principal','group','anyone'));

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
