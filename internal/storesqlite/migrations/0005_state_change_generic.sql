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
-- For Email rows, EntityID = old message_id; ParentEntityID = old
-- mailbox_id. For Mailbox rows, EntityID = old mailbox_id;
-- ParentEntityID = 0. The old message_uid column drops on the floor —
-- consumers that need IMAP UID join the messages table.
--
-- Pattern is the SQLite-portable new-table / copy / swap. Postgres
-- 0005 expresses the same mutation as ALTER TABLE in place; the row
-- contents land identical so the SQLite ↔ Postgres migration tool
-- continues to copy row-by-row.

CREATE TABLE state_changes_new (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  principal_id      INTEGER NOT NULL,
  seq               INTEGER NOT NULL,
  entity_kind       TEXT    NOT NULL,
  entity_id         INTEGER NOT NULL,
  parent_entity_id  INTEGER NOT NULL DEFAULT 0,
  op                INTEGER NOT NULL,
  produced_at_us    INTEGER NOT NULL,
  UNIQUE(principal_id, seq)
) STRICT;

INSERT INTO state_changes_new
  (id, principal_id, seq, entity_kind, entity_id, parent_entity_id, op, produced_at_us)
SELECT
  id,
  principal_id,
  seq,
  CASE
    WHEN kind IN (1, 2, 3) THEN 'email'
    WHEN kind IN (4, 5, 6) THEN 'mailbox'
    ELSE ''
  END,
  CASE
    WHEN kind IN (1, 2, 3) THEN message_id
    WHEN kind IN (4, 5, 6) THEN mailbox_id
    ELSE 0
  END,
  CASE
    WHEN kind IN (1, 2, 3) THEN mailbox_id
    ELSE 0
  END,
  CASE
    WHEN kind IN (1, 4) THEN 1
    WHEN kind IN (2, 5) THEN 2
    WHEN kind IN (3, 6) THEN 3
    ELSE 0
  END,
  produced_at_us
FROM state_changes;

DROP INDEX IF EXISTS idx_state_changes_principal_seq;
DROP INDEX IF EXISTS idx_state_changes_global_id;
DROP TABLE state_changes;
ALTER TABLE state_changes_new RENAME TO state_changes;

CREATE INDEX idx_state_changes_principal_seq
  ON state_changes(principal_id, seq);

CREATE INDEX idx_state_changes_global_id
  ON state_changes(id);

-- Per-datatype filter for JMAP `Foo/changes` reads ("emails since seq
-- X"). Partial-on-kind would cost a per-kind index; the composite
-- handles every kind uniformly.
CREATE INDEX idx_state_changes_principal_kind_seq
  ON state_changes(principal_id, entity_kind, seq);
