-- 0105_message_ingest_source.sql -- records which ingest path produced a
-- messages row (issue #143, maintainer finding #2, 2026-09-09).
--
-- Message research shows an operator the "received" entry for a message
-- but gives no hint whether it arrived by live SMTP delivery or was
-- pulled in later by an importer. ingest_source is written once, by the
-- caller, alongside the InsertMessage / InsertMessages call that creates
-- the row, and is never recomputed -- mirroring delivery_disposition
-- (migration 0090).
--
-- '' (the NOT NULL DEFAULT) means "not recorded": every row that
-- predates this migration, and every row inserted by a caller that has
-- not been updated to set it (store.IngestSourceUnknown). The non-empty
-- values (store.MessageIngestSource: "smtp", "imap-import",
-- "jmap-import", "imap-append", "imap-copy", "mailing-list-archive",
-- "gmail-import") name the write path.
--
-- ingest_source_ref carries path-specific free text alongside
-- ingest_source: the import account name for "imap-import", the mailing
-- list address for "mailing-list-archive", empty for every other source.
--
-- Forward-only. Mirrors storepg 0105.

ALTER TABLE messages ADD COLUMN ingest_source TEXT NOT NULL DEFAULT '';
ALTER TABLE messages ADD COLUMN ingest_source_ref TEXT NOT NULL DEFAULT '';
