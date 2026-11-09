-- 0122_queue_interactive_submission.sql -- adds
-- queue.interactive_submission (issue #565).
--
-- interactive_submission is 1 on a queue row whose message the user sent
-- themselves (JMAP EmailSubmission, SMTP submission) and 0 on a row that
-- the HTTP send API, the server itself, or a relay path queued. The
-- loopback deliverer delivers a row to the submitting principal's own
-- mailbox as read only when the flag is 1; every other row arrives unread.
--
-- Rows queued before this migration default to 0 (unread).
--
-- Forward-only.

ALTER TABLE queue ADD COLUMN interactive_submission INTEGER NOT NULL DEFAULT 0;
