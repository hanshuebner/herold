-- 0024_message_mailboxes.sql — multi-mailbox membership (Wave 3.11)
--
-- REQ-STORE-36..38: a single JMAP Email may live in N mailboxes
-- simultaneously. The per-(message, mailbox) state (UID, MODSEQ, flags,
-- keywords) moves from messages into a new message_mailboxes join table.
-- messages gains a principal_id denorm column for query speed.
--
-- Migration steps (forward-only, REQ-OPS-100):
--   1. Add messages.principal_id (backfilled from mailbox owner).
--   2. Create message_mailboxes with composite PK + secondary indexes.
--   3. Backfill message_mailboxes from existing messages rows.
--   4. Drop the per-mailbox columns from messages.
--
-- The migration is a single offline pass (REQ-STORE-38). The integrity
-- check in herold diag fsck (and herold diag migrate-messages-mailboxes)
-- verifies row counts before and after.

-- Step 1: add principal_id to messages.
-- Default 0 so the column is NOT NULL; the UPDATE below backfills real
-- values immediately. SQLite does not allow non-constant defaults that
-- reference other tables so we use a two-step approach.
ALTER TABLE messages ADD COLUMN principal_id INTEGER NOT NULL DEFAULT 0;

-- Backfill principal_id from the mailbox owner.
UPDATE messages
   SET principal_id = (
       SELECT mb.principal_id FROM mailboxes mb WHERE mb.id = messages.mailbox_id
   );

-- Step 2: create the join table.
CREATE TABLE message_mailboxes (
  message_id        INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  mailbox_id        INTEGER NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
  uid               INTEGER NOT NULL,
  modseq            INTEGER NOT NULL,
  flags             INTEGER NOT NULL DEFAULT 0,
  keywords_csv      TEXT    NOT NULL DEFAULT '',
  snoozed_until_us  INTEGER,
  PRIMARY KEY (message_id, mailbox_id),
  UNIQUE (mailbox_id, uid)
) STRICT;

-- Index for IMAP UID lookup: SELECT WHERE mailbox_id = ? AND uid > ?
CREATE INDEX idx_message_mailboxes_mailbox_uid
  ON message_mailboxes(mailbox_id, uid);

-- Index for JMAP mailboxIds reads: SELECT WHERE message_id = ?
CREATE INDEX idx_message_mailboxes_message_id
  ON message_mailboxes(message_id);

-- Index for modseq queries: SELECT WHERE mailbox_id = ? AND modseq > ?
CREATE INDEX idx_message_mailboxes_mailbox_modseq
  ON message_mailboxes(mailbox_id, modseq);

-- Step 3: backfill one row per existing messages row.
INSERT INTO message_mailboxes
  (message_id, mailbox_id, uid, modseq, flags, keywords_csv, snoozed_until_us)
SELECT id, mailbox_id, uid, modseq, flags, keywords_csv, snoozed_until_us
  FROM messages;

-- Step 4: drop per-mailbox columns from messages.
-- SQLite does not support DROP COLUMN on columns that are part of a UNIQUE
-- constraint or index that covers other columns (prior to 3.35.0 it
-- could not drop any column at all). The safest path is a table rebuild
-- with only the desired columns retained.
CREATE TABLE messages_new (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  principal_id      INTEGER NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  blob_hash         TEXT    NOT NULL,
  blob_size         INTEGER NOT NULL,
  internal_date_us  INTEGER NOT NULL,
  received_at_us    INTEGER NOT NULL,
  size              INTEGER NOT NULL,
  thread_id         INTEGER NOT NULL DEFAULT 0,
  env_subject       TEXT    NOT NULL DEFAULT '',
  env_from          TEXT    NOT NULL DEFAULT '',
  env_to            TEXT    NOT NULL DEFAULT '',
  env_cc            TEXT    NOT NULL DEFAULT '',
  env_bcc           TEXT    NOT NULL DEFAULT '',
  env_reply_to      TEXT    NOT NULL DEFAULT '',
  env_message_id    TEXT    NOT NULL DEFAULT '',
  env_in_reply_to   TEXT    NOT NULL DEFAULT '',
  env_date_us       INTEGER NOT NULL DEFAULT 0
) STRICT;

INSERT INTO messages_new
  (id, principal_id, blob_hash, blob_size, internal_date_us, received_at_us,
   size, thread_id, env_subject, env_from, env_to, env_cc, env_bcc,
   env_reply_to, env_message_id, env_in_reply_to, env_date_us)
SELECT id, principal_id, blob_hash, blob_size, internal_date_us, received_at_us,
       size, thread_id, env_subject, env_from, env_to, env_cc, env_bcc,
       env_reply_to, env_message_id, env_in_reply_to, env_date_us
  FROM messages;

DROP TABLE messages;
ALTER TABLE messages_new RENAME TO messages;

-- Recreate indexes dropped when messages was rebuilt.
CREATE INDEX idx_messages_blob_hash ON messages(blob_hash);
CREATE INDEX idx_messages_principal_id ON messages(principal_id);
CREATE INDEX idx_messages_env_message_id ON messages(env_message_id);
