-- 0099_drop_principal_managed_domains.sql -- retire the legacy
-- principal_managed_domains association table (issue #237, REQ-ADM-307,
-- REQ-AC-30/31).
--
-- Migration 0079 back-filled a domain:operator grant for every
-- principal_managed_domains row that existed at that time. Since then,
-- the write path (handleAssignManagedDomain, internal/protoadmin) has kept
-- writing new assignments only into principal_managed_domains, so a row
-- created after 0079 may have no corresponding grant. Defensively re-run
-- the same back-fill, scoped with NOT EXISTS against the grants natural key
-- (subject_kind, subject_id, resource_kind, resource_id, provenance) --
-- SQLite's UPSERT clause is not accepted on an INSERT ... SELECT, only on
-- INSERT ... VALUES, so ON CONFLICT DO NOTHING is not an option here -- so
-- the drop below can never lose an operator's domain: a row already covered
-- by an equivalent grant is skipped, and a row missing one gets its grant
-- written now.
--
-- After this migration, grants is the only source of domain:operator
-- authority; internal/authz.OperatorDomains and Resolve no longer consult
-- principal_managed_domains.
--
-- Forward-only.

INSERT INTO grants (subject_kind, subject_id, resource_kind, resource_id,
                    level, provenance, granted_by, granted_at_us)
SELECT 'principal', pmd.principal_id, 'domain', pmd.domain, 'operator', 'local', NULL,
       CAST(strftime('%s','now') AS INTEGER) * 1000000
  FROM principal_managed_domains pmd
 WHERE NOT EXISTS (
   SELECT 1 FROM grants g
    WHERE g.subject_kind = 'principal'
      AND g.subject_id = pmd.principal_id
      AND g.resource_kind = 'domain'
      AND g.resource_id = pmd.domain
      AND g.provenance = 'local'
 );

DROP TABLE principal_managed_domains;
