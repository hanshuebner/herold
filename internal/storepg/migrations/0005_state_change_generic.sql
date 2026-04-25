-- 0005_state_change_generic.sql — datatype-agnostic state-change row.
--
-- Replaces the mail-only typed columns (mailbox_id, message_id,
-- message_uid) with the generic (entity_kind, entity_id,
-- parent_entity_id, op) shape mandated by
-- docs/architecture/05-sync-and-state.md §Forward-compatibility. Old
-- rows are mapped 1:1 into the new shape; the JMAP `Foo/changes`
-- consumer is now uniform across types. Per-type sync auxiliaries
-- (IMAP UID, MODSEQ for mail) live on the type's own tables and are
-- joined when needed — they MUST NOT live here.
--
-- Old `kind` (combined entity+op) → new (entity_kind, op):
--   1 MessageCreated     → ('email',   1 Created)
--   2 MessageUpdated     → ('email',   2 Updated)
--   3 MessageDestroyed   → ('email',   3 Destroyed)
--   4 MailboxCreated     → ('mailbox', 1 Created)
--   5 MailboxUpdated     → ('mailbox', 2 Updated)
--   6 MailboxDestroyed   → ('mailbox', 3 Destroyed)
--
-- For Email rows, entity_id = old message_id; parent_entity_id = old
-- mailbox_id. For Mailbox rows, entity_id = old mailbox_id;
-- parent_entity_id = 0. The old message_uid column drops on the floor
-- — consumers that need IMAP UID join the messages table.
--
-- Postgres expresses this as ALTER TABLE in place; the SQLite 0005
-- migration of the same name does the equivalent via new-table /
-- copy / swap because SQLite STRICT-table column drops want a fresh
-- table. Row contents land identical so the SQLite ↔ Postgres
-- migration tool continues to copy row-by-row.

ALTER TABLE state_changes ADD COLUMN entity_kind      TEXT    NOT NULL DEFAULT '';
ALTER TABLE state_changes ADD COLUMN entity_id        BIGINT  NOT NULL DEFAULT 0;
ALTER TABLE state_changes ADD COLUMN parent_entity_id BIGINT  NOT NULL DEFAULT 0;
ALTER TABLE state_changes ADD COLUMN op               SMALLINT NOT NULL DEFAULT 0;

UPDATE state_changes SET
  entity_kind = CASE
    WHEN kind IN (1, 2, 3) THEN 'email'
    WHEN kind IN (4, 5, 6) THEN 'mailbox'
    ELSE ''
  END,
  entity_id = CASE
    WHEN kind IN (1, 2, 3) THEN message_id
    WHEN kind IN (4, 5, 6) THEN mailbox_id
    ELSE 0
  END,
  parent_entity_id = CASE
    WHEN kind IN (1, 2, 3) THEN mailbox_id
    ELSE 0
  END,
  op = CASE
    WHEN kind IN (1, 4) THEN 1
    WHEN kind IN (2, 5) THEN 2
    WHEN kind IN (3, 6) THEN 3
    ELSE 0
  END;

ALTER TABLE state_changes DROP COLUMN kind;
ALTER TABLE state_changes DROP COLUMN mailbox_id;
ALTER TABLE state_changes DROP COLUMN message_id;
ALTER TABLE state_changes DROP COLUMN message_uid;

ALTER TABLE state_changes ALTER COLUMN entity_kind DROP DEFAULT;
ALTER TABLE state_changes ALTER COLUMN entity_id   DROP DEFAULT;
ALTER TABLE state_changes ALTER COLUMN op          DROP DEFAULT;

-- Per-datatype filter for JMAP `Foo/changes` reads ("emails since seq
-- X"). Composite handles every kind uniformly without per-kind partial
-- indexes.
CREATE INDEX idx_state_changes_principal_kind_seq
  ON state_changes(principal_id, entity_kind, seq);
