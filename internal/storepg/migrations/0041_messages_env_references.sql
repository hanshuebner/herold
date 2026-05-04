-- 0041_messages_env_references.sql — add env_references column to messages.
--
-- Thread resolution in InsertMessage previously only consulted the
-- In-Reply-To header to find ancestor messages. The References header
-- lists the full ancestry chain and is the authoritative source per
-- RFC 5256 sec 2.2 and RFC 8621 sec 8.1. Without it, messages that
-- reference an ancestor only via References (not In-Reply-To) end up in
-- separate threads, breaking Thread/get, someInThreadHaveKeyword,
-- noneInThreadHaveKeyword, and collapseThreads.
--
-- The new column stores the raw References header value for thread-
-- resolution use at ingest time. Existing rows default to '' (empty
-- string) which is equivalent to "no References header" and preserves
-- the existing threading for already-stored messages.
--
-- Forward-only. Mirrors storesqlite 0041.

ALTER TABLE messages ADD COLUMN env_references TEXT NOT NULL DEFAULT '';
