-- 0040_email_pretrash_mailboxes.sql — snapshot of pre-trash mailbox memberships.
--
-- When a message is moved into Trash (via AddMessageToMailbox or MoveMessage),
-- the server records which non-Trash mailboxes the message belonged to so that
-- a subsequent Restore operation can replay those memberships rather than always
-- falling back to Inbox.
--
-- Lifecycle:
--   snapshot: written (replacing any prior snapshot) when the message gains a
--             Trash mailbox membership (AddMessageToMailbox / MoveMessage to Trash).
--   replay:   read and re-applied when the message loses its Trash membership
--             (RemoveMessageFromMailbox / MoveMessage from Trash). The snapshot
--             is deleted after replay.
--   cascade:  ON DELETE CASCADE from messages(id) so permanently deleting a
--             message (ExpungeMessages drains all memberships, then removes the
--             messages row) automatically cleans up the snapshot.
--
-- Forward-only. Mirrors storesqlite 0040.

CREATE TABLE email_pretrash_mailboxes (
  email_id    BIGINT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  mailbox_id  BIGINT NOT NULL,
  PRIMARY KEY (email_id, mailbox_id)
);
