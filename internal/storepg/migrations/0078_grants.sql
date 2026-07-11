-- 0078_grants.sql -- unified resource-grant authorization substrate
-- (epic #182, REQ-AC-01..05). Mirrors storesqlite/migrations/0078_grants.sql.
-- Postgres idioms: BIGSERIAL id, BIGINT for ids/timestamps.
--
-- One grant binds a subject (a principal today; subject_kind also admits
-- 'group') to an access level on a typed resource. provenance keeps
-- operator-assigned ('local') and IdP-derived ('idp:<provider>') grants
-- independent (REQ-AC-61); last_asserted_at_us is set only on idp: rows and
-- drives the #188 staleness sweep. granted_by is NULL for migration-derived
-- and IdP-derived rows (ON DELETE SET NULL). subject_id is polymorphic and
-- carries no FK; ids are monotonic (BIGSERIAL) so a deleted principal's id is
-- never reused.
--
-- Back-fill maps today's authority into grants so the table is a faithful
-- projection on upgrade (no privilege change, no lockout):
--   * server:superadmin for every principal with PrincipalFlagSuperAdmin (bit 5 = 32).
--   * domain:operator for every principal_managed_domains row (#145).
--
-- Forward-only.

CREATE TABLE grants (
  id                  BIGSERIAL PRIMARY KEY,
  subject_kind        TEXT   NOT NULL DEFAULT 'principal'
                             CHECK(subject_kind IN ('principal','group')),
  subject_id          BIGINT NOT NULL,
  resource_kind       TEXT   NOT NULL
                             CHECK(resource_kind IN ('server','domain','list','mailbox')),
  resource_id         TEXT   NOT NULL DEFAULT '',
  level               TEXT   NOT NULL,
  provenance          TEXT   NOT NULL DEFAULT 'local',
  granted_by          BIGINT REFERENCES principals(id) ON DELETE SET NULL,
  granted_at_us       BIGINT NOT NULL,
  last_asserted_at_us BIGINT,
  UNIQUE(subject_kind, subject_id, resource_kind, resource_id, provenance)
);

CREATE INDEX IF NOT EXISTS idx_grants_resource ON grants(resource_kind, resource_id);
CREATE INDEX IF NOT EXISTS idx_grants_subject  ON grants(subject_kind, subject_id);

-- Back-fill: server:superadmin for existing super-admins (flags bit 5 = 32).
INSERT INTO grants (subject_kind, subject_id, resource_kind, resource_id,
                    level, provenance, granted_by, granted_at_us)
SELECT 'principal', id, 'server', '', 'superadmin', 'local', NULL,
       (extract(epoch from now())::bigint) * 1000000
  FROM principals
 WHERE (flags & 32) != 0;

-- Back-fill: domain:operator for existing managed-domain rows (#145).
INSERT INTO grants (subject_kind, subject_id, resource_kind, resource_id,
                    level, provenance, granted_by, granted_at_us)
SELECT 'principal', principal_id, 'domain', domain, 'operator', 'local', NULL,
       (extract(epoch from now())::bigint) * 1000000
  FROM principal_managed_domains;
