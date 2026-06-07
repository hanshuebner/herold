-- 0057_file_shares_source.sql -- Message back-reference columns for file_shares.
-- (REQ-SHARE-04). Forward-only. Mirrors storepg 0057.
--
-- Adds three nullable columns to file_shares so each confirmed share
-- records which message it was shared from. The columns are populated
-- atomically during the pending -> active transition (ConfirmFileShare).
-- They remain NULL while the share is still pending.
--
-- Columns:
--   source_message_id   TEXT NULL -- JMAP Email id / store message id of the
--                                    originating message. Soft reference: no
--                                    FK so the context survives message deletion.
--   source_subject      TEXT NULL -- Subject line snapshot at confirmation time.
--   source_recipients   TEXT NULL -- JSON array of recipient email addresses
--                                    (To + Cc + Bcc envelope), snapshotted at
--                                    confirmation time.
--
-- All three are NULL for a share that has not yet been confirmed (state =
-- 'pending'), and for confirmed shares where the caller provided a
-- zero-value FileShareSource (the JMAP layer did not supply the context).
-- The recipient-facing surfaces MUST NOT expose these columns.

ALTER TABLE file_shares ADD COLUMN source_message_id TEXT;
ALTER TABLE file_shares ADD COLUMN source_subject    TEXT;
ALTER TABLE file_shares ADD COLUMN source_recipients TEXT;
