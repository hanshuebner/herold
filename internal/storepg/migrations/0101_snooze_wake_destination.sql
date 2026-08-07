-- 0101_snooze_wake_destination.sql -- wake destination for the JMAP
-- snooze extension (issue #274). Mirrors
-- storesqlite/migrations/0101_snooze_wake_destination.sql.
--
-- Snooze waking cleared the snooze in place, in whatever mailbox the
-- message already lived in: a message snoozed from a non-Inbox folder
-- (Sent, Archive, a label) silently reappeared there on wake with no
-- reminder. This migration adds the destination the wake worker adds
-- the message to on release, stored on the same per-(message, mailbox)
-- row that already carries snoozed_until_us (migration 0024).
--
-- wake_mailbox_id is nullable: NULL means no destination was chosen
-- (the wake-time caller resolves the principal's Inbox, or wakes the
-- message in place when the principal has no Inbox). It is set
-- alongside snoozed_until_us by SetSnooze and cleared together with it
-- when the snooze clears.
--
-- Back-fill: every row with snoozed_until_us already set (an in-flight
-- snooze straddling this migration) gets wake_mailbox_id pointed at
-- the owning principal's Inbox, resolved first by the
-- store.MailboxAttrInbox bit (value 1, the low bit of
-- mailboxes.attributes) and falling back to a case-insensitive name
-- match on "INBOX". Where neither resolves, the column stays NULL and
-- the message wakes in place, matching today's behaviour exactly.

ALTER TABLE message_mailboxes
  ADD COLUMN wake_mailbox_id BIGINT REFERENCES mailboxes(id) ON DELETE SET NULL;

UPDATE message_mailboxes
   SET wake_mailbox_id = (
         SELECT mb.id
           FROM mailboxes mb
          WHERE mb.principal_id = (
                  SELECT m.principal_id FROM messages m WHERE m.id = message_mailboxes.message_id
                )
            AND ((mb.attributes & 1) = 1 OR UPPER(mb.name) = 'INBOX')
          ORDER BY (mb.attributes & 1) DESC, mb.id ASC
          LIMIT 1
       )
 WHERE snoozed_until_us IS NOT NULL;
