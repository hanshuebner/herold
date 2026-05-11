-- 0050_tagged_address_filters.sql — GUI-managed sub-address routing
-- filters (REQ-TAG-10..11, REQ-TAG-30..32).
--
-- Mirrors storesqlite/migrations/0050_tagged_address_filters.sql.
-- Column types map BIGINT <-> INTEGER per the established backend pattern.
-- Cap enforcement (max 100 filters per principal, REQ-TAG-11) lives in
-- the store helpers, not the schema.
--
-- Forward-only.

CREATE TABLE tagged_address_filters (
  id                TEXT    PRIMARY KEY,
  principal_id      BIGINT  NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  base_identity_id  TEXT    NOT NULL REFERENCES jmap_identities(id) ON DELETE CASCADE,
  suffix            TEXT    NOT NULL,
  action            TEXT    NOT NULL CHECK(action IN ('label','label_archive','label_archive_read')),
  label_name        TEXT    NOT NULL,
  created_at_us     BIGINT  NOT NULL,
  updated_at_us     BIGINT  NOT NULL,
  UNIQUE(principal_id, base_identity_id, suffix)
);

CREATE INDEX IF NOT EXISTS idx_tagged_address_filters_principal_identity
  ON tagged_address_filters(principal_id, base_identity_id);
