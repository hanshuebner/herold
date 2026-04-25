-- 0007_snooze.sql — JMAP snooze extension (REQ-PROTO-49).
--
-- Adds the snoozed_until_us column to the messages table. NULL means
-- "not snoozed"; non-NULL is the wake-up deadline (Unix micros, UTC).
-- The column carries the same atomicity invariant the JMAP/IMAP
-- handlers and the SetSnooze store helper enforce:
--   snoozed_until_us IS NOT NULL  iff  keywords_csv lists '$snoozed'.
--
-- The partial index supports the wake-up sweeper's range scan
-- ("snoozed_until_us <= now") without bloating the messages table's
-- general write path. Forward-only; the migrations table records 0007
-- as applied.

ALTER TABLE messages ADD COLUMN snoozed_until_us BIGINT;

CREATE INDEX idx_messages_snoozed_until
  ON messages(snoozed_until_us)
  WHERE snoozed_until_us IS NOT NULL;
