-- 0079_grants.sql -- unified resource-grant authorization substrate
-- (epic #182, REQ-AC-01..05; docs/design/server/architecture/15-access-control.md).
--
-- One grant binds a subject (a principal today; the subject_kind column
-- already admits 'group' for a later step) to an access level on a typed
-- resource. A principal's effective authority is the union of its grants plus
-- structural implicit ownership, resolved in internal/authz. Levels are per
-- resource_kind and totally ordered (REQ-AC-03):
--   server  : superadmin
--   domain  : operator < owner
--   list    : moderator < owner
--   mailbox : read < write < admin
--
-- provenance keeps operator-assigned ('local') and IdP-derived
-- ('idp:<provider>') grants independent (REQ-AC-61): they are distinct rows
-- for one (subject, resource) because the unique key includes provenance, so
-- IdP reconciliation (#188) never disturbs a local grant. last_asserted_at_us
-- is set only on idp: rows and drives the #188 staleness sweep; NULL for local.
--
-- granted_by is the acting principal for a local grant and NULL for
-- migration-derived and IdP-derived rows; ON DELETE SET NULL keeps the grant
-- when its granting actor is removed. subject_id is polymorphic (principal id
-- or, later, group id) and carries no FK; principal ids are monotonic
-- (AUTOINCREMENT) so a deleted principal's id is never reused and a stale
-- grant can never re-attach to a different identity.
--
-- Back-fill maps today's authority into grants so the table is a faithful
-- projection on upgrade (no privilege change, no lockout):
--   * server:superadmin for every principal with PrincipalFlagSuperAdmin
--     (flags bit 5 = 32).
--   * domain:operator for every principal_managed_domains row (#145).
--
-- Forward-only.

CREATE TABLE grants (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  subject_kind        TEXT    NOT NULL DEFAULT 'principal'
                              CHECK(subject_kind IN ('principal','group')),
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

CREATE INDEX idx_grants_resource ON grants(resource_kind, resource_id);
CREATE INDEX idx_grants_subject  ON grants(subject_kind, subject_id);

-- Back-fill: server:superadmin for existing super-admins (flags bit 5 = 32).
INSERT INTO grants (subject_kind, subject_id, resource_kind, resource_id,
                    level, provenance, granted_by, granted_at_us)
SELECT 'principal', id, 'server', '', 'superadmin', 'local', NULL,
       CAST(strftime('%s','now') AS INTEGER) * 1000000
  FROM principals
 WHERE (flags & 32) != 0;

-- Back-fill: domain:operator for existing managed-domain rows (#145).
INSERT INTO grants (subject_kind, subject_id, resource_kind, resource_id,
                    level, provenance, granted_by, granted_at_us)
SELECT 'principal', principal_id, 'domain', domain, 'operator', 'local', NULL,
       CAST(strftime('%s','now') AS INTEGER) * 1000000
  FROM principal_managed_domains;
