-- 0061_blob_part_index.sql -- per-blob serialised MIME part index (re #46).
--
-- Adds the blob_part_index table so the JMAP part-download path can serve
-- a message part by byte range without re-parsing the full message blob.
-- The index payload is opaque to the store (serialised JSON produced by
-- internal/bodymeta or an equivalent worker).
--
-- Column semantics:
--
--   blob_hash        -- TEXT PRIMARY KEY referencing the content-addressed
--                       message blob in blob_refs(hash). The table is keyed
--                       on blob_hash (not message_id) because a single blob
--                       may be referenced by many messages; dedup is automatic
--                       and the index never goes stale (blobs are immutable).
--
--   index_version    -- INTEGER NOT NULL: schema version of the JSON payload.
--                       A worker with a higher required version will treat rows
--                       with lower index_version as absent via
--                       ListMessagesNeedingPartIndex (WHERE index_version < ?).
--
--   parts_json       -- TEXT NOT NULL: opaque serialised MIME part index.
--                       Treated as bytes by the store; the caller owns
--                       the schema.
--
--   computed_at_us   -- INTEGER NOT NULL: wall-clock microseconds when the
--                       index was computed (from the injected clock at the
--                       call site).
--
-- The backfill query (ListMessagesNeedingPartIndex) joins messages against
-- this table using a NOT EXISTS subquery filtered on index_version >= ?.
-- The existing messages(id DESC) index keeps that scan efficient.
--
-- Forward-only. Mirrors storepg 0061.

CREATE TABLE blob_part_index (
    blob_hash      TEXT    PRIMARY KEY,
    index_version  INTEGER NOT NULL,
    parts_json     TEXT    NOT NULL,
    computed_at_us INTEGER NOT NULL
);
