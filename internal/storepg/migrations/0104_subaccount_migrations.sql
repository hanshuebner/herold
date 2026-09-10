-- 0104_subaccount_migrations.sql -- sub-account promotion sweep bookkeeping
-- (issue #227, REQ-SUBACCT-09, REQ-IMAP-IMP-106/107).
-- Mirrors storesqlite/migrations/0104_subaccount_migrations.sql.
--
-- subaccount_migrations tracks one row per SeparateIdentity call: the
-- (parent, sub, identity) triple being separated, the resumable sweep's
-- status, and running counts. RunSubAccountMigration re-derives its work
-- list from imapimport_message_state on every call rather than a
-- persisted cursor, so a crash between batches is resumed exactly, not
-- replayed from scratch; this row exists for status/progress reporting
-- and boot-time resume (ListPendingSubAccountMigrations). One row per
-- identity_id (idempotent re-separation returns the existing row) and
-- one row per sub_principal_id (a sub-principal is created by exactly
-- one SeparateIdentity call).
--
-- imapimport_message_state gains copied_message_id: the durable marker
-- set, atomically with the copy insert, when a message claimed by more
-- than one channel is copied rather than moved into a sub-account
-- (REQ-IMAP-IMP-103/106 dedup-safe copy). 0 means "not copied"; the
-- ordinary flag-sync UPSERT (UpsertIMAPImportMessageState) never
-- includes this column in its SET list, so a live worker's routine
-- writes cannot clobber the marker.
--
-- Forward-only.

CREATE TABLE subaccount_migrations (
  id                    TEXT    NOT NULL PRIMARY KEY,
  parent_principal_id   BIGINT  NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  sub_principal_id      BIGINT  NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  identity_id           TEXT    NOT NULL,
  status                TEXT    NOT NULL DEFAULT 'pending'
                                 CHECK(status IN ('pending','running','done')),
  messages_total        BIGINT  NOT NULL DEFAULT 0,
  messages_moved        BIGINT  NOT NULL DEFAULT 0,
  messages_copied       BIGINT  NOT NULL DEFAULT 0,
  last_error            TEXT    NOT NULL DEFAULT '',
  created_at_us         BIGINT  NOT NULL,
  updated_at_us         BIGINT  NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_subaccount_migrations_identity
  ON subaccount_migrations(identity_id);

CREATE UNIQUE INDEX IF NOT EXISTS idx_subaccount_migrations_sub_principal
  ON subaccount_migrations(sub_principal_id);

CREATE INDEX IF NOT EXISTS idx_subaccount_migrations_status
  ON subaccount_migrations(status);

ALTER TABLE imapimport_message_state
  ADD COLUMN copied_message_id BIGINT NOT NULL DEFAULT 0;
