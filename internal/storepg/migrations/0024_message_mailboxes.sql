-- 0024_message_mailboxes.sql — multi-mailbox membership (Wave 3.11)
--
-- REQ-STORE-36..38: a single JMAP Email may live in N mailboxes
-- simultaneously. The per-(message, mailbox) state (UID, MODSEQ, flags,
-- keywords) moves from messages into a new message_mailboxes join table.
-- messages gains a principal_id denorm column for query speed.
--
-- Mirrors storesqlite/migrations/0024_message_mailboxes.sql.
-- Forward-only (REQ-OPS-100).

-- Step 1: add principal_id to messages, backfill, add NOT NULL.
ALTER TABLE messages ADD COLUMN principal_id BIGINT NOT NULL DEFAULT 0;

UPDATE messages m
   SET principal_id = mb.principal_id
  FROM mailboxes mb
 WHERE mb.id = m.mailbox_id;

-- Step 2: create the join table.
CREATE TABLE message_mailboxes (
  message_id        BIGINT  NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  mailbox_id        BIGINT  NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
  uid               BIGINT  NOT NULL,
  modseq            BIGINT  NOT NULL,
  flags             BIGINT  NOT NULL DEFAULT 0,
  keywords_csv      TEXT    NOT NULL DEFAULT '',
  snoozed_until_us  BIGINT,
  PRIMARY KEY (message_id, mailbox_id),
  UNIQUE (mailbox_id, uid)
);

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
-- Also drop the old unique constraint on (mailbox_id, uid) which no longer
-- exists once the columns are removed.
ALTER TABLE messages DROP COLUMN mailbox_id;
ALTER TABLE messages DROP COLUMN uid;
ALTER TABLE messages DROP COLUMN modseq;
ALTER TABLE messages DROP COLUMN flags;
ALTER TABLE messages DROP COLUMN keywords_csv;
ALTER TABLE messages DROP COLUMN snoozed_until_us;

-- Add an FK from messages.principal_id now that the default-0 bootstrap
-- rows have been cleaned up.
ALTER TABLE messages ALTER COLUMN principal_id SET NOT NULL;
ALTER TABLE messages ADD CONSTRAINT messages_principal_id_fk
  FOREIGN KEY (principal_id) REFERENCES principals(id) ON DELETE CASCADE;

-- Recreate the principal_id index.
CREATE INDEX idx_messages_principal_id ON messages(principal_id);
