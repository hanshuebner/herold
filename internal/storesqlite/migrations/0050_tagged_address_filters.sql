-- 0050_tagged_address_filters.sql — GUI-managed sub-address routing
-- filters (REQ-TAG-10..11, REQ-TAG-30..32).
--
-- Each row binds one (principal, base Identity, suffix) to a filing
-- action plus a target label/mailbox name. The user pre-registers
-- nothing: rows arrive only when the user explicitly accepts a
-- per-message banner offering to file mail to a sub-address suffix
-- they have just opened.
--
-- Columns:
--   id              opaque string primary key (UUID-shaped; supplied by
--                   the store helper at insert time).
--   principal_id    owning principal (FK to principals(id) ON DELETE
--                   CASCADE so account deletion takes the filters with
--                   it).
--   base_identity_id  FK to jmap_identities(id) ON DELETE CASCADE per
--                   REQ-TAG-10. Removing the Identity that hosts a
--                   sub-address strand drops every dependent filter row
--                   atomically.
--   suffix          TEXT NOT NULL, lower-cased canonical form
--                   (REQ-TAG-02). Equality matching is case-insensitive
--                   on the wire; storage is normalised so the index
--                   carries one canonical key per logical suffix.
--   action          TEXT NOT NULL, one of {label, label_archive,
--                   label_archive_read} per REQ-TAG-30. Stored as a
--                   string (with a CHECK constraint) for forward
--                   compatibility with future actions; mapping to the
--                   internal enum lives in the application layer.
--   label_name      TEXT NOT NULL — the target mailbox/label name. The
--                   filter-create REST/JMAP path enforces existence /
--                   case-canonicalisation (REQ-TAG-32) before
--                   inserting, so the store treats this column as a
--                   verbatim string.
--   created_at_us   row insert instant in unix-micros.
--   updated_at_us   most recent write instant in unix-micros (bumped
--                   by UpdateTaggedAddressFilter).
--
-- Constraints:
--   UNIQUE(principal_id, base_identity_id, suffix) — at most one filter
--   per (identity, suffix) combo (REQ-TAG-10).
--
-- Indexes:
--   idx_tagged_address_filters_principal_identity — fan-out lookup at
--   inbound-delivery time. The per-recipient delivery code reads every
--   filter for (principal, base_identity) once and matches in memory
--   against the suffix string (REQ-TAG-20). Index-only scan satisfies
--   the unique constraint and the lookup.
--
-- Forward-only. Mirrors storepg 0050. Cap enforcement
-- (max 100 filters per principal, REQ-TAG-11) lives in the store
-- helpers, not the schema.

CREATE TABLE tagged_address_filters (
  id                TEXT    PRIMARY KEY,
  principal_id      INTEGER NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  base_identity_id  TEXT    NOT NULL REFERENCES jmap_identities(id) ON DELETE CASCADE,
  suffix            TEXT    NOT NULL,
  action            TEXT    NOT NULL CHECK(action IN ('label','label_archive','label_archive_read')),
  label_name        TEXT    NOT NULL,
  created_at_us     INTEGER NOT NULL,
  updated_at_us     INTEGER NOT NULL,
  UNIQUE(principal_id, base_identity_id, suffix)
) STRICT;

CREATE INDEX idx_tagged_address_filters_principal_identity
  ON tagged_address_filters(principal_id, base_identity_id);
